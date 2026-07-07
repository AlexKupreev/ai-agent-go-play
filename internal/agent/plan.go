package agent

import (
	"strings"

	"ai-agent-go-play/internal/provider"
)

// Plan is the structured output produced by the planner agent. It is shared by `run`
// and every deliberate chat / session turn; the chat pipeline (docs/adr/chat-planner.md) widened it from
// the original {refined_task, assumptions, confirmed} with a Context block, cache-with-
// fallback artifact references, and an optional success criterion for the critique loop.
//
// The nullable fields (Context, SuccessCriteria, and each ArtifactRef's Source/
// Description) are pointers because OpenAI strict mode requires every property in
// `required` at every level: "may be empty" is modelled as a nullable JSON type, and the
// planner emits `null` (never omission) — see planResponseFormat and the prompt rule in
// plannerPrompt. A nil pointer renders as "absent" in Brief().
type Plan struct {
	// RefinedTask is the clear, unambiguous task description passed to the executor.
	RefinedTask string `json:"refined_task"`
	// Context is background the executor needs but that isn't part of the task verb
	// itself (nil ⇒ nothing to add).
	Context *string `json:"context"`
	// ArtifactRefs are cache-with-fallback pointers to data the executor should read
	// (path if present, else fetch from source). May be empty.
	ArtifactRefs []ArtifactRef `json:"artifact_refs"`
	// SuccessCriteria is the objective done-condition the critique loop checks the
	// answer against (nil ⇒ no explicit criterion). See docs §9.2 / §D9.
	SuccessCriteria *string `json:"success_criteria"`
	// Assumptions lists things the planner inferred without explicit user confirmation.
	Assumptions []string `json:"assumptions"`
	// Confirmed lists things the user explicitly confirmed during clarification.
	Confirmed []string `json:"confirmed"`
}

// ArtifactRef is a cache-with-fallback pointer to a working artifact: read Path if it
// exists, otherwise fetch it from Source (docs §D5). Source/Description are nullable.
type ArtifactRef struct {
	// Path is where the artifact may be cached (relative to the workspace/scratch dir).
	Path string `json:"path"`
	// Source is where to (re-)fetch it if Path is absent (nil ⇒ no fallback known).
	Source *string `json:"source"`
	// Description is a one-line note on the artifact's shape/columns (nil ⇒ none).
	Description *string `json:"description"`
}

// Brief flattens a Plan into the single self-contained seed string the executor's
// Run(ctx, string) entry point takes (docs §4 "Brief → executor rendering"). It is the
// human-legible artifact the chat loop surfaces to the user, so the ordering and labels
// are chosen to read as an instruction: the refined task first, then the context, then
// the cache-with-fallback artifact list, the success criterion, and finally the
// assumptions/confirmations `run` already prints. Nil/empty fields are omitted (they
// carry no instruction), which is why the schema makes them nullable rather than
// free-form empty. Pure: no I/O.
func (p Plan) Brief() string {
	var b strings.Builder
	b.WriteString(p.RefinedTask)

	if p.Context != nil && strings.TrimSpace(*p.Context) != "" {
		b.WriteString("\n\nContext:\n")
		b.WriteString(strings.TrimSpace(*p.Context))
	}

	if len(p.ArtifactRefs) > 0 {
		b.WriteString("\n\nArtifacts (read the path if it exists, else fetch from the source):")
		for _, ref := range p.ArtifactRefs {
			b.WriteString("\n- ")
			b.WriteString(ref.Path)
			if ref.Description != nil && strings.TrimSpace(*ref.Description) != "" {
				b.WriteString(" — ")
				b.WriteString(strings.TrimSpace(*ref.Description))
			}
			if ref.Source != nil && strings.TrimSpace(*ref.Source) != "" {
				b.WriteString(" (else fetch from ")
				b.WriteString(strings.TrimSpace(*ref.Source))
				b.WriteString(")")
			}
		}
	}

	if p.SuccessCriteria != nil && strings.TrimSpace(*p.SuccessCriteria) != "" {
		b.WriteString("\n\nSuccess criteria: ")
		b.WriteString(strings.TrimSpace(*p.SuccessCriteria))
	}

	for _, a := range p.Assumptions {
		if strings.TrimSpace(a) != "" {
			b.WriteString("\n\nAssumption: ")
			b.WriteString(strings.TrimSpace(a))
		}
	}
	for _, c := range p.Confirmed {
		if strings.TrimSpace(c) != "" {
			b.WriteString("\n\nConfirmed: ")
			b.WriteString(strings.TrimSpace(c))
		}
	}

	return b.String()
}

// nullableString is the strict-mode JSON type for a field that may be empty: the planner
// must emit an explicit null (never omit it), so the field stays in `required` while
// still allowing "nothing to say" (docs §4).
var nullableString = map[string]any{"type": []string{"string", "null"}}

// planResponseFormat is the structured output schema enforced on the planner's final
// response. Strict mode requires every property in `required` at every object level with
// additionalProperties:false, so genuinely-optional fields are modelled as nullable
// (see nullableString) rather than omitted.
var planResponseFormat = provider.ResponseFormat{
	Name:        "plan",
	Description: "Refined task, context, artifact references, and clarification notes produced by the planner",
	Strict:      true,
	Schema: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"refined_task", "context", "artifact_refs", "success_criteria", "assumptions", "confirmed"},
		"properties": map[string]any{
			"refined_task": map[string]any{
				"type":        "string",
				"description": "Clear, unambiguous task description for the executor",
			},
			"context": map[string]any{
				"type":        []string{"string", "null"},
				"description": "Background the executor needs but that isn't part of the task verb; null when there is nothing to add",
			},
			"artifact_refs": map[string]any{
				"type":        "array",
				"description": "Cache-with-fallback pointers to working data: read the path if present, else fetch from the source. Empty array when none.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"path", "source", "description"},
					"properties": map[string]any{
						"path":        map[string]any{"type": "string", "description": "Where the artifact may be cached (relative to the workspace/scratch dir)"},
						"source":      nullableString,
						"description": nullableString,
					},
				},
			},
			"success_criteria": map[string]any{
				"type":        []string{"string", "null"},
				"description": "Objective done-condition for the answer (e.g. 'a per-region 2024-vs-2025 delta table'); null when none",
			},
			"assumptions": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Things the planner inferred without explicit user confirmation",
			},
			"confirmed": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Things the user explicitly confirmed during clarification",
			},
		},
	},
}
