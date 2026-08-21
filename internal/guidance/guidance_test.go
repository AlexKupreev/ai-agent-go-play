package guidance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
)

func TestFileStoreRoundTripClearAndAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".agent", "guidance.md")
	rec := &audit.MemoryRecorder{}
	store := NewFileStore(path, "global", rec)

	if got, err := store.Get(); err != nil || got != "" {
		t.Fatalf("missing Get = %q, %v; want empty", got, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(""); err != nil {
		t.Fatalf("clear manually-created empty file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty guidance file after clear: %v, want missing", err)
	}
	text := "Odpowiadaj po polsku 🐻"
	if err := store.Set(text); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, err := store.Get(); err != nil || got != text {
		t.Fatalf("Get = %q, %v; want exact text", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("guidance mode = %v, %v; want 0600", info, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".guidance-*.tmp")); len(matches) != 0 {
		t.Fatalf("temporary files remain after atomic rename: %v", matches)
	}

	// Replacing with the same value is idempotent and does not duplicate the audit event.
	if err := store.Set(text); err != nil {
		t.Fatalf("idempotent Set: %v", err)
	}
	if got := len(rec.Snapshot()); got != 1 {
		t.Fatalf("events after unchanged Set = %d, want 1", got)
	}

	if err := store.Set(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("guidance file after clear: %v, want missing", err)
	}
	if err := store.Set(""); err != nil {
		t.Fatalf("idempotent clear: %v", err)
	}

	events := rec.Snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %d, want set + clear", len(events))
	}
	set := events[0]
	if set.Type != audit.EventGuidanceUpdated || set.Fields["scope"] != "global" ||
		set.Fields["previous_size"] != 0 || set.Fields["resulting_size"] != CharCount(text) {
		t.Fatalf("set audit metadata = %+v", set)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), text) {
		t.Fatalf("audit leaked guidance body: %s", encoded)
	}
	if set.Fields["previous_hash"] == "" || set.Fields["resulting_hash"] == "" {
		t.Fatalf("audit hashes missing: %+v", set.Fields)
	}
}

func TestUnicodeCharacterLimit(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "guidance.md"), "", nil)
	atLimit := strings.Repeat("🐻", MaxChars)
	if CharCount(atLimit) != MaxChars {
		t.Fatalf("CharCount(atLimit) = %d", CharCount(atLimit))
	}
	if err := store.Set(atLimit); err != nil {
		t.Fatalf("Set at Unicode limit: %v", err)
	}
	if err := store.Set(atLimit + "🐻"); err == nil || !strings.Contains(err.Error(), "4001") {
		t.Fatalf("Set over Unicode limit = %v, want 4001-character error", err)
	}
}

func TestOversizedFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guidance.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxChars+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path, "global", nil).Get(); err == nil {
		t.Fatal("Get oversized guidance returned nil error")
	}
}
