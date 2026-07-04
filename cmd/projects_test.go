package cmd

import (
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/projects"
)

// resetProjectFlags clears the package-level flag vars the resolver reads, so tests don't
// leak state into each other (mirrors the workspace_test.go pattern).
func resetProjectFlags() {
	noProjectFlag = false
	projectFlag = ""
}

func TestResolveProjects_DefaultRoot(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	home := t.TempDir()
	root, workDir, err := resolveProjects(home, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "projects"); root != want {
		t.Errorf("root = %q, want %q", root, want)
	}
	if workDir != home {
		t.Errorf("workDir = %q, want home %q", workDir, home)
	}
}

func TestResolveProjects_NoProjectFlagDisables(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	noProjectFlag = true
	home := t.TempDir()
	root, workDir, err := resolveProjects(home, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if root != "" {
		t.Errorf("root = %q, want empty (registry disabled)", root)
	}
	if workDir != home {
		t.Errorf("workDir = %q, want home %q", workDir, home)
	}
}

func TestResolveProjects_ConfigFalseDisables(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	off := false
	home := t.TempDir()
	root, _, err := resolveProjects(home, Config{Projects: &off})
	if err != nil {
		t.Fatal(err)
	}
	if root != "" {
		t.Errorf("root = %q, want empty (config projects:false)", root)
	}
}

func TestResolveProjects_ConfigTrueEnables(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	on := true
	home := t.TempDir()
	root, _, err := resolveProjects(home, Config{Projects: &on})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "projects"); root != want {
		t.Errorf("root = %q, want %q", root, want)
	}
}

func TestResolveProjects_ConfigRootOverride(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	home := t.TempDir()
	elsewhere := t.TempDir()
	root, _, err := resolveProjects(home, Config{ProjectsRoot: elsewhere})
	if err != nil {
		t.Fatal(err)
	}
	if root != elsewhere {
		t.Errorf("root = %q, want %q", root, elsewhere)
	}
}

func TestResolveProjects_NoProjectAndProjectConflict(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	noProjectFlag = true
	projectFlag = "anything"
	if _, _, err := resolveProjects(t.TempDir(), Config{}); err == nil {
		t.Error("--no-project with --project should be a conflict error")
	}
}

func TestResolveProjects_ActivateByTitle(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	home := t.TempDir()
	root := projects.Root(home)
	p, err := projects.Create(root, projects.CreateOptions{Title: "Articles"})
	if err != nil {
		t.Fatal(err)
	}

	projectFlag = "articles" // case-insensitive title match
	gotRoot, workDir, err := resolveProjects(home, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Errorf("root = %q, want the home registry %q", gotRoot, root)
	}
	if workDir != p.Path {
		t.Errorf("workDir = %q, want the project path %q", workDir, p.Path)
	}
}

func TestResolveProjects_ActivateByPath(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	home := t.TempDir()
	explicit := t.TempDir() // a directory that is not a registry entry
	projectFlag = explicit
	_, workDir, err := resolveProjects(home, Config{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(workDir)
	want, _ := filepath.EvalSymlinks(explicit)
	if got != want {
		t.Errorf("workDir = %q, want the explicit path %q", workDir, explicit)
	}
}

func TestResolveProjects_ActivateOverridesConfigFalse(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	home := t.TempDir()
	root := projects.Root(home)
	p, err := projects.Create(root, projects.CreateOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}

	off := false
	projectFlag = "notes"
	gotRoot, workDir, err := resolveProjects(home, Config{Projects: &off})
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Errorf("root = %q, want %q (explicit --project overrides config projects:false)", gotRoot, root)
	}
	if workDir != p.Path {
		t.Errorf("workDir = %q, want %q", workDir, p.Path)
	}
}

func TestResolveProjects_ActivateUnknownIsError(t *testing.T) {
	resetProjectFlags()
	defer resetProjectFlags()

	projectFlag = "no-such-project"
	if _, _, err := resolveProjects(t.TempDir(), Config{}); err == nil {
		t.Error("--project naming an unknown project should error")
	}
}
