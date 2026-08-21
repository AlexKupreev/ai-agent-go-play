package agent

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/provider"
)

func TestSummarize(t *testing.T) {
	// A provider that echoes what it was asked to summarize, so we can assert both the return
	// value and that the transcript + system prompt reached it.
	var gotSystem, gotUser string
	var gotMaxOutput int64
	prov := providerFunc(func(_ context.Context, req provider.StepRequest) (provider.StepResponse, error) {
		gotMaxOutput = req.MaxOutputTokens
		for _, m := range req.Messages {
			switch m.Role {
			case provider.RoleSystem:
				gotSystem = m.Content[0].Text
			case provider.RoleUser:
				gotUser = m.Content[0].Text
			}
		}
		return textStep("- goal: learn Go\n- decided: use table tests"), nil
	})

	out, err := Summarize(context.Background(), prov, "gpt-4o-mini", "user: teach me Go\nassistant: sure")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.Contains(out, "goal: learn Go") {
		t.Fatalf("summary = %q, want the model's text", out)
	}
	if !strings.Contains(gotSystem, "compress a conversation") {
		t.Errorf("system prompt not sent; got %q", gotSystem)
	}
	if !strings.Contains(gotUser, "teach me Go") {
		t.Errorf("transcript not sent to the model; got %q", gotUser)
	}
	if gotMaxOutput != DefaultExecutorMaxOutputTokens {
		t.Errorf("max output tokens = %d, want %d", gotMaxOutput, DefaultExecutorMaxOutputTokens)
	}
}

func TestSummarize_EmptyIsError(t *testing.T) {
	prov := providerFunc(func(_ context.Context, _ provider.StepRequest) (provider.StepResponse, error) {
		return textStep(""), nil
	})
	if _, err := Summarize(context.Background(), prov, "m", "x"); err == nil {
		t.Fatal("empty summary should be an error so the caller keeps the original conversation")
	}
}

// providerFunc adapts a function to provider.Provider for tests.
type providerFunc func(context.Context, provider.StepRequest) (provider.StepResponse, error)

func (f providerFunc) Step(ctx context.Context, req provider.StepRequest) (provider.StepResponse, error) {
	return f(ctx, req)
}
