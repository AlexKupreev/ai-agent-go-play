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
}

// Info is the lightweight listing form (no message bodies).
type Info struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Turns     int       `json:"turns"` // user messages so far
	Title     string    `json:"title"` // first user message, truncated
}

// Info projects a session onto its listing form.
func (s Session) toInfo() Info {
	info := Info{ID: s.ID, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
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
type Store interface {
	Create() (Session, error)
	Get(id string) (Session, error) // ErrNotFound if absent
	Save(s Session) error
	Delete(id string) error // ErrNotFound if absent
	List() ([]Info, error)  // newest-updated first
}

// FileStore is a Store backed by one JSON file per session under dir.
type FileStore struct {
	mu  sync.Mutex
	dir string
}

// NewFileStore returns a store writing sessions under dir (created on first write).
func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

func (s *FileStore) path(id string) string { return filepath.Join(s.dir, id+".json") }

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

func (s *FileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	return err
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
		infos = append(infos, sess.toInfo())
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
