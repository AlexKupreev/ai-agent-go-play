package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/projects"
	"ai-agent-go-play/internal/provider"
)

// The project built-ins (list_projects read-side, create_project write-side) are offered only
// when a projects root is wired (projects.md P1–P2) — matching how the other optional built-ins
// gate on their dep.
func TestProjectTools_Wiring(t *testing.T) {
	offered := func(root string) []string {
		prov := &scriptedProvider{steps: []provider.StepResponse{textStep("done")}}
		exec := NewExecutor(ExecutorConfig{
			Provider: prov, WorkDir: t.TempDir(),
			Tier: capability.TierBalanced, ProjectsRoot: root,
		})
		if _, err := exec.Run(context.Background(), "hi"); err != nil {
			t.Fatalf("run: %v", err)
		}
		return prov.seenTools[0]
	}

	for _, name := range []string{"list_projects", "create_project"} {
		if slices.Contains(offered(""), name) {
			t.Errorf("%s offered without a projects root", name)
		}
		if !slices.Contains(offered(t.TempDir()), name) {
			t.Errorf("%s not offered even though a projects root was wired", name)
		}
	}
}

// switch_project is offered only when both a projects root and the SwitchWorkspace reload
// seam are wired (projects.md P3) — it needs a root to resolve names and the seam to reload.
func TestSwitchProjectTool_Wiring(t *testing.T) {
	seam := func(string) (PromptCustomization, error) { return PromptCustomization{}, nil }
	offered := func(root string, sw func(string) (PromptCustomization, error)) []string {
		prov := &scriptedProvider{steps: []provider.StepResponse{textStep("done")}}
		exec := NewExecutor(ExecutorConfig{
			Provider: prov, WorkDir: t.TempDir(),
			Tier: capability.TierBalanced, ProjectsRoot: root, SwitchWorkspace: sw,
		})
		if _, err := exec.Run(context.Background(), "hi"); err != nil {
			t.Fatalf("run: %v", err)
		}
		return prov.seenTools[0]
	}
	if slices.Contains(offered("", seam), "switch_project") {
		t.Error("switch_project offered without a projects root")
	}
	if slices.Contains(offered(t.TempDir(), nil), "switch_project") {
		t.Error("switch_project offered without the SwitchWorkspace seam")
	}
	if !slices.Contains(offered(t.TempDir(), seam), "switch_project") {
		t.Error("switch_project not offered even though root + seam were wired")
	}
}

// recordObserver captures each tool's result text so a test can assert on it.
type recordObserver struct{ results map[string]string }

func (o *recordObserver) Emit(e Event) {
	if e.Kind == EvToolResult && e.Call != nil {
		o.results[e.Call.Name] = e.Result
	}
}

// End to end: the agent calls switch_project, then a shell `pwd` runs in the *new* project
// directory — proving the switch re-anchors the live executor mid-run (projects.md P3, §7).
// The SwitchWorkspace seam also picks up the target project's SYSTEM.md, exercising the
// prompt reload path.
func TestSwitchProject_ReanchorsShell(t *testing.T) {
	root := t.TempDir()
	start := t.TempDir()

	// A real project to switch into, with its own SYSTEM.md to prove the prompt reload runs.
	target, err := projects.Create(root, projects.CreateOptions{Title: "Health analysis"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.Path, "SYSTEM.md"), []byte("PROJECT SYSTEM PROMPT"), 0600); err != nil {
		t.Fatal(err)
	}

	// The reload seam: load the target's SYSTEM.md as the override (mirrors loadPrompts at
	// balanced tier). Records that it was asked for the target dir.
	var reloadedFor string
	seam := func(ws string) (PromptCustomization, error) {
		reloadedFor = ws
		b, _ := os.ReadFile(filepath.Join(ws, "SYSTEM.md"))
		return PromptCustomization{SystemPromptOverride: string(b)}, nil
	}

	obs := &recordObserver{results: map[string]string{}}
	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "switch_project", map[string]any{"project": "health"}),
		toolCallStep("c2", "shell", map[string]any{"command": "pwd"}),
		textStep("done"),
	}}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: start, Tier: capability.TierBalanced,
		Observer: obs, ProjectsRoot: root, SwitchWorkspace: seam,
	})
	if _, err := exec.Run(context.Background(), "switch then pwd"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// pwd ran in the target project, not the starting dir.
	pwd := strings.TrimSpace(obs.results["shell"])
	wantDir, _ := filepath.EvalSymlinks(target.Path) // macOS/CI may symlink TempDir
	gotDir, _ := filepath.EvalSymlinks(pwd)
	if gotDir != wantDir {
		t.Errorf("shell after switch ran in %q, want the project dir %q", pwd, target.Path)
	}
	if reloadedFor != target.Path {
		t.Errorf("prompt reload asked for %q, want %q", reloadedFor, target.Path)
	}
	if exec.systemPrompt != "PROJECT SYSTEM PROMPT" {
		t.Errorf("system prompt after switch = %q, want the project's SYSTEM.md", exec.systemPrompt)
	}
}
