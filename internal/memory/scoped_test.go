package memory

import "testing"

// TestScopedStore proves the active-over-global layering (spaces.md §4): writes land in
// the active space, reads merge with the active scope shadowing global keys.
func TestScopedStore(t *testing.T) {
	active, global := NewMemoryStore(), NewMemoryStore()
	s := NewScopedStore(active, global)

	if err := global.Put(Entry{Key: "user.editor", Value: "vim"}); err != nil {
		t.Fatal(err)
	}
	if err := global.Put(Entry{Key: "level", Value: "global-level"}); err != nil {
		t.Fatal(err)
	}

	// Put goes to the active space only.
	if err := s.Put(Entry{Key: "level", Value: "B1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := active.Get("level"); !ok {
		t.Fatal("Put did not land in the active store")
	}
	if e, _ := global.Get("level"); e.Value != "global-level" {
		t.Fatal("Put touched the global store")
	}

	// Get: active shadows global; global still visible for unshadowed keys.
	if e, ok := s.Get("level"); !ok || e.Value != "B1" {
		t.Fatalf("Get(level) = %+v, want the space's B1", e)
	}
	if e, ok := s.Get("user.editor"); !ok || e.Value != "vim" {
		t.Fatalf("Get(user.editor) = %+v, want the global vim", e)
	}

	// List: both scopes, shadowed global entry absent, active first.
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("List = %d entries, want 2 (shadowed global dropped)", len(list))
	}
	if list[0].Key != "level" || list[0].Value != "B1" {
		t.Fatalf("List[0] = %+v, want the active space's entry first", list[0])
	}

	// Search: same merge, bounded by k.
	hits := s.Search("level", 0)
	if len(hits) != 1 || hits[0].Value != "B1" {
		t.Fatalf("Search(level) = %+v, want only the space's entry", hits)
	}
	if hits := s.Search("vim editor", 1); len(hits) != 1 || hits[0].Key != "user.editor" {
		t.Fatalf("Search(vim) = %+v, want the global entry", hits)
	}

	// Delete removes from the active scope first, re-exposing the global entry.
	if err := s.Delete("level"); err != nil {
		t.Fatal(err)
	}
	if e, ok := s.Get("level"); !ok || e.Value != "global-level" {
		t.Fatalf("after Delete, Get(level) = %+v, want the global entry back", e)
	}
	if err := s.Delete("level"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("level"); ok {
		t.Fatal("second Delete did not remove the global entry")
	}
}
