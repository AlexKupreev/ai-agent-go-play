package tools

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/guidance"
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
	Audit    audit.Recorder
	RunID    string
}

// NewSpaceTools returns the space management built-ins: list_spaces, create_space,
// switch_space, space_guidance, update_space_guidance. All trusted, not sandbox-exposed
// (like remember) — authored code cannot re-scope the agent's memory via call_tool.
func NewSpaceTools(sc SpaceContext) []Tool {
	return []Tool{
		{
			Name: "list_spaces",
			Description: "List the agent's spaces (switchable data contexts, each with its own memory and guidance). " +
				"Shows each space's id, name, a guidance preview, and which one is active. Use switch_space to change.",
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
					if n := guidancePreview(sp.Guidance); n != "" {
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
			Description: "Create a new space (a named data context with its own memory and guidance) and return its id. " +
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
				"(plus the global scope), and its guidance loads into context. Effective from the next turn. " +
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
				return fmt.Sprintf("switched to space %q (id %s); its memory and guidance apply from the next turn", sp.Name, sp.ID), nil
			},
		},
		{
			Name: "space_guidance",
			Description: "Read the active space's guidance — the short, always-loaded profile and instructions for this context " +
				"(goals, current state, preferences). Use update_space_guidance to change it.",
			Parameters: map[string]any{},
			Required:   []string{},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				if sc.ActiveID == "" {
					return "no space is active; switch_space first to read space guidance", nil
				}
				sp, err := sc.Store.Get(sc.ActiveID)
				if err != nil {
					return fmt.Sprintf("space_guidance failed: %v", err), nil
				}
				if strings.TrimSpace(sp.Guidance) == "" {
					return fmt.Sprintf("space %q has no guidance yet; update_space_guidance sets it", sp.Name), nil
				}
				return sp.Guidance, nil
			},
		},
		{
			Name: "update_space_guidance",
			Description: "Replace the active space's guidance (its always-loaded profile and instructions). Keep it brief — it is " +
				"injected into the system prompt whenever the space is active (capped at " +
				fmt.Sprint(space.MaxGuidanceChars) + " characters); put factual detail into memory entries instead. " +
				"The updated guidance loads from the next turn.",
			Parameters: map[string]any{
				"guidance": map[string]any{"type": "string", "description": "the full replacement space guidance text"},
			},
			Required: []string{"guidance"},
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				if sc.ActiveID == "" {
					return "no space is active; switch_space first", nil
				}
				text, _ := args["guidance"].(string)
				sp, err := sc.Store.Get(sc.ActiveID)
				if err != nil {
					return fmt.Sprintf("update_space_guidance failed: %v", err), nil
				}
				previous := sp.Guidance
				if previous == text {
					return fmt.Sprintf("guidance for space %q unchanged (%d chars)", sp.Name, utf8.RuneCountInString(text)), nil
				}
				sp.Guidance = text
				if err := sc.Store.Save(sp); err != nil {
					return fmt.Sprintf("update_space_guidance failed: %v", err), nil
				}
				guidance.RecordUpdate(sc.Audit, sc.RunID, "space", previous, text, map[string]any{"space_id": sp.ID})
				return fmt.Sprintf("guidance for space %q updated (%d chars); it loads into context from the next turn", sp.Name, utf8.RuneCountInString(text)), nil
			},
		},
	}
}

// guidancePreview returns the first line of guidance, truncated, for the listing.
func guidancePreview(text string) string {
	line := strings.TrimSpace(text)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	runes := []rune(line)
	if len(runes) > 80 {
		line = string(runes[:80]) + "…"
	}
	return line
}
