package capability

import (
	"context"
	"errors"
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

// capturingHTTP returns a client whose transport records the last request it saw, so a test
// can assert what the broker injected (header/query) without a live server.
func capturingHTTP(seen **http.Request) *http.Client {
	return &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		*seen = r
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
}

// TestBrokerHTTPGet_SecretInjection covers the three secret placements/failure modes: a
// header secret and a query secret reach the request with the resolved value, while the value
// never appears in the audit log (only the name); a named-but-unresolvable secret and a
// missing secret store both deny (fail closed).
func TestBrokerHTTPGet_SecretInjection(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		rec := &audit.MemoryRecorder{}
		b := NewBroker(rec, nil)
		var seen *http.Request
		b.HTTP = capturingHTTP(&seen)
		b.Secrets = func(name string) (string, bool) {
			if name == "scrapingant" {
				return "SECRETVAL", true
			}
			return "", false
		}
		grant := &GrantContext{Run: "r1", Granted: []Capability{
			{Kind: HTTPGet, Hosts: []string{"api.scrapingant.com"}, Secret: "scrapingant", SecretIn: "header:x-api-key"},
		}}
		if _, err := b.HTTPGet(context.Background(), grant, "https://api.scrapingant.com/v2/general?url=x"); err != nil {
			t.Fatalf("HTTPGet: %v", err)
		}
		if got := seen.Header.Get("x-api-key"); got != "SECRETVAL" {
			t.Fatalf("injected header = %q, want SECRETVAL", got)
		}
		// The audit summary names the secret but never carries its value.
		ev := lastEvent(t, rec)
		arg, _ := ev.Fields["arg"].(string)
		if !strings.Contains(arg, "secret:scrapingant") {
			t.Errorf("audit arg %q should name the secret", arg)
		}
		if strings.Contains(arg, "SECRETVAL") {
			t.Errorf("audit arg %q leaked the secret value", arg)
		}
	})

	t.Run("bearer", func(t *testing.T) {
		b := NewBroker(&audit.MemoryRecorder{}, nil)
		var seen *http.Request
		b.HTTP = capturingHTTP(&seen)
		b.Secrets = func(string) (string, bool) { return "TOKEN", true }
		grant := &GrantContext{Run: "r1", Granted: []Capability{
			{Kind: HTTPGet, Hosts: []string{"api.svc.com"}, Secret: "svc", SecretIn: "bearer"},
		}}
		if _, err := b.HTTPGet(context.Background(), grant, "https://api.svc.com/x"); err != nil {
			t.Fatalf("HTTPGet: %v", err)
		}
		if got := seen.Header.Get("Authorization"); got != "Bearer TOKEN" {
			t.Fatalf("Authorization header = %q, want \"Bearer TOKEN\"", got)
		}
	})

	t.Run("query", func(t *testing.T) {
		b := NewBroker(&audit.MemoryRecorder{}, nil)
		var seen *http.Request
		b.HTTP = capturingHTTP(&seen)
		b.Secrets = func(string) (string, bool) { return "QSECRET", true }
		grant := &GrantContext{Run: "r1", Granted: []Capability{
			{Kind: HTTPGet, Hosts: []string{"api.scrapingant.com"}, Secret: "scrapingant", SecretIn: "query:x-api-key"},
		}}
		if _, err := b.HTTPGet(context.Background(), grant, "https://api.scrapingant.com/v2/general?url=x"); err != nil {
			t.Fatalf("HTTPGet: %v", err)
		}
		if got := seen.URL.Query().Get("x-api-key"); got != "QSECRET" {
			t.Fatalf("injected query param = %q, want QSECRET", got)
		}
		if got := seen.URL.Query().Get("url"); got != "x" {
			t.Fatalf("existing query param url = %q, want it preserved", got)
		}
	})

	t.Run("unknown secret denies", func(t *testing.T) {
		b := NewBroker(&audit.MemoryRecorder{}, nil)
		b.HTTP = fakeHTTP("ok")
		b.Secrets = func(string) (string, bool) { return "", false }
		grant := &GrantContext{Run: "r1", Granted: []Capability{
			{Kind: HTTPGet, Hosts: []string{"h.com"}, Secret: "missing", SecretIn: "header:k"},
		}}
		if _, err := b.HTTPGet(context.Background(), grant, "https://h.com/x"); err == nil {
			t.Fatal("unknown secret should deny")
		}
	})

	t.Run("no secret store denies", func(t *testing.T) {
		b := NewBroker(&audit.MemoryRecorder{}, nil) // Secrets nil
		b.HTTP = fakeHTTP("ok")
		grant := &GrantContext{Run: "r1", Granted: []Capability{
			{Kind: HTTPGet, Hosts: []string{"h.com"}, Secret: "s", SecretIn: "header:k"},
		}}
		if _, err := b.HTTPGet(context.Background(), grant, "https://h.com/x"); err == nil {
			t.Fatal("a secret-bearing cap with no secret store should deny (fail closed)")
		}
	})
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

