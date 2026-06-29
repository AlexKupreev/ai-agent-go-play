package agent

import (
	"fmt"
	"slices"
	"testing"

	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// registryToolNames returns the names in defs that are not built-ins.
func (a *Agent) registryToolNames(defs []provider.ToolDef) []string {
	var out []string
	for _, d := range defs {
		if _, isBuiltin := a.byName[d.Name]; !isBuiltin {
			out = append(out, d.Name)
		}
	}
	return out
}

func TestSelectRegistryTools_LargeCatalogTopK(t *testing.T) {
	a, reg, _ := newTestExecutor(t)

	// 14 unrelated persistent tools + 1 matching the task, all distinct code.
	for i := range 14 {
		mustRegister(t, reg, tools.ToolSpec{
			Name:        fmt.Sprintf("filler%02d", i),
			Description: "miscellaneous unrelated helper",
			InputSchema: map[string]any{"type": "object"},
			Impl:        tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: fmt.Sprintf("return %d", i)},
			Scope:       tools.ScopeShared,
		})
	}
	mustRegister(t, reg, tools.ToolSpec{
		Name:        "weatherbot",
		Description: "fetch the weather forecast for a city",
		InputSchema: map[string]any{"type": "object"},
		Impl:        tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: "return 100"},
		Scope:       tools.ScopeShared,
	})
	// An ephemeral (run-local) tool that does NOT match the task query.
	mustRegister(t, reg, tools.ToolSpec{
		Name:        "scratch",
		Description: "zzz unrelated scratchpad",
		InputSchema: map[string]any{"type": "object"},
		Impl:        tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: "return 200"},
		Scope:       tools.ScopeEphemeral,
	})

	a.task = "what is the weather forecast"
	names := a.registryToolNames(a.buildToolDefs())

	// Large catalog must not flood: far fewer than the 16 registered.
	if len(names) > maxInlineTools {
		t.Errorf("offered %d registry tools, want <= %d: %v", len(names), maxInlineTools, names)
	}
	if !slices.Contains(names, "weatherbot") {
		t.Errorf("relevant tool weatherbot not surfaced: %v", names)
	}
	if !slices.Contains(names, "scratch") {
		t.Errorf("run-local ephemeral tool dropped: %v", names)
	}
	if slices.Contains(names, "filler00") {
		t.Errorf("unrelated tool should not be offered for a large catalog: %v", names)
	}
}
