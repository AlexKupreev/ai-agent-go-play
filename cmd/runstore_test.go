package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/api"
)

// TestFileRunStore_RoundTrip proves a saved RunInfo is written under <base>/<id>/info.json
// and read back with its fields intact.
func TestFileRunStore_RoundTrip(t *testing.T) {
	base := t.TempDir()
	s := fileRunStore{base: base}

	info := api.RunInfo{ID: "abc123", Task: "do a thing", State: api.StateDone, Result: "did it", Steps: 3}
	s.Save(info)

	if _, err := os.Stat(filepath.Join(base, "abc123", "info.json")); err != nil {
		t.Fatalf("info.json not written: %v", err)
	}
	got, ok := s.Load("abc123")
	if !ok {
		t.Fatal("Load returned not-found for a saved run")
	}
	if got.Task != info.Task || got.State != info.State || got.Result != info.Result || got.Steps != info.Steps {
		t.Fatalf("loaded info mismatch: %+v", got)
	}
	if _, ok := s.Load("missing"); ok {
		t.Fatal("Load returned ok for a run that was never saved")
	}
}

// TestFileRunStore_RejectsUnsafeID guards against path traversal via the run id (it arrives
// from GET /runs/{id}): a "id" containing traversal must neither read nor write outside base.
func TestFileRunStore_RejectsUnsafeID(t *testing.T) {
	base := t.TempDir()
	s := fileRunStore{base: base}

	// A traversal id must not be persisted anywhere, and must not load.
	s.Save(api.RunInfo{ID: "..", State: api.StateDone})
	if _, err := os.Stat(filepath.Join(base, "info.json")); err == nil {
		t.Fatal("unsafe id escaped the base dir on Save")
	}
	if _, ok := s.Load(".."); ok {
		t.Fatal("Load accepted an unsafe id")
	}

	for _, bad := range []string{"", "..", "a/b", "../x", "a\x00b"} {
		if safeRunID(bad) {
			t.Errorf("safeRunID(%q) = true, want false", bad)
		}
	}
	for _, ok := range []string{"abc123", "2026-07-07T10-00-00_deadbeef", "a_b-c"} {
		if !safeRunID(ok) {
			t.Errorf("safeRunID(%q) = false, want true", ok)
		}
	}
}
