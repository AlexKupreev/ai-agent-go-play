package session

import (
	"errors"
	"testing"

	"ai-agent-go-play/internal/provider"
)

func TestFileStore_RoundTrip(t *testing.T) {
	store := NewFileStore(t.TempDir())

	s, err := store.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.ID == "" {
		t.Fatal("Create returned empty id")
	}

	// Append a couple of turns and save.
	s.Messages = append(s.Messages,
		provider.UserText("hello"),
		provider.Message{Role: provider.RoleAssistant, Content: []provider.ContentBlock{{Kind: provider.BlockText, Text: "hi there"}}},
	)
	if err := store.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload: history persisted.
	got, err := store.Get(s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("reloaded history wrong: %+v", got.Messages)
	}

	// List reflects the session with a title + turn count.
	infos, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != s.ID || infos[0].Turns != 1 || infos[0].Title != "hello" {
		t.Fatalf("unexpected list: %+v", infos)
	}

	// Delete removes it; a second delete is ErrNotFound.
	if err := store.Delete(s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := store.Delete(s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

// TestFileStore_PersistsAcrossInstances proves resumability: a second store over the
// same dir sees a session written by the first (the point of on-disk sessions).
func TestFileStore_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	first := NewFileStore(dir)
	s, err := first.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.Messages = append(s.Messages, provider.UserText("remember me"))
	if err := first.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	second := NewFileStore(dir)
	got, err := second.Get(s.ID)
	if err != nil {
		t.Fatalf("Get from second store: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "remember me" {
		t.Fatalf("did not resume across instances: %+v", got.Messages)
	}
}
