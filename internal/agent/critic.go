package agent

import "ai-agent-go-play/internal/provider"

// Verdict is the critic's structured output (docs/adr/chat-planner.md §9.6 / Q9a): a
// judgment, not a plan. The critique loop delivers the executor's answer when Satisfied;
// otherwise it re-runs the PLANNER (unchanged contract) with Gaps as added context and
// re-delegates. Keeping the verdict on its own small schema is what lets Plan stay exactly
// as plan.go defines it — there is no accept/verdict field crammed into the plan, so D1's
// "the planner never answers" guarantee is untouched.
type Verdict struct {
	// Satisfied is true when the answer meets the success criteria / the task.
	Satisfied bool `json:"satisfied"`
	// Gaps lists concrete shortfalls when not satisfied (empty when satisfied). These feed
	// the re-plan so the next brief targets what was missing.
	Gaps []string `json:"gaps"`
}

const criticPrompt = `You are a critique agent. You judge whether an execution agent's answer actually satisfies the task and its stated success criteria. You do NOT redo the task, and you do NOT rewrite the answer — you only return a verdict.

You will be given the task/brief (including any success criteria), the answer produced, and bounded runtime execution evidence for that executor attempt. Evidence records provenance, not truth: a successful web call proves that the runtime obtained the listed source, not that the source or the answer's reading of it is correct. You have no execution tools and must not demand a second fetch. Decide:
- satisfied = true if the answer genuinely meets the task and every stated success criterion.
- satisfied = false if it misses a criterion, is incomplete, is unsupported/likely fabricated, or does not actually do what was asked.

When not satisfied, list the concrete gaps — specific, actionable shortfalls the next attempt must fix (e.g. "missing the per-region delta table", "gives 2024 totals but not the 2024-vs-2025 comparison asked for"). When satisfied, return an empty gaps list.

For current or external factual claims, check whether relevant answer citations correspond to sources in successful evidence. Failed evidence cannot support a claim, though an honest limitation may still satisfy the task. General knowledge and conversation do not require ceremonial web evidence. Be a fair judge, not a perfectionist: minor stylistic preferences are not gaps. Only fail an answer for a real shortfall against the task or its success criteria. Your only output is the structured verdict.`

// verdictResponseFormat is the strict schema enforced on the critic's output. Like the
// planner's Plan, the shape is guaranteed in code, not prose — the critic cannot answer the
// user's question, only judge.
var verdictResponseFormat = provider.ResponseFormat{
	Name:        "verdict",
	Description: "Whether the answer satisfies the task/success criteria, and any gaps if not",
	Strict:      true,
	Schema: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"satisfied", "gaps"},
		"properties": map[string]any{
			"satisfied": map[string]any{
				"type":        "boolean",
				"description": "True iff the answer meets the task and every stated success criterion",
			},
			"gaps": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Concrete shortfalls to fix on the next attempt; empty when satisfied",
			},
		},
	},
}

// NewCritic creates the verdict-emitting critic for the chat planner's critique loop
// (docs §9). It is deliberately tools-light — judging is not planning or executing, so it
// has no shell/web; it reads the brief + answer and returns a Verdict. promptOverride
// (empty ⇒ criticPrompt) lets the judgment be tuned without a rebuild, like PLANNER.md; the
// structured Verdict is enforced regardless, so an override cannot break the contract.
func NewCritic(p provider.Provider, model, promptOverride string, obs Observer) *Agent {
	base := criticPrompt
	if promptOverride != "" {
		base = promptOverride
	}
	a := newAgent(p, model, base, nil, obs)
	a.responseFormat = &verdictResponseFormat
	return a
}
