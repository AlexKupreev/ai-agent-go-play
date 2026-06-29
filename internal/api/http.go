package api

import (
	"encoding/json"
	"net/http"
)

// NewServer returns the SSE transport adapter: net/http handlers over the Engine
// and the approval queue. This is the only file that knows the wire is HTTP+SSE — a
// future JSON-RPC adapter is a sibling over the same core (see docs/api-transport.md).
//
// approvals may be nil (run/stream only); the approval endpoints are registered just
// when a queue is supplied.
func NewServer(e *Engine, approvals *ApprovalQueue) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runs", handleStartRun(e))
	mux.HandleFunc("GET /runs/{id}/events", handleRunEvents(e))
	if approvals != nil {
		mux.HandleFunc("GET /approvals", handleListApprovals(approvals))
		mux.HandleFunc("POST /approvals/{id}", handleResolveApproval(approvals))
	}
	return mux
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
		id := e.StartRun(req.Task)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(startRunResponse{RunID: id})
	}
}

func handleRunEvents(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		ch, cancel, err := e.Subscribe(r.PathValue("id"))
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
		_ = json.NewEncoder(w).Encode(q.Pending())
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
		if err := q.Resolve(r.PathValue("id"), req.Approved); err != nil {
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
