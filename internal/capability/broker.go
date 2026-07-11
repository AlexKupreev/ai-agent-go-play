package capability

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"ai-agent-go-play/internal/audit"
)

// maxHTTPBytes caps a single brokered HTTP response to keep memory bounded.
const maxHTTPBytes = 1 << 20 // 1 MiB

// ToolCaller invokes another registered tool by name. Injected by the host so
// the broker need not depend on the tool registry (avoids an import cycle).
type ToolCaller func(ctx context.Context, name string, input map[string]any) (string, error)

// Broker is the single path to any effect. Each method: check the grant + the
// argument allowlist, execute, then audit. A denied call is audited too.
type Broker struct {
	HTTP    *http.Client
	Audit   audit.Recorder
	Tools   ToolCaller
	Clock   func() time.Time
	RandSrc io.Reader

	// MaxHTTPBytes caps a single brokered HTTP response body. 0 ⇒ maxHTTPBytes (1 MiB).
	// Tunable so a data-heavy deployment can raise it without a rebuild.
	MaxHTTPBytes int64

	// Trusted reports whether a tool name runs with ambient authority — a
	// built-in like shell that is itself NOT sandboxed. call_tool from authored
	// code must not reach such a tool transitively, or the sandbox leaks: an
	// authored tool with a "*" grant would get shell. So a Trusted tool is
	// callable from the sandbox only when also Exposed AND named explicitly in
	// the grant (never via "*"). Nil ⇒ no tool is treated as trusted; the host
	// MUST populate this for any ambient-authority tool reachable via Tools.
	Trusted func(name string) bool
	// Exposed reports whether a trusted built-in has been deliberately opened to
	// the sandbox. Consulted only for Trusted names. Nil ⇒ none exposed.
	Exposed func(name string) bool

	// Secrets resolves a named secret (a capability's Secret field) to its value, which
	// HTTPGet injects into the request host-side (a header or query param). The value never
	// enters the sandbox, the tool source, or the audit log — only the secret's name is
	// recorded. Nil ⇒ no secret store; a cap that names a secret is denied (fail closed).
	// See docs/adr/external-apis.md §2.
	Secrets func(name string) (string, bool)
}

func (b *Broker) trusted(name string) bool { return b.Trusted != nil && b.Trusted(name) }
func (b *Broker) exposed(name string) bool { return b.Exposed != nil && b.Exposed(name) }

// NewBroker builds a broker with sensible defaults; fields may be overridden.
func NewBroker(rec audit.Recorder, tools ToolCaller) *Broker {
	return &Broker{
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Audit:   rec,
		Tools:   tools,
		Clock:   time.Now,
		RandSrc: rand.Reader,
	}
}

func (b *Broker) record(g *GrantContext, kind Kind, summary string, allowed bool) {
	if b.Audit == nil {
		return
	}
	typ := audit.EventCapabilityExercised
	if !allowed {
		typ = audit.EventCapabilityDenied
	}
	run := ""
	if g != nil {
		run = g.Run
	}
	b.Audit.Record(audit.Event{
		Type:   typ,
		Run:    run,
		Fields: map[string]any{"capability": string(kind), "arg": summary},
	})
}

func denied(kind Kind, detail string) error {
	return fmt.Errorf("capability denied: %s (%s)", kind, detail)
}

