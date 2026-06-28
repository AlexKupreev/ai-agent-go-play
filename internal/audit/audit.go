// Package audit is the append-only event log that makes self-extension
// reviewable: every capability a tool exercises (or is denied) is recorded here.
// A JSONL file is the Phase 2 backing store; a richer store (e.g. SQLite) can
// implement Recorder later without touching callers.
package audit

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Event types. More are added as later phases record richer history.
const (
	EventCapabilityExercised = "capability_exercised"
	EventCapabilityDenied    = "capability_denied"
	EventToolAuthored        = "tool_authored"
)

// Event is one append-only record.
type Event struct {
	Type   string         `json:"type"`
	Run    string         `json:"run,omitempty"`
	At     time.Time      `json:"at"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Recorder appends events. Implementations must be safe for concurrent use.
type Recorder interface {
	Record(Event)
}

// MemoryRecorder keeps events in memory; useful for tests and inspection.
type MemoryRecorder struct {
	mu     sync.Mutex
	Events []Event
}

func (m *MemoryRecorder) Record(e Event) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Events = append(m.Events, e)
}

// Snapshot returns a copy of the recorded events.
func (m *MemoryRecorder) Snapshot() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.Events))
	copy(out, m.Events)
	return out
}

// JSONLRecorder appends one JSON object per line to a file.
type JSONLRecorder struct {
	mu sync.Mutex
	f  *os.File
}

// NewJSONLRecorder opens (creating if needed) an append-only JSONL file.
func NewJSONLRecorder(path string) (*JSONLRecorder, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &JSONLRecorder{f: f}, nil
}

func (r *JSONLRecorder) Record(e Event) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.f.Write(b)
	r.f.Write([]byte("\n"))
}

func (r *JSONLRecorder) Close() error {
	return r.f.Close()
}
