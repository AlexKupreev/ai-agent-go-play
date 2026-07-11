package artifact

import (
	"os"
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

// writeFile creates a file (with parent dirs) under a scratch dir for the reaper tests.
func writeFile(t *testing.T, dir, rel string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// TestReapScratch_NoUserFiles: with only agent artifacts (today's only case), the reaper
// removes the whole scratch dir — identical to the os.RemoveAll it replaces.
func TestReapScratch_NoUserFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.csv")
	writeFile(t, dir, "sub/b.json")
	m, _ := New(filepath.Join(dir, ManifestName))
	_ = m.Append(Entry{Path: "a.csv", Origin: OriginAgent})
	// b.json is deliberately unrecorded — the reaper must still remove it (re-derivable scratch).

	if err := ReapScratch(dir); err != nil {
		t.Fatalf("ReapScratch: %v", err)
	}
	if exists(dir) {
		t.Fatalf("scratch dir %s should be gone when nothing is user-provided", dir)
	}
}

// TestReapScratch_KeepsUserFiles: a user-provided file survives; agent artifacts and
// unrecorded scratch are removed; the pruned manifest describes exactly the survivor.
func TestReapScratch_KeepsUserFiles(t *testing.T) {
	dir := t.TempDir()
	userPath := writeFile(t, dir, "uploads/report.pdf")
	agentPath := writeFile(t, dir, "derived.csv")
	strayPath := writeFile(t, dir, "sub/tmp.bin") // unrecorded scratch
	m, _ := New(filepath.Join(dir, ManifestName))
	_ = m.Append(Entry{Path: "derived.csv", Origin: OriginAgent})
	_ = m.Append(Entry{Path: "uploads/report.pdf", Origin: OriginUser})

	if err := ReapScratch(dir); err != nil {
		t.Fatalf("ReapScratch: %v", err)
	}
	if !exists(userPath) {
		t.Errorf("user file %s was removed, want kept", userPath)
	}
	if exists(agentPath) {
		t.Errorf("agent artifact %s survived, want removed", agentPath)
	}
	if exists(strayPath) {
		t.Errorf("unrecorded scratch %s survived, want removed", strayPath)
	}
	// The manifest persists and lists only the user file.
	m2, err := New(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatalf("reopen manifest: %v", err)
	}
	list := m2.List()
	if len(list) != 1 || list[0].Origin != OriginUser || list[0].Path != "uploads/report.pdf" {
		t.Fatalf("pruned manifest = %+v, want only the user entry", list)
	}
}

// TestReapScratch_NoManifest: a scratch dir without a manifest is wiped wholesale (fallback).
func TestReapScratch_NoManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "leftover.txt")
	if err := ReapScratch(dir); err != nil {
		t.Fatalf("ReapScratch: %v", err)
	}
	if exists(dir) {
		t.Fatalf("scratch dir %s should be gone with no manifest", dir)
	}
}
