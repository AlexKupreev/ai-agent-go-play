package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/sandbox"
)

// smokeTestTimeout bounds the author_tool smoke test.
const smokeTestTimeout = 5 * time.Second

// AuthorToolDeps are the host-side dependencies of the author_tool pipeline. None
// are model-controllable: the model only supplies the tool spec as arguments.
type AuthorToolDeps struct {
	Registry Registry
	Glue     *sandbox.LuaGlue
	Audit    audit.Recorder
	Tier     capability.Tier
	RunID    string
	Approver Approver // approval gate for caps beyond the tier; nil = cannot escalate
}

// NewAuthorTool returns the author_tool meta-tool: the agent's path to promote an
// idea into a named, scoped, capability-bounded, tested tool that persists and is
// callable in the same run. The pipeline runs entirely host-side:
//
//	validate → approve → smoke-test (under exactly the approved caps) → register → audit
//
// Each gate rejects by returning a message to the model (not a hard error) so it
// can fix the tool and retry.
func NewAuthorTool(d AuthorToolDeps) Tool {
	return Tool{
		Name: "author_tool",
		Description: "Create a new reusable tool from a Lua script and register it for the rest of this run. " +
			"The script body reads its arguments from `input` and ends with `return <value>`. You must include a " +
			"`test` that calls tool({...}) with sample input and `return true` on success. Request only the " +
			"capabilities the tool needs in `required_caps`; capabilities beyond the current trust tier require " +
			"user approval. Prefer authoring a tool over repeating the same multi-step work.",
		Parameters: map[string]any{
			"name":         map[string]any{"type": "string", "description": "tool name: lowercase letters, digits, underscores; starts with a letter"},
			"description":  map[string]any{"type": "string", "description": "what the tool does (shown to you when choosing tools later)"},
			"input_schema": map[string]any{"type": "object", "description": "JSON Schema object describing the tool's input"},
			"code":         map[string]any{"type": "string", "description": "Lua body: reads `input`, ends with `return <value>`"},
			"test":         map[string]any{"type": "string", "description": "Lua smoke test: call tool({...}) and `return true` on success"},
			"required_caps": map[string]any{
				"type":        "array",
				"description": "capabilities the tool needs, e.g. [{\"kind\":\"http_get\",\"hosts\":[\"api.example.com\"]}]",
				"items":       map[string]any{"type": "object"},
			},
			"scope": map[string]any{"type": "string", "description": "ephemeral (this run only) | shared (persists). Default ephemeral.", "enum": []any{"ephemeral", "user", "shared"}},
		},
		Required: []string{"name", "description", "input_schema", "code", "test"},
		Run:      d.run,
	}
}

func (d AuthorToolDeps) run(ctx context.Context, args map[string]any) (string, error) {
	// 1. Validate ----------------------------------------------------------
	spec, reject, err := d.parseSpec(args)
	if err != nil {
		return reject, nil // model-facing rejection, not a hard error
	}
	if msg := d.validate(spec); msg != "" {
		return msg, nil
	}

	// 2. Approve -----------------------------------------------------------
	if msg := d.approve(ctx, spec); msg != "" {
		return msg, nil
	}

	// 3. Smoke-test under exactly the approved caps ------------------------
	if msg := d.smokeTest(ctx, spec); msg != "" {
		return msg, nil
	}

	// 4. Register ----------------------------------------------------------
	stored, err := d.Registry.Register(spec)
	if err != nil {
		return fmt.Sprintf("author_tool rejected: registration failed: %v", err), nil
	}
	// Dedup: identical code already exists under another name — nothing new was
	// registered. Tell the model to call the existing tool instead.
	if stored.Name != spec.Name {
		return fmt.Sprintf("identical tool already registered as %q — call it instead of re-authoring.", stored.Name), nil
	}

	// 5. Audit -------------------------------------------------------------
	d.auditAuthored(stored)

	return fmt.Sprintf("registered tool %q (v%d, scope=%s). You can call it now.", stored.Name, stored.Version, stored.Scope), nil
}

// parseSpec converts model arguments into a ToolSpec. The returned string is a
// model-facing rejection when err != nil.
func (d AuthorToolDeps) parseSpec(args map[string]any) (ToolSpec, string, error) {
	name, _ := args["name"].(string)
	desc, _ := args["description"].(string)
	code, _ := args["code"].(string)
	test, _ := args["test"].(string)

	schema, ok := args["input_schema"].(map[string]any)
	if !ok {
		return ToolSpec{}, "author_tool rejected: input_schema must be a JSON object", fmt.Errorf("bad schema")
	}

	caps, err := parseCaps(args["required_caps"])
	if err != nil {
		return ToolSpec{}, fmt.Sprintf("author_tool rejected: %v", err), err
	}

	scope, err := parseScope(args["scope"])
	if err != nil {
		return ToolSpec{}, fmt.Sprintf("author_tool rejected: %v", err), err
	}

	return ToolSpec{
		Name:         name,
		Description:  desc,
		InputSchema:  schema,
		Impl:         Impl{Kind: ImplScript, Lang: "lua", Source: code},
		RequiredCaps: caps,
		Scope:        scope,
		Test:         test,
		CreatedBy:    "agent",
	}, "", nil
}

