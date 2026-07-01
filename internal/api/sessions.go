package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"ai-agent-go-play/internal/session"
)

// Session endpoints layer persistent multi-turn conversations on top of runs: a turn
// is a run whose executor is seeded with the session's history, so it streams via the
// usual GET /runs/{id}/events and its escalations park in the usual approval queue.

type startSessionResponse struct {
	SessionID string `json:"session_id"`
}

func handleStartSession(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := e.StartSession()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, startSessionResponse{SessionID: id})
	}
}

func handleListSessions(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		infos, err := e.ListSessions()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if infos == nil {
			infos = []session.Info{}
		}
		writeJSON(w, infos)
	}
}

func handleCloseSession(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := e.CloseSession(r.PathValue("id")); err != nil {
			sessionErrStatus(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type postTurnRequest struct {
	Text string `json:"text"`
}

type postTurnResponse struct {
	RunID string `json:"run_id"`
}

func handlePostTurn(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req postTurnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.Text == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}
		runID, err := e.PostTurn(r.PathValue("id"), req.Text)
		if err != nil {
			sessionErrStatus(w, err)
			return
		}
		writeJSON(w, postTurnResponse{RunID: runID})
	}
}

// sessionErrStatus maps a session error to an HTTP status: unknown session ⇒ 404,
// anything else ⇒ 500.
func sessionErrStatus(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
