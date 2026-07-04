package projects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreate_MintsProjectAndRoundTrips(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects") // does not exist yet — Create must make it
	p, err := Create(root, CreateOptions{Title: "Shared Reading List!", Description: "articles the user shared"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Folder name is <slug>-<uid>; slug is derived from the title, uid is the stable id.
	base := filepath.Base(p.Path)
	if !strings.HasPrefix(base, "shared-reading-list-") {
		t.Errorf("folder name = %q, want shared-reading-list-<uid>", base)
	}
	if p.UID == "" || !strings.HasSuffix(base, "-"+p.UID) {
		t.Errorf("uid %q not the folder suffix of %q", p.UID, base)
	}
	if p.Created.IsZero() || p.LastActive.IsZero() {
		t.Errorf("timestamps not set: created=%v last_active=%v", p.Created, p.LastActive)
	}

	// The seeded marker must round-trip back through the read side.
	ps, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("want 1 project, got %d", len(ps))
	}
	got := ps[0]
	if got.UID != p.UID || got.Title != "Shared Reading List!" || got.Description != "articles the user shared" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Created.IsZero() {
		t.Error("created did not round-trip through the marker")
	}
}

func TestCreate_BlankTitleRejected(t *testing.T) {
	if _, err := Create(t.TempDir(), CreateOptions{Title: "   "}); err == nil {
		t.Fatal("want error for blank title, got nil")
	}
}

func TestCreate_UIDsAreUnique(t *testing.T) {
	root := t.TempDir()
	a, err := Create(root, CreateOptions{Title: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(root, CreateOptions{Title: "notes"}) // same title ⇒ same slug, distinct uid
	if err != nil {
		t.Fatal(err)
	}
	if a.UID == b.UID {
		t.Fatalf("two projects share a uid: %s", a.UID)
	}
	if a.Path == b.Path {
		t.Fatalf("two same-titled projects share a dir: %s", a.Path)
	}
}

func TestCreate_PromotesFromPaths(t *testing.T) {
	work := t.TempDir()
	root := filepath.Join(work, "projects")
	// A scratch file to promote into the new project.
	scratch := filepath.Join(work, "draft.md")
	if err := os.WriteFile(scratch, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Create(root, CreateOptions{Title: "health", FromPaths: []string{scratch}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("scratch file should have been moved, still present at %s", scratch)
	}
	moved := filepath.Join(p.Path, "draft.md")
	b, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("moved file not in project: %v", err)
	}
	if string(b) != "hello" {
		t.Errorf("moved content = %q", b)
	}
}

func TestCreate_PromoteMissingPathErrors(t *testing.T) {
	root := t.TempDir()
	_, err := Create(root, CreateOptions{Title: "x", FromPaths: []string{filepath.Join(root, "nope")}})
	if err == nil {
		t.Fatal("want error promoting a missing path, got nil")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Shared Reading List": "shared-reading-list",
		"  Health/Analysis  ": "health-analysis",
		"C++ notes":           "c-notes",
		"!!!":                 "project",
		"":                    "project",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugify_LengthBounded(t *testing.T) {
	got := slugify(strings.Repeat("a", 100))
	if len(got) > maxSlugLen {
		t.Errorf("slug length %d exceeds bound %d", len(got), maxSlugLen)
	}
}

func TestMintUID_FormatAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		u := mintUID()
		if u == "" {
			t.Fatal("empty uid")
		}
		for _, r := range u {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				t.Fatalf("uid %q has a non-filesystem-safe rune %q", u, r)
			}
		}
		if seen[u] {
			t.Fatalf("duplicate uid within 100 mints: %s", u)
		}
		seen[u] = true
	}
}
