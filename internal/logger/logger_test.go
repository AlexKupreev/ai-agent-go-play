package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/provider"
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

func TestLogResponseKeepsMalformedToolArgumentsSerializable(t *testing.T) {
	l, err := NewWithID(t.TempDir(), "run-malformed")
	if err != nil {
		t.Fatal(err)
	}
	l.LogResponse(0, "", []provider.ToolCall{{ID: "c1", Name: "web_search", Input: `{"query":"truncated`}}, provider.StopToolCalls, provider.Usage{OutputTokens: 128000}, 12)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("transcript line is invalid JSON: %v\n%s", err, data)
	}
	calls := entry["tool_calls"].([]any)
	input := calls[0].(map[string]any)["input"]
	if input != `{"query":"truncated` {
		t.Fatalf("logged input = %q", input)
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
