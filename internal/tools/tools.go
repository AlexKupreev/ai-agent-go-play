package tools

import "context"

// Tool represents a capability the agent can invoke.
// The Parameters field is a JSON Schema "properties" map — OpenAI uses this
// to know what arguments to pass when calling the tool.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	// Required lists the parameter names the model must supply. If nil, every
	// key in Parameters is treated as required. Set it explicitly once a tool
	// gains optional parameters (e.g. agent-authored tools in Phase 3).
	Required []string
	// Run is called when the LLM decides to use this tool.
	// args is a map of parameter name → value, parsed from the LLM's JSON output.
	// Returns the result as a string that gets fed back to the LLM.
	Run func(ctx context.Context, args map[string]any) (string, error)
}
