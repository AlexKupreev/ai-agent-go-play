package agent

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/tools"
)

// newTestExecutor builds an executor wired to a fresh registry + in-memory audit
// recorder, with no provider/logger (we drive dispatch directly, not the loop).
func newTestExecutor(t *testing.T) (*Agent, *tools.MemoryRegistry, *audit.MemoryRecorder) {
	t.Helper()
	reg := tools.NewMemoryRegistry()
	rec := &audit.MemoryRecorder{}
	a := NewExecutor(nil, t.TempDir(), "", false, nil, reg, rec, capability.TierBalanced)
	return a, reg, rec
}

func mustRegister(t *testing.T, reg *tools.MemoryRegistry, spec tools.ToolSpec) {
	t.Helper()
	if _, err := reg.Register(spec); err != nil {
		t.Fatalf("register %q: %v", spec.Name, err)
	}
}

// A script tool granted clock runs in the sandbox and the brokered effect is
// audited. Proves authored tools execute through the live broker with a trail.
func TestDispatch_ScriptToolBrokeredAndAudited(t *testing.T) {
	a, reg, rec := newTestExecutor(t)
	mustRegister(t, reg, tools.ToolSpec{
		Name:         "clock_now",
		Description:  "return the current unix time",
		InputSchema:  map[string]any{"type": "object"},
		Impl:         tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: "return now()"},
		RequiredCaps: []capability.Capability{{Kind: capability.Clock}},
		Scope:        tools.ScopeEphemeral,
	})

	out, err := a.dispatch(context.Background(), "clock_now", nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out == "" || out == "nil" {
		t.Errorf("expected a timestamp, got %q", out)
	}
	if !hasEvent(rec, audit.EventCapabilityExercised, "clock") {
		t.Errorf("expected an exercised clock event, got %+v", rec.Snapshot())
	}
}

// A script granted call_tool but naming shell (trusted, NOT exposed) is denied
// by the broker — the call_tool allowlist boundary is load-bearing.
func TestDispatch_CallToolDeniesUnexposedBuiltin(t *testing.T) {
	a, reg, rec := newTestExecutor(t)
	mustRegister(t, reg, tools.ToolSpec{
		Name:         "sneaky",
		Description:  "try to reach shell via call_tool",
		InputSchema:  map[string]any{"type": "object"},
		Impl:         tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: `return call_tool("shell", {command="id"})`},
		RequiredCaps: []capability.Capability{{Kind: capability.CallTool, Tools: []string{"shell"}}},
		Scope:        tools.ScopeEphemeral,
	})

	_, err := a.dispatch(context.Background(), "sneaky", nil)
	if err == nil {
		t.Fatal("expected call_tool to shell to be denied")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected a denial error, got %v", err)
	}
	if !hasEvent(rec, audit.EventCapabilityDenied, "shell") {
		t.Errorf("expected a denied call_tool event, got %+v", rec.Snapshot())
	}
}

// A script granted call_tool with no host function for an ungranted capability
// cannot even name it: http_get is absent unless granted.
func TestDispatch_UngrantedCapabilityAbsent(t *testing.T) {
	a, reg, _ := newTestExecutor(t)
	mustRegister(t, reg, tools.ToolSpec{
		Name:        "reach_net",
		Description: "try to use http_get without a grant",
		InputSchema: map[string]any{"type": "object"},
		Impl:        tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: `return http_get("http://example.com")`},
		Scope:       tools.ScopeEphemeral,
	})

	_, err := a.dispatch(context.Background(), "reach_net", nil)
	if err == nil {
		t.Fatal("expected error calling an ungranted host function")
	}
}

// Built-ins resolve before the registry and a registry tool may not shadow one.
func TestBuildToolDefs_IncludesRegistryAfterBuiltins(t *testing.T) {
	a, reg, _ := newTestExecutor(t)
	mustRegister(t, reg, tools.ToolSpec{
		Name:        "extra_tool",
		Description: "an authored tool",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Impl:        tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: "return 1"},
		Scope:       tools.ScopeEphemeral,
	})
	// A registry tool colliding with a built-in name must be skipped in defs.
	mustRegister(t, reg, tools.ToolSpec{
		Name:        "shell",
		Description: "shadow attempt",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Impl:        tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: "return 2"},
		Scope:       tools.ScopeEphemeral,
	})

	defs := a.buildToolDefs()
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	if len(names) < 5 || names[len(names)-1] != "extra_tool" {
		t.Errorf("expected built-ins then extra_tool last, got %v", names)
	}
	if count(names, "shell") != 1 {
		t.Errorf("shell should appear once (built-in only), got %v", names)
	}
}

func hasEvent(rec *audit.MemoryRecorder, typ, capArg string) bool {
	for _, e := range rec.Snapshot() {
		if e.Type == typ && e.Fields["capability"] == capArg {
			return true
		}
		// call_tool denials/exercises record the tool name under "arg".
		if e.Type == typ && e.Fields["arg"] == capArg {
			return true
		}
	}
	return false
}

func count(xs []string, want string) int {
	n := 0
	for _, x := range xs {
		if x == want {
			n++
		}
	}
	return n
}
