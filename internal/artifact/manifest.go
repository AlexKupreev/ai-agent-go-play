// Package artifact provides the chat planner's artifact manifest: a small, on-disk index
// of the working data materialized during a session (docs/adr/chat-planner.md §D4).
// The filesystem — not either agent's context — is the working memory (§D3): the executor
// writes sizeable intermediate data to a scratch dir and records it here via the
// record_artifact tool; the planner reads the rendered manifest each turn to know what
// data exists and its shape, instead of inferring it from transcript prose.
package artifact

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Origin records who produced an artifact. It drives retention (§D5): user-provided files
// are never auto-reaped, agent-derived files are subject to review. v1 has no reaper, but
// the provenance is recorded now so the lifecycle can be added without a migration.
type Origin string

const (
	OriginAgent Origin = "agent" // materialized by the executor
	OriginUser  Origin = "user"  // provided by the user (explicit attach)
)

// Entry is one manifest record: a materialized artifact plus the provenance and shape the
// planner needs to brief work over it.
type Entry struct {
	Path        string    `json:"path"`                  // location (relative to the scratch/workspace root)
	Origin      Origin    `json:"origin"`                // agent | user
	Source      string    `json:"source,omitempty"`      // where it came from / can be re-fetched
	Description string    `json:"description,omitempty"` // one-line shape/columns note
	Timestamp   time.Time `json:"timestamp"`             // when it was recorded
}

// Manifest is the on-disk index of a session's artifacts. It is backed by a JSON file and
// guarded by a mutex; chat turns are sequential, so contention is nil, but record_artifact
// and the planner's read can interleave with a cancelled turn. The file is the source of
// truth — Append rewrites it atomically so a crash can't leave a half-written index.
type Manifest struct {
	mu      sync.Mutex
	path    string
	entries []Entry
}

// New opens (or creates) the manifest at path, loading any existing entries. A missing
// file is not an error — it is the first-write case (the index starts empty).
func New(path string) (*Manifest, error) {
	m := &Manifest{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("read manifest %q: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m.entries); err != nil {
		return nil, fmt.Errorf("parse manifest %q: %w", path, err)
	}
	return m, nil
}

// Append records an artifact and persists the whole manifest. The timestamp is set here if
// the caller left it zero, so records are consistently stamped.
func (m *Manifest) Append(e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	m.entries = append(m.entries, e)
	return m.flushLocked()
}

// List returns a copy of the current entries (in record order).
func (m *Manifest) List() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Entry(nil), m.entries...)
}

// Render produces the planner-facing view of the manifest: one line per artifact with its
// path, origin, and shape note, under a header — appended to the planner prompt the way
// EnvironmentSummary is (§D4). Empty when there are no artifacts, so the caller adds no
// dangling header (mirrors internal/agent's toolRosterNote convention).
func (m *Manifest) Render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Artifacts already materialized (trust this for what data exists and its shape):\n")
	for _, e := range m.entries {
		fmt.Fprintf(&b, "- %s [%s]", e.Path, e.Origin)
		if e.Description != "" {
			fmt.Fprintf(&b, " — %s", e.Description)
		}
		if e.Source != "" {
			fmt.Fprintf(&b, " (source: %s)", e.Source)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// flushLocked writes the manifest to disk atomically (temp file + rename). Caller holds mu.
func (m *Manifest) flushLocked() error {
	data, err := json.MarshalIndent(m.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	return nil
}
