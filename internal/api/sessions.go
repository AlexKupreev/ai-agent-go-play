package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/session"
)

// sessionIDPattern bounds a path-supplied session id to hex (the shape session.newID
// produces) so a destructive purge/restore can't be steered at a crafted path. It is a
// belt-and-suspenders guard on top of the mux's single-segment {id}: no separators, no
// traversal.
var sessionIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{1,64}$`)

// validSessionID reports whether id is a well-formed session id.
func validSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

// Session endpoints layer persistent multi-turn conversations on top of runs: a turn
// is a run whose executor is seeded with the session's history, so it streams via the
// usual GET /runs/{id}/events and its escalations park in the usual approval queue.

type startSessionRequest struct {
	RunOptions // optional initial sticky "model"/"tier"/"space" (flattened into the body)
}

type startSessionResponse struct {
	SessionID string `json:"session_id"`
}

func handleStartSession(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The body is optional: an empty POST creates a session with engine defaults.
		var req startSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := validateTierOpt(req.Tier); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, err := e.StartSession(req.RunOptions)
		if err != nil {
			sessionErrStatus(w, err)
			return
		}
		writeJSON(w, startSessionResponse{SessionID: id})
	}
}

// updateSessionRequest is the body of PATCH /sessions/{id}. Pointer fields make the update
// per-field: a nil field is left unchanged, a present field is set (an empty string clears it
// back to the engine default), so `/model x` cannot inadvertently wipe a previously-set tier.
type updateSessionRequest struct {
	Model *string `json:"model,omitempty"`
	Tier  *string `json:"tier,omitempty"`
	Space *string `json:"space,omitempty"`
}

func handleUpdateSession(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req updateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.Tier != nil {
			if err := validateTierOpt(*req.Tier); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		info, err := e.UpdateSession(r.PathValue("id"), req.Model, req.Tier, req.Space)
		if err != nil {
			sessionErrStatus(w, err)
			return
		}
		writeJSON(w, info)
	}
}

// validateTierOpt accepts an empty string (inherit the default) or a syntactically valid
// tier name. It rejects nonsense at the transport boundary so a bad value can't be stored
// and silently break every future turn; clamping to the serve ceiling stays the cmd layer's
// job (resolveOpts).
func validateTierOpt(tier string) error {
	if tier == "" {
		return nil
	}
	_, err := capability.ParseTier(tier)
	return err
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

// handlePurgeSession serves DELETE /sessions/{id}/purge — the irreversible removal (a
// distinct sub-path from the archiving DELETE /sessions/{id}, so a destructive verb never
// hinges on a droppable query flag). The id is validated before it reaches disk.
func handlePurgeSession(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validSessionID(id) {
			http.Error(w, "invalid session id", http.StatusBadRequest)
			return
		}
		if err := e.PurgeSession(id); err != nil {
			sessionErrStatus(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleRestoreSession serves POST /sessions/{id}/restore — un-archive a closed session so
// it is resumable again.
func handleRestoreSession(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validSessionID(id) {
			http.Error(w, "invalid session id", http.StatusBadRequest)
			return
		}
		if err := e.RestoreSession(id); err != nil {
			sessionErrStatus(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type postTurnRequest struct {
	Text       string `json:"text"`
	RunOptions        // optional per-turn "model"/"tier" override (flattened into the body)
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
		runID, err := e.PostTurn(r.PathValue("id"), req.Text, req.RunOptions)
		if err != nil {
			sessionErrStatus(w, err)
			return
		}
		writeJSON(w, postTurnResponse{RunID: runID})
	}
}

// sessionErrStatus maps a session error to an HTTP status: unknown session ⇒ 404, a space
// that does not exist ⇒ 400 (a caller error, like a malformed tier — and the message names
// the spaces that do exist), anything else ⇒ 500.
func sessionErrStatus(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrUnknownSpace):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
