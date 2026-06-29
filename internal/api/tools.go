package api

import (
	"encoding/json"
	"net/http"
	"strconv"

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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
