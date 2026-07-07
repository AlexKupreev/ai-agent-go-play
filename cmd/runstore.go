package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"

	"ai-agent-go-play/internal/api"
)

// fileRunStore persists a finished run's metadata as <base>/<run-id>/info.json — a compact,
// single-read summary (task, state, result, usage, timings) next to the run's transcript. It
// implements api.RunStore, so the engine writes it on completion and reads it back as a
// RunStatus fallback for a run it has since evicted (or that predates this process). It is
// best-effort: a write/read/parse failure changes nothing (the transcript stays the full
// record), so a run's status is never worse than "unknown".
type fileRunStore struct{ base string }

func (s fileRunStore) infoPath(id string) string { return filepath.Join(s.base, id, "info.json") }

// Save writes info.json atomically (temp + rename). The run id is engine-generated, but the
// path guard keeps this safe even if that ever changes.
func (s fileRunStore) Save(info api.RunInfo) {
	if !safeRunID(info.ID) {
		return
	}
	dir := filepath.Join(s.base, info.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return
	}
	tmp := s.infoPath(info.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.infoPath(info.ID))
}

// Load reads a persisted RunInfo. The id arrives from GET /runs/{id}, so it is guarded
// against path traversal before it is joined onto the runs dir.
func (s fileRunStore) Load(id string) (api.RunInfo, bool) {
	if !safeRunID(id) {
		return api.RunInfo{}, false
	}
	data, err := os.ReadFile(s.infoPath(id))
	if err != nil {
		return api.RunInfo{}, false
	}
	var info api.RunInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return api.RunInfo{}, false
	}
	return info, true
}

// safeRunID reports whether id is a single, traversal-free path component (the shape every
// engine run id has: hex, optionally with '-'/'_'). Rejects "", "..", and anything with a
// separator, so it can be joined onto a directory without escaping it.
func safeRunID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
