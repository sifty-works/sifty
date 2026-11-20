package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	siftyapi "github.com/sifty-works/sifty-server/api"
	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/collect"
	"github.com/sifty-works/sifty-server/internal/config"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/fetch"
	"github.com/sifty-works/sifty-server/internal/fetch/fetchtest"
	"github.com/sifty-works/sifty-server/internal/fit"
	"github.com/sifty-works/sifty-server/internal/inbox"
	"github.com/sifty-works/sifty-server/internal/industry"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/models"
	"github.com/sifty-works/sifty-server/internal/runner"
	"github.com/sifty-works/sifty-server/internal/server"
	"github.com/sifty-works/sifty-server/web"
)

const (
	logMaxBytes  = 10 << 20
	lockFileName = "sifty.lock"
	logDir       = "logs"
	logFileName  = "sifty.log"
	dbFileName   = "sifty.db"
)

func cmdServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags, err := parseServeFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return err
	}

	dataDir, err := filepath.Abs(resolveDataDir(flags.dataDir))
	if err != nil {
		return fmt.Errorf("resolving data dir: %w", err)
	}

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("creating data dir %s: %w", dataDir, err)
	}

	lockPath := filepath.Join(dataDir, lockFileName)
	lockFile, err := acquireLock(lockPath)
	if err != nil {
		fmt.Fprintf(stderr, "Another Sifty is already running on this data folder (%s)\n", dataDir)
		return fmt.Errorf("lock: %w", err)
	}
	defer lockFile.Close()

	logger, logClose, err := openLog(dataDir, stderr)
	if err != nil {
		return fmt.Errorf("opening log: %w", err)
	}
	defer logClose()

	settingsPath := filepath.Join(dataDir, "sifty.yaml")
	store, err := config.Open(settingsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return err
	}
	cfg := store.Get()

	if flags.port >= 0 {
		cfg.Port = flags.port
	}
	if flags.debug {
		cfg.Debug = true
	}
	offline := flags.offline

	dbPath := filepath.Join(dataDir, dbFileName)
	database, err := db.Open(dbPath, db.Migrations)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return err
	}
	defer database.Close()

	secretsPath := filepath.Join(dataDir, "secrets.env")
	secrets, err := models.LoadSecrets(secretsPath)
	if err != nil {
		logger.Warn("secrets.env load failed", "err", err)
		secrets = map[string]string{}
	}
	gate := models.NewGate(database, store, offline, secrets)
	defer gate.Stop(context.Background())

	vocabPath := filepath.Join(dataDir, "vocab", "industries.yaml")
	vocab, err := industry.LoadOrDefault(vocabPath)
	if err != nil {
		logger.Warn("industry vocab load failed; using default", "err", err)
		vocab = industry.DefaultVocab()
	}

	var fetcher fetch.Fetcher
	if flags.replayDir != "" {
		rm, err := fetchtest.Load(flags.replayDir)
		if err != nil {
			return fmt.Errorf("loading replay dir: %w", err)
		}
		rm.OnMiss = func(url string) { logger.Info("replay: not recorded " + url) }
		fetcher = rm
	} else {
		fetcher = fetch.New(fetch.Options{
			Blocks:  fetch.NewDomainBlocks(database),
			Offline: offline,
		})
	}

	vocabRef := &vocab
	assignFn := func(title, tags, text string) []string {
		return industry.Assign(title, tags, text, *vocabRef)
	}
	cat := catalog.New(database, time.Now, assignFn)
	r := runner.New(database, map[string]int{"browser": 1, "net": 4, "cpu": 1}, time.Now, logger)

	r.Register(runner.Kind{
		Name:   "assign_industries",
		Resume: true,
		Run: func(runCtx context.Context, j *runner.Job) error {
			return runAssignIndustries(runCtx, j, cat, vocabRef)
		},
	})

	rules := func() fit.Rules { c := store.Get(); return fit.Rules{Weights: c.Weights, Rates: c.Rates} }
	recomputeFit := func() { r.Enqueue(runner.Spec{Kind: "recompute_fit"}) }
	r.Register(runner.Kind{
		Name:   "recompute_fit",
		Resume: true,
		Run: func(runCtx context.Context, j *runner.Job) error {
			rel, err := j.Lane(runCtx, "cpu")
			if err != nil {
				return err
			}
			defer rel()
			return fit.Recompute(runCtx, database, rules, j.Progress)
		},
	})
	recomputeFit()

	r.Register(runner.Kind{
		Name:   "retention",
		Resume: true,
		Run: func(runCtx context.Context, j *runner.Job) error {
			return runRetention(runCtx, j, cat, database, logger, time.Now())
		},
	})

	blocked := offline && flags.replayDir == ""
	col := collect.New(store, cat, r, fetcher, database, blocked, nil, logger)
	col.Register()

	col.OnTick = func(now time.Time) {
		if retentionDue(now, r) {
			r.Enqueue(runner.Spec{Kind: "retention"})
		}
	}

	if retentionDue(time.Now(), r) {
		r.Enqueue(runner.Spec{Kind: "retention"})
	}

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	collectActive := func() bool {
		for _, j := range r.Active() {
			if j.Kind == "collect" {
				return true
			}
		}
		return false
	}

	filesDir := filepath.Join(dataDir, "files")

	getVocab := func() []inbox.IndustryItem {
		v := *vocabRef
		items := make([]inbox.IndustryItem, len(v.Industries))
		for i, ind := range v.Industries {
			items[i] = inbox.IndustryItem{Name: ind.Name, Keywords: ind.Keywords}
		}
		return items
	}
	saveVocab := func(items []inbox.IndustryItem) error {
		inds := make([]industry.Industry, len(items))
		for i, it := range items {
			inds[i] = industry.Industry{Name: it.Name, Keywords: it.Keywords}
		}
		newVocab := industry.Vocab{Industries: inds}
		if err := industry.Validate(newVocab); err != nil {
			return err
		}
		if err := industry.Save(vocabPath, newVocab); err != nil {
			return err
		}
		*vocabRef = newVocab
		r.Enqueue(runner.Spec{Kind: "assign_industries", Subject: "", Priority: 0})
		return nil
	}

	handler := server.New(server.Options{
		Version:           version,
		APISha256:         siftyapi.Sha256(),
		Port:              port,
		Offline:           offline,
		Debug:             cfg.Debug,
		Logger:            logger,
		Static:            web.StaticFS(),
		Status:            col.StatusFunc(),
		ModelsStatus:      gate.Status,
		BinaryUploadPaths: []string{"/api/me/photo"},
	}, func(mux *http.ServeMux) {
		getVocabNames := func() []string {
			v := *vocabRef
			names := make([]string, len(v.Industries))
			for i, ind := range v.Industries {
				names[i] = ind.Name
			}
			return names
		}
		registerModelRoutes(mux, gate, logger)
		inbox.RegisterRoutes(mux, database, time.Now, logger, collectActive, func() map[string]string {
			cfg := store.Get()
			m := make(map[string]string, len(cfg.Sources))
			for _, s := range cfg.Sources {
				if key, err := config.SourceKey(s.URL); err == nil {
					m[key] = s.Name
				}
			}
			return m
		}, getVocabNames, rules)
		inbox.RegisterIndustryRoutes(mux, logger, getVocab, saveVocab)
		candidate.RegisterRoutes(mux, database, filesDir, logger, recomputeFit)
		col.RegisterRoutes(mux)
	})

	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	listenMsg := fmt.Sprintf("Listening on http://127.0.0.1:%d", port)
	logger.Info(listenMsg)
	fmt.Fprintln(stdout, listenMsg)

	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	go r.Start(bgCtx)
	go col.Ticker(bgCtx)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		bgCancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		return srv.Shutdown(shutCtx)
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func openLog(dataDir string, stderr io.Writer) (*slog.Logger, func(), error) {
	logsDir := filepath.Join(dataDir, logDir)
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		return nil, nil, err
	}

	logPath := filepath.Join(logsDir, logFileName)

	if info, err := os.Stat(logPath); err == nil && info.Size() > logMaxBytes {
		rotated := filepath.Join(logsDir, logFileName+".1")
		os.Rename(logPath, rotated)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}

	tee := io.MultiWriter(stderr, logFile)
	handler := slog.NewTextHandler(tee, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	return logger, func() { logFile.Close() }, nil
}

