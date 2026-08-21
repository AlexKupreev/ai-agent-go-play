package cmd

import (
	"strings"
	"testing"

	"ai-agent-go-play/internal/space"
)

// TestSpaceResolver proves the seam serve hands the engine: a name or id resolves to the
// canonical id to store, and a miss reports the spaces that would have worked (this error
// is what a user of /space sees).
func TestSpaceResolver(t *testing.T) {
	spaces := space.NewStore(t.TempDir() + "/spaces")
	if _, err := spaces.Create("Polish lessons"); err != nil {
		t.Fatal(err)
	}
	resolve := spaceResolver(spaces)

	for _, q := range []string{"polish-lessons", "Polish lessons"} {
		id, err := resolve(q)
		if err != nil || id != "polish-lessons" {
			t.Fatalf("resolve(%q) = %q, %v; want polish-lessons", q, id, err)
		}
	}
	_, err := resolve("polsih")
	if err == nil {
		t.Fatal("resolve of a missing space returned nil error")
	}
	if !strings.Contains(err.Error(), "polish-lessons") {
		t.Fatalf("resolve error = %q, want it to name polish-lessons", err)
	}
}
