package cmd

import (
	"fmt"
	"strings"
	"sync"

	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/space"
)

// Space wiring shared by chat and serve (docs/adr/spaces.md). A space scopes the
// agent's memory (active-space shard over the global store) and injects its guidance —
// the per-space profile — into the system prompt. The cmd layer owns all of this
// composition; internal/agent only sees a memory.Store, a prompt append, and the
// SpaceContext for the management tools.

// spaceResolver is the engine's SpaceResolver over a space store: it accepts an id or a
// display name and returns the canonical id to store on the session. It is what makes
// `POST /sessions` / `PATCH /sessions/{id}` reject an unknown space up front — the store's
// Resolve error already names the spaces that would have worked — instead of letting a typo
// stick and fail the session's next turn.
func spaceResolver(spaces *space.Store) func(string) (string, error) {
	return func(nameOrID string) (string, error) {
		sp, err := spaces.Resolve(nameOrID)
		if err != nil {
			return "", err
		}
		return sp.ID, nil
	}
}

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
	return memory.NewScopedStore(shard, global), spacePromptGuidance(sp), nil
}

// spacePromptGuidance renders the always-loaded guidance section for an active space —
// the deliberate, per-space guidelines + context the operator or the agent itself
// maintains via update_space_guidance. Appended to the system prompt like an AGENTS.md
// body, but agent-writable and scoped to the space (spaces.md §6).
func spacePromptGuidance(sp space.Space) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Active space: %s (id: %s)\n\n", sp.Name, sp.ID)
	b.WriteString("This session works in the space above — a named context with its own memory. " +
		"remember/recall are scoped to it (plus the shared global scope).\n")
	if strings.TrimSpace(sp.Guidance) != "" {
		b.WriteString("\nSpace guidance (standing profile and instructions — treat as current context):\n\n")
		b.WriteString(strings.TrimSpace(sp.Guidance))
	} else {
		b.WriteString("\nThis space has no guidance yet. Once you learn durable context here " +
			"(goals, level, preferences, state), save a brief profile with update_space_guidance.")
	}
	return b.String()
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
