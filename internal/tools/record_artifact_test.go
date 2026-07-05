package tools

import (
	"context"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/artifact"
)

func TestRecordArtifactAppends(t *testing.T) {
	scratch := t.TempDir()
	m, err := artifact.New(filepath.Join(scratch, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	tool := NewRecordArtifactTool(m, scratch)

	out, err := tool.Run(context.Background(), map[string]any{
		"path":        filepath.Join(scratch, "data.csv"),
		"source":      "https://ex.gov/data.csv",
		"description": "CSV: a, b",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out == "" {
		t.Error("expected a confirmation message")
	}
	entries := m.List()
	if len(entries) != 1 || entries[0].Origin != artifact.OriginAgent {
		t.Fatalf("expected one agent-origin entry, got %+v", entries)
	}
}

func TestRecordArtifactRelativePathResolved(t *testing.T) {
	scratch := t.TempDir()
	m, _ := artifact.New(filepath.Join(scratch, "manifest.json"))
	tool := NewRecordArtifactTool(m, scratch)
	if _, err := tool.Run(context.Background(), map[string]any{"path": "sub/data.csv"}); err != nil {
		t.Fatalf("relative path inside scratch should be accepted: %v", err)
	}
}

func TestRecordArtifactRejectsPathOutsideScratch(t *testing.T) {
	scratch := t.TempDir()
	m, _ := artifact.New(filepath.Join(scratch, "manifest.json"))
	tool := NewRecordArtifactTool(m, scratch)

	for _, bad := range []string{
		"../escape.csv",
		"/etc/passwd",
		filepath.Join(scratch, "..", "sibling.csv"),
	} {
		if _, err := tool.Run(context.Background(), map[string]any{"path": bad}); err == nil {
			t.Errorf("path %q outside scratch should be rejected", bad)
		}
	}
	if len(m.List()) != 0 {
		t.Error("no entries should have been recorded for rejected paths")
	}
}

func TestRecordArtifactRequiresPath(t *testing.T) {
	scratch := t.TempDir()
	m, _ := artifact.New(filepath.Join(scratch, "manifest.json"))
	tool := NewRecordArtifactTool(m, scratch)
	if _, err := tool.Run(context.Background(), map[string]any{"path": "  "}); err == nil {
		t.Error("blank path should be rejected")
	}
}
