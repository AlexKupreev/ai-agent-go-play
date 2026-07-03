package agent

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/tools"
)

// fakeTool is a minimal built-in for wiring a parent agent in these tests.
func fakeTool(name string) tools.Tool {
	return tools.Tool{
		Name:        name,
		Description: name + " tool",
		Run:         func(context.Context, map[string]any) (string, error) { return "", nil },
	}
}

// testParent builds a parent Agent with a known built-in set (provider nil — we never Run here).
func testParent(prompt string, toolNames ...string) *Agent {
	ts := make([]tools.Tool, len(toolNames))
	for i, n := range toolNames {
		ts[i] = fakeTool(n)
	}
	return newAgent(nil, "parent-model", prompt, ts, nil)
}

func toolNames(a *Agent) []string {
	names := make([]string, len(a.tools))
	for i, t := range a.tools {
		names[i] = t.Name
	}
	return names
}

func TestAgentTypeValidate(t *testing.T) {
	cases := []struct {
		name    string
		typ     AgentType
		wantErr bool
	}{
		{"ok replace", AgentType{Name: "r", Prompt: "p", Tools: []string{"shell"}, PromptMode: PromptReplace}, false},
		{"ok default prompt mode", AgentType{Name: "r", Prompt: "p"}, false},
		{"ok parallel read-only", AgentType{Name: "r", Prompt: "p", Parallel: true, Tools: []string{"web_search", "web_fetch"}}, false},
		{"ok parallel empty tools", AgentType{Name: "r", Prompt: "p", Parallel: true}, false},
		{"bad name", AgentType{Name: "1bad", Prompt: "p"}, true},
		{"bad name space", AgentType{Name: "a b", Prompt: "p"}, true},
		{"empty prompt", AgentType{Name: "r"}, true},
		{"bad prompt mode", AgentType{Name: "r", Prompt: "p", PromptMode: "prepend"}, true},
		{"parallel with write tool", AgentType{Name: "r", Prompt: "p", Parallel: true, Tools: []string{"web_search", "shell"}}, true},
		{"parallel inherit-all", AgentType{Name: "r", Prompt: "p", Parallel: true, Tools: []string{inheritAll}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.typ.validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestBuiltinCatalog(t *testing.T) {
	c := NewAgentCatalog() // panics if a built-in is invalid

	if got := len(c.List()); got != 2 {
		t.Fatalf("built-in count = %d, want 2", got)
	}
	for _, name := range []string{"researcher", "general-purpose"} {
		typ, ok := c.Get(name)
		if !ok {
			t.Fatalf("built-in %q missing", name)
		}
		if err := typ.validate(); err != nil {
			t.Errorf("built-in %q invalid: %v", name, err)
		}
	}

	r, _ := c.Get("researcher")
	if !r.Parallel || r.promptMode() != PromptReplace {
		t.Errorf("researcher: Parallel=%v mode=%q, want true/replace", r.Parallel, r.promptMode())
	}
	gp, _ := c.Get("general-purpose")
	if gp.Parallel || gp.promptMode() != PromptAppend || !gp.inheritsAll() {
		t.Errorf("general-purpose: Parallel=%v mode=%q inheritsAll=%v, want false/append/true", gp.Parallel, gp.promptMode(), gp.inheritsAll())
	}
}

func TestCatalogRegisterOverride(t *testing.T) {
	c := NewAgentCatalog()
	before := len(c.List())
	// Override a built-in by re-registering its name; List length is unchanged, value replaced.
	if err := c.Register(AgentType{Name: "researcher", Prompt: "custom body", PromptMode: PromptReplace, Tools: []string{"web_search"}}); err != nil {
		t.Fatalf("Register override: %v", err)
	}
	if got := len(c.List()); got != before {
		t.Fatalf("override changed count: %d != %d", got, before)
	}
	if r, _ := c.Get("researcher"); r.Prompt != "custom body" {
		t.Errorf("override not applied: prompt = %q", r.Prompt)
	}
	// A new name grows the catalog.
	if err := c.Register(AgentType{Name: "extra", Prompt: "p"}); err != nil {
		t.Fatalf("Register new: %v", err)
	}
	if got := len(c.List()); got != before+1 {
		t.Fatalf("new type count = %d, want %d", got, before+1)
	}
}

func TestSelectSubAgentTools_ExplicitIntersectsParent(t *testing.T) {
	parent := testParent("base", "web_search", "web_fetch", "shell")
	got := toolNames(newAgent(nil, "", "", parent.selectSubAgentTools([]string{"web_search", "nonexistent", "shell"}), nil))
	want := []string{"web_search", "shell"} // unknown dropped, order preserved
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("explicit tools = %v, want %v", got, want)
	}
}

func TestSelectSubAgentTools_EmptyIsReadOnlyDefault(t *testing.T) {
	parent := testParent("base", "shell", "web_search", "recall", "author_tool", "read_self_docs")
	got := toolNames(newAgent(nil, "", "", parent.selectSubAgentTools(nil), nil))
	want := []string{"web_search", "recall", "read_self_docs"} // only read-only, parent order
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("read-only default = %v, want %v", got, want)
	}
}

func TestSelectSubAgentTools_InheritAllMinusExcluded(t *testing.T) {
	parent := testParent("base", "web_search", "shell", "spawn_agent", "remember")
	got := toolNames(newAgent(nil, "", "", parent.selectSubAgentTools([]string{inheritAll}), nil))
	want := []string{"web_search", "shell", "remember"} // spawn_agent excluded, order preserved
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("inherit-all = %v, want %v", got, want)
	}
}

func TestNewSubAgent_ReplaceModeAndModelInherit(t *testing.T) {
	parent := testParent("PARENT PROMPT", "web_search", "web_fetch", "shell")
	rt := AgentType{Name: "researcher", Prompt: "RESEARCH PROMPT", Tools: []string{"web_search", "web_fetch"}, PromptMode: PromptReplace}

	child := newSubAgent(parent, rt, nil)

	if child.systemPrompt != "RESEARCH PROMPT" {
		t.Errorf("replace prompt = %q, want the type body alone", child.systemPrompt)
	}
	if strings.Contains(child.systemPrompt, "PARENT PROMPT") {
		t.Error("replace mode leaked the parent prompt")
	}
	if child.model != "parent-model" {
		t.Errorf("model = %q, want inherited parent-model", child.model)
	}
	if child.responseFormat != nil {
		t.Error("sub-agent must not carry a responseFormat")
	}
	if child.registry != nil || child.glue != nil {
		t.Error("sub-agent must not inherit registry/sandbox in v1")
	}
	if names := toolNames(child); strings.Join(names, ",") != "web_search,web_fetch" {
		t.Errorf("child tools = %v, want web_search,web_fetch", names)
	}
}

func TestNewSubAgent_AppendModeInheritsParentPrompt(t *testing.T) {
	parent := testParent("PARENT PROMPT with operator AGENTS.md folded in", "web_search", "shell")
	gp := AgentType{Name: "general-purpose", Prompt: "SUBAGENT NOTE", Tools: []string{inheritAll}, PromptMode: PromptAppend}

	child := newSubAgent(parent, gp, nil)

	if !strings.Contains(child.systemPrompt, "PARENT PROMPT") {
		t.Error("append mode should inherit the parent prompt")
	}
	if !strings.Contains(child.systemPrompt, "SUBAGENT NOTE") {
		t.Error("append mode should include the type body")
	}
	// parent prompt must come before the appended note.
	if strings.Index(child.systemPrompt, "PARENT PROMPT") > strings.Index(child.systemPrompt, "SUBAGENT NOTE") {
		t.Error("append mode should place the type body after the parent prompt")
	}
}
