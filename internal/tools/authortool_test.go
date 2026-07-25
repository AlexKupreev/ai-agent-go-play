package tools

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/sandbox"
)

// authorFixture wires author_tool to a fresh registry, in-memory audit, a
// broker-backed sandbox, and a programmable confirm.
type authorFixture struct {
	tool    Tool
	reg     *MemoryRegistry
	rec     *audit.MemoryRecorder
	confirm *fakeConfirm
}

type fakeConfirm struct {
	answer bool
	calls  int
}

func (f *fakeConfirm) Approve(context.Context, ApprovalRequest) (bool, error) {
	f.calls++
	return f.answer, nil
}

func (f *fakeConfirm) Ask(context.Context, Question) (string, error) { return "", nil }

func newAuthorFixture(t *testing.T, tier capability.Tier, approve bool) authorFixture {
	t.Helper()
	reg := NewMemoryRegistry()
	rec := &audit.MemoryRecorder{}
	glue := sandbox.NewLuaGlue(capability.NewBroker(rec, nil))
	cf := &fakeConfirm{answer: approve}
	tool := NewAuthorTool(AuthorToolDeps{
		Registry: reg, Glue: glue, Audit: rec, Tier: tier, RunID: "test-run", Gate: cf,
	})
	return authorFixture{tool: tool, reg: reg, rec: rec, confirm: cf}
}

// TestAuthorTool_DescriptionDocumentsHostGlobals guards the host-function reference so
// the agent can see each granted capability's injected global (name + signature) while
// authoring, instead of guessing the calling convention.
func TestAuthorTool_DescriptionDocumentsHostGlobals(t *testing.T) {
	desc := NewAuthorTool(AuthorToolDeps{}).Description
	for _, want := range []string{
		"http_get(url) -> string",
		"read_file(path) -> string",
		"write_file(path, content)",
		"call_tool(name, args_table) -> string",
		"now() -> number",
		"random(n) -> string",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("author_tool description missing host-global signature %q", want)
		}
	}
}

// The secret mechanism is only usable if the model knows which names exist — it cannot read
// a value, and a guessed name fails closed at the broker. So the names belong in the
// required_caps description, and their absence must be stated too.
func TestAuthorTool_DescriptionListsStoredSecretNames(t *testing.T) {
	caps := func(d AuthorToolDeps) string {
		p, ok := NewAuthorTool(d).Parameters["required_caps"].(map[string]any)
		if !ok {
			t.Fatal("required_caps parameter is not an object")
		}
		s, _ := p["description"].(string)
		return s
	}

	none := caps(AuthorToolDeps{})
	if !strings.Contains(none, "No secrets are stored") {
		t.Errorf("with no secrets, required_caps should say so; got %q", none)
	}

	listed := caps(AuthorToolDeps{SecretNames: []string{"scrapingant", "weather"}})
	for _, want := range []string{"scrapingant", "weather"} {
		if !strings.Contains(listed, want) {
			t.Errorf("required_caps description missing secret name %q; got %q", want, listed)
		}
	}
	if strings.Contains(listed, "No secrets are stored") {
		t.Error("required_caps should not claim no secrets when names are configured")
	}
}

func authorArgs(name, code, test string) map[string]any {
	return map[string]any{
		"name":         name,
		"description":  "a test tool",
		"input_schema": map[string]any{"type": "object"},
		"code":         code,
		"test":         test,
	}
}

func TestAuthorTool_HappyPath_ComputeTool(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false)
	out, err := f.tool.Run(context.Background(),
		authorArgs("adder", "return input.a + input.b", "assert(tool({a=2,b=3}) == 5); return true"))
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	if !strings.Contains(out, "registered tool") {
		t.Fatalf("expected success, got %q", out)
	}
	if _, ok := f.reg.Get("adder"); !ok {
		t.Error("tool not registered")
	}
	if !hasAuthoredEvent(f.rec, "adder") {
		t.Errorf("expected tool_authored audit event, got %+v", f.rec.Snapshot())
	}
}

func TestAuthorTool_ParseError_Rejected(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false)
	out, _ := f.tool.Run(context.Background(),
		authorArgs("broken", "return input.a +", "return true"))
	if !strings.Contains(out, "syntax error") {
		t.Errorf("expected syntax-error rejection, got %q", out)
	}
	if _, ok := f.reg.Get("broken"); ok {
		t.Error("broken tool should not be registered")
	}
}

func TestAuthorTool_TestFails_Rejected(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false)
	// Test returns false → must reject.
	out, _ := f.tool.Run(context.Background(),
		authorArgs("wrong", "return 1", "return tool({}) == 2"))
	if !strings.Contains(out, "test did not return true") {
		t.Errorf("expected test-failure rejection, got %q", out)
	}
	if _, ok := f.reg.Get("wrong"); ok {
		t.Error("tool failing its test should not be registered")
	}
}

func TestAuthorTool_TestRaises_Rejected(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false)
	out, _ := f.tool.Run(context.Background(),
		authorArgs("asserts", "return 1", "assert(tool({}) == 2); return true"))
	if !strings.Contains(out, "test failed") {
		t.Errorf("expected test-failed rejection, got %q", out)
	}
}

func TestAuthorTool_BadName_Rejected(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false)
	out, _ := f.tool.Run(context.Background(),
		authorArgs("Bad Name", "return 1", "return true"))
	if !strings.Contains(out, "rejected") {
		t.Errorf("expected rejection, got %q", out)
	}
}

