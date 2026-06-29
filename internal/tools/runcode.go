package tools

import (
	"context"
	"fmt"
	"time"

	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/sandbox"
)

// NewRunCode returns a tool that runs a short Lua script for computation and
// data shaping, and returns its result.
//
// It runs through the shared sandbox (sandbox.LuaGlue) with an EMPTY grant, so it
// has no host functions and no brokered effects — pure computation only. The glue
// is the live, broker-backed sandbox shared with agent-authored tools (Phase 3);
// run_code simply requests no capabilities.
func NewRunCode(glue *sandbox.LuaGlue, timeout time.Duration) Tool {
	return Tool{
		Name: "run_code",
		Description: "Execute a short Lua script for computation and data shaping, returning its result. " +
			"Sandboxed: no filesystem, network, OS, or I/O access — only pure computation with the " +
			"string, table, and math libraries. End the script with `return <value>` (string, number, " +
			"boolean, or table); tables are returned as JSON. Prefer this over shelling out for " +
			"calculations, parsing, and transforming data.",
		Parameters: map[string]any{
			"code": map[string]any{
				"type":        "string",
				"description": "Lua source to execute; end with `return <value>`",
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			code, ok := args["code"].(string)
			if !ok {
				return "", fmt.Errorf("code must be a string")
			}
			out, err := glue.Run(ctx, code, nil, &capability.GrantContext{}, timeout)
			if err != nil {
				// Feed errors back to the model as content (like shell) so it can adapt.
				return fmt.Sprintf("error: %v", err), nil
			}
			return out, nil
		},
	}
}