// HTTPGet fetches a URL if the grant allows the host.
func (b *Broker) HTTPGet(ctx context.Context, g *GrantContext, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		b.record(g, HTTPGet, rawURL, false)
		return "", fmt.Errorf("http_get: invalid url %q", rawURL)
	}
	c, ok := g.find(HTTPGet)
	if !ok || !hostAllowed(c.Hosts, u.Hostname()) {
		b.record(g, HTTPGet, u.Host, false)
		return "", denied(HTTPGet, u.Host)
	}
	// The audit summary carries the secret's NAME (not value) when one is used, so the log
	// shows a credential was exercised without ever recording it.
	summary := u.Host
	if c.Secret != "" {
		summary += " [secret:" + c.Secret + "]"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	// Inject a named secret host-side, if the cap requests one. It is bounded to the same
	// host allowlist as the fetch (the request only reaches allowed hosts, and redirects are
	// re-checked below), so the credential can't be steered off the approved host. Fail
	// closed: a named-but-unresolvable secret denies the call rather than fetching without it.
	if c.Secret != "" {
		if b.Secrets == nil {
			b.record(g, HTTPGet, summary, false)
			return "", denied(HTTPGet, "secret "+c.Secret+" requested but no secret store is configured")
		}
		val, found := b.Secrets(c.Secret)
		if !found || val == "" {
			b.record(g, HTTPGet, summary, false)
			return "", denied(HTTPGet, "unknown or empty secret "+c.Secret)
		}
		where, key, prefix, perr := c.SecretPlacement()
		if perr != nil {
			b.record(g, HTTPGet, summary, false)
			return "", fmt.Errorf("http_get: %w", perr)
		}
		switch where {
		case "header":
			req.Header.Set(key, prefix+val)
		case "query":
			q := req.URL.Query()
			q.Set(key, prefix+val)
			req.URL.RawQuery = q.Encode()
		}
	}
	base := b.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	// Follow redirects only to hosts the grant still allows. Without this a
	// granted host could 30x to an internal/disallowed host (e.g. cloud metadata)
	// and the broker would fetch it — the allowlist is the entire boundary here,
	// so it must hold across every hop. Reuse the base Transport for pooling.
	client := &http.Client{
		Transport: base.Transport,
		Timeout:   base.Timeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if !hostAllowed(c.Hosts, r.URL.Hostname()) {
				return denied(HTTPGet, "redirect to "+r.URL.Host)
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	limit := b.MaxHTTPBytes
	if limit <= 0 {
		limit = maxHTTPBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", err
	}
	b.record(g, HTTPGet, summary, true)
	return string(body), nil
}

// ReadFile reads a file if the grant allows its path prefix.
func (b *Broker) ReadFile(g *GrantContext, path string) (string, error) {
	c, ok := g.find(ReadFile)
	if !ok || !pathAllowed(c.PathPrefix, path) {
		b.record(g, ReadFile, path, false)
		return "", denied(ReadFile, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	b.record(g, ReadFile, path, true)
	return string(data), nil
}

// WriteFile writes a file if the grant allows its path prefix.
func (b *Broker) WriteFile(g *GrantContext, path, content string) error {
	c, ok := g.find(WriteFile)
	if !ok || !pathAllowed(c.PathPrefix, path) {
		b.record(g, WriteFile, path, false)
		return denied(WriteFile, path)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return err
	}
	b.record(g, WriteFile, path, true)
	return nil
}

// CallTool invokes another tool if the grant allows its name. A trusted
// (ambient-authority) built-in is additionally gated: it is callable only when
// the host has Exposed it AND the grant names it directly — a "*" grant never
// reaches one, so call_tool cannot become a transitive sandbox escape.
func (b *Broker) CallTool(ctx context.Context, g *GrantContext, name string, input map[string]any) (string, error) {
	c, ok := g.find(CallTool)
	if !ok || !toolAllowed(c.Tools, name) {
		b.record(g, CallTool, name, false)
		return "", denied(CallTool, name)
	}
	// A trusted (ambient-authority) built-in is reachable from sandboxed code
	// only if the host explicitly exposed it AND the grant names it directly. A
	// "*" grant never escalates into one — that would make call_tool a
	// transitive sandbox escape into e.g. shell.
	if b.trusted(name) && (!b.exposed(name) || !toolNamed(c.Tools, name)) {
		b.record(g, CallTool, name, false)
		return "", denied(CallTool, name+": trusted built-in not callable from sandbox")
	}
	if b.Tools == nil {
		return "", fmt.Errorf("call_tool: no tool caller configured")
	}
	out, err := b.Tools(ctx, name, input)
	if err != nil {
		return "", err
	}
	b.record(g, CallTool, name, true)
	return out, nil
}

// Now returns the current time if the grant includes the Clock capability.
func (b *Broker) Now(g *GrantContext) (time.Time, error) {
	if !g.Has(Clock) {
		b.record(g, Clock, "", false)
		return time.Time{}, denied(Clock, "now")
	}
	clk := b.Clock
	if clk == nil {
		clk = time.Now
	}
	b.record(g, Clock, "", true)
	return clk(), nil
}

// RandomBytes returns n random bytes if the grant includes the Random capability.
func (b *Broker) RandomBytes(g *GrantContext, n int) ([]byte, error) {
	if !g.Has(Random) {
		b.record(g, Random, "", false)
		return nil, denied(Random, "random")
	}
	if n <= 0 || n > 4096 {
		return nil, fmt.Errorf("random: n out of range")
	}
	src := b.RandSrc
	if src == nil {
		src = rand.Reader
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(src, buf); err != nil {
		return nil, err
	}
	b.record(g, Random, "", true)
	return buf, nil
}