func TestAuthorTool_MissingTest_Rejected(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false)
	args := authorArgs("notest", "return 1", "")
	out, _ := f.tool.Run(context.Background(), args)
	if !strings.Contains(out, "test is mandatory") {
		t.Errorf("expected mandatory-test rejection, got %q", out)
	}
}

// A capability beyond the tier prompts; declining rejects before any effect.
func TestAuthorTool_CapBeyondTier_DeclinedRejects(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false) // confirm answers no
	args := authorArgs("writer", `write_file("/tmp/x", "y"); return true`, "return true")
	args["required_caps"] = []any{
		map[string]any{"kind": "write_file", "path_prefix": "/tmp"},
	}
	out, _ := f.tool.Run(context.Background(), args)
	if !strings.Contains(out, "declined") {
		t.Errorf("expected decline rejection, got %q", out)
	}
	if f.confirm.calls != 1 {
		t.Errorf("expected one confirm prompt, got %d", f.confirm.calls)
	}
	if _, ok := f.reg.Get("writer"); ok {
		t.Error("declined tool should not be registered")
	}
}

// A balanced-tier benign capability (clock) auto-approves: no prompt, tool runs.
func TestAuthorTool_BenignCapAutoApproves(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false)
	args := authorArgs("clock_tool", "return now()", "assert(tool({}) ~= nil); return true")
	args["required_caps"] = []any{map[string]any{"kind": "clock"}}
	out, err := f.tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	if !strings.Contains(out, "registered tool") {
		t.Fatalf("expected success, got %q", out)
	}
	if f.confirm.calls != 0 {
		t.Errorf("benign cap should not prompt, got %d calls", f.confirm.calls)
	}
}

// No approval channel + cap beyond tier → rejected (unattended-safe default).
func TestAuthorTool_CapBeyondTier_NoConfirmRejects(t *testing.T) {
	reg := NewMemoryRegistry()
	rec := &audit.MemoryRecorder{}
	glue := sandbox.NewLuaGlue(capability.NewBroker(rec, nil))
	tool := NewAuthorTool(AuthorToolDeps{Registry: reg, Glue: glue, Audit: rec, Tier: capability.TierBalanced, RunID: "r", Gate: nil})

	args := authorArgs("writer", `write_file("/tmp/x","y"); return true`, "return true")
	args["required_caps"] = []any{map[string]any{"kind": "write_file", "path_prefix": "/tmp"}}
	out, _ := tool.Run(context.Background(), args)
	if !strings.Contains(out, "no approval channel") {
		t.Errorf("expected no-channel rejection, got %q", out)
	}
}

// Authoring identical code under a new name is deduped: the model is pointed at
// the existing tool instead of creating a duplicate.
func TestAuthorTool_DedupsIdenticalCode(t *testing.T) {
	f := newAuthorFixture(t, capability.TierBalanced, false)
	code, test := "return input.a + input.b", "assert(tool({a=1,b=1}) == 2); return true"

	if out, _ := f.tool.Run(context.Background(), authorArgs("sum", code, test)); !strings.Contains(out, "registered tool") {
		t.Fatalf("first author should succeed, got %q", out)
	}
	out, _ := f.tool.Run(context.Background(), authorArgs("plus", code, test))
	if !strings.Contains(out, "already registered as \"sum\"") {
		t.Errorf("expected dedup pointer to sum, got %q", out)
	}
	if _, ok := f.reg.Get("plus"); ok {
		t.Error("duplicate-code tool should not be registered under a new name")
	}
}

func hasAuthoredEvent(rec *audit.MemoryRecorder, name string) bool {
	for _, e := range rec.Snapshot() {
		if e.Type == audit.EventToolAuthored && e.Fields["name"] == name {
			return true
		}
	}
	return false
}

// A secret-bearing http_get cap forces approval even on the permissive tier (the one moment
// to catch a credential pointed at the wrong host); declining rejects before any effect.
func TestAuthorTool_SecretCapForcesApprovalOnPermissive(t *testing.T) {
	f := newAuthorFixture(t, capability.TierPermissive, false) // confirm answers no
	args := authorArgs("scrape", `return http_get("https://api.scrapingant.com/x")`, "return true")
	args["required_caps"] = []any{map[string]any{
		"kind": "http_get", "hosts": []any{"api.scrapingant.com"},
		"secret": "scrapingant", "secret_in": "header:x-api-key",
	}}
	out, _ := f.tool.Run(context.Background(), args)
	if f.confirm.calls != 1 {
		t.Fatalf("a secret cap should force one approval even on permissive, got %d", f.confirm.calls)
	}
	if !strings.Contains(out, "declined") {
		t.Errorf("expected decline rejection, got %q", out)
	}
	if _, ok := f.reg.Get("scrape"); ok {
		t.Error("declined tool should not be registered")
	}
}

// A malformed secret placement is rejected at authoring (capability validation), before any
// approval or effect.
func TestAuthorTool_BadSecretPlacement_Rejected(t *testing.T) {
	f := newAuthorFixture(t, capability.TierPermissive, true)
	args := authorArgs("scrape", `return http_get("https://h.com/x")`, "return true")
	args["required_caps"] = []any{map[string]any{
		"kind": "http_get", "hosts": []any{"h.com"},
		"secret": "s", "secret_in": "cookie:c",
	}}
	out, _ := f.tool.Run(context.Background(), args)
	if !strings.Contains(out, "secret_in") {
		t.Errorf("expected a secret-placement rejection, got %q", out)
	}
	if _, ok := f.reg.Get("scrape"); ok {
		t.Error("invalid tool should not be registered")
	}
}
