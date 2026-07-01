package logger

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewWithID_UsesGivenBaseDir confirms the transcript lands under the supplied
// sessions root, so distinct agents can keep separate transcripts.
func TestNewWithID_UsesGivenBaseDir(t *testing.T) {
	base := t.TempDir()
	l, err := NewWithID(base, "run-xyz")
	if err != nil {
		t.Fatalf("NewWithID: %v", err)
	}
	defer l.Close()

	want := filepath.Join(base, "run-xyz")
	if l.SessionDir != want {
		t.Fatalf("SessionDir = %q, want %q", l.SessionDir, want)
	}
	if _, err := os.Stat(l.Path); err != nil {
		t.Fatalf("run.jsonl not created: %v", err)
	}
	if _, err := os.Stat(l.ArtifactsDir); err != nil {
		t.Fatalf("artifacts dir not created: %v", err)
	}
}

// TestNewWithID_EmptyBaseDirFallsBack confirms an empty base uses the default
// sessions location (so callers that don't override keep working).
func TestNewWithID_EmptyBaseDirFallsBack(t *testing.T) {
	// Redirect HOME so the default lands in a temp dir, not the real profile.
	home := t.TempDir()
	t.Setenv("HOME", home)

	l, err := NewWithID("", "run-abc")
	if err != nil {
		t.Fatalf("NewWithID: %v", err)
	}
	defer l.Close()

	want := filepath.Join(home, ".local", "share", "ai-agent", "sessions", "run-abc")
	if l.SessionDir != want {
		t.Fatalf("SessionDir = %q, want %q", l.SessionDir, want)
	}
}
