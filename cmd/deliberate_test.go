package cmd

import (
	"strings"
	"testing"

	"ai-agent-go-play/internal/provider"
)

func TestMessagesToTurnLogRoundTrip(t *testing.T) {
	// The session store round-trips the turn log as user/assistant text pairs.
	var msgs []provider.Message
	msgs = appendTurnMessages(msgs, "hello", "hi there")
	msgs = appendTurnMessages(msgs, "what's 2+2?", "4")

	log := messagesToTurnLog(msgs)
	if len(log) != 2 {
		t.Fatalf("got %d turns, want 2", len(log))
	}
	if log[0].User != "hello" || log[0].Answer != "hi there" {
		t.Errorf("turn 0 = %+v", log[0])
	}
	if log[1].User != "what's 2+2?" || log[1].Answer != "4" {
		t.Errorf("turn 1 = %+v", log[1])
	}
}

func TestMessagesToTurnLogPairsUserWithNextAssistant(t *testing.T) {
	// A trailing user with no answer yet (mid-turn) still yields a pending entry.
	msgs := []provider.Message{
		provider.UserText("q1"),
		provider.AssistantText("a1"),
		provider.UserText("q2"),
	}
	log := messagesToTurnLog(msgs)
	if len(log) != 2 || log[1].User != "q2" || log[1].Answer != "" {
		t.Fatalf("unexpected: %+v", log)
	}
}

func TestComposePlannerInputFirstTurnIsBare(t *testing.T) {
	if got := composePlannerInput(nil, "hi"); got != "hi" {
		t.Errorf("first turn should be the bare message, got %q", got)
	}
	got := composePlannerInput([]chatTurn{{User: "a", Answer: "b"}}, "c")
	if !strings.Contains(got, "Conversation so far:") || !strings.Contains(got, "Current user message: c") {
		t.Errorf("multi-turn input malformed:\n%s", got)
	}
}

func TestRenderTurnLogGuardTrims(t *testing.T) {
	// Build a log well past the cap; oldest turns should be dropped behind a marker.
	big := strings.Repeat("x", 5000)
	var log []chatTurn
	for range 10 {
		log = append(log, chatTurn{User: big, Answer: big})
	}
	out := renderTurnLog(log)
	if len(out) > turnLogCharCap+len("[earlier conversation omitted]\n\n") {
		t.Errorf("rendered log %d exceeds cap %d", len(out), turnLogCharCap)
	}
	if !strings.HasPrefix(out, "[earlier conversation omitted]") {
		t.Errorf("expected omission marker, got start: %q", out[:60])
	}
	// The most recent turn must survive.
	if !strings.Contains(out, big) {
		t.Error("most recent turn should be retained")
	}
}

func TestRenderTurnLogUnderCapIsVerbatim(t *testing.T) {
	log := []chatTurn{{User: "a", Answer: "b"}, {User: "c", Answer: "d"}}
	out := renderTurnLog(log)
	if strings.Contains(out, "omitted") {
		t.Errorf("small log should not be trimmed:\n%s", out)
	}
	if !strings.Contains(out, "User: a") || !strings.Contains(out, "Assistant: d") {
		t.Errorf("malformed render:\n%s", out)
	}
}
