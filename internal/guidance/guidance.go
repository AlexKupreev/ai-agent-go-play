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
	"strings"
	"sync"
	"unicode/utf8"

	"ai-agent-go-play/internal/audit"
)

// MaxChars is the independent limit for each persistent guidance scope. Character
// counts are Unicode code points, not UTF-8 bytes, matching what the UI reports.
const MaxChars = 4000

// Scope names the three durable guidance layers, from broadest to most specific.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeSpace   Scope = "space"
	ScopeSession Scope = "session"
)

// ParseScope validates a user/API-facing scope name.
func ParseScope(value string) (Scope, error) {
	scope := Scope(strings.ToLower(strings.TrimSpace(value)))
	switch scope {
	case ScopeGlobal, ScopeSpace, ScopeSession:
		return scope, nil
	default:
		return "", fmt.Errorf("unknown guidance scope %q (want global, space, or session)", value)
	}
}

// Command is the transport-neutral form of a /guidance command.
type Command struct {
	Scope Scope
	Op    string
	Text  string
}

// ParseCommand parses "<scope> show|set|add|clear [text]" without losing spaces in
// the guidance body. The leading /guidance word is intentionally not part of input.
func ParseCommand(input string) (Command, error) {
	scopeWord, rest := cutWord(input)
	scope, err := ParseScope(scopeWord)
	if err != nil {
		if scopeWord == "" {
			return Command{}, fmt.Errorf("usage: /guidance global|space|session show|set|add|clear [text]")
		}
		return Command{}, err
	}
	op, text := cutWord(rest)
	op = strings.ToLower(op)
	switch op {
	case "show", "clear":
		if text != "" {
			return Command{}, fmt.Errorf("/guidance %s %s does not take text", scope, op)
		}
	case "set", "add":
		if text == "" {
			return Command{}, fmt.Errorf("usage: /guidance %s %s <text>", scope, op)
		}
	default:
		return Command{}, fmt.Errorf("usage: /guidance %s show|set|add|clear [text]", scope)
	}
	return Command{Scope: scope, Op: op, Text: text}, nil
}

func cutWord(input string) (string, string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ""
	}
	if at := strings.IndexAny(input, " \t\r\n"); at >= 0 {
		return input[:at], strings.TrimSpace(input[at:])
	}
	return input, ""
}

// CommandResult is returned by ApplyCommand. Guidance is populated only for show;
// mutation acknowledgements therefore cannot accidentally echo the body.
type CommandResult struct {
	Scope    Scope
	Op       string
	Guidance string
	Chars    int
	Changed  bool
}

// ApplyCommand executes a parsed command against context-resolved read/write functions.
// Add places a newline between existing and appended guidance. Clear is idempotent.
func ApplyCommand(cmd Command, get func(Scope) (string, error), set func(Scope, string) error) (CommandResult, error) {
	previous, err := get(cmd.Scope)
	if err != nil {
		return CommandResult{}, err
	}
	result := CommandResult{Scope: cmd.Scope, Op: cmd.Op, Chars: CharCount(previous)}
	if cmd.Op == "show" {
		result.Guidance = previous
		return result, nil
	}

	next := cmd.Text
	switch cmd.Op {
	case "add":
		if previous != "" {
			next = previous + "\n" + cmd.Text
		}
	case "clear":
		next = ""
	}
	if err := Validate(next); err != nil {
		return CommandResult{}, err
	}
	result.Chars = CharCount(next)
	result.Changed = previous != next
	if !result.Changed {
		return result, nil
	}
	if err := set(cmd.Scope, next); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

// FormatResult renders the common human-facing response used by CLI and chat frontends.
func FormatResult(result CommandResult) string {
	label := string(result.Scope) + " guidance"
	if result.Op == "show" {
		if result.Guidance == "" {
			return fmt.Sprintf("%s is empty (0/%d chars)", label, MaxChars)
		}
		return fmt.Sprintf("%s (%d/%d chars):\n\n%s", label, result.Chars, MaxChars, result.Guidance)
	}
	if !result.Changed {
		return fmt.Sprintf("%s unchanged (%d/%d chars)", label, result.Chars, MaxChars)
	}
	if result.Op == "clear" {
		return fmt.Sprintf("%s cleared (0/%d chars)", label, MaxChars)
	}
	return fmt.Sprintf("%s updated (%d/%d chars)", label, result.Chars, MaxChars)
}

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
