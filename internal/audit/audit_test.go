package audit

import (
	"path/filepath"
	"testing"
)

func seed(r Recorder) {
	r.Record(Event{Type: EventCapabilityExercised, Run: "run-a", Fields: map[string]any{"n": 1}})
	r.Record(Event{Type: EventToolAuthored, Run: "run-a", Fields: map[string]any{"n": 2}})
	r.Record(Event{Type: EventCapabilityExercised, Run: "run-b", Fields: map[string]any{"n": 3}})
	r.Record(Event{Type: EventToolRevoked, Fields: map[string]any{"n": 4}}) // management-plane, no run
}

// tailCases is shared across the memory and JSONL backings so both prove the same
// filter semantics.
func tailCases(t *testing.T, tail func(n int, f Filter) []Event) {
	t.Helper()

	all := tail(0, Filter{})
	if len(all) != 4 {
		t.Fatalf("unfiltered = %d events, want 4", len(all))
	}
	// Oldest first.
	if all[0].Fields["n"] != float64(1) && all[0].Fields["n"] != 1 {
		t.Fatalf("not oldest-first: %+v", all[0])
	}

	byRun := tail(0, Filter{Run: "run-a"})
	if len(byRun) != 2 {
		t.Fatalf("run-a = %d events, want 2", len(byRun))
	}

	byType := tail(0, Filter{Type: EventCapabilityExercised})
	if len(byType) != 2 {
		t.Fatalf("capability_exercised = %d events, want 2", len(byType))
	}

	both := tail(0, Filter{Run: "run-a", Type: EventCapabilityExercised})
	if len(both) != 1 {
		t.Fatalf("run-a + capability_exercised = %d events, want 1", len(both))
	}

	// limit keeps the LAST n matches.
	last := tail(1, Filter{})
	if len(last) != 1 || (last[0].Type != EventToolRevoked) {
		t.Fatalf("limit=1 did not return the last event: %+v", last)
	}
}

func TestMemoryRecorder_Tail(t *testing.T) {
	m := &MemoryRecorder{}
	seed(m)
	tailCases(t, func(n int, f Filter) []Event {
		ev, err := m.Tail(n, f)
		if err != nil {
			t.Fatalf("Tail: %v", err)
		}
		return ev
	})
}

func TestJSONLRecorder_Tail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	r, err := NewJSONLRecorder(path)
	if err != nil {
		t.Fatalf("NewJSONLRecorder: %v", err)
	}
	defer r.Close()
	seed(r)
	tailCases(t, func(n int, f Filter) []Event {
		ev, err := r.Tail(n, f)
		if err != nil {
			t.Fatalf("Tail: %v", err)
		}
		return ev
	})
}

func TestJSONLRecorder_TailMissingFileIsEmpty(t *testing.T) {
	r := &JSONLRecorder{path: filepath.Join(t.TempDir(), "does-not-exist.jsonl")}
	ev, err := r.Tail(0, Filter{})
	if err != nil {
		t.Fatalf("Tail on missing file: %v", err)
	}
	if len(ev) != 0 {
		t.Fatalf("missing file = %d events, want 0", len(ev))
	}
}

func TestRecorders_FansOut(t *testing.T) {
	a, b := &MemoryRecorder{}, &MemoryRecorder{}
	rs := Recorders{a, b}
	rs.Record(Event{Type: EventMemoryWrite})
	if len(a.Snapshot()) != 1 || len(b.Snapshot()) != 1 {
		t.Fatalf("fan-out did not reach both recorders: a=%d b=%d", len(a.Snapshot()), len(b.Snapshot()))
	}
}