func runRetention(ctx context.Context, j *runner.Job, cat *catalog.Catalog, database *db.DB, log *slog.Logger, now time.Time) error {
	cutoff := now.UTC().Add(-24 * time.Hour)

	ids, err := cat.RetainableExpired(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("retention: load: %w", err)
	}
	j.Progress(0, len(ids), "")

	for done, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := cat.Delete(ctx, id); err != nil {
			return fmt.Errorf("retention: delete %d: %w", id, err)
		}
		j.Progress(done+1, len(ids), "")
	}
	log.Info("retention: removed expired vacancies", "count", len(ids))

	modelCallCutoff := now.UTC().Add(-90 * 24 * time.Hour)
	if _, err := database.ExecContext(ctx, `DELETE FROM model_call WHERE created_at < ?`, modelCallCutoff.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("retention: model_call: %w", err)
	}
	return nil
}

func retentionDue(now time.Time, r *runner.Runner) bool {
	last, ok := r.Last("retention")
	if !ok {
		return true
	}
	return now.After(last.Created.Add(24 * time.Hour))
}

func runAssignIndustries(ctx context.Context, j *runner.Job, cat *catalog.Catalog, vocabRef *industry.Vocab) error {
	rel, err := j.Lane(ctx, "cpu")
	if err != nil {
		return err
	}
	defer rel()

	total, err := cat.CountVacancies(ctx)
	if err != nil {
		return fmt.Errorf("assign_industries: count: %w", err)
	}

	const batchSize = 200
	done := 0
	for after := int64(0); ; {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		batch, err := cat.VacancyTexts(ctx, after, batchSize)
		if err != nil {
			return fmt.Errorf("assign_industries: load batch: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		after = batch[len(batch)-1].ID
		v := *vocabRef
		for _, row := range batch {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var tags []string
			json.Unmarshal([]byte(row.TagsJSON), &tags)
			tagsText := strings.Join(tags, " ")
			var plainText string
			if len(row.Body) > 0 {
				if h, err := catalog.Body(row.Body); err == nil {
					plainText = listing.PlainText(h)
				}
			}
			names := industry.Assign(row.Title, tagsText, plainText, v)
			if err := cat.SetIndustries(ctx, row.ID, names); err != nil {
				return fmt.Errorf("assign_industries: set %d: %w", row.ID, err)
			}
			done++
		}
		j.Progress(done, total, "")
	}
	return nil
}
