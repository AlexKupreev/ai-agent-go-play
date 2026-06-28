package sandbox

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func brokerWithHTTP(rec audit.Recorder, body string) *capability.Broker {
	b := capability.NewBroker(rec, nil)
	b.HTTP = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	return b
}

func TestSandbox_ComputeOnly(t *testing.T) {
	g := NewLuaGlue(nil)
	out, err := g.Run(context.Background(), "return input.a + input.b", map[string]any{"a": 2.0, "b": 3.0}, &capability.GrantContext{}, time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "5" {
		t.Errorf("got %q, want 5", out)
	}
}

func TestSandbox_UngrantedHostFuncAbsent(t *testing.T) {
	// With an empty grant, http_get must not exist in the script environment.
	g := NewLuaGlue(brokerWithHTTP(&audit.MemoryRecorder{}, "secret"))
	out, err := g.Run(context.Background(), `return type(http_get)`, nil, &capability.GrantContext{}, time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "nil" {
		t.Errorf("ungranted http_get should be nil, got %q", out)
	}
}

func TestSandbox_GrantedHTTPGet(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	g := NewLuaGlue(brokerWithHTTP(rec, "remote-body"))
	grant := &capability.GrantContext{Run: "r1", Granted: []capability.Capability{
		{Kind: capability.HTTPGet, Hosts: []string{"example.com"}},
	}}

	out, err := g.Run(context.Background(), `return http_get("https://example.com/p")`, nil, grant, time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "remote-body" {
		t.Errorf("got %q, want remote-body", out)
	}
	if len(rec.Snapshot()) == 0 {
		t.Error("brokered call should have been audited")
	}
}

func TestSandbox_DeniedHostCallRaises(t *testing.T) {
	// http_get is granted but the host is not allowlisted -> broker denies ->
	// the script raises -> Run reports a runtime error.
	rec := &audit.MemoryRecorder{}
	g := NewLuaGlue(brokerWithHTTP(rec, "x"))
	grant := &capability.GrantContext{Run: "r1", Granted: []capability.Capability{
		{Kind: capability.HTTPGet, Hosts: []string{"allowed.com"}},
	}}

	_, err := g.Run(context.Background(), `return http_get("https://evil.com")`, nil, grant, time.Second)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("expected a capability-denied runtime error, got %v", err)
	}
}

func TestSandbox_CallTool(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	b := capability.NewBroker(rec, func(_ context.Context, name string, input map[string]any) (string, error) {
		return "tool:" + name, nil
	})
	g := NewLuaGlue(b)
	grant := &capability.GrantContext{Run: "r1", Granted: []capability.Capability{
		{Kind: capability.CallTool, Tools: []string{"weather"}},
	}}

	out, err := g.Run(context.Background(), `return call_tool("weather", {city = "NYC"})`, nil, grant, time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "tool:weather" {
		t.Errorf("got %q, want tool:weather", out)
	}
}
