package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
)

// stubAnt stands in for ScrapingAnt, capturing the request the tool made so the tests can
// assert on how the key and options were sent.
type stubAnt struct {
	srv    *httptest.Server
	gotKey string
	gotQry map[string]string
	gotURL string
}

func newStubAnt(t *testing.T, status int, body string) *stubAnt {
	t.Helper()
	s := &stubAnt{gotQry: map[string]string{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.gotKey = r.Header.Get("x-api-key")
		s.gotURL = r.URL.String()
		for k := range r.URL.Query() {
			s.gotQry[k] = r.URL.Query().Get(k)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func newScraper(t *testing.T, ant *stubAnt, rec audit.Recorder) scraper {
	t.Helper()
	return scraper{
		secrets:  func(n string) (string, bool) { return "tok-123", n == ScrapeSecretName },
		audit:    rec,
		runID:    "run-1",
		endpoint: ant.srv.URL,
	}
}

const scrapePage = `<html><body><script>junk()</script><p>Hello from a rendered page.</p></body></html>`

func TestScrape_SendsKeyAsHeaderAndExtractsText(t *testing.T) {
	ant := newStubAnt(t, http.StatusOK, scrapePage)
	s := newScraper(t, ant, nil)

	out, err := s.run(context.Background(), map[string]any{"url": "https://example.com/a"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The credential travels in a header, never the query string — an error path that echoes
	// the request URL then cannot leak it.
	if ant.gotKey != "tok-123" {
		t.Errorf("x-api-key header = %q, want tok-123", ant.gotKey)
	}
	if strings.Contains(ant.gotURL, "tok-123") {
		t.Errorf("API key leaked into the request URL: %s", ant.gotURL)
	}
	if ant.gotQry["url"] != "https://example.com/a" {
		t.Errorf("target url param = %q", ant.gotQry["url"])
	}
	// Browser rendering is the default: it is the reason to use this tool over web_fetch.
	if ant.gotQry["browser"] != "true" {
		t.Errorf("browser param = %q, want true", ant.gotQry["browser"])
	}

	if !strings.Contains(out, "Hello from a rendered page.") {
		t.Errorf("output missing page text: %q", out)
	}
	if strings.Contains(out, "junk()") {
		t.Errorf("script content should be stripped: %q", out)
	}
	// Scraped pages are the highest-risk injection surface — they must be fenced like any
	// other web content.
	if !strings.Contains(out, untrustedBegin) || !strings.Contains(out, untrustedEnd) {
		t.Errorf("scraped content is not fenced as untrusted: %q", out)
	}
}

func TestScrape_OptionsMapToAPIParams(t *testing.T) {
	ant := newStubAnt(t, http.StatusOK, scrapePage)
	s := newScraper(t, ant, nil)

	if _, err := s.run(context.Background(), map[string]any{
		"url":               "https://example.com",
		"render_js":         false,
		"proxy_country":     "de",
		"wait_for_selector": "",
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if ant.gotQry["browser"] != "false" {
		t.Errorf("browser = %q, want false", ant.gotQry["browser"])
	}
	if ant.gotQry["proxy_country"] != "DE" {
		t.Errorf("proxy_country = %q, want DE (upper-cased)", ant.gotQry["proxy_country"])
	}
	if _, set := ant.gotQry["wait_for_selector"]; set {
		t.Error("blank wait_for_selector should not be sent")
	}
}

// Models often emit booleans as strings; a stringly-typed "false" must not silently become
// the default of true, since that is the 10x-cost option.
func TestScrape_AcceptsStringBooleans(t *testing.T) {
	ant := newStubAnt(t, http.StatusOK, scrapePage)
	s := newScraper(t, ant, nil)

	if _, err := s.run(context.Background(), map[string]any{"url": "https://example.com", "render_js": "false"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if ant.gotQry["browser"] != "false" {
		t.Errorf("browser = %q, want false", ant.gotQry["browser"])
	}
}

func TestScrape_ReturnHTMLSkipsExtraction(t *testing.T) {
	ant := newStubAnt(t, http.StatusOK, scrapePage)
	s := newScraper(t, ant, nil)

	out, err := s.run(context.Background(), map[string]any{"url": "https://example.com", "return_html": true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "<p>Hello from a rendered page.</p>") {
		t.Errorf("raw HTML should be preserved: %q", out)
	}
}

func TestScrape_WaitForSelectorWithoutBrowserRejected(t *testing.T) {
	ant := newStubAnt(t, http.StatusOK, scrapePage)
	s := newScraper(t, ant, nil)

	out, err := s.run(context.Background(), map[string]any{
		"url": "https://example.com", "render_js": false, "wait_for_selector": "#main",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "requires render_js") {
		t.Errorf("want an explanation of the impossible combination, got %q", out)
	}
	if ant.gotKey != "" {
		t.Error("the request should not have been sent (and no credit spent)")
	}
}

// Every non-200 must come back as actionable content, not a hard error, and must tell the
// model whether retrying is worthwhile — retry loops here cost real money.
func TestScrape_APIErrorsAreActionable(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		wantParts []string
	}{
		{http.StatusForbidden, `{"detail":"bad key"}`, []string{"403", "key was rejected", "Do not retry", "bad key"}},
		{http.StatusTooManyRequests, `{"detail":"no credits"}`, []string{"429", "out of credits", "Do not retry"}},
		{http.StatusLocked, `{"detail":"blocked"}`, []string{"423", "blocked the request"}},
		{http.StatusConflict, ``, []string{"409", "concurrency limit"}},
		{http.StatusInternalServerError, ``, []string{"500", "server-side error"}},
	}
	for _, tc := range cases {
		ant := newStubAnt(t, tc.status, tc.body)
		s := newScraper(t, ant, nil)
		out, err := s.run(context.Background(), map[string]any{"url": "https://example.com"})
		if err != nil {
			t.Fatalf("status %d: run returned a hard error: %v", tc.status, err)
		}
		for _, want := range tc.wantParts {
			if !strings.Contains(out, want) {
				t.Errorf("status %d: message %q missing %q", tc.status, out, want)
			}
		}
	}
}

func TestScrape_BadURLRejectedBeforeSpending(t *testing.T) {
	ant := newStubAnt(t, http.StatusOK, scrapePage)
	s := newScraper(t, ant, nil)

	for _, bad := range []string{"", "   ", "not-a-url", "ftp://example.com", "file:///etc/passwd"} {
		if _, err := s.run(context.Background(), map[string]any{"url": bad}); err == nil {
			t.Errorf("url %q should be rejected", bad)
		}
	}
	if ant.gotKey != "" {
		t.Error("no request should have been sent for an invalid URL")
	}
}

// A paid call must leave an audit line so spend is reconstructable — carrying the host and
// the cost driver (browser), but never the key.
func TestScrape_AuditsCallWithoutLeakingKey(t *testing.T) {
	ant := newStubAnt(t, http.StatusOK, scrapePage)
	rec := &audit.MemoryRecorder{}
	s := newScraper(t, ant, rec)

	if _, err := s.run(context.Background(), map[string]any{"url": "https://example.com/x"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	events := rec.Snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d audit events, want 1", len(events))
	}
	e := events[0]
	if e.Type != audit.EventCapabilityExercised {
		t.Errorf("type = %q, want %q", e.Type, audit.EventCapabilityExercised)
	}
	if e.Run != "run-1" {
		t.Errorf("run = %q, want run-1", e.Run)
	}
	arg, _ := e.Fields["arg"].(string)
	if !strings.Contains(arg, "example.com") || !strings.Contains(arg, "[browser]") {
		t.Errorf("arg = %q, want the host and the browser marker", arg)
	}
	if strings.Contains(arg, "tok-123") {
		t.Errorf("audit leaked the API key: %q", arg)
	}

	// A failed call is audited as denied, so a burst of failures is visible too.
	antFail := newStubAnt(t, http.StatusLocked, "")
	rec2 := &audit.MemoryRecorder{}
	s2 := newScraper(t, antFail, rec2)
	if _, err := s2.run(context.Background(), map[string]any{"url": "https://example.com/x"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rec2.Snapshot(); len(got) != 1 || got[0].Type != audit.EventCapabilityDenied {
		t.Errorf("failed scrape should record one %q event, got %+v", audit.EventCapabilityDenied, got)
	}
}

// The tool is omitted entirely when no token is stored: a paid tool the model can only ever
// fail with wastes turns discovering that.
func TestNewScrape_OmittedWithoutSecret(t *testing.T) {
	if _, ok := NewScrape(nil, nil, ""); ok {
		t.Error("nil resolver should not register the tool")
	}
	empty := func(string) (string, bool) { return "", false }
	if _, ok := NewScrape(empty, nil, ""); ok {
		t.Error("missing secret should not register the tool")
	}
	blank := func(string) (string, bool) { return "", true }
	if _, ok := NewScrape(blank, nil, ""); ok {
		t.Error("empty-valued secret should not register the tool")
	}
	present := func(n string) (string, bool) { return "tok", n == ScrapeSecretName }
	tool, ok := NewScrape(present, nil, "")
	if !ok {
		t.Fatal("stored secret should register the tool")
	}
	if tool.Name != "scrape" {
		t.Errorf("tool name = %q, want scrape", tool.Name)
	}
	// The description must steer the model to the free path first — this is the guardrail
	// against it reaching for the paid tool by habit.
	if !strings.Contains(tool.Description, "web_fetch") {
		t.Error("description should tell the model to try web_fetch first")
	}
}

// Mid-run removal of the secret fails closed with an explanation rather than a bare error.
func TestScrape_SecretRemovedMidRun(t *testing.T) {
	ant := newStubAnt(t, http.StatusOK, scrapePage)
	s := newScraper(t, ant, nil)
	s.secrets = func(string) (string, bool) { return "", false }

	out, err := s.run(context.Background(), map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "set-secret") {
		t.Errorf("want operator guidance, got %q", out)
	}
	if ant.gotKey != "" {
		t.Error("no request should have been sent")
	}
}
