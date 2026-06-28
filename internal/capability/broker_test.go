package capability

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeHTTP(body string) *http.Client {
	return &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
}

func lastEvent(t *testing.T, rec *audit.MemoryRecorder) audit.Event {
	t.Helper()
	ev := rec.Snapshot()
	if len(ev) == 0 {
		t.Fatal("no audit events recorded")
	}
	return ev[len(ev)-1]
}

func TestBrokerHTTPGet_AllowedAndDenied(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	b := NewBroker(rec, nil)
	b.HTTP = fakeHTTP("page-body")

	grant := &GrantContext{Run: "r1", Granted: []Capability{
		{Kind: HTTPGet, Hosts: []string{"example.com"}},
	}}

	out, err := b.HTTPGet(context.Background(), grant, "https://example.com/x")
	if err != nil || out != "page-body" {
		t.Fatalf("allowed GET: out=%q err=%v", out, err)
	}
	if e := lastEvent(t, rec); e.Type != audit.EventCapabilityExercised {
		t.Errorf("expected exercised event, got %q", e.Type)
	}

	if _, err := b.HTTPGet(context.Background(), grant, "https://evil.com/x"); err == nil {
		t.Fatal("GET to non-allowlisted host should be denied")
	}
	if e := lastEvent(t, rec); e.Type != audit.EventCapabilityDenied {
		t.Errorf("expected denied event, got %q", e.Type)
	}
}

func TestBrokerHTTPGet_RedirectToDisallowedHostBlocked(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	b := NewBroker(rec, nil)
	// Allowed host 302s to a disallowed one; the broker must not follow it.
	b.HTTP = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "example.com" {
			h := make(http.Header)
			h.Set("Location", "https://evil.com/pwn")
			return &http.Response{StatusCode: 302, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret"))}, nil
	})}

	grant := &GrantContext{Run: "r1", Granted: []Capability{
		{Kind: HTTPGet, Hosts: []string{"example.com"}},
	}}

	out, err := b.HTTPGet(context.Background(), grant, "https://example.com/x")
	if err == nil {
		t.Fatalf("redirect to disallowed host should fail, got out=%q", out)
	}
	if strings.Contains(out, "secret") {
		t.Fatal("broker followed redirect to a disallowed host")
	}
}

func TestBrokerHTTPGet_NotGranted(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	b := NewBroker(rec, nil)
	b.HTTP = fakeHTTP("nope")
	grant := &GrantContext{Run: "r1"} // no capabilities

	if _, err := b.HTTPGet(context.Background(), grant, "https://example.com"); err == nil {
		t.Fatal("ungranted http_get must be denied")
	}
}

func TestBrokerReadWriteFile(t *testing.T) {
	dir := t.TempDir()
	rec := &audit.MemoryRecorder{}
	b := NewBroker(rec, nil)
	grant := &GrantContext{Run: "r1", Granted: []Capability{
		{Kind: ReadFile, PathPrefix: dir},
		{Kind: WriteFile, PathPrefix: dir},
	}}

	target := filepath.Join(dir, "note.txt")
	if err := b.WriteFile(grant, target, "hi"); err != nil {
		t.Fatalf("write within prefix: %v", err)
	}
	got, err := b.ReadFile(grant, target)
	if err != nil || got != "hi" {
		t.Fatalf("read within prefix: got=%q err=%v", got, err)
	}

	// Outside the prefix is denied (and not created).
	outside := filepath.Join(os.TempDir(), "escape-xyz.txt")
	if err := b.WriteFile(grant, outside, "x"); err == nil {
		t.Error("write outside prefix must be denied")
	}
	if _, err := b.ReadFile(grant, "/etc/hostname"); err == nil {
		t.Error("read outside prefix must be denied")
	}
}

func TestBrokerCallTool(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	called := ""
	caller := func(_ context.Context, name string, input map[string]any) (string, error) {
		called = name
		return "ran:" + name, nil
	}
	b := NewBroker(rec, caller)
	grant := &GrantContext{Run: "r1", Granted: []Capability{
		{Kind: CallTool, Tools: []string{"echo"}},
	}}

	out, err := b.CallTool(context.Background(), grant, "echo", map[string]any{"x": 1})
	if err != nil || out != "ran:echo" || called != "echo" {
		t.Fatalf("allowed call_tool: out=%q called=%q err=%v", out, called, err)
	}
	if _, err := b.CallTool(context.Background(), grant, "shell", nil); err == nil {
		t.Error("call to non-allowlisted tool must be denied")
	}
}

func TestBrokerCallTool_TrustedBuiltinNotReachable(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	ran := ""
	b := NewBroker(rec, func(_ context.Context, name string, _ map[string]any) (string, error) {
		ran = name
		return "ran:" + name, nil
	})
	b.Trusted = func(name string) bool { return name == "shell" } // shell has ambient authority

	star := &GrantContext{Run: "r", Granted: []Capability{{Kind: CallTool, Tools: []string{"*"}}}}

	// A "*" grant reaches normal (sandboxed) tools...
	if out, err := b.CallTool(context.Background(), star, "summarize", nil); err != nil || out != "ran:summarize" {
		t.Fatalf("wildcard should reach a sandboxed tool: out=%q err=%v", out, err)
	}
	// ...but never a trusted built-in, even when it is exposed.
	b.Exposed = func(name string) bool { return name == "shell" }
	if _, err := b.CallTool(context.Background(), star, "shell", nil); err == nil {
		t.Error("wildcard grant must not reach a trusted built-in")
	}

	named := &GrantContext{Run: "r", Granted: []Capability{{Kind: CallTool, Tools: []string{"shell"}}}}

	// Named explicitly but NOT exposed → denied.
	b.Exposed = nil
	if _, err := b.CallTool(context.Background(), named, "shell", nil); err == nil {
		t.Error("explicit grant to an unexposed built-in must be denied")
	}

	// Named explicitly AND exposed → allowed.
	ran = ""
	b.Exposed = func(name string) bool { return name == "shell" }
	if out, err := b.CallTool(context.Background(), named, "shell", nil); err != nil || ran != "shell" || out != "ran:shell" {
		t.Fatalf("explicit + exposed built-in should be callable: out=%q ran=%q err=%v", out, ran, err)
	}
}

func TestBrokerClockAndRandomGated(t *testing.T) {
	rec := &audit.MemoryRecorder{}
	b := NewBroker(rec, nil)

	none := &GrantContext{Run: "r1"}
	if _, err := b.Now(none); err == nil {
		t.Error("Now without Clock grant must be denied")
	}
	if _, err := b.RandomBytes(none, 8); err == nil {
		t.Error("RandomBytes without Random grant must be denied")
	}

	full := &GrantContext{Run: "r1", Granted: []Capability{{Kind: Clock}, {Kind: Random}}}
	if _, err := b.Now(full); err != nil {
		t.Errorf("Now with grant: %v", err)
	}
	buf, err := b.RandomBytes(full, 8)
	if err != nil || len(buf) != 8 {
		t.Errorf("RandomBytes with grant: len=%d err=%v", len(buf), err)
	}
}
