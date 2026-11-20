package models_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/config"
	"github.com/sifty-works/sifty-server/internal/models"
)

func makeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return p
}

func cliGate(t *testing.T, pc config.ProviderConfig) *models.Gate {
	t.Helper()
	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{"p": pc}
	cfg.Work = &config.WorkConfig{Reading: "p"}
	store := openTestStore(t, cfg)
	gate := models.NewGate(openTestDB(t), store, false, nil)
	t.Cleanup(func() { gate.Stop(context.Background()) })
	return gate
}

func TestCLIClaudeStructuredOutputReturned(t *testing.T) {
	script := makeScript(t, `printf '{"is_error":false,"structured_output":{"score":42},"result":""}'`)
	pc := providerConfig("cli-claude", script, "sonnet", "local")
	gate := cliGate(t, pc)
	resp, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "rate this"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text != `{"score":42}` {
		t.Errorf("text: got %q, want %q", resp.Text, `{"score":42}`)
	}
}

func TestCLIClaudePlainResultReturned(t *testing.T) {
	script := makeScript(t, `printf '{"is_error":false,"result":"hello from claude"}'`)
	pc := providerConfig("cli-claude", script, "sonnet", "local")
	gate := cliGate(t, pc)
	resp, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text != "hello from claude" {
		t.Errorf("text: got %q, want %q", resp.Text, "hello from claude")
	}
}

func TestCLIClaudeErrorAnswerReturnsError(t *testing.T) {
	script := makeScript(t, `printf '{"is_error":true,"result":"something went wrong"}'`)
	pc := providerConfig("cli-claude", script, "sonnet", "local")
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error for is_error response")
	}
	if !strings.Contains(err.Error(), "signed in") {
		t.Errorf("error message: got %q, want to contain 'signed in'", err.Error())
	}
}

func TestCLIClaudeIsolatedEnvironment(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.txt")
	script := makeScript(t, fmt.Sprintf(`env | cut -d= -f1 | sort > %s
printf '{"is_error":false,"result":"ok"}'`, envFile))
	t.Setenv("SIFTY_SHOULD_NOT_PASS", "secret")
	pc := providerConfig("cli-claude", script, "sonnet", "local")
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	for _, k := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if k == "SIFTY_SHOULD_NOT_PASS" {
			t.Error("secret environment variable was passed to the script")
		}
	}
}

func TestCLIClaudePromptOverContextRefused(t *testing.T) {
	pc := providerConfig("cli-claude", "/nonexistent/script", "sonnet", "local")
	pc.Context = 10
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "this prompt is too long"})
	if err == nil {
		t.Fatal("expected error for over-context prompt")
	}
}

func TestCLIClaudeTimeoutKillsScript(t *testing.T) {
	script := makeScript(t, `sleep 10
printf '{"is_error":false,"result":"ok"}'`)
	pc := providerConfig("cli-claude", script, "sonnet", "local")
	pc.Timeout = config.Duration(150 * time.Millisecond)
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error from timeout")
	}
}

func TestCLIOpencodeTextEventsJoined(t *testing.T) {
	script := makeScript(t, `printf '{"type":"text","part":{"type":"text","text":"hello "}}\n'
printf '{"type":"text","part":{"type":"text","text":"world"}}\n'`)
	pc := providerConfig("cli-opencode", script, "claude-sonnet", "local")
	gate := cliGate(t, pc)
	resp, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text != "hello world" {
		t.Errorf("text: got %q, want %q", resp.Text, "hello world")
	}
}

func TestCLIOpencodeToolEventRefused(t *testing.T) {
	script := makeScript(t, `printf '{"type":"tool_call","part":{"type":"tool","text":""}}\n'`)
	pc := providerConfig("cli-opencode", script, "claude-sonnet", "local")
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error for tool event")
	}
	if !strings.Contains(err.Error(), "tool call") {
		t.Errorf("error: got %q, want to contain 'tool call'", err.Error())
	}
}

func TestCLIOpencodeErrorEventReturnsError(t *testing.T) {
	script := makeScript(t, `printf '{"type":"error","error":"connection failed"}\n'`)
	pc := providerConfig("cli-opencode", script, "claude-sonnet", "local")
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error for error event")
	}
	if !strings.Contains(err.Error(), "opencode error") {
		t.Errorf("error: got %q, want to contain 'opencode error'", err.Error())
	}
}

func TestCLIOpencodeNoTextReturnsError(t *testing.T) {
	script := makeScript(t, `printf '{"type":"status","part":{"type":"status","text":"done"}}\n'`)
	pc := providerConfig("cli-opencode", script, "claude-sonnet", "local")
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error when no text events")
	}
	if !strings.Contains(err.Error(), "No text from opencode") {
		t.Errorf("error: got %q, want to contain 'No text from opencode'", err.Error())
	}
}

func TestCLIOpencodePromptOverContextRefused(t *testing.T) {
	pc := providerConfig("cli-opencode", "/nonexistent/script", "claude-sonnet", "local")
	pc.Context = 10
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "this prompt is too long"})
	if err == nil {
		t.Fatal("expected error for over-context prompt")
	}
}

func TestCLIOpencodeTimeoutKillsScript(t *testing.T) {
	script := makeScript(t, `sleep 10`)
	pc := providerConfig("cli-opencode", script, "claude-sonnet", "local")
	pc.Timeout = config.Duration(150 * time.Millisecond)
	gate := cliGate(t, pc)
	_, err := gate.For("reading", "user").Complete(context.Background(), models.Request{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error from timeout")
	}
}
