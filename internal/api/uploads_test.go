package api

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ai-agent-go-play/internal/session"
)

// memFileStore is a FileStore that keeps uploads in memory, standing in for the cmd layer's
// scratch-dir + manifest store.
type memFileStore struct {
	mu    sync.Mutex
	saved map[string]string // stored name -> content
	last  UploadInfo
	src   string
}

func newMemFileStore() *memFileStore { return &memFileStore{saved: map[string]string{}} }

func (s *memFileStore) SaveUpload(sessionID, name, source string, r io.Reader) (UploadInfo, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return UploadInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved[name] = string(body)
	s.src = source
	s.last = UploadInfo{Path: "/scratch/" + sessionID + "/" + name, Name: name, Bytes: int64(len(body))}
	return s.last, nil
}

// TestUploadFileRoundTrip drives the whole upload path a frontend uses: Client.UploadFile ⇒
// POST /sessions/{id}/files ⇒ FileStore, with the file streamed through untouched.
func TestUploadFileRoundTrip(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())
	store := newMemFileStore()
	e.SetFileStore(store)

	srv := httptest.NewServer(NewServer(e, nil, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	sessionID, err := c.StartSession(ctx, RunOptions{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	const content = "date,region,sales\n2026-01-01,eu,10\n"
	info, err := c.UploadFile(ctx, sessionID, "sales.csv", "telegram upload", strings.NewReader(content))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if info.Name != "sales.csv" || info.Bytes != int64(len(content)) {
		t.Errorf("info = %+v, want sales.csv of %d bytes", info, len(content))
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.saved["sales.csv"]; got != content {
		t.Errorf("stored %q, want the uploaded bytes %q", got, content)
	}
	if store.src != "telegram upload" {
		t.Errorf("source = %q, want %q — provenance reaches the manifest", store.src, "telegram upload")
	}
}

// TestUploadFileUnknownSession keeps an upload scoped to a real conversation: its lifecycle
// (reaped on close, deleted on purge) belongs to a session, so a file with no session is a 404
// rather than an orphan on disk.
func TestUploadFileUnknownSession(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())
	store := newMemFileStore()
	e.SetFileStore(store)

	srv := httptest.NewServer(NewServer(e, nil, nil, nil, nil))
	defer srv.Close()

	_, err := NewClient(srv.URL).UploadFile(context.Background(), "deadbeef", "x.csv", "test", strings.NewReader("x"))
	if err == nil {
		t.Fatal("UploadFile to an unknown session succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v, want a 404", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.saved) != 0 {
		t.Errorf("stored %d files for an unknown session, want 0", len(store.saved))
	}
}

// TestUploadsDisabled: with no FileStore wired, the endpoint does not exist at all.
func TestUploadsDisabled(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())
	if e.UploadsEnabled() {
		t.Fatal("uploads enabled with no FileStore")
	}

	srv := httptest.NewServer(NewServer(e, nil, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	sessionID, err := c.StartSession(ctx, RunOptions{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if _, err := c.UploadFile(ctx, sessionID, "x.csv", "test", strings.NewReader("x")); err == nil {
		t.Fatal("UploadFile succeeded with uploads disabled, want an error")
	}
}
