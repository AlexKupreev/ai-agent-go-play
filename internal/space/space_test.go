package space

import (
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Polish lessons", "polish-lessons"},
		{"  The Tax Stuff!  ", "the-tax-stuff"},
		{"already-a-slug", "already-a-slug"},
		{"__weird__/../path", "weird-path"},
		{"***", ""},
		{"", ""},
	} {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStore_CreateGetListResolve(t *testing.T) {
	s := NewStore(t.TempDir() + "/spaces")

	// Empty store: List is empty, not an error (the dir may not exist yet).
	if all, err := s.List(); err != nil || len(all) != 0 {
		t.Fatalf("empty List = %v, %v; want empty, nil", all, err)
	}
	// ...and Resolve against it says so rather than listing nothing.
	if _, err := s.Resolve("anything"); err == nil || !strings.Contains(err.Error(), "no spaces yet") {
		t.Fatalf("Resolve on an empty store = %v, want a 'no spaces yet' error", err)
	}

	sp, err := s.Create("Polish Lessons")
	if err != nil {
		t.Fatal(err)
	}
	if sp.ID != "polish-lessons" || sp.Name != "Polish Lessons" {
		t.Fatalf("Create = %+v, want id polish-lessons", sp)
	}
	// Duplicate (same slug) is rejected.
	if _, err := s.Create("polish lessons"); err == nil {
		t.Fatal("duplicate Create returned nil error")
	}
	// A name with no usable characters is rejected.
	if _, err := s.Create("!!!"); err == nil {
		t.Fatal("unusable name returned nil error")
	}

	if _, err := s.Create("tax"); err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil || len(all) != 2 {
		t.Fatalf("List = %d spaces (%v), want 2", len(all), err)
	}

	// Get by exact id; Resolve by id, by name (case-insensitive), and by sluggable name.
	if _, err := s.Get("polish-lessons"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, q := range []string{"polish-lessons", "Polish Lessons", "POLISH lessons"} {
		got, err := s.Resolve(q)
		if err != nil || got.ID != "polish-lessons" {
			t.Fatalf("Resolve(%q) = %+v, %v", q, got, err)
		}
	}
	// A miss names what would have worked — the message goes straight to a human.
	_, err = s.Resolve("nope")
	if err == nil {
		t.Fatal("Resolve of a missing space returned nil error")
	}
	if !strings.Contains(err.Error(), "polish-lessons") || !strings.Contains(err.Error(), "tax") {
		t.Fatalf("Resolve miss = %q, want it to list the available spaces", err)
	}

	// Ids never escape the spaces dir: path-ish ids fail closed.
	if _, err := s.Get("../evil"); err == nil {
		t.Fatal("path-traversal id returned nil error")
	}
}

func TestStore_SaveNotesAndCap(t *testing.T) {
	s := NewStore(t.TempDir() + "/spaces")
	sp, err := s.Create("tax")
	if err != nil {
		t.Fatal(err)
	}
	sp.Notes = "standing profile: married filing jointly"
	if err := s.Save(sp); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("tax")
	if err != nil || got.Notes != sp.Notes {
		t.Fatalf("reloaded notes = %q, %v", got.Notes, err)
	}
	// The always-loaded profile is size-capped.
	sp.Notes = strings.Repeat("x", MaxNotesLen+1)
	if err := s.Save(sp); err == nil {
		t.Fatal("oversized notes saved, want cap error")
	}
	// Saving an unknown space fails (Save is an update, not an upsert).
	if err := s.Save(Space{ID: "ghost"}); err == nil {
		t.Fatal("Save of unknown space returned nil error")
	}
}

func TestStore_MemoryPathUnderSpaceDir(t *testing.T) {
	s := NewStore("/w/.agent/spaces")
	if got := s.MemoryPath("tax"); got != "/w/.agent/spaces/tax/memory.json" {
		t.Fatalf("MemoryPath = %q", got)
	}
}
