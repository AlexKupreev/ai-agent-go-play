package logger

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Logger struct {
	RunID        string
	SessionDir   string // ~/.local/share/ai-agent/sessions/<runID>
	ArtifactsDir string // ~/.local/share/ai-agent/sessions/<runID>/artifacts
	Path         string // path to run.jsonl
	file         *os.File
}

// New creates a session directory under a freshly generated run id. See NewWithID.
func New(baseDir string) (*Logger, error) {
	return NewWithID(baseDir, generateRunID())
}

// NewWithID creates a session directory for the given run id and opens the log file
// inside it. Callers that already have a run id (e.g. the engine, so the session
// dir, event stream, audit records, and approvals all share one id) pass it here.
//
// baseDir is the sessions root; the transcript lands in baseDir/<runID>/. An empty
// baseDir falls back to the default (~/.local/share/ai-agent/sessions), so distinct
// agents can keep separate transcripts by passing distinct roots. Structure:
//
//	<baseDir>/<runID>/
//	  run.jsonl
//	  artifacts/
func NewWithID(baseDir, runID string) (*Logger, error) {
	sessionsDir := baseDir
	if sessionsDir == "" {
		var err error
		sessionsDir, err = baseSessionsDir()
		if err != nil {
			return nil, err
		}
	}

	sessionDir := filepath.Join(sessionsDir, runID)
	artifactsDir := filepath.Join(sessionDir, "artifacts")

	for _, dir := range []string{sessionDir, artifactsDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}

	logPath := filepath.Join(sessionDir, "run.jsonl")
	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}

	return &Logger{
		RunID:        runID,
		SessionDir:   sessionDir,
		ArtifactsDir: artifactsDir,
		Path:         logPath,
		file:         f,
	}, nil
}

func (l *Logger) Close() error {
	return l.file.Close()
}

// LogStart records the task at the beginning of a run.
func (l *Logger) LogStart(task string) {
	l.write("start", map[string]any{
		"task": task,
	})
}

// LogRequest records the full message history sent to the LLM on each iteration.
func (l *Logger) LogRequest(iteration int, messages any) {
	l.write("request", map[string]any{
		"iteration": iteration,
		"messages":  messages,
	})
}

// LogResponse records what the LLM returned, including token usage and latency.
func (l *Logger) LogResponse(iteration int, content string, toolCalls any, usage any, durationMs int64) {
	l.write("response", map[string]any{
		"iteration":   iteration,
		"content":     content,
		"tool_calls":  toolCalls,
		"usage":       usage,
		"duration_ms": durationMs,
	})
}

// LogToolResult records a tool call and its result.
func (l *Logger) LogToolResult(toolName, callID, args, result string) {
	l.write("tool_result", map[string]any{
		"tool":    toolName,
		"call_id": callID,
		"args":    args,
		"result":  result,
	})
}

// write appends one JSON line to the log file.
func (l *Logger) write(entryType string, data map[string]any) {
	data["type"] = entryType
	data["ts"] = time.Now().UTC().Format(time.RFC3339Nano)

	line, err := json.Marshal(data)
	if err != nil {
		return
	}
	l.file.Write(line)
	l.file.Write([]byte("\n"))
}

func generateRunID() string {
	ts := time.Now().UTC().Format("2006-01-02T15-04-05")
	b := make([]byte, 4) // 4 bytes = 8 hex chars
	rand.Read(b)
	return ts + "_" + hex.EncodeToString(b)
}

func baseSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "ai-agent", "sessions"), nil
}
