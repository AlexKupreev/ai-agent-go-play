package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/tools"
)

func catalogWith(t *testing.T, specs ...tools.ToolSpec) tools.Registry {
	t.Helper()
	reg := tools.NewMemoryRegistry()
	for _, s := range specs {
		if _, err := reg.Register(s); err != nil {
			t.Fatalf("register %q: %v", s.Name, err)
		}
	}
	return reg
}

func scriptSpec(name, desc string) tools.ToolSpec {
	return tools.ToolSpec{
		Name:        name,
		Description: desc,
		InputSchema: map[string]any{"type": "object"},
		// Source must be distinct per tool, else the registry dedups by code hash.
		Impl:  tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: "return '" + name + "'"},
		Scope: tools.ScopeShared,
	}
}

func getToolViews(t *testing.T, url string) []ToolView {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d", url, resp.StatusCode)
	}
	var out []ToolView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestHTTP_ListTools(t *testing.T) {
	cat := catalogWith(t,
		scriptSpec("reverse_string", "reverse the characters in a string"),
		scriptSpec("sum_numbers", "add a list of numbers"),
	)
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, cat, nil, nil))
	defer srv.Close()

	views := getToolViews(t, srv.URL+"/tools")
	if len(views) != 2 {
		t.Fatalf("got %d tools, want 2", len(views))
	}
	// Registration order is preserved.
	if views[0].Name != "reverse_string" || views[1].Name != "sum_numbers" {
		t.Fatalf("unexpected order: %s, %s", views[0].Name, views[1].Name)
	}
	if views[0].Kind != "script" || views[0].Scope != "shared" || views[0].Version != 1 {
		t.Errorf("unexpected view: %+v", views[0])
	}
}

func TestHTTP_SearchTools(t *testing.T) {
	cat := catalogWith(t,
		scriptSpec("reverse_string", "reverse the characters in a string"),
		scriptSpec("sum_numbers", "add a list of numbers"),
	)
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, cat, nil, nil))
	defer srv.Close()

	views := getToolViews(t, srv.URL+"/tools/search?q=reverse+string")
	if len(views) == 0 || views[0].Name != "reverse_string" {
		t.Fatalf("search did not rank reverse_string first: %+v", views)
	}
	for _, v := range views {
		if v.Name == "sum_numbers" {
			t.Errorf("unrelated tool returned: %s", v.Name)
		}
	}

	// Missing q is a 400.
	resp, err := http.Get(srv.URL + "/tools/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing q status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTP_ToolDetail(t *testing.T) {
	cat := catalogWith(t, scriptSpec("reverse_string", "reverse the characters in a string"))
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, cat, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/tools/reverse_string")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var detail ToolDetailView
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The detail view carries the source that the listing omits.
	if detail.Name != "reverse_string" || detail.Source == "" || detail.Lang != "lua" {
		t.Fatalf("unexpected detail: %+v", detail)
	}

	// Unknown tool is a 404.
	resp2, err := http.Get(srv.URL + "/tools/does_not_exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown tool status = %d, want 404", resp2.StatusCode)
	}
}

func TestHTTP_RevokeTool(t *testing.T) {
	cat := catalogWith(t, scriptSpec("reverse_string", "reverse the characters in a string"))
	rec := &audit.MemoryRecorder{}
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, cat, rec, nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/tools/reverse_string", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	// The tool drops from the live set.
	if _, ok := cat.Get("reverse_string"); ok {
		t.Fatal("tool still present after revoke")
	}
	if len(cat.List(tools.ScopeAny)) != 0 {
		t.Fatalf("catalog not empty after revoke")
	}

	// The revoke is audited.
	events := rec.Snapshot()
	if len(events) != 1 || events[0].Type != audit.EventToolRevoked {
		t.Fatalf("expected one tool_revoked event, got %+v", events)
	}
	if events[0].Fields["name"] != "reverse_string" {
		t.Fatalf("audit event missing tool name: %+v", events[0].Fields)
	}

	// Revoking again is a 404.
	req2, _ := http.NewRequest(http.MethodDelete, srv.URL+"/tools/reverse_string", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("second revoke status = %d, want 404", resp2.StatusCode)
	}
}

func TestHTTP_ToolsEndpointsAbsentWithoutCatalog(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/tools")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no catalog wired)", resp.StatusCode)
	}
}
