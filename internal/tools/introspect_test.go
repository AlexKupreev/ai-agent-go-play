package tools

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
)

func TestRecentActivityTool(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	rec.Record(audit.Event{Type: audit.EventMemoryWrite, Run: "r1", Fields: map[string]any{"key": "user.editor"}})
	rec.Record(audit.Event{Type: audit.EventToolAuthored, Run: "r1", Fields: map[string]any{"name": "triple"}})
	tool := NewRecentActivityTool(rec)
	ctx := context.Background()

	// No filter → both events.
	out, _ := tool.Run(ctx, map[string]any{})
	if !strings.Contains(out, "memory_write") || !strings.Contains(out, "tool_authored") {
		t.Fatalf("unfiltered = %q, want both events", out)
	}

	// Type filter → only the matching event.
	out, _ = tool.Run(ctx, map[string]any{"type": "tool_authored"})
	if !strings.Contains(out, "tool_authored") || strings.Contains(out, "memory_write") {
		t.Fatalf("type-filtered = %q, want only tool_authored", out)
	}

	// No matches → a friendly message, not an error.
	out, err := tool.Run(ctx, map[string]any{"type": "nonexistent"})
	if err != nil || !strings.Contains(out, "no matching activity") {
		t.Fatalf("empty filter = %q, %v", out, err)
	}
}

func TestCatalogTool(t *testing.T) {
	reg := NewMemoryRegistry()
	if _, err := reg.Register(ToolSpec{
		Name:         "fetch_weather",
		Description:  "get weather for a city",
		InputSchema:  map[string]any{"type": "object"},
		Impl:         Impl{Kind: ImplScript, Lang: "lua", Source: "return 1"},
		RequiredCaps: []capability.Capability{{Kind: capability.HTTPGet, Hosts: []string{"api.weather.com"}}},
		Scope:        ScopeShared,
	}); err != nil {
		t.Fatal(err)
	}
	tool := NewCatalogTool(reg)
	ctx := context.Background()

	out, _ := tool.Run(ctx, map[string]any{})
	for _, want := range []string{"fetch_weather", "get weather", "http_get(api.weather.com)"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog = %q, missing %q", out, want)
		}
	}

	// Empty registry → a friendly message.
	out, _ = NewCatalogTool(NewMemoryRegistry()).Run(ctx, map[string]any{})
	if !strings.Contains(out, "not authored any tools") {
		t.Errorf("empty catalog = %q", out)
	}
}
