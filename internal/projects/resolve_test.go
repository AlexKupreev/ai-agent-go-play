package projects

import (
	"errors"
	"testing"
)

// seedRegistry creates two projects with distinct uids/titles for resolve tests.
func seedRegistry(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeProject(t, root, "reading-a3f9c1", "---\ntitle: Shared reading list\nuid: a3f9c1\n---")
	writeProject(t, root, "health-7b2e04", "---\ntitle: Health analysis\nuid: 7b2e04\n---")
	return root
}

func TestResolve_ByUID(t *testing.T) {
	root := seedRegistry(t)
	p, err := Resolve(root, "7b2e04")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Title != "Health analysis" {
		t.Errorf("got %q", p.Title)
	}
	// UID match is case-insensitive.
	if _, err := Resolve(root, "A3F9C1"); err != nil {
		t.Errorf("case-insensitive uid: %v", err)
	}
}

func TestResolve_ByExactTitle(t *testing.T) {
	root := seedRegistry(t)
	p, err := Resolve(root, "health analysis") // case-insensitive
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.UID != "7b2e04" {
		t.Errorf("got uid %q", p.UID)
	}
}

func TestResolve_BySubstring(t *testing.T) {
	root := seedRegistry(t)
	p, err := Resolve(root, "reading")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.UID != "a3f9c1" {
		t.Errorf("got uid %q", p.UID)
	}
}

func TestResolve_Ambiguous(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "notes-one-111111", "---\ntitle: Project notes one\nuid: 111111\n---")
	writeProject(t, root, "notes-two-222222", "---\ntitle: Project notes two\nuid: 222222\n---")

	_, err := Resolve(root, "notes") // substring matches both
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("want AmbiguousError, got %v", err)
	}
	if len(amb.Candidates) != 2 {
		t.Errorf("want 2 candidates, got %d", len(amb.Candidates))
	}
}

func TestResolve_ExactTitleBeatsSubstring(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "notes-111111", "---\ntitle: Notes\nuid: 111111\n---")
	writeProject(t, root, "notes-archive-222222", "---\ntitle: Notes archive\nuid: 222222\n---")

	// "notes" is a substring of both, but an exact title match on the first disambiguates.
	p, err := Resolve(root, "Notes")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.UID != "111111" {
		t.Errorf("exact title should win: got uid %q", p.UID)
	}
}

func TestResolve_NotFound(t *testing.T) {
	root := seedRegistry(t)
	if _, err := Resolve(root, "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := Resolve(root, "  "); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blank query want ErrNotFound, got %v", err)
	}
}
