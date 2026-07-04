package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"ai-agent-go-play/internal/projects"
)

// noProjectFlag / projectFlag are bound to the persistent --no-project / --project flags
// (see cmd/root.go). They are the CLI face of the projects model (projects.md §6):
// --no-project is flat-repo mode (no registry), --project activates a specific project at
// launch (by uid, title, or a path).
var (
	noProjectFlag bool
	projectFlag   string
)

// resolveProjects turns the resolved home workspace + config into the two values the
// executor needs: the projects registry root (empty ⇒ the list/create/switch_project tools
// are omitted) and the workspace the agent actually acts on at launch (the home workspace,
// or a specific project when --project selects one). projects.md §6 — three modes:
//
//   - default: the registry lives at <home>/projects (or config projects_root), the agent
//     works at the home root until it creates/switches into a project;
//   - --no-project (or config projects:false): flat-repo mode — no registry, no tools, the
//     workspace *is* the repo. The pi-faithful "I cd'd in, just act on this" path;
//   - --project <uid|title|path>: activate a specific project at launch — the workspace
//     becomes that project's directory, while the registry root stays the home one so the
//     agent can still recall and switch to its siblings.
//
// Precedence for enable/disable: --no-project wins outright; then an explicit --project
// forces projects on (you asked for one); then config projects:false disables; else enabled.
func resolveProjects(homeWorkDir string, cfg Config) (root, workDir string, err error) {
	if noProjectFlag && projectFlag != "" {
		return "", "", fmt.Errorf("--no-project and --project are mutually exclusive")
	}
	if noProjectFlag {
		return "", homeWorkDir, nil // flat-repo mode: no registry
	}
	// Config can disable projects, but an explicit --project overrides that (the operator
	// is naming a project, so they clearly want the feature on for this launch).
	if projectFlag == "" && cfg.Projects != nil && !*cfg.Projects {
		return "", homeWorkDir, nil
	}

	root, err = resolveProjectsRoot(homeWorkDir, cfg)
	if err != nil {
		return "", "", err
	}
	workDir = homeWorkDir
	if projectFlag != "" {
		workDir, err = activateProject(root, projectFlag)
		if err != nil {
			return "", "", err
		}
		fmt.Fprintf(os.Stderr, "project: %s\n", workDir)
	}
	return root, workDir, nil
}

// resolveProjectsRoot returns where the projects registry lives: config projects_root
// (made absolute) if set, else <home-workspace>/projects (projects.md §1 — projects nest
// under the already-authorized home workspace).
func resolveProjectsRoot(homeWorkDir string, cfg Config) (string, error) {
	if cfg.ProjectsRoot != "" {
		abs, err := filepath.Abs(cfg.ProjectsRoot)
		if err != nil {
			return "", fmt.Errorf("projects_root %q: %w", cfg.ProjectsRoot, err)
		}
		return abs, nil
	}
	return projects.Root(homeWorkDir), nil
}

// activateProject resolves a --project reference to a workspace directory. A value that
// names an existing directory is used as a path directly (a project outside the registry,
// or an explicit path); otherwise it is resolved against the registry root as a uid/title
// via projects.Resolve, which reports (not guesses) an ambiguous or absent reference.
func activateProject(root, ref string) (string, error) {
	if info, err := os.Stat(ref); err == nil && info.IsDir() {
		return filepath.Abs(ref)
	}
	p, err := projects.Resolve(root, ref)
	if err != nil {
		return "", fmt.Errorf("--project %q: %w", ref, err)
	}
	return p.Path, nil
}
