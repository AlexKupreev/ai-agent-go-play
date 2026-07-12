package api

import (
	"encoding/json"
	"net/http"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/tools"
)

// NewServer returns the SSE transport adapter: net/http handlers over the Engine,
// the approval queue, and the tool catalog. This is the only file that knows the
// wire is HTTP+SSE — a future JSON-RPC adapter is a sibling over the same core (see
// docs/api-transport.md).
//
// approvals and catalog may each be nil; their endpoints are registered only when
// the corresponding dependency is supplied (run/stream is always available). rec is
// the management-plane audit sink (used when a tool is revoked over the API); it may
// be nil, in which case revokes are not audited. auditLog is the query side of the
// audit log for GET /audit; it may be nil, in which case that endpoint is absent.
func NewServer(e *Engine, approvals *ApprovalQueue, catalog tools.Registry, rec audit.Recorder, auditLog audit.Reader) http.Handler {
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
		// The literal /tools/search takes precedence over the {name} wildcard in
		// Go 1.22+ mux precedence, so both can coexist.
		mux.HandleFunc("GET /tools/search", handleSearchTools(catalog))
		mux.HandleFunc("GET /tools/{name}", handleToolDetail(catalog))
		mux.HandleFunc("DELETE /tools/{name}", handleRevokeTool(catalog, rec))
	}
	if auditLog != nil {
		mux.HandleFunc("GET /audit", handleAudit(auditLog))
	}
	if e.SessionsEnabled() {
		mux.HandleFunc("POST /sessions", handleStartSession(e))
		mux.HandleFunc("GET /sessions", handleListSessions(e))
		mux.HandleFunc("PATCH /sessions/{id}", handleUpdateSession(e))
		mux.HandleFunc("DELETE /sessions/{id}", handleCloseSession(e))
		// Distinct sub-paths for the destructive purge and the recovery restore, so
		// neither rides on the archiving DELETE. Literal segments take precedence over
		// {id}, so they coexist with the routes above.
		mux.HandleFunc("DELETE /sessions/{id}/purge", handlePurgeSession(e))
		mux.HandleFunc("POST /sessions/{id}/restore", handleRestoreSession(e))
		mux.HandleFunc("POST /sessions/{id}/turns", handlePostTurn(e))
		// User file uploads into the session's working area, served only when a FileStore is
		// wired (uploads.go).
		if e.UploadsEnabled() {
			mux.HandleFunc("POST /sessions/{id}/files", handleUploadFile(e))
		}
	}
	return mux
}

// runErrStatus maps an engine run error to an HTTP status: unknown run ⇒ 404.
func runErrStatus(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusNotFound)
}

type startRunRequest struct {
	Task       string `json:"task"`
	RunOptions        // optional per-request "model"/"tier" (flattened into the body)
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
		id := e.StartRun(req.Task, req.RunOptions)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(startRunResponse{RunID: id})
	}
}

// handleListRuns serves GET /runs — all runs, newest first.
func handleListRuns(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, e.ListRuns())
	}
}

// handleRunStatus serves GET /runs/{id} — metadata for a run.
func handleRunStatus(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info, err := e.RunStatus(r.PathValue("id"))
		if err != nil {
			runErrStatus(w, err)
			return
		}
		writeJSON(w, info)
	}
}

// handleCancelRun serves POST /runs/{id}/cancel — the per-run kill switch.
func handleCancelRun(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := e.StopRun(r.PathValue("id")); err != nil {
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

// resolveApprovalRequest is the body of POST /approvals/{id}. It resolves either half of
// the human gate: an approval carries `approved` (bool); a question carries `answer`
// (string). When `answer` is present the item is answered, otherwise it is approved/denied.
type resolveApprovalRequest struct {
	Approved bool    `json:"approved"`
	Answer   *string `json:"answer,omitempty"`
}

func handleResolveApproval(q *ApprovalQueue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resolveApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		var err error
		if req.Answer != nil {
			err = q.Answer(r.PathValue("id"), *req.Answer)
		} else {
			err = q.Resolve(r.PathValue("id"), req.Approved)
		}
		if err != nil {
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
