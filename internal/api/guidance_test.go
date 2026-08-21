package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-agent-go-play/internal/guidance"
	"ai-agent-go-play/internal/session"
)

type memoryGuidanceService struct {
	values map[string]string
}

func (s *memoryGuidanceService) key(scope guidance.Scope, target string) string {
	return string(scope) + ":" + target
}

func (s *memoryGuidanceService) GetGuidance(scope guidance.Scope, target string) (string, error) {
	if target == "missing" {
		return "", ErrGuidanceTargetNotFound
	}
	return s.values[s.key(scope, target)], nil
}

func (s *memoryGuidanceService) SetGuidance(scope guidance.Scope, target, text string) error {
	if target == "missing" {
		return ErrGuidanceTargetNotFound
	}
	s.values[s.key(scope, target)] = text
	return nil
}

func TestHTTPGuidanceManagement(t *testing.T) {
	service := &memoryGuidanceService{values: map[string]string{}}
	e := NewEngine(RunnerFunc(fakeRunner))
	sessions := session.NewFileStore(t.TempDir())
	e.EnableSessions(sessions, echoTurns())
	e.SetGuidanceService(service)
	handler := NewServer(e, nil, nil, nil, nil)

	body, _ := json.Marshal(putGuidanceRequest{Guidance: "Pisz po polsku 🐻"})
	req := httptest.NewRequest(http.MethodPut, "/spaces/polish/guidance", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	var updated GuidanceDocument
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Scope != guidance.ScopeSpace || updated.Target != "polish" || updated.Guidance == "" || updated.Chars != guidance.CharCount(updated.Guidance) {
		t.Fatalf("PUT response = %+v", updated)
	}

	req = httptest.NewRequest(http.MethodGet, "/spaces/polish/guidance", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Pisz po polsku") {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body.String())
	}

	oversized, _ := json.Marshal(putGuidanceRequest{Guidance: strings.Repeat("🐻", guidance.MaxChars+1)})
	req = httptest.NewRequest(http.MethodPut, "/guidance/global", bytes.NewReader(oversized))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized PUT status = %d, want 400", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/spaces/missing/guidance", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing target status = %d, want 404", rec.Code)
	}

	sess, err := sessions.Create()
	if err != nil {
		t.Fatal(err)
	}
	sessionBody, _ := json.Marshal(putGuidanceRequest{Guidance: "session only"})
	req = httptest.NewRequest(http.MethodPut, "/sessions/"+sess.ID+"/guidance", bytes.NewReader(sessionBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || service.values["session:"+sess.ID] != "session only" {
		t.Fatalf("session guidance PUT = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/sessions/not-hex/guidance", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid session id status = %d, want 400", rec.Code)
	}
}
