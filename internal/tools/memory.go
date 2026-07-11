package tools

import (
	"context"
	"fmt"
	"strings"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/memory"
)

// defaultRecallLimit bounds how many entries recall returns when the model does
// not ask for a specific number, so a large store cannot flood the context.
const defaultRecallLimit = 5

// NewRememberTool returns the `remember` built-in: the agent saves a durable fact
// to long-term memory under a stable key, persisted across runs. Writes are
// audited (rec may be nil). It is a trusted built-in and is not exposed to
// sandboxed code, so authored tools cannot reach memory via call_tool.
func NewRememberTool(store memory.Store, rec audit.Recorder, runID string) Tool {
	return Tool{
		Name: "remember",
		Description: "Save a durable fact to long-term memory under a stable key, so you can recall it in " +
			"later runs. Use it for things worth keeping: user preferences, project details, decisions, or " +
			"results that future tasks will need. Re-using a key overwrites that entry. Do not store secrets.",
		Parameters: map[string]any{
			"key":   map[string]any{"type": "string", "description": "stable identifier for this fact (e.g. \"user.editor\"); re-using it overwrites"},
			"value": map[string]any{"type": "string", "description": "the fact to remember"},
			"tags":  map[string]any{"type": "array", "description": "optional labels to aid later recall", "items": map[string]any{"type": "string"}},
		},
		Required: []string{"key", "value"},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return "remember: both key and value are required", nil
			}
			entry := memory.Entry{Key: key, Value: value, Tags: stringSlice(args["tags"]), CreatedBy: runID}
			if err := store.Put(entry); err != nil {
				return fmt.Sprintf("remember failed: %v", err), nil
			}
			if rec != nil {
				rec.Record(audit.Event{
					Type:   audit.EventMemoryWrite,
					Run:    runID,
					Fields: map[string]any{"key": key, "tags": entry.Tags},
				})
			}
			return fmt.Sprintf("remembered %q", key), nil
		},
	}
}

// NewRecallTool returns the `recall` built-in: read-only lookup of long-term
// memory by key, by relevance to a query, or a listing of recent entries.
func NewRecallTool(store memory.Store) Tool {
	return Tool{
		Name: "recall",
		Description: "Look up long-term memory saved by `remember` in this or earlier runs. Give `key` for an " +
			"exact entry, `query` to find relevant entries, or neither to list the most recent. Check memory " +
			"before starting a task in case a past run saved something useful.",
		Parameters: map[string]any{
			"key":   map[string]any{"type": "string", "description": "exact key to fetch"},
			"query": map[string]any{"type": "string", "description": "search terms (matched on shared words); returns the most relevant entries, or the most recent ones if nothing matches literally"},
			"limit": map[string]any{"type": "integer", "description": "max entries to return (default 5)"},
		},
		Required: []string{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			limit := defaultRecallLimit
			if n, ok := args["limit"].(float64); ok && n > 0 {
				limit = int(n)
			}

			if key, ok := args["key"].(string); ok && strings.TrimSpace(key) != "" {
				e, found := store.Get(key)
				if !found {
					return fmt.Sprintf("no memory entry for %q", key), nil
				}
				return formatEntries([]memory.Entry{e}), nil
			}

			if query, ok := args["query"].(string); ok && strings.TrimSpace(query) != "" {
				if entries := store.Search(query, limit); len(entries) > 0 {
					return formatEntries(entries), nil
				}
				// Search is token-overlap, so it misses when the query and the stored value
				// share no literal words — a different language/script (a Russian query over an
				// English note), a synonym, or a fact phrased unexpectedly. A false "nothing"
				// here is worse than noise: the agent concludes the fact isn't stored when it is.
				// So fall back to listing recent entries — for a small personal store the agent
				// can just read the few results and find the relevant one itself.
				all := store.List()
				if len(all) == 0 {
					return "memory is empty (no entries saved yet)", nil
				}
				if len(all) > limit {
					all = all[:limit]
				}
				return fmt.Sprintf("no direct match for %q; the %d most recent memory entries (read them — the one you want may be phrased differently):\n%s",
					strings.TrimSpace(query), len(all), formatEntries(all)), nil
			}

			entries := store.List()
			if len(entries) > limit {
				entries = entries[:limit]
			}
			if len(entries) == 0 {
				return "memory is empty (no entries saved yet)", nil
			}
			return formatEntries(entries), nil
		},
	}
}

// formatEntries renders entries for the model: one "key: value [tags]" per line.
func formatEntries(entries []memory.Entry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s: %s", e.Key, e.Value)
		if len(e.Tags) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(e.Tags, ", "))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// stringSlice coerces a decoded JSON array into []string, dropping non-strings.
func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
