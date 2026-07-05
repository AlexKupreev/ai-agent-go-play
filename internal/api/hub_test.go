package api

import (
	"testing"

	"ai-agent-go-play/internal/agent"
)

// TestHubDropsInternalEvents verifies the wire-suppression contract: an Internal agent event
// (background deliberation — the chat planner's planner/critic) is dropped from the stream,
// while a normal event is delivered. The transcript/usage sinks are separate observers, so
// this only affects what a client sees.
func TestHubDropsInternalEvents(t *testing.T) {
	h := newHub()
	ch, cancel := h.Subscribe()
	defer cancel()

	h.Emit(agent.Event{Kind: agent.EvResponse, Text: "visible answer"})
	h.Emit(agent.Event{Kind: agent.EvResponse, Text: "raw plan json", Internal: true})
	h.Close()

	var got []Event
	for e := range ch {
		got = append(got, e)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 streamed event, got %d: %+v", len(got), got)
	}
	if got[0].Text != "visible answer" {
		t.Errorf("streamed the wrong event: %q", got[0].Text)
	}
}

// TestHubPublishBriefReaches confirms an out-of-band KindBrief (the surfaced deliberation)
// published directly reaches subscribers — the path the deliberate turn runner uses.
func TestHubPublishBriefReaches(t *testing.T) {
	h := newHub()
	ch, cancel := h.Subscribe()
	defer cancel()

	h.publish(Event{Kind: KindBrief, Text: "refined task: do X"})
	h.Close()

	var got []Event
	for e := range ch {
		got = append(got, e)
	}
	if len(got) != 1 || got[0].Kind != KindBrief || got[0].Text != "refined task: do X" {
		t.Fatalf("expected one KindBrief event, got %+v", got)
	}
}
