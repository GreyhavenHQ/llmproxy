package server_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/monadical/llmproxy/internal/store"
)

// The core guarantee: no request/response content can be persisted. Two
// layers: the schema is structurally incapable of holding content, and a
// request containing a marker string leaves no trace in the database files.

// Content-shaped names, matched as whole underscore-separated segments (so
// 'credential_ciphertext' is fine but 'request_text' is not).
var bannedColumn = regexp.MustCompile(
	`(?i)(^|_)(body|contents?|prompt|completions?|messages?|inputs?|outputs?|audio|transcripts?|payload|text|images?|file)(_|$)`)

var columnLine = regexp.MustCompile(`(?m)^\s{4}([a-z_]+)\s`)

func TestNoContentShapedColumns(t *testing.T) {
	var offenders []string
	for _, m := range columnLine.FindAllStringSubmatch(store.Schema, -1) {
		if bannedColumn.MatchString(m[1]) {
			offenders = append(offenders, m[1])
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("content-shaped column names are forbidden: %v", offenders)
	}
	// Sanity check that the extractor actually saw the schema's columns.
	if !strings.Contains(store.Schema, "usage_quantity") {
		t.Fatal("schema constant missing expected table")
	}
}

func TestMarkerNeverReachesDisk(t *testing.T) {
	e := newEnv(t)
	marker := "SECRETMARKER-a fox jumped over a very confidential fence"

	resp, _ := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{map[string]any{"role": "user", "content": marker}}})
	if resp.StatusCode != 200 {
		t.Fatal("chat failed")
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Outcome == "ok" })

	// Flush the WAL so everything SQLite knows about is in the main file.
	if _, err := e.st.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}

	needle := []byte("SECRETMARKER")
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, needle) {
			t.Fatalf("request content leaked into %s", entry.Name())
		}
	}
}
