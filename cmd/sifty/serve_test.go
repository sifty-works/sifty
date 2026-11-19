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

	if port == 7373 {
		t.Error("--port 0 should not listen on the default port 7373")
	}
	if port <= 0 {
		t.Errorf("--port 0 should resolve to a positive port number, got %d", port)
	}
}

func TestReplayFetchesFromFixtureAndLogsUnrecordedAddress(t *testing.T) {
	replayDir := t.TempDir()
	boardBody := `{"company":{"name":"TestCo"},"jobs":[{"id":1,"title":"Engineer","absolute_url":"https://boards.greenhouse.io/testco/jobs/1","content":"<p>Job</p>"}]}`
	if err := os.WriteFile(filepath.Join(replayDir, "board.json"), []byte(boardBody), 0600); err != nil {
		t.Fatal(err)
	}
	index := `{"entries":[{"url":"https://boards-api.greenhouse.io/v1/boards/testco/jobs?content=true","status":200,"content_type":"application/json","body_file":"board.json"}]}`
	if err := os.WriteFile(filepath.Join(replayDir, "index.json"), []byte(index), 0600); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	ctx, cancelCtx := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, []string{"serve", "--offline", "--replay", replayDir, "--data-dir", dataDir, "--port", "0"},
			pw, io.Discard)
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
	var port int
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
	defer func() { cancelCtx(); <-done; pr.Close() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	postJSON := func(path, body string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
	}

	postJSON("/api/sources", `{"url":"https://boards.greenhouse.io/testco"}`)
	postJSON("/api/sources", `{"url":"https://feeds.unrecorded.example.com/jobs.xml"}`)

	postJSON("/api/collect", `{"force":true}`)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/status")
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), `"lastRun":{`) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	logPath := filepath.Join(dataDir, "logs", "sifty.log")
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	logText := string(logBytes)
	if !strings.Contains(logText, "replay: not recorded") {
		t.Error("log should contain 'replay: not recorded' for the unrecorded address")
	}
	if !strings.Contains(logText, "feeds.unrecorded.example.com") {
		t.Errorf("log should include the unrecorded URL; log:\n%s", logText)
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
