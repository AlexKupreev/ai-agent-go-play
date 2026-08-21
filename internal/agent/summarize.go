package agent

import (
	"context"
	"strings"

	"ai-agent-go-play/internal/provider"
)

// summarizeSystemPrompt instructs the model to compress a conversation into a briefing that
// will REPLACE the earlier turns as continuation context — so it must preserve what a resumed
// agent needs, not read like a summary for a human.
const summarizeSystemPrompt = `You compress a conversation into a compact briefing that will REPLACE the earlier turns as the context for continuing it. Preserve, tightly:
- the user's goal(s), stated preferences, and any personal facts, identifiers, paths, or values mentioned;
- decisions made and the reasons for them;
- concrete results produced (files written, outputs, findings);
- open threads and what is still to do.
Drop pleasantries and repetition. Write terse notes, not prose. Invent nothing that is not in the conversation.`

// Summarize condenses a rendered conversation transcript into a compact briefing via a single
// model call (no tools, no structured output). It backs the /compact chat command; the
// returned text is meant to replace the earlier turns as seed context for the conversation.
func Summarize(ctx context.Context, prov provider.Provider, model, transcript string) (string, error) {
	if model == "" {
		model = DefaultModel
	}
	resp, err := prov.Step(ctx, provider.StepRequest{
		Model:           model,
		MaxOutputTokens: DefaultExecutorMaxOutputTokens,
		Messages: []provider.Message{
			provider.SystemText(summarizeSystemPrompt),
			provider.UserText("Conversation to compress:\n\n" + transcript),
		},
	})
	if err != nil {
		return "", err
	}
	if resp.Stop == provider.StopMaxTokens {
		return "", &ModelOutputLimitError{Usage: resp.Usage}
	}
	out := strings.TrimSpace(resp.Text())
	if out == "" {
		return "", errEmptySummary
	}
	return out, nil
}

// errEmptySummary is returned when the model produced no summary text (so the caller keeps the
// original conversation rather than replacing it with nothing).
var errEmptySummary = errEmpty("summarizer returned no text")

type errEmpty string

func (e errEmpty) Error() string { return string(e) }
