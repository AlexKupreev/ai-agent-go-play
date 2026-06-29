package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/tools"
)

// TestHTTP_ApprovalParksAndResolves drives the full async-approval loop over the
// API: a run blocks on the queue, the request surfaces at GET /approvals, and a
// POST /approvals/{id} unblocks the run with the supplied decision.
func TestHTTP_ApprovalParksAndResolves(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approved bool
		want     string // final answer the runner returns for the decision
	}{
		{"approve", true, "did it"},
		{"deny", false, "declined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := NewApprovalQueue()

			// The runner asks the queue to approve a destructive action, mirroring
			// what shell/author_tool do through the Approver seam.
			runner := RunnerFunc(func(ctx context.Context, task string, obs agent.Observer) (string, error) {
				ok, err := q.Approve(ctx, tools.ApprovalRequest{
					Kind:   "shell.destructive",
					Title:  "rm -rf build",
					Detail: "rm -rf ./build",
					RunID:  task,
				})
				if err != nil {
					return "", err
				}
				if !ok {
					return "declined", nil
				}
				return "did it", nil
			})

			srv := httptest.NewServer(NewServer(NewEngine(runner), q))
			defer srv.Close()

			// Start a run; it will block in the queue.
			runID := startRun(t, srv.URL, "clean build")

			// The parked request should surface (poll: the run goroutine may not have
			// reached Approve the instant POST /runs returns).
			pending := waitForPending(t, srv.URL, 1)
			if pending[0].Kind != "shell.destructive" || pending[0].RunID != "clean build" {
				t.Fatalf("unexpected pending: %+v", pending[0])
			}

			// Resolve it.
			resolve(t, srv.URL, pending[0].ID, tc.approved)

			// The run completes with the decision reflected in the terminal event.
			final := waitForDone(t, srv.URL, runID)
			if final != tc.want {
				t.Fatalf("final answer = %q, want %q", final, tc.want)
			}

			// Queue is empty again.
			if got := getPending(t, srv.URL); len(got) != 0 {
				t.Fatalf("queue not drained: %+v", got)
			}
		})
	}
}

func TestHTTP_ResolveUnknownIs404(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), NewApprovalQueue()))
	defer srv.Close()

	body, _ := json.Marshal(resolveApprovalRequest{Approved: true})
	resp, err := http.Post(srv.URL+"/approvals/nope", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// --- helpers ---

func startRun(t *testing.T, base, task string) string {
	t.Helper()
	body, _ := json.Marshal(startRunRequest{Task: task})
	resp, err := http.Post(base+"/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /runs: %v", err)
	}
	defer resp.Body.Close()
	var out startRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode run id: %v", err)
	}
	return out.RunID
}

func getPending(t *testing.T, base string) []PendingApproval {
	t.Helper()
	resp, err := http.Get(base + "/approvals")
	if err != nil {
		t.Fatalf("GET /approvals: %v", err)
	}
	defer resp.Body.Close()
	var out []PendingApproval
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode approvals: %v", err)
	}
	return out
}

func waitForPending(t *testing.T, base string, n int) []PendingApproval {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := getPending(t, base); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d pending approval(s)", n)
	return nil
}

func resolve(t *testing.T, base, id string, approved bool) {
	t.Helper()
	body, _ := json.Marshal(resolveApprovalRequest{Approved: approved})
	resp, err := http.Post(base+"/approvals/"+id, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /approvals/%s: %v", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resolve status = %d, want 204", resp.StatusCode)
	}
}

// waitForDone streams the run and returns the text of its terminal done/error event.
func waitForDone(t *testing.T, base, runID string) string {
	t.Helper()
	resp, err := http.Get(base + "/runs/" + runID + "/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	for _, e := range readSSE(t, resp.Body) {
		if e.Kind == KindDone || e.Kind == KindError {
			return e.Text
		}
	}
	t.Fatal("no terminal event in stream")
	return ""
}
