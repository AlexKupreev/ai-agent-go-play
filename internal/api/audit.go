package api

import (
	"net/http"
	"strconv"

	"ai-agent-go-play/internal/audit"
)

// handleAudit serves GET /audit?run=&type=&limit= — the single review surface for
// everything effectful (capability use, tool authoring/revocation, memory writes).
// run and type filter; limit caps to the last N matches (limit<=0 ⇒ all). Events are
// returned oldest first.
func handleAudit(reader audit.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if ls := r.URL.Query().Get("limit"); ls != "" {
			n, err := strconv.Atoi(ls)
			if err != nil {
				http.Error(w, "limit must be an integer", http.StatusBadRequest)
				return
			}
			limit = n
		}
		filter := audit.Filter{
			Run:  r.URL.Query().Get("run"),
			Type: r.URL.Query().Get("type"),
		}
		events, err := reader.Tail(limit, filter)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Never serialize a nil slice as JSON null — a browser expects [].
		if events == nil {
			events = []audit.Event{}
		}
		writeJSON(w, events)
	}
}
