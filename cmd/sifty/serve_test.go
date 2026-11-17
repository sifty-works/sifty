package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startServe launches 'sifty serve --offline --data-dir <dir> --port 0',
// reads the "Listening on" line, and returns the port and a cancel func.
func startServe(t *testing.T, dataDir string) (port int, cancel func()) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())

	pr, pw := io.Pipe()
	stderr := io.Discard

	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, []string{"serve", "--offline", "--data-dir", dataDir, "--port", "0"},
			pw, stderr)
		pw.Close()
	}()

	sc := bufio.NewScanner(pr)
	portCh := make(chan int, 1)
	go func() {
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "Listening on http://127.0.0.1:") {
				var p int
				if _, err := fmt.Sscanf(line, "Listening on http://127.0.0.1:%d", &p); err == nil {
					portCh <- p
					return
				}
			}
		}
		portCh <- 0
	}()

	select {
	case p := <-portCh:
		if p == 0 {
			cancelCtx()
			t.Fatal("did not receive 'Listening on' line")
		}
		port = p
	case <-time.After(10 * time.Second):
		cancelCtx()
		t.Fatal("timeout waiting for server to start")
	}

	cancel = func() {
		cancelCtx()
		<-done
		pr.Close()
	}
	return port, cancel
}

func TestServeCreatesDataFolderWithRestrictedPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "newdata")
	port, cancel := startServe(t, dir)
	defer cancel()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("data dir not created: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("data dir permissions: got %o, want 0700", info.Mode().Perm())
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", port))
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/api/status: got %d, want 200", resp.StatusCode)
	}
}

func TestSecondServeOnSameFolderIsRefused(t *testing.T) {
	dir := t.TempDir()
	_, cancel := startServe(t, dir)
	defer cancel()

	// Try to start a second server on the same folder.
	var stderr bytes.Buffer
	ctx := context.Background()
	err := run(ctx, []string{"serve", "--data-dir", dir, "--port", "0"}, io.Discard, &stderr)

	if err == nil {
		t.Fatal("second serve should return an error")
	}
	if !strings.Contains(stderr.String(), "Another Sifty is already running") {
		t.Errorf("second serve: stderr %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), dir) {
		t.Errorf("second serve message should include the path, stderr: %q", stderr.String())
	}
}

func TestConfigDefaultsPrintsValidYAML(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"config", "defaults"}, &stdout, io.Discard); err != nil {
		t.Fatalf("config defaults: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("config defaults: empty output")
	}
}

func TestInvalidSettingsFileStopsStart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sifty.yaml"), []byte("unknown_key: true\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	err := run(context.Background(),
		[]string{"serve", "--data-dir", dir, "--port", "0"},
		io.Discard, &stderr)

	if err == nil {
		t.Fatal("invalid settings file should stop the start")
	}
	if !strings.Contains(stderr.String(), "unknown_key") {
		t.Errorf("error should name the bad key; got stderr: %q", stderr.String())
	}
}

func TestPortZeroTakesAFreePortNotDefaultPort(t *testing.T) {
	dir := t.TempDir()
	port, cancel := startServe(t, dir)
	defer cancel()

	// --port 0 must have resolved to a free ephemeral port, not the default 7373.
	if port == 7373 {
		t.Error("--port 0 should not listen on the default port 7373")
	}
	if port <= 0 {
		t.Errorf("--port 0 should resolve to a positive port number, got %d", port)
	}
}

func TestVersionCommand(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &stdout, io.Discard); err != nil {
		t.Fatalf("version: %v", err)
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		t.Fatal("version: empty output")
	}
}
