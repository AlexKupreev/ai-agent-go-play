// Package projects implements the read side of the project registry (projects.md P1):
// named, recallable workspaces that live as directories under <home-workspace>/projects/.
//
// The filesystem IS the registry — there is no separate index. A project is any directory
// under the projects root that carries a `.agent/project.md` marker; enumerating them is a
// directory listing, so a project can never fall out of sync with an index (there is none)
// and never has a stale path. The marker holds the human title and a stable UID (projects.md
// §1–§2). This package only reads; creation and switching are P2/P3.
package projects

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Subdir is the folder, under the home workspace, that holds projects. Nesting projects
// under the already-authorized workspace is what supplies "trust by containment"
// (projects.md §1).
const Subdir = "projects"

// markerFile is the per-project registry marker, relative to the project directory. Kept
// distinct from the project's own AGENTS.md/SYSTEM.md so registry metadata (title ≠ prompt
// text) never conflates with prompt content (projects.md §8).
const markerFile = ".agent/project.md"

// Project is one entry in the registry: the stable UID identity plus the mutable,
// human-facing metadata read from its marker. Path is the project directory.
type Project struct {
	UID         string
	Title       string
	Description string
	Created     time.Time
	LastActive  time.Time
	Path        string
}

// Root returns the projects directory for a home workspace: <workspace>/projects.
func Root(workspace string) string { return filepath.Join(workspace, Subdir) }

// List enumerates the projects under root, most-recently-active first. A missing root (no
// projects created yet) is an empty registry, not an error. Directories without a readable,
// parseable marker are not projects and are skipped — the listing stays usable even if one
// project's marker is hand-broken (projects.md §1: the tree is the source of truth).
func List(root string) ([]Project, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ps []Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := load(filepath.Join(root, e.Name()))
		if err != nil {
			continue // not a well-formed project directory
		}
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].LastActive.After(ps[j].LastActive) })
	return ps, nil
}

// marker is the YAML frontmatter of a .agent/project.md file. Timestamps are decoded as
// strings and parsed leniently (see parseTime) rather than relying on the YAML timestamp
// type, so a hand-written `2026-07-04` still loads.
type marker struct {
	Title       string `yaml:"title"`
	UID         string `yaml:"uid"`
	Created     string `yaml:"created"`
	LastActive  string `yaml:"last_active"`
	Description string `yaml:"description"`
}

// load reads and parses one project directory's marker. The returned Project fills in stable
// fallbacks so the filesystem stays authoritative: a missing UID is derived from the folder's
// <slug>-<uid> suffix, a missing title from the folder name, and a missing last_active from
// the directory mtime (projects.md §2 — recency ordering).
func load(dir string) (Project, error) {
	b, err := os.ReadFile(filepath.Join(dir, markerFile))
	if err != nil {
		return Project{}, err
	}
	front, _ := splitFrontmatter(b)
	var fm marker
	if len(front) > 0 {
		if err := yaml.Unmarshal(front, &fm); err != nil {
			return Project{}, fmt.Errorf("invalid project marker in %s: %w", dir, err)
		}
	}
	p := Project{
		UID:         strings.TrimSpace(fm.UID),
		Title:       strings.TrimSpace(fm.Title),
		Description: strings.TrimSpace(fm.Description),
		Created:     parseTime(fm.Created),
		LastActive:  parseTime(fm.LastActive),
		Path:        dir,
	}
	base := filepath.Base(dir)
	if p.UID == "" {
		p.UID = uidFromDir(base)
	}
	if p.Title == "" {
		p.Title = base
	}
	if p.LastActive.IsZero() {
		if fi, err := os.Stat(dir); err == nil {
			p.LastActive = fi.ModTime()
		}
	}
	return p, nil
}

// uidFromDir extracts the UID from a <slug>-<uid> folder name — the final hyphen-delimited
// token (the slug itself may contain hyphens). Falls back to the whole name if there is no
// hyphen.
func uidFromDir(base string) string {
	if i := strings.LastIndexByte(base, '-'); i >= 0 && i < len(base)-1 {
		return base[i+1:]
	}
	return base
}

// parseTime accepts RFC3339, a bare local datetime, or a date; anything else (including empty)
// yields the zero time, letting callers fall back to the directory mtime.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// splitFrontmatter separates a leading `---`-fenced YAML block from the body, tolerating LF
// and CRLF endings and a UTF-8 BOM. Mirrors the agents/*.md loader (cmd/agents.go) so the
// marker format is consistent with the rest of the project's front-matter files.
func splitFrontmatter(b []byte) (front, body []byte) {
	s := bytes.TrimPrefix(b, []byte("\ufeff"))
	if !bytes.HasPrefix(s, []byte("---\n")) && !bytes.HasPrefix(s, []byte("---\r\n")) {
		return nil, b
	}
	rest := s[bytes.IndexByte(s, '\n')+1:]
	for i := 0; i <= len(rest); {
		end := bytes.IndexByte(rest[i:], '\n')
		if end < 0 {
			if strings.TrimRight(string(rest[i:]), "\r") == "---" {
				return rest[:i], nil
			}
			return rest, nil
		}
		if strings.TrimRight(string(rest[i:i+end]), "\r") == "---" {
			return rest[:i], rest[i+end+1:]
		}
		i += end + 1
	}
	return rest, nil
}