// validate enforces structure and that both the tool body and the test parse.
func (d AuthorToolDeps) validate(spec ToolSpec) string {
	if err := spec.validate(); err != nil { // name regex, schema, impl
		return "author_tool rejected: " + err.Error()
	}
	if strings.TrimSpace(spec.Test) == "" {
		return "author_tool rejected: a test is mandatory"
	}
	if err := sandbox.Parse(WrapScript(spec.Impl.Source)); err != nil {
		return fmt.Sprintf("author_tool rejected: code has a syntax error: %v", err)
	}
	if err := sandbox.Parse(WrapTest(spec.Impl.Source, spec.Test)); err != nil {
		return fmt.Sprintf("author_tool rejected: test has a syntax error: %v", err)
	}
	return ""
}

// approve prompts the user for any capability beyond the run's tier. Returns a
// rejection message if approval is needed but unavailable, errored, or declined.
func (d AuthorToolDeps) approve(ctx context.Context, spec ToolSpec) string {
	var beyond []string
	for _, c := range spec.RequiredCaps {
		if !d.Tier.AutoApproves(c.Kind) {
			beyond = append(beyond, capSummary(c))
		}
	}
	if len(beyond) == 0 {
		return ""
	}
	if d.Approver == nil {
		return "author_tool rejected: tool requests capabilities beyond the current tier and no approval channel is available: " + strings.Join(beyond, ", ")
	}
	ok, err := d.Approver.Approve(ctx, ApprovalRequest{
		Kind:   "tool.capability",
		Title:  fmt.Sprintf("Authorize tool %q with elevated capabilities", spec.Name),
		Detail: strings.Join(beyond, "\n"),
		RunID:  d.RunID,
	})
	if err != nil {
		return fmt.Sprintf("author_tool rejected: approval failed: %v", err)
	}
	if !ok {
		return "author_tool rejected: capability approval declined by user"
	}
	return ""
}

// smokeTest runs the test under a grant of exactly the tool's caps. Effects are
// real and brokered/audited. The test must return a truthy value.
func (d AuthorToolDeps) smokeTest(ctx context.Context, spec ToolSpec) string {
	if d.Glue == nil {
		return "author_tool rejected: no sandbox available to run the test"
	}
	grant := &capability.GrantContext{Run: d.RunID, Granted: spec.RequiredCaps, Tier: d.Tier}
	out, err := d.Glue.Run(ctx, WrapTest(spec.Impl.Source, spec.Test), nil, grant, smokeTestTimeout)
	if err != nil {
		return fmt.Sprintf("author_tool rejected: test failed: %v", err)
	}
	if strings.TrimSpace(out) != "true" {
		return fmt.Sprintf("author_tool rejected: test did not return true (got %q)", out)
	}
	return ""
}

func (d AuthorToolDeps) auditAuthored(spec ToolSpec) {
	if d.Audit == nil {
		return
	}
	kinds := make([]string, len(spec.RequiredCaps))
	for i, c := range spec.RequiredCaps {
		kinds[i] = string(c.Kind)
	}
	d.Audit.Record(audit.Event{
		Type: audit.EventToolAuthored,
		Run:  d.RunID,
		Fields: map[string]any{
			"name":      spec.Name,
			"code_hash": spec.CodeHash,
			"caps":      kinds,
			"scope":     string(spec.Scope),
			"version":   spec.Version,
		},
	})
}

// parseCaps converts the model's required_caps array into typed capabilities via
// a JSON round-trip (the capability fields carry JSON tags).
func parseCaps(v any) ([]capability.Capability, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("required_caps is not valid: %w", err)
	}
	var caps []capability.Capability
	if err := json.Unmarshal(raw, &caps); err != nil {
		return nil, fmt.Errorf("required_caps must be an array of {kind,...} objects: %w", err)
	}
	for _, c := range caps {
		if c.Kind == "" {
			return nil, fmt.Errorf("each capability needs a non-empty \"kind\"")
		}
	}
	return caps, nil
}

func parseScope(v any) (Scope, error) {
	s, _ := v.(string)
	switch Scope(s) {
	case ScopeAny: // unset → default
		return ScopeEphemeral, nil
	case ScopeEphemeral, ScopeUser, ScopeShared:
		return Scope(s), nil
	default:
		return "", fmt.Errorf("scope must be ephemeral, user, or shared")
	}
}

// capSummary renders a capability for an approval prompt.
func capSummary(c capability.Capability) string {
	switch c.Kind {
	case capability.HTTPGet:
		return fmt.Sprintf("http_get → %s", strings.Join(c.Hosts, ", "))
	case capability.ReadFile, capability.WriteFile:
		return fmt.Sprintf("%s → %s", c.Kind, c.PathPrefix)
	case capability.CallTool:
		return fmt.Sprintf("call_tool → %s", strings.Join(c.Tools, ", "))
	default:
		return string(c.Kind)
	}
}
