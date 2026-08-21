// Package space implements switchable data contexts (docs/adr/spaces.md): a space is a
// named scope for the agent's own data — its memory entries and an always-loaded notes
// blob (the per-space "profile") — NOT a working directory. Exactly one space is active
// per session; memory written while it is active belongs to it, and recall merges the
// active space over the global scope.
//
// Storage is sharded per space (the ADR's governing decision, 2026-07-08): spaces live
// under <workspace>/.agent/spaces/<id>/ with a space.json (metadata + notes) and that
// space's own memory.json. The filesystem is the registry — List reads the directory,
// mirroring the session store — and a `remember` rewrites only the active space's file,
// not the whole corpus. SQLite stays the migration target behind memory.Store when a
// space's size or search bites.
package space

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// MaxNotesLen caps a space's notes blob. The notes are injected into the system prompt
// whenever the space is active, so they must stay brief — the cap forces the /compact
// discipline instead of letting the always-on prompt bloat (ADR §9).
const MaxNotesLen = 4000

// Space is one data scope's metadata. Its memory entries live beside it in the space's
// own memory.json, not in this struct.
type Space struct {
	ID        string    `json:"id"`   // slug of the name at creation; stable identity
	Name      string    `json:"name"` // display name
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store manages the spaces directory (dir-of-dirs, one subdirectory per space). Safe for
// concurrent use. The directory is created lazily on the first Create.
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore returns a store over dir (e.g. <workspace>/.agent/spaces).
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the root the store manages.
func (s *Store) Dir() string { return s.dir }

// MemoryPath returns the path of a space's own memory shard. The caller opens it with
// memory.NewPersistentStore; this store only names it.
func (s *Store) MemoryPath(id string) string {
	return filepath.Join(s.dir, id, "memory.json")
}

func (s *Store) metaPath(id string) string { return filepath.Join(s.dir, id, "space.json") }

// Create makes a new space named name, with the slugified name as its id. The name must
// slugify to something non-empty; an existing space with the same id is an error (spaces
// are few and human-named — no uniquifying suffixes).
func (s *Store) Create(name string) (Space, error) {
	id := Slug(name)
	if id == "" {
		return Space{}, fmt.Errorf("space: name %q has no usable characters", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.metaPath(id)); err == nil {
		return Space{}, fmt.Errorf("space %q already exists", id)
	}
	now := time.Now().UTC()
	sp := Space{ID: id, Name: strings.TrimSpace(name), Notes: "", CreatedAt: now, UpdatedAt: now}
	if err := s.write(sp); err != nil {
		return Space{}, err
	}
	return sp, nil
}

// Get returns the space with the exact id.
func (s *Store) Get(id string) (Space, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(id)
}

// Resolve finds a space by id or, failing that, by case-insensitive name — so
// `switch_space("Polish lessons")` works as well as the slug. A miss names the spaces
// that would have worked: every caller of Resolve (the switch_space tool, the CLI
// /space, the engine's session validation) reports the error straight to a human, and
// "no space \"polsih\"" without the list makes them go look it up.
func (s *Store) Resolve(nameOrID string) (Space, error) {
	if sp, err := s.Get(Slug(nameOrID)); err == nil {
		return sp, nil
	}
	all, err := s.List()
	if err != nil {
		return Space{}, err
	}
	want := strings.ToLower(strings.TrimSpace(nameOrID))
	for _, sp := range all {
		if strings.ToLower(sp.Name) == want || sp.ID == want {
			return sp, nil
		}
	}
	return Space{}, fmt.Errorf("no space %q; %s", nameOrID, available(all))
}

// maxListedSpaces bounds how many ids a not-found error names. Spaces are few and
// human-named by design, so the cap only guards against an error message growing
// unbounded with a runaway directory.
const maxListedSpaces = 20

// available renders the "what would have worked" half of a not-found error.
func available(all []Space) string {
	if len(all) == 0 {
		return "there are no spaces yet (create_space makes one)"
	}
	ids := make([]string, 0, len(all))
	for _, sp := range all {
		if len(ids) == maxListedSpaces {
			ids = append(ids, fmt.Sprintf("… and %d more", len(all)-maxListedSpaces))
			break
		}
		ids = append(ids, sp.ID)
	}
	return "available: " + strings.Join(ids, ", ")
}

// Save persists metadata changes (rename, notes). The notes cap is enforced here so no
// caller can grow the always-loaded profile past the prompt budget.
func (s *Store) Save(sp Space) error {
	if err := validateNotes(sp.Notes); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.read(sp.ID); err != nil {
		return err
	}
	sp.UpdatedAt = time.Now().UTC()
	return s.write(sp)
}

// List returns every space, most-recently-updated first. The directory listing IS the
// registry; a subdirectory without a readable space.json is skipped rather than failing
// the whole list.
func (s *Store) List() ([]Space, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Space
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sp, err := s.read(e.Name())
		if err != nil {
			continue
		}
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// read/write assume the caller holds the lock.
func (s *Store) read(id string) (Space, error) {
	if id == "" || id != Slug(id) {
		return Space{}, fmt.Errorf("no space %q", id)
	}
	data, err := os.ReadFile(s.metaPath(id))
	if os.IsNotExist(err) {
		return Space{}, fmt.Errorf("no space %q", id)
	}
	if err != nil {
		return Space{}, err
	}
	var sp Space
	if err := json.Unmarshal(data, &sp); err != nil {
		return Space{}, fmt.Errorf("parse space %s: %w", id, err)
	}
	if err := validateNotes(sp.Notes); err != nil {
		return Space{}, fmt.Errorf("parse space %s: %w", id, err)
	}
	return sp, nil
}

func validateNotes(notes string) error {
	if n := utf8.RuneCountInString(notes); n > MaxNotesLen {
		return fmt.Errorf("space notes exceed %d characters (%d) — keep the profile brief; move detail into memory entries", MaxNotesLen, n)
	}
	return nil
}

func (s *Store) write(sp Space) error {
	if err := os.MkdirAll(filepath.Join(s.dir, sp.ID), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.metaPath(sp.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.metaPath(sp.ID))
}

// Slug normalizes a space name to its directory-safe id: lowercase, alphanumeric runs
// joined by single hyphens. By construction the result contains no path separators, so
// an id can never escape the spaces directory.
func Slug(name string) string {
	var b strings.Builder
	pendingSep := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		switch {
		case isAlnum:
			if pendingSep && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingSep = false
			b.WriteRune(r)
		default:
			pendingSep = true
		}
	}
	return b.String()
}
