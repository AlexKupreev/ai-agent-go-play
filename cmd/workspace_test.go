package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspace_DefaultsToCwd(t *testing.T) {
	workspaceFlag = ""
	defer func() { workspaceFlag = "" }()

	ws, err := resolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if ws != wd {
		t.Errorf("resolveWorkspace() = %q, want cwd %q", ws, wd)
	}
}

func TestResolveWorkspace_FlagOverride(t *testing.T) {
	dir := t.TempDir()
	workspaceFlag = dir
	defer func() { workspaceFlag = "" }()

	ws, err := resolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	// TempDir may itself be a symlink target (e.g. /tmp -> /private/tmp on some
	// systems); compare via EvalSymlinks so the absolute paths line up.
	got, _ := filepath.EvalSymlinks(ws)
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Errorf("resolveWorkspace() = %q, want %q", ws, dir)
	}
}

func TestResolveWorkspace_FlagMadeAbsolute(t *testing.T) {
	workspaceFlag = "."
	defer func() { workspaceFlag = "" }()

	ws, err := resolveWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(ws) {
		t.Errorf("resolveWorkspace() = %q, want an absolute path", ws)
	}
}

func TestResolveWorkspace_MissingDirIsError(t *testing.T) {
	workspaceFlag = filepath.Join(t.TempDir(), "does-not-exist")
	defer func() { workspaceFlag = "" }()

	if _, err := resolveWorkspace(); err == nil {
		t.Error("resolveWorkspace() with a nonexistent --workspace should error")
	}
}

func TestResolveWorkspace_FileIsError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceFlag = f
	defer func() { workspaceFlag = "" }()

	if _, err := resolveWorkspace(); err == nil {
		t.Error("resolveWorkspace() with a --workspace pointing at a file should error")
	}
}
