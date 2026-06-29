package api

import (
	"encoding/json"
	"net/http"

	"ai-agent-go-play/internal/tools"
)

// NewServer returns the SSE transport adapter: net/http handlers over the Engine,
// the approval queue, and the tool catalog. This is the only file that knows the
// wire is HTTP+SSE — a future JSON-RPC adapter is a sibling over the same core (see
// docs/api-transport.md).
//
// approvals and catalog may each be nil; their endpoints are registered only when
// the corresponding dependency is supplied (run/stream is always available).
func NewServer(e *Engine, approvals *ApprovalQueue, catalog tools.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runs", handleStartRun(e))
	mux.HandleFunc("GET /runs", handleListRuns(e))
	mux.HandleFunc("GET /runs/{id}", handleRunStatus(e))
	mux.HandleFunc("GET /runs/{id}/events", handleRunEvents(e))
	mux.HandleFunc("POST /runs/{id}/cancel", handleCancelRun(e))
	if approvals != nil {
		mux.HandleFunc("GET /approvals", handleListApprovals(approvals))
		mux.HandleFunc("POST /approvals/{id}", handleResolveApproval(approvals))
	}
	if catalog != nil {
		mux.HandleFunc("GET /tools", handleListTools(catalog))
		mux.HandleFunc("GET /tools/search", handleSearchTools(catalog))
	}
	return mux
}

// ownerHeader carries the caller's session identity. The trusted frontend asserts
// it (a Telegram user, the CLI's local user); it is a scoping/attribution label,
// NOT an authentication credential (design §1/§5). Absent ⇒ "local" (single-user).
const ownerHeader = "X-Agent-Owner"

func ownerOf(r *http.Request) string {
	if o := r.Header.Get(ownerHeader); o != "" {
		return o
	}
	return "local"
}

// runErrStatus maps an engine run error to an HTTP status: unknown/not-owner ⇒ 404.
func runErrStatus(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusNotFound)
}

type startRunRequest struct {
	Task string `json:"task"`
}

type startRunResponse struct {
	RunID string `json:"run_id"`
}

func handleStartRun(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req startRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.Task == "" {
			http.Error(w, "task is required", http.StatusBadRequest)
			return
		}
		id := e.StartRun(req.Task, ownerOf(r))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(startRunResponse{RunID: id})
	}
}

// handleListRuns serves GET /runs — the caller's runs, newest first.
func handleListRuns(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, e.ListRuns(ownerOf(r)))
	}
}

// handleRunStatus serves GET /runs/{id} — metadata for one of the caller's runs.
func handleRunStatus(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info, err := e.RunStatus(ownerOf(r), r.PathValue("id"))
		if err != nil {
			runErrStatus(w, err)
			return
		}
		writeJSON(w, info)
	}
}

// handleCancelRun serves POST /runs/{id}/cancel — the per-run kill switch, scoped to
// the caller's runs.
func handleCancelRun(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := e.StopRun(ownerOf(r), r.PathValue("id")); err != nil {
			runErrStatus(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleRunEvents(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		ch, cancel, err := e.Subscribe(ownerOf(r), r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer cancel()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return // run ended; hub closed the stream
				}
				writeSSE(w, ev)
				flusher.Flush()
			}
		}
	}
}

func handleListApprovals(q *ApprovalQueue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(q.Pending(ownerOf(r)))
	}
}

type resolveApprovalRequest struct {
	Approved bool `json:"approved"`
}

func handleResolveApproval(q *ApprovalQueue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resolveApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := q.Resolve(ownerOf(r), r.PathValue("id"), req.Approved); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeSSE encodes one event as an SSE frame: `event: <kind>` plus a JSON `data:`
// line, terminated by a blank line.
func writeSSE(w http.ResponseWriter, ev Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("event: " + ev.Kind + "\ndata: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
}
