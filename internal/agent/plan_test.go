package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func strptr(s string) *string { return &s }

func TestPlanBrief(t *testing.T) {
	t.Run("bare refined task, all nullable fields empty", func(t *testing.T) {
		p := Plan{RefinedTask: "respond to the user's message: how are you?"}
		got := p.Brief()
		if got != "respond to the user's message: how are you?" {
			t.Fatalf("brief = %q, want just the refined task", got)
		}
	})

	t.Run("context, artifacts, success criteria, and notes all render", func(t *testing.T) {
		p := Plan{
			RefinedTask: "compare 2024 vs 2025 sales by region",
			Context:     strptr("the user cares about the EU regions only"),
			ArtifactRefs: []ArtifactRef{
				{Path: "scratch/sales-2024.csv", Source: strptr("https://ex.gov/2024.csv"), Description: strptr("CSV: date, region, sales")},
				{Path: "scratch/sales-2025.csv"}, // no source/description
			},
			SuccessCriteria: strptr("a per-region 2024-vs-2025 delta table"),
			Assumptions:     []string{"fiscal year = calendar year"},
			Confirmed:       []string{"EU only"},
		}
		got := p.Brief()
		// Cache-with-fallback line spells out path — description (else fetch from source).
		for _, want := range []string{
			"compare 2024 vs 2025 sales by region",
			"Context:\nthe user cares about the EU regions only",
			"scratch/sales-2024.csv — CSV: date, region, sales (else fetch from https://ex.gov/2024.csv)",
			"scratch/sales-2025.csv",
			"Success criteria: a per-region 2024-vs-2025 delta table",
			"Assumption: fiscal year = calendar year",
			"Confirmed: EU only",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("brief missing %q\n--- brief ---\n%s", want, got)
			}
		}
		// The second artifact has no source, so only one ref gets a "(else fetch from …)"
		// clause (distinct from the header's wording).
		if strings.Count(got, "(else fetch from ") != 1 {
			t.Errorf("expected exactly one fallback clause, got:\n%s", got)
		}
	})

	t.Run("nil context and empty artifacts omit their blocks", func(t *testing.T) {
		p := Plan{RefinedTask: "do X"}
		got := p.Brief()
		if strings.Contains(got, "Context:") || strings.Contains(got, "Artifacts") || strings.Contains(got, "Success criteria") {
			t.Errorf("empty fields should not render blocks, got:\n%s", got)
		}
	})
}

// TestPlanSchemaStrict guards the strict-mode invariants: every property is listed in
// `required` at every object level, and additionalProperties is false. OpenAI strict mode
// rejects a schema that violates either, so this catches a drift before it reaches the API.
func TestPlanSchemaStrict(t *testing.T) {
	if !planResponseFormat.Strict {
		t.Fatal("plan schema must be strict")
	}
	assertStrictObject(t, "plan", planResponseFormat.Schema)
}

// TestPlanRoundTrip confirms the widened struct round-trips through JSON with explicit nulls
// (what the strict planner emits) and populated values.
func TestPlanRoundTrip(t *testing.T) {
	raw := `{
		"refined_task": "do X",
		"context": null,
		"artifact_refs": [{"path": "p", "source": null, "description": "d"}],
		"success_criteria": "done when X",
		"assumptions": [],
		"confirmed": ["c"]
	}`
	var p Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Context != nil {
		t.Errorf("context should be nil for JSON null, got %v", *p.Context)
	}
	if len(p.ArtifactRefs) != 1 || p.ArtifactRefs[0].Path != "p" || p.ArtifactRefs[0].Source != nil {
		t.Errorf("artifact_refs round-trip wrong: %+v", p.ArtifactRefs)
	}
	if p.SuccessCriteria == nil || *p.SuccessCriteria != "done when X" {
		t.Errorf("success_criteria wrong: %v", p.SuccessCriteria)
	}
}

// assertStrictObject recursively checks that an object schema declares additionalProperties
// false and lists every property key in `required` (the strict-mode contract).
func assertStrictObject(t *testing.T, path string, schema map[string]any) {
	t.Helper()
	if schema["additionalProperties"] != false {
		t.Errorf("%s: additionalProperties must be false", path)
	}
	props, _ := schema["properties"].(map[string]any)
	required := toStringSet(schema["required"])
	for name := range props {
		if !required[name] {
			t.Errorf("%s: property %q not in required (strict mode requires all)", path, name)
		}
		// Recurse into nested object schemas (e.g. artifact_refs items).
		if child, ok := props[name].(map[string]any); ok {
			if child["type"] == "object" {
				assertStrictObject(t, path+"."+name, child)
			}
			if items, ok := child["items"].(map[string]any); ok && items["type"] == "object" {
				assertStrictObject(t, path+"."+name+"[]", items)
			}
		}
	}
}

func toStringSet(v any) map[string]bool {
	set := map[string]bool{}
	if list, ok := v.([]string); ok {
		for _, s := range list {
			set[s] = true
		}
	}
	return set
}
