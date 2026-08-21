package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"ai-agent-go-play/internal/guidance"
	"ai-agent-go-play/internal/session"
)

// ErrGuidanceTargetNotFound classifies an unknown space or other non-session target.
var ErrGuidanceTargetNotFound = errors.New("guidance target not found")

// GuidanceService is the persistence seam for the guidance management plane. The API
// owns scopes and wire types; cmd supplies the workspace-aware implementation.
type GuidanceService interface {
	GetGuidance(scope guidance.Scope, target string) (string, error)
	SetGuidance(scope guidance.Scope, target, text string) error
}

// GuidanceDocument is the explicit management representation. Session listings remain
// body-redacted; only these target-specific endpoints return the text.
type GuidanceDocument struct {
	Scope    guidance.Scope `json:"scope"`
	Target   string         `json:"target,omitempty"`
	Guidance string         `json:"guidance"`
	Chars    int            `json:"chars"`
}

type putGuidanceRequest struct {
	Guidance string `json:"guidance"`
}

func guidanceHandler(service GuidanceService, scope guidance.Scope, target func(*http.Request) string, put bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := target(r)
		if scope == guidance.ScopeSession && !validSessionID(id) {
			http.Error(w, "invalid session id", http.StatusBadRequest)
			return
		}
		if put {
			var req putGuidanceRequest
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			if err := guidance.Validate(req.Guidance); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := service.SetGuidance(scope, id, req.Guidance); err != nil {
				guidanceErrStatus(w, err)
				return
			}
		}
		text, err := service.GetGuidance(scope, id)
		if err != nil {
			guidanceErrStatus(w, err)
			return
		}
		writeJSON(w, GuidanceDocument{Scope: scope, Target: id, Guidance: text, Chars: guidance.CharCount(text)})
	}
}

func guidanceErrStatus(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound), errors.Is(err, ErrGuidanceTargetNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrSessionsDisabled):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func guidancePathFor(scope guidance.Scope, target string) string {
	switch scope {
	case guidance.ScopeGlobal:
		return "/guidance/global"
	case guidance.ScopeSpace:
		return "/spaces/" + url.PathEscape(target) + "/guidance"
	case guidance.ScopeSession:
		return "/sessions/" + url.PathEscape(target) + "/guidance"
	default:
		panic(fmt.Sprintf("unsupported guidance scope %q", scope))
	}
}
