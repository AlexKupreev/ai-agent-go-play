package session

import (
	"errors"
	"os"
	"path/filepath"
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

// TestFileStore_DeleteArchivesAndRestore proves Delete archives (not destroys): the closed
// session leaves the live set and listing but its file survives under archive/, and Restore
// brings it back resumable with its history intact.
func TestFileStore_DeleteArchivesAndRestore(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	s, err := store.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.Messages = append(s.Messages, provider.UserText("keep me"))
	if err := store.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete(s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Gone from the live set and the resumable listing…
	if _, err := store.Get(s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
	if infos, err := store.List(); err != nil || len(infos) != 0 {
		t.Fatalf("List after delete = (%+v, %v), want empty", infos, err)
	}
	// …but the bytes survive under archive/.
	if _, err := os.Stat(filepath.Join(dir, archiveSubdir, s.ID+".json")); err != nil {
		t.Fatalf("archived file missing: %v", err)
	}

	// Restore brings it back with its history.
	if err := store.Restore(s.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := store.Get(s.ID)
	if err != nil {
		t.Fatalf("Get after restore: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "keep me" {
		t.Fatalf("restored history wrong: %+v", got.Messages)
	}
	// Restoring something not archived is ErrNotFound.
	if err := store.Restore("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Restore unknown = %v, want ErrNotFound", err)
	}
}

// TestFileStore_Purge proves Purge is the irreversible removal — of a live session and of an
// archived one — and is ErrNotFound when neither exists.
func TestFileStore_Purge(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	// Purge a live session.
	live, _ := store.Create()
	if err := store.Purge(live.ID); err != nil {
		t.Fatalf("Purge live: %v", err)
	}
	if _, err := store.Get(live.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after purge = %v, want ErrNotFound", err)
	}

	// Purge an archived session (Delete then Purge), and confirm it can't be restored.
	arch, _ := store.Create()
	if err := store.Delete(arch.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Purge(arch.ID); err != nil {
		t.Fatalf("Purge archived: %v", err)
	}
	if err := store.Restore(arch.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Restore after purge = %v, want ErrNotFound", err)
	}

	// Purging what was never there is ErrNotFound.
	if err := store.Purge("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Purge unknown = %v, want ErrNotFound", err)
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
