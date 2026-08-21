package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"ai-agent-go-play/internal/space"
)

// SpaceService is the persistence seam for human space management. The API exposes
// metadata only; full guidance remains on the explicit /spaces/{id}/guidance routes.
type SpaceService interface {
	List() ([]space.Space, error)
	Get(id string) (space.Space, error)
	Create(name string) (space.Space, error)
}

// SpaceView is the body-redacted management representation of a space.
type SpaceView struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	GuidanceChars int       `json:"guidance_chars"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type createSpaceRequest struct {
	Name string `json:"name"`
}

func spaceView(sp space.Space) SpaceView {
	return SpaceView{
		ID: sp.ID, Name: sp.Name, GuidanceChars: utf8.RuneCountInString(sp.Guidance),
		CreatedAt: sp.CreatedAt, UpdatedAt: sp.UpdatedAt,
	}
}

func handleListSpaces(service SpaceService) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		spaces, err := service.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		views := make([]SpaceView, 0, len(spaces))
		for _, sp := range spaces {
			views = append(views, spaceView(sp))
		}
		writeJSON(w, views)
	}
}

func handleGetSpace(service SpaceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sp, err := service.Get(r.PathValue("id"))
		if err != nil {
			spaceReadErrStatus(w, err)
			return
		}
		writeJSON(w, spaceView(sp))
	}
}

func handleCreateSpace(service SpaceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createSpaceRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := ensureJSONEOF(dec); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		sp, err := service.Create(req.Name)
		if err != nil {
			spaceCreateErrStatus(w, err)
			return
		}
		w.Header().Set("Location", "/spaces/"+url.PathEscape(sp.ID))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, spaceView(sp))
	}
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("multiple JSON values")
}

func spaceReadErrStatus(w http.ResponseWriter, err error) {
	if errors.Is(err, space.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func spaceCreateErrStatus(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, space.ErrInvalidName):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, space.ErrAlreadyExists):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func spacePath(id string) string { return "/spaces/" + url.PathEscape(id) }
