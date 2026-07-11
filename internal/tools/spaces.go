package tools

import (
	"context"
	"fmt"
	"strings"

	"ai-agent-go-play/internal/space"
)

// SpaceContext wires the space tools (docs/adr/spaces.md §6). Store nil ⇒ the tools are
// omitted, like every optional dep. ActiveID is the session's active space ("" ⇒ global
// scope). Switch persists a new active space for the session — effective from the NEXT
// turn (the current executor's memory scope and prompt were fixed at construction); nil
// Switch (e.g. a one-shot run with no session) makes switch_space explain that instead.
type SpaceContext struct {
	Store    *space.Store
	ActiveID string
	Switch   func(id string) error
}

// NewSpaceTools returns the space management built-ins: list_spaces, create_space,
// switch_space, space_notes, update_space_notes. All trusted, not sandbox-exposed
// (like remember) — authored code cannot re-scope the agent's memory via call_tool.
func NewSpaceTools(sc SpaceContext) []Tool {
	return []Tool{
		{
			Name: "list_spaces",
			Description: "List the agent's spaces (switchable data contexts, each with its own memory and notes). " +
				"Shows each space's id, name, a notes preview, and which one is active. Use switch_space to change.",
			Parameters: map[string]any{},
			Required:   []string{},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				spaces, err := sc.Store.List()
				if err != nil {
					return fmt.Sprintf("list_spaces failed: %v", err), nil
				}
				if len(spaces) == 0 {
					return "no spaces yet (memory is in the global scope); create_space makes one", nil
				}
				var b strings.Builder
				for _, sp := range spaces {
					marker := "  "
					if sp.ID == sc.ActiveID {
						marker = "* "
					}
					fmt.Fprintf(&b, "%s%s (%q)", marker, sp.ID, sp.Name)
					if n := notesPreview(sp.Notes); n != "" {
						fmt.Fprintf(&b, " — %s", n)
					}
					b.WriteByte('\n')
				}
				if sc.ActiveID == "" {
					b.WriteString("(no space active — memory reads/writes use the global scope)")
				}
				return strings.TrimRight(b.String(), "\n"), nil
			},
		},
		{
			Name: "create_space",
			Description: "Create a new space (a named data context with its own memory and notes) and return its id. " +
				"Does not switch to it — call switch_space when the user wants to work in it.",
			Parameters: map[string]any{
				"name": map[string]any{"type": "string", "description": "human name for the space, e.g. \"Polish lessons\""},
			},
			Required: []string{"name"},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				name, _ := args["name"].(string)
				sp, err := sc.Store.Create(name)
				if err != nil {
					return fmt.Sprintf("create_space failed: %v", err), nil
				}
				return fmt.Sprintf("created space %q (id %s); use switch_space to activate it", sp.Name, sp.ID), nil
			},
		},
		{
			Name: "switch_space",
			Description: "Set the session's active space by id or name — future memory writes and recalls scope to it " +
				"(plus the global scope), and its notes load into context. Effective from the next turn. " +
				"Pass an empty string to return to the global scope.",
			Parameters: map[string]any{
				"space": map[string]any{"type": "string", "description": "space id or name; \"\" switches back to the global scope"},
			},
			Required: []string{},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				if sc.Switch == nil {
					return "switch_space is unavailable here: this run has no session to carry an active space (use a chat session)", nil
				}
				name, _ := args["space"].(string)
				if strings.TrimSpace(name) == "" {
					if err := sc.Switch(""); err != nil {
						return fmt.Sprintf("switch_space failed: %v", err), nil
					}
					return "switched to the global scope (no active space); effective from the next turn", nil
				}
				sp, err := sc.Store.Resolve(name)
				if err != nil {
					return fmt.Sprintf("switch_space failed: %v", err), nil
				}
				if err := sc.Switch(sp.ID); err != nil {
					return fmt.Sprintf("switch_space failed: %v", err), nil
				}
				return fmt.Sprintf("switched to space %q (id %s); its memory and notes apply from the next turn", sp.Name, sp.ID), nil
			},
		},
		{
			Name: "space_notes",
			Description: "Read the active space's notes — the short, always-loaded profile for this context " +
				"(goals, current state, preferences). Use update_space_notes to change them.",
			Parameters: map[string]any{},
			Required:   []string{},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				if sc.ActiveID == "" {
					return "no space is active (global scope has no notes); switch_space first", nil
				}
				sp, err := sc.Store.Get(sc.ActiveID)
				if err != nil {
					return fmt.Sprintf("space_notes failed: %v", err), nil
				}
				if strings.TrimSpace(sp.Notes) == "" {
					return fmt.Sprintf("space %q has no notes yet; update_space_notes sets them", sp.Name), nil
				}
				return sp.Notes, nil
			},
		},
		{
			Name: "update_space_notes",
			Description: "Replace the active space's notes (its always-loaded profile). Keep them brief — they are " +
				"injected into the system prompt whenever the space is active (capped at " +
				fmt.Sprint(space.MaxNotesLen) + " characters); put detail into memory entries instead. " +
				"The updated notes load from the next turn.",
			Parameters: map[string]any{
				"notes": map[string]any{"type": "string", "description": "the full replacement notes text"},
			},
			Required: []string{"notes"},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				if sc.ActiveID == "" {
					return "no space is active; switch_space first", nil
				}
				notes, _ := args["notes"].(string)
				sp, err := sc.Store.Get(sc.ActiveID)
				if err != nil {
					return fmt.Sprintf("update_space_notes failed: %v", err), nil
				}
				sp.Notes = notes
				if err := sc.Store.Save(sp); err != nil {
					return fmt.Sprintf("update_space_notes failed: %v", err), nil
				}
				return fmt.Sprintf("notes for space %q updated (%d chars); they load into context from the next turn", sp.Name, len(notes)), nil
			},
		},
	}
}

// notesPreview returns the first line of notes, truncated, for the listing.
func notesPreview(notes string) string {
	line := strings.TrimSpace(notes)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if len(line) > 80 {
		line = line[:80] + "…"
	}
	return line
}
