package tools

import (
	"context"
	"strings"
	"testing"

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

	// Notes tools need an active space.
	if out := runSpaceTool(t, tt, "space_notes", nil); !strings.Contains(out, "no space is active") {
		t.Fatalf("space_notes without active = %q", out)
	}
	activeTools := NewSpaceTools(SpaceContext{Store: store, ActiveID: "polish-lessons"})
	if out := runSpaceTool(t, activeTools, "space_notes", nil); !strings.Contains(out, "no notes yet") {
		t.Fatalf("space_notes empty = %q", out)
	}
	if out := runSpaceTool(t, activeTools, "update_space_notes", map[string]any{"notes": "level B1; likes grammar drills"}); !strings.Contains(out, "updated") {
		t.Fatalf("update_space_notes = %q", out)
	}
	if out := runSpaceTool(t, activeTools, "space_notes", nil); out != "level B1; likes grammar drills" {
		t.Fatalf("space_notes = %q", out)
	}
	// Active space is marked in the listing.
	if out := runSpaceTool(t, activeTools, "list_spaces", nil); !strings.Contains(out, "* polish-lessons") {
		t.Fatalf("list_spaces active marker missing: %q", out)
	}

	// No session to carry the switch (nil Switch): explained, not an error.
	noSwitch := NewSpaceTools(SpaceContext{Store: store})
	if out := runSpaceTool(t, noSwitch, "switch_space", map[string]any{"space": "polish-lessons"}); !strings.Contains(out, "no session") {
		t.Fatalf("switch_space without session = %q", out)
	}
}
