package tools

import (
	"context"
	"fmt"
	"strings"

	"ai-agent-go-play/internal/selfdocs"
)

// NewReadSelfDocsTool returns the `read_self_docs` built-in: read-only access to the
// agent's OWN embedded documentation (its README + docs), NOT the project files in its
// working directory. The model uses it to answer questions about how it works, what it
// can do, and how it is operated — from the real docs rather than by guessing. Trusted,
// and (like remember/recall) not exposed to the sandbox.
func NewReadSelfDocsTool(docs *selfdocs.Docs) Tool {
	return Tool{
		Name: "read_self_docs",
		Description: "Read your OWN documentation — this agent's manual (README + docs), embedded in the " +
			"binary, NOT files in the current working directory. Use it to answer questions about how you " +
			"work, what you can do, your tools, trust tiers, approvals, memory, or APIs. Give `topic` to read " +
			"one doc, `query` to find relevant docs, or neither to list what's available. Docs marked " +
			"[reference] describe how you work now; [vision] is design intent and may include not-yet-built ideas.",
		Parameters: map[string]any{
			"topic": map[string]any{"type": "string", "description": "doc to read, e.g. \"usage\", \"security\", \"design\", \"tools\", \"readme\", \"vision\""},
			"query": map[string]any{"type": "string", "description": "search terms; lists the most relevant docs to then read by topic"},
		},
		Required: []string{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			topic, _ := args["topic"].(string)
			query, _ := args["query"].(string)

			switch {
			case strings.TrimSpace(topic) != "":
				body, err := docs.Get(topic)
				if err != nil {
					return err.Error(), nil // guide the model back with the available topics
				}
				return body, nil
			case strings.TrimSpace(query) != "":
				hits := docs.Search(query, 5)
				if len(hits) == 0 {
					return "no docs matched. Available:\n" + formatDocList(docs.List()), nil
				}
				return "most relevant docs (read one with topic=<name>):\n" + formatDocList(hits), nil
			default:
				return "your documentation (read one with topic=<name>):\n" + formatDocList(docs.List()), nil
			}
		},
	}
}

// formatDocList renders doc listing lines: "- topic [kind] — Title".
func formatDocList(infos []selfdocs.Info) string {
	var b strings.Builder
	for _, in := range infos {
		fmt.Fprintf(&b, "- %s [%s] — %s\n", in.Topic, in.Kind, in.Title)
	}
	return strings.TrimRight(b.String(), "\n")
}
