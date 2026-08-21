package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/tools"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestClient_DrivesRunWithApproval exercises the Client end-to-end against a real
// server: start a run that parks an approval, resolve it via the client while
// streaming, and confirm the streamed terminal event reflects the decision.
func TestClient_DrivesRunWithApproval(t *testing.T) {
	q := NewApprovalQueue()
	runner := RunnerFunc(func(ctx context.Context, runID, task string, _ RunOptions, obs agent.Observer) (string, error) {
		obs.Emit(agent.Event{Kind: agent.EvStart, Task: task})
		ok, err := q.Approve(ctx, tools.ApprovalRequest{Kind: "shell.destructive", Title: "rm", Detail: "rm -rf x", RunID: task})
		if err != nil {
			return "", err
		}
		if !ok {
			return "declined", nil
		}
		return "did it", nil
	})

	srv := httptest.NewServer(NewServer(NewEngine(runner), q, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	runID, err := c.StartRun(ctx, "clean", RunOptions{})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Background: watch for the parked approval and approve it.
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			pending, err := c.Pending(ctx)
			if err == nil && len(pending) > 0 {
				_ = c.Resolve(ctx, pending[0].ID, true)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	var mu sync.Mutex
	var terminal string
	err = c.StreamEvents(ctx, runID, func(e Event) {
		if e.Kind == KindDone || e.Kind == KindError {
			mu.Lock()
			terminal = e.Text
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if terminal != "did it" {
		t.Fatalf("terminal event text = %q, want %q", terminal, "did it")
	}
}

// TestClient_AnswersQuestion drives the ask_user half over the Client: the run parks a
// question, the client reads it from Pending (mode "question") and delivers a free-text
// answer via Answer, which unblocks the run.
func TestClient_AnswersQuestion(t *testing.T) {
	q := NewApprovalQueue()
	runner := RunnerFunc(func(ctx context.Context, runID, task string, _ RunOptions, obs agent.Observer) (string, error) {
		ans, err := q.Ask(ctx, tools.Question{Prompt: "which env?", RunID: task})
		if err != nil {
			return "", err
		}
		return "using " + ans, nil
	})

	srv := httptest.NewServer(NewServer(NewEngine(runner), q, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	runID, err := c.StartRun(ctx, "deploy", RunOptions{})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			pending, err := c.Pending(ctx)
			if err == nil && len(pending) > 0 && pending[0].Mode == "question" {
				_ = c.Answer(ctx, pending[0].ID, "prod")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	var mu sync.Mutex
	var terminal string
	err = c.StreamEvents(ctx, runID, func(e Event) {
		if e.Kind == KindDone || e.Kind == KindError {
			mu.Lock()
			terminal = e.Text
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if terminal != "using prod" {
		t.Fatalf("terminal event text = %q, want %q", terminal, "using prod")
	}
}

// TestClient_RevokeTool exercises the client's tool-detail + revoke path against a
// real server sharing a catalog.
func TestClient_RevokeTool(t *testing.T) {
	cat := catalogWith(t, scriptSpec("reverse_string", "reverse the characters in a string"))
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, cat, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	detail, err := c.ToolDetail(ctx, "reverse_string")
	if err != nil {
		t.Fatalf("ToolDetail: %v", err)
	}
	if detail.Source == "" {
		t.Fatalf("detail missing source: %+v", detail)
	}

	if err := c.RevokeTool(ctx, "reverse_string"); err != nil {
		t.Fatalf("RevokeTool: %v", err)
	}
	if _, ok := cat.Get("reverse_string"); ok {
		t.Fatal("tool still present after client revoke")
	}

	// Revoking a missing tool surfaces an error.
	if err := c.RevokeTool(ctx, "reverse_string"); err == nil {
		t.Fatal("expected error revoking absent tool, got nil")
	}
}

func TestClient_StartRunErrorsOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)

	// Empty task is rejected with 400 by the server.
	if _, err := c.StartRun(context.Background(), "", RunOptions{}); err == nil {
		t.Fatal("expected error for empty task, got nil")
	}
}

func TestClientDeadEngineErrorIncludesRecovery(t *testing.T) {
	c := NewClient("http://127.0.0.1:65534")
	c.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	_, err := c.EffectiveConfig(context.Background())
	if err == nil || !strings.Contains(err.Error(), "check --addr") || !strings.Contains(err.Error(), "agent serve") {
		t.Fatalf("dead-engine error lacks recovery: %v", err)
	}
}

func TestClientReloadDecodesStructuredDiff(t *testing.T) {
	c := NewClient("http://engine")
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/reload" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"changed":["config/AGENTS.md"],"prompts":{"composition":"built-in base","sources":[],"warnings":[]},"agent_types":{"count":2,"added":[],"removed":[],"changed":[],"sources":[]},"defaults":{"model":{"before":"gpt-5.1","after":"gpt-5.1"},"tier":{"before":"balanced","after":"balanced"}}}`)), Header: make(http.Header)}, nil
	})}
	diff, err := c.Reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Changed) != 1 || diff.AgentTypes.Count != 2 {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestClientStatusUsesExplicitSessionQuery(t *testing.T) {
	c := NewClient("http://engine")
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/status" || r.URL.Query().Get("session_id") != "ab cd" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"version":"dev","config":{"model":{"value":"gpt-5.1","source":"built-in"}},"session":{"id":"ab cd","model":{"requested":"","effective":"gpt-5.1"},"tier":{"requested":"","effective":"balanced"},"guidance_chars":0,"active_space":null},"host":{},"state":[]}`)),
		}, nil
	})}
	id := "ab cd"
	got, err := c.Status(context.Background(), &id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Session == nil || got.Session.ID != id || got.Config.Model.Value != "gpt-5.1" {
		t.Fatalf("status = %+v", got)
	}
}