func TestBrokerHTTPGet_FailuresAreNotDenials(t *testing.T) {
	grant := &GrantContext{Run: "run-failed", Granted: []Capability{
		{Kind: HTTPGet, Hosts: []string{"example.com"}},
	}}

	t.Run("transport", func(t *testing.T) {
		rec := &audit.MemoryRecorder{}
		b := NewBroker(rec, nil)
		b.HTTP = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network unavailable")
		})}
		if _, err := b.HTTPGet(context.Background(), grant, "https://example.com/private?q=secret"); err == nil {
			t.Fatal("transport failure should be returned")
		}
		e := lastEvent(t, rec)
		if e.Type != audit.EventCapabilityFailed || e.Run != "run-failed" {
			t.Fatalf("event = %+v, want failed event attributed to run-failed", e)
		}
		if e.Fields["arg"] != "example.com" || e.Fields["error_class"] != "transport" {
			t.Fatalf("fields = %+v, want host-only transport classification", e.Fields)
		}
	})

	t.Run("http status", func(t *testing.T) {
		rec := &audit.MemoryRecorder{}
		b := NewBroker(rec, nil)
		b.HTTP = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("try later")),
				Header:     make(http.Header),
			}, nil
		})}
		out, err := b.HTTPGet(context.Background(), grant, "https://example.com/x")
		if err != nil || out != "try later" {
			t.Fatalf("HTTPGet preserved response: out=%q err=%v", out, err)
		}
		e := lastEvent(t, rec)
		if e.Type != audit.EventCapabilityFailed || e.Fields["status"] != http.StatusServiceUnavailable {
			t.Fatalf("event = %+v, want failed event with status 503", e)
		}
	})
}

// TestBrokerHTTPGet_MaxBytesCap proves the configurable body cap truncates a large response;
// the default (unset MaxHTTPBytes) applies when zero.
func TestBrokerHTTPGet_MaxBytesCap(t *testing.T) {
	b := NewBroker(&audit.MemoryRecorder{}, nil)
	b.HTTP = fakeHTTP("0123456789") // 10-byte body
	b.MaxHTTPBytes = 4              // cap below the body length

	grant := &GrantContext{Run: "r1", Granted: []Capability{
		{Kind: HTTPGet, Hosts: []string{"example.com"}},
	}}
	out, err := b.HTTPGet(context.Background(), grant, "https://example.com/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if out != "0123" {
		t.Fatalf("body = %q, want it capped to 4 bytes (%q)", out, "0123")
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
	if e := lastEvent(t, rec); e.Type != audit.EventCapabilityDenied {
		t.Fatalf("redirect policy refusal event = %q, want denied", e.Type)
	}
}

func TestBrokerExecutionFailuresAreAudited(t *testing.T) {
	t.Run("read file", func(t *testing.T) {
		dir := t.TempDir()
		rec := &audit.MemoryRecorder{}
		b := NewBroker(rec, nil)
		grant := &GrantContext{Run: "r", Granted: []Capability{{Kind: ReadFile, PathPrefix: dir}}}
		if _, err := b.ReadFile(grant, filepath.Join(dir, "missing")); err == nil {
			t.Fatal("missing file should fail")
		}
		if e := lastEvent(t, rec); e.Type != audit.EventCapabilityFailed || e.Fields["error_class"] != "filesystem_read" {
			t.Fatalf("event = %+v, want filesystem_read failure", e)
		}
	})

	t.Run("called tool", func(t *testing.T) {
		rec := &audit.MemoryRecorder{}
		b := NewBroker(rec, func(context.Context, string, map[string]any) (string, error) {
			return "", errors.New("tool broke")
		})
		grant := &GrantContext{Run: "r", Granted: []Capability{{Kind: CallTool, Tools: []string{"broken"}}}}
		if _, err := b.CallTool(context.Background(), grant, "broken", nil); err == nil {
			t.Fatal("tool failure should be returned")
		}
		if e := lastEvent(t, rec); e.Type != audit.EventCapabilityFailed || e.Fields["error_class"] != "tool_error" {
			t.Fatalf("event = %+v, want tool_error failure", e)
		}
	})
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
