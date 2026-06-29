package memory

import (
	"path/filepath"
	"testing"
)

func TestStore_PutGetUpsert(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Put(Entry{Key: "user.editor", Value: "neovim"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := s.Get("user.editor")
	if !ok || got.Value != "neovim" {
		t.Fatalf("get = %+v, %v; want neovim", got, ok)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt was not stamped")
	}

	// Re-using a key overwrites in place.
	if err := s.Put(Entry{Key: "user.editor", Value: "emacs"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got, _ := s.Get("user.editor"); got.Value != "emacs" {
		t.Errorf("after upsert value = %q, want emacs", got.Value)
	}
	if n := len(s.List()); n != 1 {
		t.Errorf("upsert created a duplicate: %d entries", n)
	}
}

func TestStore_EmptyKeyRejected(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Put(Entry{Key: "  ", Value: "x"}); err == nil {
		t.Error("empty key should be rejected")
	}
}

func TestStore_Delete(t *testing.T) {
	s := NewMemoryStore()
	_ = s.Put(Entry{Key: "k", Value: "v"})
	if err := s.Delete("k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := s.Get("k"); ok {
		t.Error("entry should be gone after delete")
	}
	if err := s.Delete("missing"); err == nil {
		t.Error("deleting a missing key should error")
	}
}

func TestStore_Search(t *testing.T) {
	s := NewMemoryStore()
	_ = s.Put(Entry{Key: "proj.lang", Value: "the project is written in Go", Tags: []string{"project"}})
	_ = s.Put(Entry{Key: "user.editor", Value: "neovim", Tags: []string{"preference"}})

	hits := s.Search("project language go", 5)
	if len(hits) == 0 || hits[0].Key != "proj.lang" {
		t.Fatalf("search top hit = %+v, want proj.lang", hits)
	}
	if got := s.Search("nothing matches here xyz", 5); len(got) != 0 {
		t.Errorf("expected no matches, got %d", len(got))
	}
}

// A fact written by one store instance is read back by a fresh instance over the
// same file — the cross-run persistence guarantee.
func TestPersistentStore_ReloadAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")

	s1, err := NewPersistentStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s1.Put(Entry{Key: "build.cmd", Value: "go build ./...", Tags: []string{"project"}}); err != nil {
		t.Fatalf("put: %v", err)
	}

	s2, err := NewPersistentStore(path)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	got, ok := s2.Get("build.cmd")
	if !ok || got.Value != "go build ./..." {
		t.Fatalf("reloaded entry = %+v, %v; want go build ./...", got, ok)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "project" {
		t.Errorf("tags not preserved across reload: %v", got.Tags)
	}
}
