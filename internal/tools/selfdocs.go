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
//
// Retrieval is section-scoped: `query` ranks sections, a bare `topic` on a large doc
// returns its outline, and `section` fetches one chunk. That keeps a single call to
// roughly a screen instead of the ~14k tokens a whole `usage.md` would cost, and lets
// the agent cite which section it answered from.
func NewReadSelfDocsTool(docs *selfdocs.Docs) Tool {
	return Tool{
		Name: "read_self_docs",
		Description: "Read your OWN documentation — this agent's manual (README + docs), embedded in the " +
			"binary, NOT files in the current working directory. Use it to answer questions about how you " +
			"work, what you can do, your tools, trust tiers, approvals, memory, or APIs. Give `query` to " +
			"find the most relevant sections, `topic` to read a doc (large docs return a section outline " +
			"instead), `topic`+`section` to read one section, or nothing to list the docs. Cite the section " +
			"you answered from. Docs marked [reference] describe how you work now; [vision] is design " +
			"intent and may include not-yet-built ideas.",
		Parameters: map[string]any{
			"topic":   map[string]any{"type": "string", "description": "doc to read, e.g. \"usage\", \"security\", \"design\", \"tools\", \"readme\", \"vision\""},
			"section": map[string]any{"type": "string", "description": "section of `topic` to read, e.g. \"spaces\" or \"trust-tiers\" (from a query result or an outline)"},
			"query":   map[string]any{"type": "string", "description": "search terms; lists the most relevant sections as topic#section"},
		},
		Required: []string{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			topic, _ := args["topic"].(string)
			section, _ := args["section"].(string)
			query, _ := args["query"].(string)
			topic, section, query = strings.TrimSpace(topic), strings.TrimSpace(section), strings.TrimSpace(query)

			switch {
			case topic != "" && section != "":
				body, err := docs.Section(topic, section)
				if err != nil {
					return err.Error(), nil // guide the model back with the available sections
				}
				return truncateForContext(body), nil
			case topic != "":
				return readDoc(docs, topic), nil
			case query != "":
				hits := docs.Search(query, 8)
				if len(hits) == 0 {
					return "no docs matched. Available:\n" + formatDocList(docs.List()), nil
				}
				return "most relevant sections (read one with topic=<doc> section=<name>):\n" + formatHits(hits), nil
			default:
				return "your documentation (read one with topic=<name>):\n" + formatDocList(docs.List()), nil
			}
		},
	}
}

// readDoc returns a small doc whole; anything over the context cap becomes an outline so
// the model spends one cheap call picking a section instead of one expensive call on all
// of them.
func readDoc(docs *selfdocs.Docs, topic string) string {
	body, err := docs.Get(topic)
	if err != nil {
		return err.Error()
	}
	info, sections, err := docs.Outline(topic)
	if err != nil || len(sections) < 2 || len(body) <= maxFetchChars {
		return truncateForContext(body)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] — %s (%d chars, %d sections; too large to return whole)\n",
		info.Topic, info.Kind, info.Title, info.Bytes, len(sections))
	fmt.Fprintf(&b, "read one with topic=%s section=<name>:\n", info.Topic)
	for _, s := range sections {
		heading := s.Heading
		if heading == "" {
			heading = "(opening)"
		}
		fmt.Fprintf(&b, "- %s — %s (%d chars)\n", s.Slug, heading, s.Bytes)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatDocList renders doc listing lines: "- topic [kind] — Title".
func formatDocList(infos []selfdocs.Info) string {
	var b strings.Builder
	for _, in := range infos {
		fmt.Fprintf(&b, "- %s [%s] — %s\n", in.Topic, in.Kind, in.Title)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatHits renders ranked section lines: "- topic#slug [kind] — Heading (N chars)".
func formatHits(hits []selfdocs.Hit) string {
	var b strings.Builder
	for _, h := range hits {
		heading := h.Heading
		if heading == "" {
			heading = "(opening)"
		}
		fmt.Fprintf(&b, "- %s [%s] — %s (%d chars)\n", h.Ref(), h.Kind, heading, h.Bytes)
	}
	return strings.TrimRight(b.String(), "\n")
}
