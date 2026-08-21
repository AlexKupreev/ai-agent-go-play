package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeEffectiveConfig struct{ snapshot EffectiveConfig }

func (f fakeEffectiveConfig) EffectiveConfig() EffectiveConfig { return f.snapshot }

func TestEffectiveConfigEndpoint(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.SetEffectiveConfigService(fakeEffectiveConfig{snapshot: EffectiveConfig{
		Model: ConfigValue{Value: "gpt-5.1", Source: "built-in"}, Workspace: "/work",
		SecretNames: []string{"weather"},
	}})
	r := httptest.NewRequest(http.MethodGet, "/config/effective", nil)
	w := httptest.NewRecorder()
	NewServer(e, nil, nil, nil, nil).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got EffectiveConfig
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Model.Value != "gpt-5.1" || got.Workspace != "/work" || len(got.SecretNames) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
}
