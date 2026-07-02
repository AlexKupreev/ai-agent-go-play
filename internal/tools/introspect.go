package tools

import (
	"context"
	"fmt"
	"strings"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
)

// NewRecentActivityTool returns the `recent_activity` built-in: the agent reviews its
// own recorded activity from the audit log (capabilities used, tools authored/revoked,
// memory saved, token usage) to answer "what have I done recently?". Read-only, trusted,
// not sandbox-exposed. Omitted when no reader is wired.
func NewRecentActivityTool(reader audit.Reader) Tool {
	return Tool{
		Name: "recent_activity",
		Description: "Review your own recently recorded activity from the audit log: capabilities you used, " +
			"tools you authored or revoked, memory you saved, and token usage. Filter by `type` (e.g. " +
			"tool_authored, memory_write, capability_exercised, run_usage) or `run`. Answers 'what have I done?'",
		Parameters: map[string]any{
			"type":  map[string]any{"type": "string", "description": "filter by event type"},
			"run":   map[string]any{"type": "string", "description": "filter by run id"},
			"limit": map[string]any{"type": "integer", "description": "max events, most recent (default 20)"},
		},
		Required: []string{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			limit := 20
			if n, ok := args["limit"].(float64); ok && n > 0 {
				limit = int(n)
			}
			typ, _ := args["type"].(string)
			run, _ := args["run"].(string)
			events, err := reader.Tail(limit, audit.Filter{Run: strings.TrimSpace(run), Type: strings.TrimSpace(typ)})
			if err != nil {
				return fmt.Sprintf("could not read the audit log: %v", err), nil
			}
			if len(events) == 0 {
				return "no matching activity recorded", nil
			}
			var b strings.Builder
			for _, e := range events {
				r := e.Run
				if r == "" {
					r = "-"
				}
				fmt.Fprintf(&b, "%s  %-22s  run:%s  %v\n", e.At.Format("2006-01-02 15:04:05"), e.Type, shortID(r), e.Fields)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

// NewCatalogTool returns the `tool_catalog` built-in: the agent lists the tools it has
// authored (via author_tool) with their capabilities and scope, so it reuses an existing
// tool instead of writing a duplicate. Read-only, trusted, not sandbox-exposed.
func NewCatalogTool(registry Registry) Tool {
	return Tool{
		Name: "tool_catalog",
		Description: "List the tools you have authored (via author_tool), with their capabilities and scope, " +
			"so you can reuse an existing one instead of writing a duplicate. Give `query` to find relevant " +
			"tools, or omit it to list all.",
		Parameters: map[string]any{
			"query": map[string]any{"type": "string", "description": "search terms; omit to list all authored tools"},
		},
		Required: []string{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			var specs []ToolSpec
			if q, ok := args["query"].(string); ok && strings.TrimSpace(q) != "" {
				specs = registry.Search(q, 12)
			} else {
				specs = registry.List(ScopeAny)
			}
			if len(specs) == 0 {
				return "you have not authored any tools yet", nil
			}
			var b strings.Builder
			for _, s := range specs {
				fmt.Fprintf(&b, "- %s (v%d, %s) — %s", s.Name, s.Version, s.Scope, s.Description)
				if caps := formatCaps(s.RequiredCaps); caps != "" {
					fmt.Fprintf(&b, " [caps: %s]", caps)
				}
				b.WriteByte('\n')
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

// formatCaps renders a tool's capabilities compactly, e.g. "http_get(example.com),
// write_file(/tmp), call_tool(web_fetch)".
func formatCaps(caps []capability.Capability) string {
	if len(caps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(caps))
	for _, c := range caps {
		target := ""
		switch {
		case len(c.Hosts) > 0:
			target = strings.Join(c.Hosts, "|")
		case c.PathPrefix != "":
			target = c.PathPrefix
		case len(c.Tools) > 0:
			target = strings.Join(c.Tools, "|")
		}
		if target != "" {
			parts = append(parts, fmt.Sprintf("%s(%s)", c.Kind, target))
		} else {
			parts = append(parts, string(c.Kind))
		}
	}
	return strings.Join(parts, ", ")
}
