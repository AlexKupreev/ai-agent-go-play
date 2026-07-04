package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
)

var errFake = errors.New("boom")

func TestListProjectsTool_Empty(t *testing.T) {
	tool := NewListProjectsTool(filepath.Join(t.TempDir(), "projects"))
	out, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "no projects yet" {
		t.Errorf("empty registry: got %q", out)
	}
}

func TestListProjectsTool_ListsProjects(t *testing.T) {
	root := t.TempDir()
	pdir := filepath.Join(root, "articles-a3f9c1", ".agent")
	if err := os.MkdirAll(pdir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := "---\ntitle: Reading list\nuid: a3f9c1\nlast_active: 2026-07-01T00:00:00Z\ndescription: shared articles\n---"
	if err := os.WriteFile(filepath.Join(pdir, "project.md"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}

	tool := NewListProjectsTool(root)
	if tool.Name != "list_projects" {
		t.Fatalf("name = %q", tool.Name)
	}
	out, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"Reading list", "a3f9c1", "shared articles", "2026-07-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCreateProjectTool_ApprovedCreatesAndAudits(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	rec := &audit.MemoryRecorder{}
	gate := GateFuncs{ApproveFn: func(context.Context, ApprovalRequest) (bool, error) { return true, nil }}
	tool := NewCreateProjectTool(root, gate, rec, "run-1")

	out, err := tool.Run(context.Background(), map[string]any{
		"title":       "Health analysis",
		"description": "BP + weight trends",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "created project") || !strings.Contains(out, "Health analysis") {
		t.Errorf("unexpected output: %q", out)
	}

	// The created project is now visible to the read side.
	list := NewListProjectsTool(root)
	lo, err := list.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lo, "Health analysis") {
		t.Errorf("created project not listed:\n%s", lo)
	}

	// The side effect is audited.
	if len(rec.Events) != 1 || rec.Events[0].Type != audit.EventProjectCreated {
		t.Fatalf("want one project_created event, got %+v", rec.Events)
	}
	if rec.Events[0].Run != "run-1" {
		t.Errorf("audit event run = %q", rec.Events[0].Run)
	}
}

func TestCreateProjectTool_DeniedCreatesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	rec := &audit.MemoryRecorder{}
	gate := GateFuncs{ApproveFn: func(context.Context, ApprovalRequest) (bool, error) { return false, nil }}
	tool := NewCreateProjectTool(root, gate, rec, "run-1")

	out, err := tool.Run(context.Background(), map[string]any{"title": "nope"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "not approved") {
		t.Errorf("expected a not-approved message, got %q", out)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("projects root should not exist after denial")
	}
	if len(rec.Events) != 0 {
		t.Errorf("no audit event expected on denial, got %+v", rec.Events)
	}
}

func TestSwitchProjectTool_ResolvesAndSwitches(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	// Create a project to switch into.
	create := NewCreateProjectTool(root, GateFuncs{}, nil, "")
	if _, err := create.Run(context.Background(), map[string]any{"title": "Health analysis"}); err != nil {
		t.Fatal(err)
	}

	rec := &audit.MemoryRecorder{}
	var switched string
	tool := NewSwitchProjectTool(root, func(dir string) error { switched = dir; return nil }, rec, "run-1")

	out, err := tool.Run(context.Background(), map[string]any{"project": "health"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "switched to project") || !strings.Contains(out, "Health analysis") {
		t.Errorf("unexpected output: %q", out)
	}
	if switched == "" || !strings.Contains(switched, "health-analysis-") {
		t.Errorf("doSwitch got %q, want the project dir", switched)
	}
	if len(rec.Events) != 1 || rec.Events[0].Type != audit.EventProjectSwitched {
		t.Fatalf("want one project_switched event, got %+v", rec.Events)
	}
}

func TestSwitchProjectTool_NotFound(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	called := false
	tool := NewSwitchProjectTool(root, func(string) error { called = true; return nil }, nil, "")
	out, err := tool.Run(context.Background(), map[string]any{"project": "ghost"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "no project matches") {
		t.Errorf("want a not-found message, got %q", out)
	}
	if called {
		t.Error("doSwitch should not run when nothing resolved")
	}
}

func TestSwitchProjectTool_Ambiguous(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	create := NewCreateProjectTool(root, GateFuncs{}, nil, "")
	for _, title := range []string{"Notes one", "Notes two"} {
		if _, err := create.Run(context.Background(), map[string]any{"title": title}); err != nil {
			t.Fatal(err)
		}
	}
	called := false
	tool := NewSwitchProjectTool(root, func(string) error { called = true; return nil }, nil, "")
	out, err := tool.Run(context.Background(), map[string]any{"project": "notes"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "matches multiple projects") {
		t.Errorf("want an ambiguity message, got %q", out)
	}
	if called {
		t.Error("doSwitch should not run on an ambiguous match")
	}
}

func TestSwitchProjectTool_SwitchErrorSurfaced(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	create := NewCreateProjectTool(root, GateFuncs{}, nil, "")
	if _, err := create.Run(context.Background(), map[string]any{"title": "x"}); err != nil {
		t.Fatal(err)
	}
	rec := &audit.MemoryRecorder{}
	tool := NewSwitchProjectTool(root, func(string) error { return errFake }, rec, "run-1")
	out, err := tool.Run(context.Background(), map[string]any{"project": "x"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "switch_project failed") {
		t.Errorf("want a failure message, got %q", out)
	}
	if len(rec.Events) != 0 {
		t.Errorf("no audit event expected on a failed switch, got %+v", rec.Events)
	}
}

func TestCreateProjectTool_RequiresTitle(t *testing.T) {
	gate := GateFuncs{ApproveFn: func(context.Context, ApprovalRequest) (bool, error) {
		t.Fatal("gate should not be consulted for a blank title")
		return false, nil
	}}
	tool := NewCreateProjectTool(t.TempDir(), gate, nil, "")
	out, err := tool.Run(context.Background(), map[string]any{"title": "  "})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "title is required") {
		t.Errorf("want title-required message, got %q", out)
	}
}
