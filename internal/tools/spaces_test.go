package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/space"
)

func runSpaceTool(t *testing.T, tt []Tool, name string, args map[string]any) string {
	t.Helper()
	for _, tool := range tt {
		if tool.Name == name {
			out, err := tool.Run(context.Background(), args)
			if err != nil {
				t.Fatalf("%s returned hard error: %v", name, err)
			}
			return out
		}
	}
	t.Fatalf("tool %s not found", name)
	return ""
}

func TestSpaceTools(t *testing.T) {
	store := space.NewStore(t.TempDir() + "/spaces")
	var switched string
	sc := SpaceContext{Store: store, ActiveID: "", Switch: func(id string) error { switched = id; return nil }}
	tt := NewSpaceTools(sc)
	if len(tt) != 5 {
		t.Fatalf("NewSpaceTools = %d tools, want 5", len(tt))
	}

	// Empty store: listing explains the global scope.
	if out := runSpaceTool(t, tt, "list_spaces", nil); !strings.Contains(out, "no spaces yet") {
		t.Fatalf("list_spaces on empty = %q", out)
	}

	// create → visible in the listing; switch resolves the name and calls back.
	if out := runSpaceTool(t, tt, "create_space", map[string]any{"name": "Polish Lessons"}); !strings.Contains(out, "polish-lessons") {
		t.Fatalf("create_space = %q", out)
	}
	if out := runSpaceTool(t, tt, "list_spaces", nil); !strings.Contains(out, "polish-lessons") {
		t.Fatalf("list_spaces = %q", out)
	}
	if out := runSpaceTool(t, tt, "switch_space", map[string]any{"space": "Polish Lessons"}); !strings.Contains(out, "next turn") {
		t.Fatalf("switch_space = %q", out)
	}
	if switched != "polish-lessons" {
		t.Fatalf("Switch callback got %q, want polish-lessons", switched)
	}
	// Unknown space is a soft error back to the model, no callback.
	switched = "unchanged"
	if out := runSpaceTool(t, tt, "switch_space", map[string]any{"space": "nope"}); !strings.Contains(out, "failed") {
		t.Fatalf("switch_space unknown = %q", out)
	}
	if switched != "unchanged" {
		t.Fatal("Switch called for an unknown space")
	}
	// Empty arg returns to the global scope.
	if out := runSpaceTool(t, tt, "switch_space", nil); !strings.Contains(out, "global scope") {
		t.Fatalf("switch_space to global = %q", out)
	}
	if switched != "" {
		t.Fatalf("Switch callback got %q, want \"\"", switched)
	}

	// Space-guidance tools need an active space.
	if out := runSpaceTool(t, tt, "space_guidance", nil); !strings.Contains(out, "no space is active") {
		t.Fatalf("space_guidance without active = %q", out)
	}
	activeTools := NewSpaceTools(SpaceContext{Store: store, ActiveID: "polish-lessons"})
	if out := runSpaceTool(t, activeTools, "space_guidance", nil); !strings.Contains(out, "no guidance yet") {
		t.Fatalf("space_guidance empty = %q", out)
	}
	if out := runSpaceTool(t, activeTools, "update_space_guidance", map[string]any{"guidance": "level B1; likes grammar drills"}); !strings.Contains(out, "updated") {
		t.Fatalf("update_space_guidance = %q", out)
	}
	if out := runSpaceTool(t, activeTools, "space_guidance", nil); out != "level B1; likes grammar drills" {
		t.Fatalf("space_guidance = %q", out)
	}
	// Active space is marked in the listing.
	if out := runSpaceTool(t, activeTools, "list_spaces", nil); !strings.Contains(out, "* polish-lessons") {
		t.Fatalf("list_spaces active marker missing: %q", out)
	}
	if out := runSpaceTool(t, activeTools, "update_space_guidance", map[string]any{"guidance": "🐻🐻"}); !strings.Contains(out, "(2 chars)") {
		t.Fatalf("update_space_guidance Unicode count = %q", out)
	}

	// No session to carry the switch (nil Switch): explained, not an error.
	noSwitch := NewSpaceTools(SpaceContext{Store: store})
	if out := runSpaceTool(t, noSwitch, "switch_space", map[string]any{"space": "polish-lessons"}); !strings.Contains(out, "no session") {
		t.Fatalf("switch_space without session = %q", out)
	}
}

func TestUpdateSpaceGuidanceAuditIsRedactedAndIdempotent(t *testing.T) {
	store := space.NewStore(t.TempDir() + "/spaces")
	sp, err := store.Create("Private")
	if err != nil {
		t.Fatal(err)
	}
	rec := &audit.MemoryRecorder{}
	tools := NewSpaceTools(SpaceContext{Store: store, ActiveID: sp.ID, Audit: rec, RunID: "run-1"})
	secret := "sensitive standing context"
	if out := runSpaceTool(t, tools, "update_space_guidance", map[string]any{"guidance": secret}); !strings.Contains(out, "updated") {
		t.Fatalf("update = %q", out)
	}
	if out := runSpaceTool(t, tools, "update_space_guidance", map[string]any{"guidance": secret}); !strings.Contains(out, "unchanged") {
		t.Fatalf("idempotent update = %q", out)
	}
	events := rec.Snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want one changed update", len(events))
	}
	e := events[0]
	if e.Type != audit.EventGuidanceUpdated || e.Run != "run-1" || e.Fields["scope"] != "space" || e.Fields["space_id"] != sp.ID {
		t.Fatalf("audit event = %+v", e)
	}
	for key, value := range e.Fields {
		if strings.Contains(fmt.Sprint(value), secret) {
			t.Fatalf("audit field %q leaked guidance body", key)
		}
	}
}
