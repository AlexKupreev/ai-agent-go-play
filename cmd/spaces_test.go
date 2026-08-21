package cmd

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/api"
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

func TestSpaceCommandOutputContract(t *testing.T) {
	store := space.NewStore(t.TempDir() + "/spaces")
	engine := api.NewEngine(api.RunnerFunc(func(_ context.Context, _, _ string, _ api.RunOptions, _ agent.Observer) (string, error) {
		return "", nil
	}))
	engine.SetSpaceService(store)
	srv := httptest.NewServer(api.NewServer(engine, nil, nil, nil, nil))
	defer srv.Close()

	originalAddr := spaceAddrFlag
	spaceAddrFlag = strings.TrimPrefix(srv.URL, "http://")
	listOut, showOut, createOut := spaceListCmd.OutOrStdout(), spaceShowCmd.OutOrStdout(), spaceCreateCmd.OutOrStdout()
	t.Cleanup(func() {
		spaceAddrFlag = originalAddr
		spaceListCmd.SetOut(listOut)
		spaceShowCmd.SetOut(showOut)
		spaceCreateCmd.SetOut(createOut)
	})

	var out bytes.Buffer
	spaceListCmd.SetOut(&out)
	if err := spaceListCmd.RunE(spaceListCmd, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no spaces (create one with: agent space create <name>)\n" {
		t.Fatalf("empty list output = %q", got)
	}

	out.Reset()
	spaceCreateCmd.SetOut(&out)
	if err := spaceCreateCmd.RunE(spaceCreateCmd, []string{"Polish", "lessons"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "created space \"Polish lessons\" (id polish-lessons)\n" {
		t.Fatalf("create output = %q", got)
	}

	out.Reset()
	spaceShowCmd.SetOut(&out)
	if err := spaceShowCmd.RunE(spaceShowCmd, []string{"polish-lessons"}); err != nil {
		t.Fatal(err)
	}
	shown := out.String()
	for _, line := range []string{"id: polish-lessons", "name: Polish lessons", "guidance: 0 chars", "created:", "updated:"} {
		if !strings.Contains(shown, line) {
			t.Fatalf("show output missing %q: %s", line, shown)
		}
	}

	out.Reset()
	spaceListCmd.SetOut(&out)
	if err := spaceListCmd.RunE(spaceListCmd, nil); err != nil {
		t.Fatal(err)
	}
	listed := out.String()
	if !strings.HasPrefix(listed, "SPACE             NAME                  GUIDANCE  UPDATED\n") ||
		!strings.Contains(listed, "polish-lessons") || !strings.Contains(listed, time.Now().UTC().Format("2006-01-02")) {
		t.Fatalf("list output = %q", listed)
	}
}
