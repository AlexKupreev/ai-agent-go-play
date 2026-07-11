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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ManifestName is the on-disk filename of a session's manifest inside its scratch dir.
const ManifestName = "manifest.json"

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

// KeepOnly rewrites the manifest to retain only entries for which keep returns true,
// persisting the result atomically. The scratch reaper uses it to drop the agent-artifact
// entries whose files it removed, leaving the manifest describing exactly the surviving
// (user-provided) files.
func (m *Manifest) KeepOnly(keep func(Entry) bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.entries[:0:0]
	for _, e := range m.entries {
		if keep(e) {
			kept = append(kept, e)
		}
	}
	m.entries = kept
	return m.flushLocked()
}

// ReapScratch cleans a session's scratch directory when the session is closed (archived),
// preserving only the files the user provided (Origin == user) — which the file-upload
// lifecycle keeps until an explicit deletion (docs/planning/deletion.md §3). Agent-materialized
// artifacts and any unrecorded scratch are re-derivable, so they are removed; the manifest is
// pruned to the surviving user files.
//
// Today no upload path exists, so every manifest entry is agent-origin and this reaps the whole
// directory — identical to the os.RemoveAll it replaces — but it becomes correct-by-construction
// the moment user uploads land. (A *purge*, being an explicit whole-session deletion, removes
// everything and does not call this — see the engine's purge path.)
func ReapScratch(dir string) error {
	m, err := New(filepath.Join(dir, ManifestName))
	if err != nil {
		// Unreadable/corrupt manifest: fall back to a full wipe (the prior behavior).
		return os.RemoveAll(dir)
	}
	preserve := map[string]bool{}
	for _, e := range m.List() {
		if e.Origin == OriginUser {
			preserve[resolveUnder(dir, e.Path)] = true
		}
	}
	if len(preserve) == 0 {
		return os.RemoveAll(dir) // nothing to keep — the common (and, today, only) case
	}
	// Keep the manifest itself so the surviving user files stay tracked.
	preserve[filepath.Clean(filepath.Join(dir, ManifestName))] = true

	var dirs []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if !preserve[filepath.Clean(p)] {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Prune directories emptied by the removals, deepest first; leave dir itself. os.Remove
	// is a no-op error (ignored) on a directory that still holds a preserved file.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, p := range dirs {
		if p != dir {
			_ = os.Remove(p)
		}
	}
	// Drop the agent entries whose files were removed; keep only the user files.
	return m.KeepOnly(func(e Entry) bool { return e.Origin == OriginUser })
}

// resolveUnder resolves a manifest path (which may be relative to dir or absolute) to a
// cleaned absolute path for set membership, mirroring record_artifact's containment logic.
func resolveUnder(dir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(dir, path))
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
