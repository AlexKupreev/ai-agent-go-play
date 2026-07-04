package projects

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeProject creates <root>/<dir>/.agent/project.md with the given marker content.
func writeProject(t *testing.T, root, dir, marker string) string {
	t.Helper()
	pdir := filepath.Join(root, dir)
	if err := os.MkdirAll(filepath.Join(pdir, ".agent"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, ".agent", "project.md"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	return pdir
}

func TestList_NoProjectsDir(t *testing.T) {
	// A root that does not exist is an empty registry, not an error.
	ps, err := List(filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatalf("List on missing root: %v", err)
	}
	if len(ps) != 0 {
		t.Fatalf("want 0 projects, got %d", len(ps))
	}
}

func TestList_ParsesAndSortsByRecency(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "articles-a3f9c1", `---
title: Shared reading list
uid: a3f9c1
created: 2026-06-01T09:00:00Z
last_active: 2026-06-20T09:00:00Z
description: articles the user shared
---
freeform notes`)
	writeProject(t, root, "health-7b2e04", `---
title: Health analysis
uid: 7b2e04
last_active: 2026-07-01T09:00:00Z
description: BP + weight trends
---`)

	ps, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ps) != 2 {
		t.Fatalf("want 2 projects, got %d", len(ps))
	}
	// Most-recently-active first: health (Jul 1) before articles (Jun 20).
	if ps[0].UID != "7b2e04" || ps[1].UID != "a3f9c1" {
		t.Fatalf("recency order wrong: %s then %s", ps[0].UID, ps[1].UID)
	}
	a := ps[1]
	if a.Title != "Shared reading list" || a.Description != "articles the user shared" {
		t.Errorf("articles metadata: %+v", a)
	}
	if !a.Created.Equal(time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("created not parsed: %v", a.Created)
	}
	if a.Path != filepath.Join(root, "articles-a3f9c1") {
		t.Errorf("path = %q", a.Path)
	}
}

func TestList_SkipsNonProjectDirsAndFiles(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "real-abc123", "---\ntitle: Real\nuid: abc123\n---")
	// A directory with no marker is scratch, not a project.
	if err := os.MkdirAll(filepath.Join(root, "scratch-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	// A stray file at the projects root is ignored.
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	ps, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ps) != 1 || ps[0].UID != "abc123" {
		t.Fatalf("want only the real project, got %+v", ps)
	}
}

func TestList_MalformedMarkerSkipped(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "good-111111", "---\ntitle: Good\nuid: 111111\n---")
	// Broken YAML frontmatter — the listing must stay usable, skipping just this one.
	writeProject(t, root, "bad-222222", "---\ntitle: [unterminated\nuid: 222222\n---")
	ps, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ps) != 1 || ps[0].UID != "111111" {
		t.Fatalf("want only the good project, got %+v", ps)
	}
}

func TestList_Fallbacks(t *testing.T) {
	root := t.TempDir()
	// Marker with neither uid, title, nor last_active: uid derives from the folder's
	// <slug>-<uid> suffix, title from the folder name, last_active from the dir mtime.
	dir := writeProject(t, root, "health-analysis-7b2e04", "---\ndescription: trends\n---")
	// Give the dir a known mtime so the fallback is observable.
	mt := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(dir, mt, mt); err != nil {
		t.Fatal(err)
	}
	ps, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("want 1, got %d", len(ps))
	}
	p := ps[0]
	if p.UID != "7b2e04" {
		t.Errorf("uid fallback: got %q, want 7b2e04", p.UID)
	}
	if p.Title != "health-analysis-7b2e04" {
		t.Errorf("title fallback: got %q", p.Title)
	}
	if p.LastActive.IsZero() {
		t.Error("last_active should fall back to the dir mtime, got zero")
	}
}

func TestRoot(t *testing.T) {
	if got := Root("/home/me/work"); got != filepath.Join("/home/me/work", "projects") {
		t.Errorf("Root = %q", got)
	}
}
