// Package session persists multi-turn conversations so a chat can be resumed across
// process restarts and across frontends (CLI, Telegram, web). A session stores only
// the message history — the live executor is rebuilt per turn and seeded from it, so
// nothing unserializable is persisted. A JSON file per session mirrors the memory and
// tool-catalog stores; SQLite is the eventual home (design §9) behind this interface.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-agent-go-play/internal/provider"
)

// ErrNotFound is returned when a session id is unknown.
var ErrNotFound = errors.New("session not found")

// Session is a persisted conversation: an id plus the message history. The system
// prompt is deliberately NOT stored — the executor re-seeds it from current code each
// turn, so prompt changes take effect on resume.
type Session struct {
	ID        string             `json:"id"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Messages  []provider.Message `json:"messages"`

	// Model and Tier are the session's sticky per-turn overrides: a turn that does not
	// carry its own model/tier inherits these (which in turn fall back to the engine
	// default). Empty ⇒ inherit. Set at creation (POST /sessions) or live (PATCH
	// /sessions/{id}); the stored tier is a REQUEST, clamped to the serve ceiling per turn.
	Model string `json:"model,omitempty"`
	Tier  string `json:"tier,omitempty"`
}

// Info is the lightweight listing form (no message bodies).
type Info struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Turns     int       `json:"turns"` // user messages so far
	Title     string    `json:"title"` // first user message, truncated
	Model     string    `json:"model,omitempty"`
	Tier      string    `json:"tier,omitempty"`
}

// ToInfo projects a session onto its listing form.
func (s Session) ToInfo() Info {
	info := Info{ID: s.ID, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Model: s.Model, Tier: s.Tier}
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser {
			info.Turns++
			if info.Title == "" {
				info.Title = firstText(m)
			}
		}
	}
	return info
}

// Store persists sessions. Implementations must be safe for concurrent use.
//
// Delete/Restore/Purge form the lifecycle the management plane drives: Delete archives
// (recoverably), Restore un-archives, and Purge removes for good — the destructive
// counterpart. See docs/planning/deletion.md.
type Store interface {
	Create() (Session, error)
	Get(id string) (Session, error) // ErrNotFound if absent
	Save(s Session) error
	Delete(id string) error  // archive (recoverable); ErrNotFound if not live
	Restore(id string) error // un-archive; ErrNotFound if no archived session
	Purge(id string) error   // irreversible removal, live or archived; ErrNotFound if neither
	List() ([]Info, error)   // newest-updated first
}

// archiveSubdir holds sessions closed via Delete. Closing archives rather than deletes, so a
// mistaken `/end` is recoverable (Restore). List skips this subdirectory, so archived sessions
// drop out of the resumable listing automatically.
const archiveSubdir = "archive"

// FileStore is a Store backed by one JSON file per session under dir.
type FileStore struct {
	mu  sync.Mutex
	dir string
}

// NewFileStore returns a store writing sessions under dir (created on first write).
func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

func (s *FileStore) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *FileStore) archivePath(id string) string {
	return filepath.Join(s.dir, archiveSubdir, id+".json")
}

func (s *FileStore) Create() (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	sess := Session{ID: newID(), CreatedAt: now, UpdatedAt: now}
	if err := s.write(sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

func (s *FileStore) Get(id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(id)
}

func (s *FileStore) Save(sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.UpdatedAt = time.Now().UTC()
	return s.write(sess)
}

// Delete closes a session by ARCHIVING it: the file moves from sessions/<id>.json to
// sessions/archive/<id>.json rather than being removed, so a mistaken close (`/end`) is
// recoverable (Restore). It then reads as gone — Get/List skip the archive — while the
// bytes survive. ErrNotFound if the session isn't live. Use Purge for irreversible removal.
func (s *FileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.dir, archiveSubdir), 0700); err != nil {
		return err
	}
	return os.Rename(s.path(id), s.archivePath(id))
}

// Restore moves an archived session back to the live set so it can be resumed. ErrNotFound
// if no archived session with that id exists.
func (s *FileStore) Restore(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.archivePath(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return os.Rename(s.archivePath(id), s.path(id))
}

// Purge permanently removes a session — live or archived. It is the irreversible counterpart
// to Delete (which only archives), for a future management plane's "really delete". ErrNotFound
// if neither a live nor an archived file exists.
func (s *FileStore) Purge(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	errLive := os.Remove(s.path(id))
	errArch := os.Remove(s.archivePath(id))
	switch {
	case errLive == nil || errArch == nil:
		return nil // removed at least one
	case os.IsNotExist(errLive) && os.IsNotExist(errArch):
		return ErrNotFound
	case !os.IsNotExist(errLive):
		return errLive
	default:
		return errArch
	}
}

func (s *FileStore) List() ([]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sess, err := s.read(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue // skip a corrupt/partial file rather than fail the whole list
		}
		infos = append(infos, sess.ToInfo())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].UpdatedAt.After(infos[j].UpdatedAt) })
	return infos, nil
}

// read/write assume the caller holds the lock.
func (s *FileStore) read(id string) (Session, error) {
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return Session{}, fmt.Errorf("parse session %s: %w", id, err)
	}
	return sess, nil
}

func (s *FileStore) write(sess Session) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(sess.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(sess.ID))
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// firstText returns the first text block of a message, truncated for a title.
func firstText(m provider.Message) string {
	for _, c := range m.Content {
		if c.Kind == provider.BlockText && c.Text != "" {
			if len(c.Text) > 60 {
				return c.Text[:60]
			}
			return c.Text
		}
	}
	return ""
}
