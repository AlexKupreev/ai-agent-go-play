// Package audit is the append-only event log that makes self-extension
// reviewable: every capability a tool exercises, fails to execute, or is denied
// is recorded here.
// A JSONL file is the Phase 2 backing store; a richer store (e.g. SQLite) can
// implement Recorder later without touching callers.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Event types. More are added as later phases record richer history.
const (
	EventCapabilityExercised = "capability_exercised"
	EventCapabilityFailed    = "capability_failed"
	EventCapabilityDenied    = "capability_denied"
	EventToolAuthored        = "tool_authored"
	EventToolRevoked         = "tool_revoked"
	EventMemoryWrite         = "memory_write"
	EventRunUsage            = "run_usage"
	EventSessionPurged       = "session_purged"
)

// CapabilityOutcome is the result of one brokered or otherwise audited capability
// invocation. Failed means policy allowed the operation but execution failed; Denied
// is reserved for a policy refusal before (or during) execution.
type CapabilityOutcome string

const (
	CapabilityExercised CapabilityOutcome = EventCapabilityExercised
	CapabilityFailed    CapabilityOutcome = EventCapabilityFailed
	CapabilityDenied    CapabilityOutcome = EventCapabilityDenied
)

// Event is one append-only record.
type Event struct {
	Type   string         `json:"type"`
	Run    string         `json:"run,omitempty"`
	At     time.Time      `json:"at"`
	Fields map[string]any `json:"fields,omitempty"`
}

// NewCapabilityEvent builds a consistently shaped capability audit record. Extra
// fields may carry non-sensitive failure classification such as error_class or status.
func NewCapabilityEvent(outcome CapabilityOutcome, run, capability, arg string, extra map[string]any) Event {
	fields := map[string]any{"capability": capability, "arg": arg}
	for key, value := range extra {
		fields[key] = value
	}
	return Event{Type: string(outcome), Run: run, Fields: fields}
}

// Recorder appends events. Implementations must be safe for concurrent use.
type Recorder interface {
	Record(Event)
}

// Filter narrows a Tail query. A zero field is a wildcard: an empty Run matches
// every run (including management-plane events with no run), an empty Type matches
// every type.
type Filter struct {
	Run  string
	Type string
}

func (f Filter) matches(e Event) bool {
	if f.Run != "" && e.Run != f.Run {
		return false
	}
	if f.Type != "" && e.Type != f.Type {
		return false
	}
	return true
}

// Reader reads back recorded events. It is the query side of the audit log, so a
// management plane can browse everything effectful (capability use, tool authoring
// and revocation, memory writes) without knowing the backing store.
type Reader interface {
	// Tail returns up to the last n events matching filter, oldest first. n <= 0
	// returns all matches.
	Tail(n int, filter Filter) ([]Event, error)
}

// Recorders fans one event out to several recorders (e.g. a per-run session log and
// the process-wide log). Safe for concurrent use if its members are.
type Recorders []Recorder

func (rs Recorders) Record(e Event) {
	for _, r := range rs {
		r.Record(e)
	}
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

// Tail returns up to the last n events matching filter, oldest first.
func (m *MemoryRecorder) Tail(n int, filter Filter) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return tailMatching(m.Events, n, filter), nil
}

// tailMatching returns the last n events (oldest first) that pass filter. n <= 0
// returns all matches.
func tailMatching(events []Event, n int, filter Filter) []Event {
	matched := make([]Event, 0, len(events))
	for _, e := range events {
		if filter.matches(e) {
			matched = append(matched, e)
		}
	}
	if n > 0 && len(matched) > n {
		matched = matched[len(matched)-n:]
	}
	return matched
}

// JSONLRecorder appends one JSON object per line to a file. It is both the write
// side (Record) and, via Tail, the read side of the log at that path.
type JSONLRecorder struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// NewJSONLRecorder opens (creating if needed) an append-only JSONL file.
func NewJSONLRecorder(path string) (*JSONLRecorder, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &JSONLRecorder{f: f, path: path}, nil
}

// Tail reads the log file and returns up to the last n events matching filter,
// oldest first. It re-reads the file each call (fine for a human-scale audit log; a
// SQLite backing is the stated upgrade path when this bites — design §9). The write
// lock is held so a concurrent Record cannot interleave a partial line.
func (r *JSONLRecorder) Tail(n int, filter Filter) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	f, err := os.Open(r.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	sc := bufio.NewScanner(f)
	// Audit records embed tool output; lift the line cap above the 64 KiB default.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue // skip a corrupt line rather than fail the whole read
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return tailMatching(events, n, filter), nil
}

func (r *JSONLRecorder) Record(e Event) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		// An audit record we cannot serialize is a bug, not something to drop
		// silently — surface it. (The append-only log is the security record.)
		fmt.Fprintf(os.Stderr, "audit: dropping unserializable %q event: %v\n", e.Type, err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Write(append(b, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "audit: failed to write %q event: %v\n", e.Type, err)
	}
}

func (r *JSONLRecorder) Close() error {
	return r.f.Close()
}
