package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/tools"
)

func statusTestEngine(t *testing.T) (*Engine, session.Store, string) {
	t.Helper()
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, "sessions")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "one.json"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := session.NewFileStore(filepath.Join(workDir, "live-sessions"))
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(store, TurnRunnerFunc(func(context.Context, string, string, []provider.Message, string, RunOptions, agent.Observer) (string, []provider.Message, error) {
		return "", nil, nil
	}))
	e.SetSpaceResolver(func(requested string) (string, error) { return "polish-lessons", nil })
	e.SetEffectiveConfigService(fakeEffectiveConfig{snapshot: EffectiveConfig{
		Model:       ConfigValue{Value: "gpt-5.1", Source: "built-in"},
		TierCeiling: ConfigValue{Value: "balanced", Source: "config"},
		Workspace:   workDir,
		Prompts:     PromptConfig{Composition: "built-in base", Sources: []PromptSource{}, Warnings: []string{}},
		Guidance:    []GuidanceSource{}, AgentTypes: AgentTypeConfig{Sources: []AgentTypeSource{}},
		SecretNames: []string{},
	}})
	e.SetStatusRuntime(StatusRuntime{
		Version: "test-version", WorkDir: workDir,
		StateDirs: []tools.StateDir{{Label: "sessions", Path: stateDir}},
		ResolveSpace: func(id string) (StatusSpace, error) {
			return StatusSpace{ID: id, Name: "Polish lessons"}, nil
		},
	})
	return e, store, workDir
}

func TestStatusEndpointEngineOnlyOmitsSession(t *testing.T) {
	e, _, workDir := statusTestEngine(t)
	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	NewServer(e, nil, nil, nil, nil).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["session"]; exists {
		t.Fatalf("engine-only status must omit session: %s", w.Body.String())
	}
	var got StatusResponse
	data, _ := json.Marshal(raw)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "test-version" || got.Config.Workspace != workDir || got.Host.CPUCount < 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	if len(got.State) != 1 || got.State[0].Label != "sessions" || got.State[0].Entries != 1 || got.State[0].Bytes != 5 {
		t.Fatalf("state = %+v", got.State)
	}
}

func TestStatusEndpointSessionOverlay(t *testing.T) {
	e, store, _ := statusTestEngine(t)
	id, err := e.StartSession(RunOptions{Model: "custom-model", Tier: "permissive", Space: "Polish lessons"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	sess.Guidance = "Pisz krótko 🐻"
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/status?session_id="+id, nil)
	w := httptest.NewRecorder()
	NewServer(e, nil, nil, nil, nil).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Session == nil || got.Session.ID != id {
		t.Fatalf("session = %+v", got.Session)
	}
	if got.Session.Model != (StatusValue{Requested: "custom-model", Effective: "custom-model"}) {
		t.Fatalf("model = %+v", got.Session.Model)
	}
	if got.Session.Tier != (StatusValue{Requested: "permissive", Effective: "balanced"}) {
		t.Fatalf("tier = %+v", got.Session.Tier)
	}
	if got.Session.GuidanceChars != 13 {
		t.Fatalf("guidance chars = %d", got.Session.GuidanceChars)
	}
	if got.Session.ActiveSpace == nil || *got.Session.ActiveSpace != (StatusSpace{ID: "polish-lessons", Name: "Polish lessons"}) {
		t.Fatalf("active space = %+v", got.Session.ActiveSpace)
	}
}

func TestStatusEndpointSessionErrors(t *testing.T) {
	e, _, _ := statusTestEngine(t)
	server := NewServer(e, nil, nil, nil, nil)
	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/status?session_id=", want: http.StatusBadRequest},
		{path: "/status?session_id=deadbeef", want: http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d; body = %s", tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}
