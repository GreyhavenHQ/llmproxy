package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greyhavenhq/llmproxy/internal/config"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &logs
}

// A lost secret file signs everyone out and breaks every key. The server says so.
func TestNewSecretWithExistingCredentialsIsLogged(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DatabaseURL: filepath.Join(dir, "proxy.db"),
		SecretFile:  filepath.Join(dir, "secret"),
	}
	logs := captureLogs(t)

	_, st, err := openSecretAndStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "LLMPROXY_KEY_SECRET") {
		t.Fatalf("a first start must not warn: %s", logs.String())
	}
	ctx := context.Background()
	p, err := st.GetOrCreatePrincipal(ctx, "alice", "user", "member", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIKey(ctx, p.ID, "hash", "abcd", "k", nil); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := os.Remove(cfg.SecretFile); err != nil {
		t.Fatal(err)
	}
	_, st, err = openSecretAndStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "new secret") ||
		!strings.Contains(out, "LLMPROXY_KEY_SECRET") {
		t.Fatalf("no warning: %s", out)
	}
}
