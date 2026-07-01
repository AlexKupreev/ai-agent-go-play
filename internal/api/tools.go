package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/tools"
)

// ToolView is the wire form of a catalog entry: the model-facing fields plus
// provenance and required capabilities, so a frontend can browse and audit the
// tools the agent has authored. The implementation source is deliberately omitted
// from the listing (it can be large); a per-tool detail endpoint can add it later.
type ToolView struct {
	Name         string                  `json:"name"`
	Description  string                  `json:"description"`
	Scope        string                  `json:"scope"`
	Kind         string                  `json:"kind"` // impl kind: script | native
	Version      int                     `json:"version"`
	InputSchema  map[string]any          `json:"input_schema,omitempty"`
	RequiredCaps []capability.Capability `json:"required_caps,omitempty"`
}

func toolView(s tools.ToolSpec) ToolView {
	return ToolView{
		Name:         s.Name,
		Description:  s.Description,
		Scope:        string(s.Scope),
		Kind:         string(s.Impl.Kind),
		Version:      s.Version,
		InputSchema:  s.InputSchema,
		RequiredCaps: s.RequiredCaps,
	}
}

func toolViews(specs []tools.ToolSpec) []ToolView {
	out := make([]ToolView, len(specs))
	for i, s := range specs {
		out[i] = toolView(s)
	}
	return out
}

// ToolDetailView is the per-tool detail form: the listing view plus the fields a
// reviewer needs to actually audit a tool before revoking it — the implementation
// source and its smoke test. These are omitted from the listing (they can be large).
// Native tools carry no serializable source, so Source/Test stay empty for them.
type ToolDetailView struct {
	ToolView
	Lang   string `json:"lang,omitempty"`
	Source string `json:"source,omitempty"`
	Test   string `json:"test,omitempty"`
}

func toolDetail(s tools.ToolSpec) ToolDetailView {
	return ToolDetailView{
		ToolView: toolView(s),
		Lang:     s.Impl.Lang,
		Source:   s.Impl.Source,
		Test:     s.Test,
	}
}

// handleListTools serves GET /tools — every catalog entry, in registration order.
func handleListTools(cat tools.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, toolViews(cat.List(tools.ScopeAny)))
	}
}

// handleSearchTools serves GET /tools/search?q=...&k=... — tools ranked by relevance
// to q (name + description). k caps the result count; k<=0 (the default) returns all
// matches.
func handleSearchTools(cat tools.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, "q is required", http.StatusBadRequest)
			return
		}
		k := 0
		if ks := r.URL.Query().Get("k"); ks != "" {
			n, err := strconv.Atoi(ks)
			if err != nil {
				http.Error(w, "k must be an integer", http.StatusBadRequest)
				return
			}
			k = n
		}
		writeJSON(w, toolViews(cat.Search(q, k)))
	}
}

// handleToolDetail serves GET /tools/{name} — one tool including its implementation
// source (the listing omits it). 404 if the tool is not in the catalog.
func handleToolDetail(cat tools.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		spec, ok := cat.Get(r.PathValue("name"))
		if !ok {
			http.Error(w, "no such tool", http.StatusNotFound)
			return
		}
		writeJSON(w, toolDetail(spec))
	}
}

// handleRevokeTool serves DELETE /tools/{name} — removes a tool from the live set
// and the persistent catalog, auditing the removal. 404 if the tool is absent. rec
// may be nil (no management-plane audit sink), in which case the revoke is not logged.
func handleRevokeTool(cat tools.Registry, rec audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		// Read the spec before revoking so the audit event can carry its identity.
		spec, ok := cat.Get(name)
		if !ok {
			http.Error(w, "no such tool", http.StatusNotFound)
			return
		}
		if !cat.Revoke(name) {
			// Raced with another revoke; treat as already-gone.
			http.Error(w, "no such tool", http.StatusNotFound)
			return
		}
		if rec != nil {
			rec.Record(audit.Event{
				Type: audit.EventToolRevoked,
				Fields: map[string]any{
					"name":      spec.Name,
					"code_hash": spec.CodeHash,
					"scope":     string(spec.Scope),
					"version":   spec.Version,
				},
			})
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
