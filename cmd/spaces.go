package cmd

import (
	"fmt"
	"strings"
	"sync"

	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/space"
)

// Space wiring shared by chat and serve (docs/adr/spaces.md). A space scopes the
// agent's memory (active-space shard over the global store) and injects its notes —
// the per-space profile — into the system prompt. The cmd layer owns all of this
// composition; internal/agent only sees a memory.Store, a prompt append, and the
// SpaceContext for the management tools.

// spaceScope resolves the active space to the memory view + prompt append a turn should
// run with. activeID "" means the global scope: the global store as-is, no append.
// shards caches one store per space so concurrent turns in the same space share a
// serialized writer (pass nil for single-threaded callers like local chat).
func spaceScope(spaces *space.Store, activeID string, global memory.Store, shards *spaceMemCache) (memory.Store, string, error) {
	if activeID == "" {
		return global, "", nil
	}
	sp, err := spaces.Get(activeID)
	if err != nil {
		return nil, "", fmt.Errorf("active space: %w", err)
	}
	var shard memory.Store
	if shards != nil {
		shard, err = shards.get(sp.ID)
	} else {
		shard, err = memory.NewPersistentStore(spaces.MemoryPath(sp.ID))
	}
	if err != nil {
		return nil, "", fmt.Errorf("open space %q memory: %w", sp.ID, err)
	}
	return memory.NewScopedStore(shard, global), spacePromptNote(sp), nil
}

// spacePromptNote renders the always-loaded profile section for an active space —
// the deliberate, per-space guidelines + context the operator or the agent itself
// maintains via update_space_notes. Appended to the system prompt like an AGENTS.md
// body, but agent-writable and scoped to the space (spaces.md §6).
func spacePromptNote(sp space.Space) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Active space: %s (id: %s)\n\n", sp.Name, sp.ID)
	b.WriteString("This session works in the space above — a named context with its own memory. " +
		"remember/recall are scoped to it (plus the shared global scope).\n")
	if strings.TrimSpace(sp.Notes) != "" {
		b.WriteString("\nSpace notes (standing profile — treat as current context):\n\n")
		b.WriteString(strings.TrimSpace(sp.Notes))
	} else {
		b.WriteString("\nThis space has no notes yet. Once you learn durable context here " +
			"(goals, level, preferences, state), save a brief profile with update_space_notes.")
	}
	return b.String()
}

// withSpaceNote returns appends plus the space note, without mutating the (shared)
// input slice. A "" note returns appends unchanged.
func withSpaceNote(appends []string, note string) []string {
	if note == "" {
		return appends
	}
	out := make([]string, 0, len(appends)+1)
	out = append(out, appends...)
	return append(out, note)
}

// spaceMemCache hands out one memory store per space id, so every session/turn writing
// to a space in one serve process shares a store (writes serialize through its lock
// instead of racing whole-file rewrites from independent instances).
type spaceMemCache struct {
	mu     sync.Mutex
	spaces *space.Store
	stores map[string]*memory.MemoryStore
}

func newSpaceMemCache(spaces *space.Store) *spaceMemCache {
	return &spaceMemCache{spaces: spaces, stores: map[string]*memory.MemoryStore{}}
}

func (c *spaceMemCache) get(id string) (*memory.MemoryStore, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.stores[id]; ok {
		return s, nil
	}
	s, err := memory.NewPersistentStore(c.spaces.MemoryPath(id))
	if err != nil {
		return nil, err
	}
	c.stores[id] = s
	return s, nil
}
