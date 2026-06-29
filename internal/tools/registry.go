package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Registry holds agent-authored and natively-registered tools, separate from the
// built-in []Tool. The executor lists built-ins first, then the registry's tools
// in registration order (append-only, stable) so the serialized tool defs stay
// cache-friendly.
//
// A richer transactional store (SQLite is the stated end goal) can implement this
// interface later without touching callers.
type Registry interface {
	// Register validates a spec, assigns provenance (version, code hash, stable
	// order), stores it, and returns the stored copy. Re-registering an existing
	// name updates it in place and bumps its version, preserving its position.
	Register(spec ToolSpec) (ToolSpec, error)
	// Get returns the named tool, if present.
	Get(name string) (ToolSpec, bool)
	// List returns tools of the given scope in registration order. ScopeAny
	// ("") returns every tool.
	List(scope Scope) []ToolSpec
	// Search returns up to k tools ranked by relevance to query (name +
	// description). k <= 0 returns all matches. Tools with no term overlap are
	// excluded. (BM25-lite refinement is Phase 3d; this is token overlap.)
	Search(query string, k int) []ToolSpec
	// Revoke removes the named tool, returning whether it existed.
	Revoke(name string) bool
}

// MemoryRegistry is an in-memory Registry with optional JSON-catalog persistence
// for the persistent scopes (User/Shared). Ephemeral tools are never written to
// disk. Safe for concurrent use.
type MemoryRegistry struct {
	mu     sync.RWMutex
	seq    uint64
	byName map[string]ToolSpec
	path   string // catalog path; "" disables persistence
}

// NewMemoryRegistry returns a non-persistent registry (ephemeral-only use/tests).
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{byName: map[string]ToolSpec{}}
}

// NewPersistentRegistry returns a registry backed by a JSON catalog at path.
// Existing persistent tools are loaded immediately; subsequent changes to
// persistent-scope tools are written back to path.
func NewPersistentRegistry(path string) (*MemoryRegistry, error) {
	r := &MemoryRegistry{byName: map[string]ToolSpec{}, path: path}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *MemoryRegistry) Register(spec ToolSpec) (ToolSpec, error) {
	if err := spec.validate(); err != nil {
		return ToolSpec{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	spec.CodeHash = spec.computeHash()
	if existing, ok := r.byName[spec.Name]; ok {
		spec.seq = existing.seq // keep position so the append-only order is stable
		spec.Version = existing.Version + 1
	} else {
		r.seq++
		spec.seq = r.seq
		spec.Version = 1
	}
	r.byName[spec.Name] = spec

	if spec.Scope.persistent() {
		if err := r.save(); err != nil {
			return ToolSpec{}, fmt.Errorf("persist tool %q: %w", spec.Name, err)
		}
	}
	return spec, nil
}

func (r *MemoryRegistry) Get(name string) (ToolSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byName[name]
	return s, ok
}

func (r *MemoryRegistry) List(scope Scope) []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.collect(func(s ToolSpec) bool {
		return scope == ScopeAny || s.Scope == scope
	})
}

func (r *MemoryRegistry) Revoke(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byName[name]
	if !ok {
		return false
	}
	delete(r.byName, name)
	if s.Scope.persistent() {
		// Best-effort: the tool is already gone from the live set; a failed write
		// only means it reappears on the next load, which is the safe direction.
		_ = r.save()
	}
	return true
}

func (r *MemoryRegistry) Search(query string, k int) []ToolSpec {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	r.mu.RLock()
	scored := make([]struct {
		spec  ToolSpec
		score int
	}, 0)
	for _, s := range r.byName {
		hay := tokenize(s.Name + " " + s.Description)
		score := 0
		for t := range terms {
			if _, ok := hay[t]; ok {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, struct {
				spec  ToolSpec
				score int
			}{s, score})
		}
	}
	r.mu.RUnlock()

	// Highest score first; ties broken by registration order for determinism.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].spec.seq < scored[j].spec.seq
	})

	out := make([]ToolSpec, 0, len(scored))
	for _, s := range scored {
		if k > 0 && len(out) >= k {
			break
		}
		out = append(out, s.spec)
	}
	return out
}

// collect returns matching specs in registration (seq) order. Caller holds the lock.
func (r *MemoryRegistry) collect(keep func(ToolSpec) bool) []ToolSpec {
	out := make([]ToolSpec, 0, len(r.byName))
	for _, s := range r.byName {
		if keep(s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// save writes the persistent tools to the catalog atomically. Caller holds the lock.
func (r *MemoryRegistry) save() error {
	if r.path == "" {
		return nil
	}
	specs := r.collect(func(s ToolSpec) bool {
		// Native handlers cannot be serialized; only script-backed persistent
		// tools belong in the catalog.
		return s.Scope.persistent() && s.Impl.Kind == ImplScript
	})
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// load reads the catalog (if any) into the registry, assigning seq in catalog
// order so the live order is reproducible across restarts. Caller is the
// constructor (no concurrent access yet).
func (r *MemoryRegistry) load() error {
	if r.path == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var specs []ToolSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return fmt.Errorf("parse tool catalog %s: %w", r.path, err)
	}
	for _, s := range specs {
		r.seq++
		s.seq = r.seq
		r.byName[s.Name] = s
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
