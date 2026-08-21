// Package guidance persists user-authored standing instructions that are loaded into
// executor prompts. Workspace guidance is deliberately separate from operator prompt
// files: it is user-manageable state, independently size-capped, and safe to clear
// without changing the agent's immutable prompt blocks.
package guidance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"ai-agent-go-play/internal/audit"
)

// MaxChars is the independent limit for each persistent guidance scope. Character
// counts are Unicode code points, not UTF-8 bytes, matching what the UI reports.
const MaxChars = 4000

// CharCount returns the size used for guidance limits and user-facing metadata.
func CharCount(text string) int { return utf8.RuneCountInString(text) }

// Validate rejects guidance that would make an always-loaded prompt scope too large.
func Validate(text string) error {
	if n := CharCount(text); n > MaxChars {
		return fmt.Errorf("guidance exceeds %d characters (%d)", MaxChars, n)
	}
	return nil
}

// Store is the narrow workspace-guidance persistence seam used by cmd and, later, the
// management plane. A missing value reads as empty; setting empty clears it.
type Store interface {
	Get() (string, error)
	Set(string) error
}

// FileStore stores workspace guidance in one text file. Writes use a same-directory
// temporary file plus rename; clears atomically remove the directory entry. The optional
// recorder receives metadata only, never the guidance body.
type FileStore struct {
	mu    sync.Mutex
	path  string
	rec   audit.Recorder
	scope string
}

// NewFileStore returns a workspace guidance store at path. rec may be nil. Scope is the
// audit-facing name (normally "global"); an empty scope defaults to "global".
func NewFileStore(path, scope string, rec audit.Recorder) *FileStore {
	if scope == "" {
		scope = "global"
	}
	return &FileStore{path: path, scope: scope, rec: rec}
}

// Get returns the exact stored text. A missing file is an empty scope.
func (s *FileStore) Get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Set atomically replaces the text. Empty clears the file; setting an unchanged value is
// idempotent. Only actual changes produce guidance_updated audit metadata.
func (s *FileStore) Set(text string) error {
	if err := Validate(text); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	previous, err := s.read()
	if err != nil {
		return err
	}
	if previous == text {
		// Normalize a manually-created empty file to the documented empty-scope
		// representation (missing), without auditing a semantic no-op.
		if text == "" {
			if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	if text == "" {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := s.write(text); err != nil {
		return err
	}
	s.recordUpdate(previous, text)
	return nil
}

// read assumes the caller holds s.mu.
func (s *FileStore) read() (string, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	text := string(data)
	if err := Validate(text); err != nil {
		return "", fmt.Errorf("read workspace guidance: %w", err)
	}
	return text, nil
}

// write assumes the caller holds s.mu.
func (s *FileStore) write(text string) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".guidance-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

func (s *FileStore) recordUpdate(previous, resulting string) {
	RecordUpdate(s.rec, "", s.scope, previous, resulting, nil)
}

// RecordUpdate emits the shared redacted audit shape for a changed guidance scope.
// Callers may add non-sensitive identity metadata such as space_id or session_id.
// Equal values are a semantic no-op and are not recorded.
func RecordUpdate(rec audit.Recorder, runID, scope, previous, resulting string, extra map[string]any) {
	if rec == nil || previous == resulting {
		return
	}
	fields := map[string]any{
		"scope":          scope,
		"previous_size":  CharCount(previous),
		"previous_hash":  hash(previous),
		"resulting_size": CharCount(resulting),
		"resulting_hash": hash(resulting),
	}
	for key, value := range extra {
		fields[key] = value
	}
	rec.Record(audit.Event{Type: audit.EventGuidanceUpdated, Run: runID, Fields: fields})
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
