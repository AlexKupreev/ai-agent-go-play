package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/provider"
)

// fakeRunner emits a representative event sequence, then returns a final answer —
// standing in for the real executor so the transport is tested without a provider.
func fakeRunner(_ context.Context, task string, obs agent.Observer) (string, error) {
	obs.Emit(agent.Event{Kind: agent.EvStart, Task: task})
	obs.Emit(agent.Event{Kind: agent.EvResponse, Iteration: 0, Text: "thinking"})
	call := provider.ToolCall{Name: "shell", ID: "c1", Input: []byte(`{"cmd":"ls"}`)}
	obs.Emit(agent.Event{Kind: agent.EvToolStart, Call: &call})
	obs.Emit(agent.Event{Kind: agent.EvToolResult, Call: &call, Result: "file.txt"})
	return "all done", nil
}

func TestHTTP_StartRunAndStreamEvents(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, nil))
	defer srv.Close()

	// Start a run.
	body, _ := json.Marshal(startRunRequest{Task: "list files"})
	resp, err := http.Post(srv.URL+"/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /runs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /runs status = %d", resp.StatusCode)
	}
	var started startRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatalf("decode run id: %v", err)
	}
	if started.RunID == "" {
		t.Fatal("empty run id")
	}

	// Stream its events.
	streamResp, err := http.Get(srv.URL + "/runs/" + started.RunID + "/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer streamResp.Body.Close()
	if ct := streamResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	events := readSSE(t, streamResp.Body)

	gotKinds := make([]string, len(events))
	for i, e := range events {
		gotKinds[i] = e.Kind
	}
	wantKinds := []string{"start", "response", "tool_start", "tool_result", KindDone}
	if strings.Join(gotKinds, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("event kinds = %v, want %v", gotKinds, wantKinds)
	}

	// Spot-check payload fidelity across the wire.
	if events[0].Task != "list files" {
		t.Errorf("start.task = %q", events[0].Task)
	}
	if events[2].Tool != "shell" || events[2].Input != `{"cmd":"ls"}` {
		t.Errorf("tool_start = %+v", events[2])
	}
	if events[3].Result != "file.txt" {
		t.Errorf("tool_result.result = %q", events[3].Result)
	}
	if events[4].Text != "all done" {
		t.Errorf("done.text = %q", events[4].Text)
	}
}

func TestHTTP_UnknownRunIs404(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/runs/deadbeef/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// readSSE parses an SSE stream into events until the stream closes, decoding each
// frame's `data:` JSON into an Event.
func readSSE(t *testing.T, r interface{ Read([]byte) (int, error) }) []Event {
	t.Helper()
	var events []Event
	scanner := bufio.NewScanner(bufio.NewReader(r))
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatalf("bad SSE data %q: %v", data, err)
		}
		events = append(events, e)
	}
	return events
}
