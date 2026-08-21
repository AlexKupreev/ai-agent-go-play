package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-agent-go-play/internal/space"
)

func TestHTTPSpaceManagementContract(t *testing.T) {
	store := space.NewStore(t.TempDir() + "/spaces")
	older, err := store.Create("Polish lessons")
	if err != nil {
		t.Fatal(err)
	}
	older.Guidance = "Pisz po polsku 🐻"
	if err := store.Save(older); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("Tax stuff"); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(RunnerFunc(fakeRunner))
	e.SetSpaceService(store)
	handler := NewServer(e, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/spaces", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /spaces = %d: %s", rec.Code, rec.Body.String())
	}
	var listed []SpaceView
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != "tax-stuff" || listed[1].ID != "polish-lessons" {
		t.Fatalf("list order = %+v", listed)
	}
	if listed[1].GuidanceChars != 16 {
		t.Fatalf("Unicode guidance count = %d, want 16", listed[1].GuidanceChars)
	}
	if strings.Contains(rec.Body.String(), "Pisz po polsku") || strings.Contains(rec.Body.String(), `"guidance"`) {
		t.Fatalf("list leaked guidance body: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/spaces/polish-lessons", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Pisz po polsku") {
		t.Fatalf("GET one = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/spaces/Polish%20lessons", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET by non-canonical name = %d, want 404", rec.Code)
	}

	body := bytes.NewBufferString(`{"name":"Cooking"}`)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/spaces", body))
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/spaces/cooking" {
		t.Fatalf("POST = %d Location %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var created SpaceView
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil || created.ID != "cooking" || created.Name != "Cooking" {
		t.Fatalf("created = %+v, %v", created, err)
	}

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"duplicate", `{"name":"Cooking"}`, http.StatusConflict},
		{"empty", `{"name":"   "}`, http.StatusBadRequest},
		{"unusable", `{"name":"***"}`, http.StatusBadRequest},
		{"unknown field", `{"name":"New","guidance":"secret"}`, http.StatusBadRequest},
		{"trailing value", `{"name":"New"} {}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/spaces", strings.NewReader(tc.body)))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/spaces/cooking", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /spaces/{id} = %d, want 405 (route deliberately absent)", rec.Code)
	}
}

func TestHTTPSpaceListEmptyIsJSONArray(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.SetSpaceService(space.NewStore(t.TempDir() + "/spaces"))
	rec := httptest.NewRecorder()
	NewServer(e, nil, nil, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/spaces", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %d %q, want 200 []", rec.Code, rec.Body.String())
	}
}

func TestClientSpaceManagement(t *testing.T) {
	store := space.NewStore(t.TempDir() + "/spaces")
	e := NewEngine(RunnerFunc(fakeRunner))
	e.SetSpaceService(store)
	srv := httptest.NewServer(NewServer(e, nil, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)

	created, err := c.CreateSpace(context.Background(), "Polish lessons")
	if err != nil || created.ID != "polish-lessons" {
		t.Fatalf("CreateSpace = %+v, %v", created, err)
	}
	listed, err := c.ListSpaces(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("ListSpaces = %+v, %v", listed, err)
	}
	shown, err := c.GetSpace(context.Background(), created.ID)
	if err != nil || shown != created {
		t.Fatalf("GetSpace = %+v, %v; want %+v", shown, err, created)
	}
	if _, err := c.CreateSpace(context.Background(), "Polish lessons"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate CreateSpace error = %v", err)
	}
	if _, err := c.GetSpace(context.Background(), "missing"); err == nil || !strings.Contains(err.Error(), "no space") {
		t.Fatalf("missing GetSpace error = %v", err)
	}
}
