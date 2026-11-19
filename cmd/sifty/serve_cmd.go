package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/sifty-works/sifty-server/internal/inbox"
	"github.com/sifty-works/sifty-server/internal/industry"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/runner"
	"github.com/sifty-works/sifty-server/internal/server"
	"github.com/sifty-works/sifty-server/web"
)

const (
	logMaxBytes  = 10 << 20 // 10 MB
	lockFileName = "sifty.lock"
	logDir       = "logs"
	logFileName  = "sifty.log"
	dbFileName   = "sifty.db"
)

// cmdServe implements 'sifty serve'.
func cmdServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags, err := parseServeFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return err
	}

	// 1. Resolve and make absolute the data directory.
	dataDir, err := filepath.Abs(resolveDataDir(flags.dataDir))
	if err != nil {
		return fmt.Errorf("resolving data dir: %w", err)
	}

	// 2. Create the data folder with mode 0700 if it does not exist.
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("creating data dir %s: %w", dataDir, err)
	}

	// 3. Take the lock.
	lockPath := filepath.Join(dataDir, lockFileName)
	lockFile, err := acquireLock(lockPath)
	if err != nil {
		fmt.Fprintf(stderr, "Another Sifty is already running on this data folder (%s)\n", dataDir)
		return fmt.Errorf("lock: %w", err)
	}
	defer lockFile.Close()

	// 4. Rotate and open the log.
	logger, logClose, err := openLog(dataDir, stderr)
	if err != nil {
		return fmt.Errorf("opening log: %w", err)
	}
	defer logClose()

	// 5. Open settings store.
	settingsPath := filepath.Join(dataDir, "sifty.yaml")
	store, err := config.Open(settingsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return err
	}
	cfg := store.Get()

	// 6. Apply flag overrides.
	// flags.port == -1 means --port was not given; keep the settings value.
	// flags.port >= 0 means it was given explicitly (0 = take a free port).
	if flags.port >= 0 {
		cfg.Port = flags.port
	}
	if flags.debug {
		cfg.Debug = true
	}
	offline := flags.offline

	// 7. Open the database.
	dbPath := filepath.Join(dataDir, dbFileName)
	database, err := db.Open(dbPath, db.Migrations)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return err
	}
	defer database.Close()

	// 7a. Rebuild the full-text index if the database was just migrated and the
	// index is empty. This is a one-off step; subsequent starts skip it.
	if err := catalog.RebuildFTS(ctx, database); err != nil {
		logger.Warn("fts rebuild failed", "err", err)
	}

	// 7b. Load the industry vocabulary.
	vocabPath := filepath.Join(dataDir, "vocab", "industries.yaml")
	vocab, err := industry.LoadOrDefault(vocabPath)
	if err != nil {
		logger.Warn("industry vocab load failed; using default", "err", err)
		vocab = industry.DefaultVocab()
	}

	// 8. Build the fetcher: replay dir overrides the real client.
	var fetcher fetch.Fetcher
	if flags.replayDir != "" {
		rm, err := fetchtest.Load(flags.replayDir)
		if err != nil {
			return fmt.Errorf("loading replay dir: %w", err)
		}
		fetcher = newReplayFetcher(rm, logger)
	} else {
		fetcher = fetch.New(fetch.Options{
			Blocks:  fetch.NewDomainBlocks(database),
			Offline: offline,
		})
	}

	// 9. Build the catalogue, runner and collector.
	// vocabRef holds the active vocabulary; it is replaced atomically when
	// PUT /api/industries saves a new one so that subsequent Upsert calls and
	// the assign_industries job always use the latest version.
	vocabRef := &vocab
	assignFn := func(title, tags, text string) []string {
		return industry.Assign(title, tags, text, *vocabRef)
	}
	cat := catalog.New(database, time.Now, assignFn)
	r := runner.New(database, map[string]int{"browser": 1, "net": 4, "cpu": 1}, time.Now, logger)

	// Register the assign_industries job kind.
	r.Register(runner.Kind{
		Name:   "assign_industries",
		Resume: true,
		Run: func(runCtx context.Context, j *runner.Job) error {
			return runAssignIndustries(runCtx, j, cat, vocabRef)
		},
	})

	blocked := offline && flags.replayDir == ""
	col := collect.New(store, cat, r, fetcher, database, blocked, nil, logger)
	col.Register()

	// Queue the initial assign_industries job when no vacancy has been assigned
	// yet (fresh import of an existing database).
	if err := queueInitialAssign(ctx, database, r); err != nil {
		logger.Warn("could not queue initial industry assignment", "err", err)
	}

	// 10. Listen.
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

	sourceNames := make(map[string]string, len(cfg.Sources))
	for _, s := range cfg.Sources {
		if key, err := config.SourceKey(s.URL); err == nil {
			sourceNames[key] = s.Name
		}
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
		r.Enqueue(runner.Spec{Kind: "assign_industries", Subject: "", Priority: 0}) //nolint:errcheck
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
		inbox.RegisterRoutes(mux, database, time.Now, logger, collectActive, sourceNames, getVocabNames)
		inbox.RegisterIndustryRoutes(mux, logger, getVocab, saveVocab)
		candidate.RegisterRoutes(mux, database, filesDir, logger)
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

	// Start runner and ticker; both stop when bgCtx is cancelled on shutdown.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	go r.Start(bgCtx) //nolint:errcheck
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

// replayFetcher wraps a fetchtest.Map and logs addresses not in the index.
type replayFetcher struct {
	m   *fetchtest.Map
	log *slog.Logger
}

func newReplayFetcher(m *fetchtest.Map, log *slog.Logger) *replayFetcher {
	return &replayFetcher{m: m, log: log}
}

func (r *replayFetcher) Get(ctx context.Context, u string, opt fetch.Opt) (fetch.Response, error) {
	resp, err := r.m.Get(ctx, u, opt)
	if errors.Is(err, fetch.ErrNotFound) {
		r.log.Info("replay: not recorded " + u)
	}
	return resp, err
}

// acquireLock opens (or creates) the lock file and acquires an exclusive
// non-blocking advisory lock. Returns the open file on success; the caller
// must keep it open for the life of the process.
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

// openLog sets up the dual-writer logger (stderr + log file) and returns it
// along with a cleanup function. It rotates the log file if it exceeds 10 MB.
func openLog(dataDir string, stderr io.Writer) (*slog.Logger, func(), error) {
	logsDir := filepath.Join(dataDir, logDir)
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		return nil, nil, err
	}

	logPath := filepath.Join(logsDir, logFileName)

	// Rotate if over 10 MB.
	if info, err := os.Stat(logPath); err == nil && info.Size() > logMaxBytes {
		rotated := filepath.Join(logsDir, logFileName+".1")
		os.Rename(logPath, rotated) //nolint:errcheck
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

// queueInitialAssign enqueues one assign_industries job when every vacancy in
// the database has an empty industries column. This handles the first start
// after migrating a database that pre-dates the industries feature.
func queueInitialAssign(ctx context.Context, database *db.DB, r *runner.Runner) error {
	var total, withIndustries int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM vacancy`).Scan(&total); err != nil {
		return err
	}
	if total == 0 {
		return nil
	}
	if err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM vacancy WHERE industries != '[]'`).Scan(&withIndustries); err != nil {
		return err
	}
	if withIndustries == 0 {
		_, err := r.Enqueue(runner.Spec{Kind: "assign_industries", Subject: "", Priority: 0})
		return err
	}
	return nil
}

// runAssignIndustries reassigns industries to every vacancy using the current
// vocabulary. It runs on the cpu lane and processes vacancies in batches of 200.
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
	for offset := 0; offset < total || (total == 0 && offset == 0); offset += batchSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		batch, err := cat.VacanciesForAssign(ctx, offset, batchSize)
		if err != nil {
			return fmt.Errorf("assign_industries: load batch: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		v := *vocabRef
		for _, row := range batch {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var tags []string
			json.Unmarshal([]byte(row.TagsJSON), &tags) //nolint:errcheck
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
