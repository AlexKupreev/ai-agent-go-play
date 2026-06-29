// Package memory is the agent's long-term, cross-run note store. The agent saves
// durable facts (preferences, project details, results worth keeping) and recalls
// them in later runs. It is surfaced as built-in tools (remember/recall) and, like
// the tool catalog, persists as a JSON file.
//
// On a private, single-user box (design §1) memory is part of the trusted built-in
// tier: the engine writing notes is trusted. Writes are audited like any effect; the
// store itself stays out of the sandbox (it is not Exposed to authored code in v1).
//
// A richer transactional store (SQLite is the stated end goal) can implement Store
// later without touching callers — same migration path as the tool Registry.
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one stored note, keyed by a stable id the agent chooses.
type Entry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"` // run id that last wrote it
	UpdatedAt time.Time `json:"updated_at"`
}

// Store holds the agent's notes. Implementations must be safe for concurrent use.
type Store interface {
	// Put upserts an entry by Key, stamping UpdatedAt.
	Put(e Entry) error
	// Get returns the entry for key, if present.
	Get(key string) (Entry, bool)
	// Search returns up to k entries ranked by relevance to query (key + value +
	// tags). k <= 0 returns all matches; no term overlap means no result. Token
	// overlap, matching the tool catalog's search.
	Search(query string, k int) []Entry
	// List returns every entry, most-recently-updated first.
	List() []Entry
	// Delete removes key, returning whether it existed.
	Delete(key string) error
}

// MemoryStore is an in-memory Store with optional JSON-file persistence. With a
// path it loads at construction and writes back on every mutation (atomic
// temp+rename, like the tool catalog). Without one it is purely in-memory
// (tests / ephemeral use). Safe for concurrent use.
type MemoryStore struct {
	mu    sync.RWMutex
	byKey map[string]Entry
	path  string // "" disables persistence
}

// NewMemoryStore returns a non-persistent store (tests / ephemeral use).
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byKey: map[string]Entry{}}
}

// NewPersistentStore returns a store backed by a JSON file at path. Existing
// entries are loaded immediately; subsequent changes are written back.
func NewPersistentStore(path string) (*MemoryStore, error) {
	s := &MemoryStore{byKey: map[string]Entry{}, path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *MemoryStore) Put(e Entry) error {
	if strings.TrimSpace(e.Key) == "" {
		return fmt.Errorf("memory: key must not be empty")
	}
	e.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey[e.Key] = e
	return s.save()
}

func (s *MemoryStore) Get(key string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.byKey[key]
	return e, ok
}

func (s *MemoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byKey[key]; !ok {
		return fmt.Errorf("memory: no entry %q", key)
	}
	delete(s.byKey, key)
	return s.save()
}

func (s *MemoryStore) List() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.collect(func(Entry) bool { return true })
}

func (s *MemoryStore) Search(query string, k int) []Entry {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	s.mu.RLock()
	scored := make([]struct {
		e     Entry
		score int
	}, 0)
	for _, e := range s.byKey {
		hay := tokenize(e.Key + " " + e.Value + " " + strings.Join(e.Tags, " "))
		score := 0
		for t := range terms {
			if _, ok := hay[t]; ok {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, struct {
				e     Entry
				score int
			}{e, score})
		}
	}
	s.mu.RUnlock()

	// Highest score first; ties broken by most-recently-updated for determinism.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].e.UpdatedAt.After(scored[j].e.UpdatedAt)
	})

	out := make([]Entry, 0, len(scored))
	for _, s := range scored {
		if k > 0 && len(out) >= k {
			break
		}
		out = append(out, s.e)
	}
	return out
}

// collect returns matching entries, most-recently-updated first. Caller holds the lock.
func (s *MemoryStore) collect(keep func(Entry) bool) []Entry {
	out := make([]Entry, 0, len(s.byKey))
	for _, e := range s.byKey {
		if keep(e) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// save writes all entries to the JSON file atomically. Caller holds the lock.
func (s *MemoryStore) save() error {
	if s.path == "" {
		return nil
	}
	entries := s.collect(func(Entry) bool { return true })
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// load reads the JSON file (if any) into the store. Caller is the constructor.
func (s *MemoryStore) load() error {
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse memory store %s: %w", s.path, err)
	}
	for _, e := range entries {
		s.byKey[e.Key] = e
	}
	return nil
}

// tokenize lowercases and splits on non-alphanumeric runs into a set.
func tokenize(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		out[f] = struct{}{}
	}
	return out
}
