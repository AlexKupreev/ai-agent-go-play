package artifact

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	m, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := m.Render(); got != "" {
		t.Errorf("empty manifest should render \"\", got %q", got)
	}

	if err := m.Append(Entry{Path: "scratch/a.csv", Origin: OriginAgent, Source: "https://ex.gov/a.csv", Description: "CSV: date, val"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := m.Append(Entry{Path: "/home/u/b.csv", Origin: OriginUser}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Reopen from disk to confirm it persisted.
	m2, err := New(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	entries := m2.List()
	if len(entries) != 2 {
		t.Fatalf("got %d entries after reopen, want 2", len(entries))
	}
	if entries[0].Path != "scratch/a.csv" || entries[0].Origin != OriginAgent {
		t.Errorf("entry 0 wrong: %+v", entries[0])
	}
	if entries[0].Timestamp.IsZero() {
		t.Error("Append should stamp a timestamp")
	}
	if entries[1].Origin != OriginUser {
		t.Errorf("entry 1 origin = %q, want user", entries[1].Origin)
	}

	render := m2.Render()
	for _, want := range []string{
		"scratch/a.csv [agent] — CSV: date, val (source: https://ex.gov/a.csv)",
		"/home/u/b.csv [user]",
	} {
		if !strings.Contains(render, want) {
			t.Errorf("render missing %q\n--- render ---\n%s", want, render)
		}
	}
}

func TestNewMissingFileIsEmpty(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("New on missing file should not error: %v", err)
	}
	if len(m.List()) != 0 {
		t.Error("missing file should yield an empty manifest")
	}
}
