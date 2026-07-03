package agent

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/selfdocs"
	"ai-agent-go-play/internal/tools"
)

// The agent reads its own docs via read_self_docs, then answers. Proves the tool is
// offered when a doc set is wired and that it returns the real doc body.
func TestSelfDocs_EndToEnd(t *testing.T) {
	docs, err := selfdocs.New(fstest.MapFS{
		"docs/usage.md": {Data: []byte("# Usage\n\nThe trust tier is the autonomy dial.")},
	})
	if err != nil {
		t.Fatal(err)
	}

	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "read_self_docs", map[string]any{"topic": "usage"}),
		textStep("The tier is the autonomy dial."),
	}}
	obs := &captureObserver{}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Observer: obs, Registry: tools.NewMemoryRegistry(),
		Docs: docs, Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})

	if _, err := exec.Run(context.Background(), "how does the tier work?"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// read_self_docs was offered to the model.
	offered := false
	for _, name := range prov.seenTools[0] {
		if name == "read_self_docs" {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("read_self_docs not offered; tools = %v", prov.seenTools[0])
	}

	// Its result carried the real doc body.
	var gotBody bool
	for _, e := range obs.events {
		if e.Kind == EvToolResult && e.Call != nil && e.Call.Name == "read_self_docs" {
			if strings.Contains(e.Result, "autonomy dial") {
				gotBody = true
			}
		}
	}
	if !gotBody {
		t.Fatal("read_self_docs result did not contain the doc body")
	}
}

// Without a doc set the tool is omitted (nil docs ⇒ no dangling tool).
func TestSelfDocs_OmittedWhenNil(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{textStep("done")}}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Registry: tools.NewMemoryRegistry(),
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})
	if _, err := exec.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, name := range prov.seenTools[0] {
		if name == "read_self_docs" {
			t.Fatal("read_self_docs offered even though no doc set was wired")
		}
	}
}
