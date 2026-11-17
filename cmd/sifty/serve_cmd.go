package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sifty-works/sifty/internal/config"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/inbox"
	"github.com/sifty-works/sifty/internal/server"
	"github.com/sifty-works/sifty/web"
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

	// 8. Listen.
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	handler := server.New(server.Options{
		Version: version,
		Port:    port,
		Offline: offline,
		Debug:   cfg.Debug,
		Logger:  logger,
		Static:  web.StaticFS(),
	}, func(mux *http.ServeMux) {
		mux.Handle("GET /api/vacancies", inbox.Handler(database, time.Now, logger))
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

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
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
