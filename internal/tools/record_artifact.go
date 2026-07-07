package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"ai-agent-go-play/internal/artifact"
)

// NewRecordArtifactTool returns the `record_artifact` built-in: the executor records a
// sizeable intermediate it materialized so the planner sees it in next turn's manifest
// (docs/adr/chat-planner.md §D4). It is an auto-permitted host tool — it only appends
// a manifest entry and only accepts a path inside scratchDir, so it holds no capability,
// touches no network, and reaches no arbitrary path; it never prompts, at any tier (§D4).
// Recording must be as frictionless as writing the file it describes, or the manifest
// goes stale.
//
// origin is fixed to "agent" here: user-provided files are registered by the loop on an
// explicit attach (§D4), not by the model.
func NewRecordArtifactTool(m *artifact.Manifest, scratchDir string) Tool {
	return Tool{
		Name: "record_artifact",
		Description: "Record a data file you materialized in your scratch directory so it is tracked and " +
			"available to reuse next turn. Call this right after writing any sizeable intermediate (a " +
			"downloaded dataset, an extracted/cleaned CSV, a computed result file). Give the path (inside " +
			"your scratch dir), where it came from, and a one-line note on its shape/columns.",
		Parameters: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file inside your scratch directory.",
			},
			"source": map[string]any{
				"type":        "string",
				"description": "Where it came from / can be re-fetched (a URL, an API, or how it was derived). Optional.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "One-line note on the artifact's shape (e.g. 'CSV: date, region, sales'). Optional.",
			},
		},
		Required: []string{"path"},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			path = strings.TrimSpace(path)
			if path == "" {
				return "", fmt.Errorf("record_artifact: path is required")
			}
			if !withinDir(scratchDir, path) {
				return "", fmt.Errorf("record_artifact: path %q must be inside the scratch directory %q", path, scratchDir)
			}
			source, _ := args["source"].(string)
			desc, _ := args["description"].(string)
			e := artifact.Entry{
				Path:        path,
				Origin:      artifact.OriginAgent,
				Source:      strings.TrimSpace(source),
				Description: strings.TrimSpace(desc),
			}
			if err := m.Append(e); err != nil {
				return "", fmt.Errorf("record_artifact: %w", err)
			}
			return fmt.Sprintf("recorded artifact %s", path), nil
		},
	}
}

// withinDir reports whether path (relative paths resolved against dir) stays inside dir.
// It rejects traversal outside the scratch tree so the manifest can only index files the
// agent actually wrote there.
func withinDir(dir, path string) bool {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(dir, path)
	}
	rel, err := filepath.Rel(dir, filepath.Clean(abs))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
