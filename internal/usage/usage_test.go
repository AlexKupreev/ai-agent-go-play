package usage

import (
	"path/filepath"
	"testing"
	"time"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/provider"
)

func TestLedger_SessionAndToday(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	// Two turns in session s1 + one plain run, all today.
	Record(rec, "r1", "s1", provider.Usage{InputTokens: 100, OutputTokens: 20, CachedTokens: 5}, 2)
	Record(rec, "r2", "s1", provider.Usage{InputTokens: 50, OutputTokens: 10}, 1)
	Record(rec, "r3", "", provider.Usage{InputTokens: 7, OutputTokens: 3}, 1)
	// A run from two days ago must be excluded from Today.
	rec.Record(audit.Event{
		Type: audit.EventRunUsage, Run: "old", At: time.Now().AddDate(0, 0, -2),
		Fields: map[string]any{"input_tokens": int64(999), "output_tokens": int64(999)},
	})

	l := NewLedger(rec)

	if u, turns := l.Session("s1"); turns != 2 || u.InputTokens != 150 || u.OutputTokens != 30 || u.CachedTokens != 5 {
		t.Fatalf("Session(s1) = %+v, %d turns; want {150 30 5}, 2", u, turns)
	}
	if _, n := l.Session("nope"); n != 0 {
		t.Fatalf("Session(unknown) count = %d, want 0", n)
	}
	if u, runs := l.Today(); runs != 3 || u.InputTokens != 157 {
		t.Fatalf("Today() = %+v, %d runs; want input 157, 3 runs (old excluded)", u, runs)
	}
}

// The JSONL backing round-trips numbers as float64; the ledger must still sum them.
func TestLedger_JSONLFloatFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	rec, err := audit.NewJSONLRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	Record(rec, "r1", "s1", provider.Usage{InputTokens: 1000, OutputTokens: 250, CachedTokens: 64}, 3)
	rec.Close()

	// Read back through a fresh recorder (Tail re-reads the file).
	rec2, err := audit.NewJSONLRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rec2.Close()
	l := NewLedger(rec2)

	u, turns := l.Session("s1")
	if turns != 1 || u.InputTokens != 1000 || u.OutputTokens != 250 || u.CachedTokens != 64 {
		t.Fatalf("Session from JSONL = %+v, %d; want {1000 250 64}, 1", u, turns)
	}
}

func TestLedger_NilSafe(t *testing.T) {
	var l *Ledger
	if u, n := l.Today(); n != 0 || u.InputTokens != 0 {
		t.Fatalf("nil ledger Today() = %+v, %d; want zero", u, n)
	}
	if u, n := NewLedger(nil).Today(); n != 0 || u.InputTokens != 0 {
		t.Fatalf("NewLedger(nil) Today() = %+v, %d; want zero", u, n)
	}
}
