package memory

// ScopedStore composes a space's memory store over the global one (docs/adr/spaces.md §4):
// writes land in the active space; reads see the union, with the active space shadowing the
// global scope on key collisions. The remember/recall tools keep taking a plain Store —
// the cmd layer hands them this scoped view when a space is active, so nothing above the
// store changes. With no active space callers use the global store directly (no ScopedStore).
type ScopedStore struct {
	active Store // the active space's shard; receives all writes
	global Store // the unscoped store, visible in every space
}

// NewScopedStore returns the active-over-global view. Both stores are required.
func NewScopedStore(active, global Store) *ScopedStore {
	return &ScopedStore{active: active, global: global}
}

// Put writes to the active space — facts saved while a space is active belong to it.
func (s *ScopedStore) Put(e Entry) error { return s.active.Put(e) }

// Get prefers the active space's entry, falling back to the global scope.
func (s *ScopedStore) Get(key string) (Entry, bool) {
	if e, ok := s.active.Get(key); ok {
		return e, true
	}
	return s.global.Get(key)
}

// Delete removes the key from whichever scope holds it, active first. Deleting a
// shadowing space entry re-exposes the global one — the same layering as Get.
func (s *ScopedStore) Delete(key string) error {
	if _, ok := s.active.Get(key); ok {
		return s.active.Delete(key)
	}
	return s.global.Delete(key)
}

// List returns active-space entries followed by unshadowed global ones. The active
// space leads regardless of timestamps: when a space is active its own notes are the
// relevant ones, and a bounded listing should surface them first.
func (s *ScopedStore) List() []Entry {
	return s.merge(s.active.List(), s.global.List())
}

// Search returns up to k entries: the active space's matches first, then unshadowed
// global matches, each ranked by its own store.
func (s *ScopedStore) Search(query string, k int) []Entry {
	out := s.merge(s.active.Search(query, k), s.global.Search(query, k))
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// merge appends the global entries whose keys the active scope does not shadow. The
// shadow check is against the whole active store (not just the active result slice), so
// a global entry never resurfaces under a key the space has overridden.
func (s *ScopedStore) merge(active, global []Entry) []Entry {
	out := make([]Entry, 0, len(active)+len(global))
	out = append(out, active...)
	for _, e := range global {
		if _, shadowed := s.active.Get(e.Key); !shadowed {
			out = append(out, e)
		}
	}
	return out
}
