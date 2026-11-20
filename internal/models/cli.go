package models

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var fenceRe = regexp.MustCompile("(?s)^```(?:json)?\\s*|\\s*```$")

func unwrapFence(text string) string {
	return strings.TrimSpace(fenceRe.ReplaceAllString(strings.TrimSpace(text), ""))
}

func isolatedEnv() []string {
	keys := []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "LANG", "TERM",
		"XDG_DATA_HOME", "XDG_CACHE_HOME"}
	var env []string
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func runCLI(ctx context.Context, name string, args []string, stdin string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}
