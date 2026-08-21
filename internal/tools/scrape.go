package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-agent-go-play/internal/audit"
)

// ScrapeSecretName is the secret the scrape tool resolves its API key from. The operator
// stores it with `agent config set-secret scrapingant <token>` or, on a deployment, as
// AI_AGENT_SECRET_SCRAPINGANT — the same store authored tools reference by name, so there
// is one place secrets live (docs/adr/external-apis.md §2).
const ScrapeSecretName = "scrapingant"

// scrapeEndpoint is ScrapingAnt's general-purpose extraction API.
const scrapeEndpoint = "https://api.scrapingant.com/v2/general"

// scrapeTimeout bounds one scrape. Browser rendering runs a real headless Chrome on
// ScrapingAnt's side and routinely takes tens of seconds, so this is far longer than a
// plain web_fetch would ever need.
const scrapeTimeout = 120 * time.Second

// NewScrape returns the scrape tool, which fetches a page through ScrapingAnt (headless
// browser + rotating proxies) for sites plain web_fetch cannot read: JS-rendered pages and
// anti-bot walls.
//
// It reports false when no ScrapingAnt token is stored, and the caller omits the tool
// entirely — a paid tool the model can only ever fail with is worse than no tool, since it
// burns turns discovering that. The key is read host-side at call time and sent as a
// header, so it never reaches the model, the arguments, the audit log, or a URL that might
// be echoed back in an error.
//
// Unlike web_fetch this costs the operator real money per call, which is why it is a
// separate tool rather than a flag: the model picks the paid path deliberately and every
// call is one auditable line.
func NewScrape(secrets func(name string) (string, bool), rec audit.Recorder, runID string) (Tool, bool) {
	if secrets == nil {
		return Tool{}, false
	}
	if v, ok := secrets(ScrapeSecretName); !ok || v == "" {
		return Tool{}, false
	}
	s := scraper{secrets: secrets, audit: rec, runID: runID, endpoint: scrapeEndpoint}
	return Tool{
		Name: "scrape",
		Description: "Fetch a web page through ScrapingAnt, a paid scraping service that renders JavaScript " +
			"in a real browser and routes through rotating proxies. Use it ONLY when web_fetch has failed or " +
			"cannot work: a page whose content is rendered client-side (web_fetch returns an empty or " +
			"skeleton page), or one that blocks plain requests (403/429, a CAPTCHA or bot wall). " +
			"Every call costs the operator credits, so try web_fetch first and do not retry a scrape in a " +
			"loop — if it fails twice, report why instead of burning credits.",
		Parameters: map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The URL to fetch",
			},
			"render_js": map[string]any{
				"type": "boolean",
				"description": "Render the page in a headless browser (default true). This is the reason to " +
					"use scrape at all, but costs ~10x the credits of a plain proxied fetch — pass false when " +
					"you only need to get past a bot wall and the content is in the raw HTML.",
			},
			"return_html": map[string]any{
				"type": "boolean",
				"description": "Return raw HTML instead of extracted text (default false). Use it when you " +
					"need markup — links, attributes, structure — rather than prose.",
			},
			"proxy_country": map[string]any{
				"type": "string",
				"description": "Two-letter country code to proxy from (e.g. \"US\", \"DE\"), for " +
					"geo-restricted or geo-varying pages. Omit unless the page needs it.",
			},
			"wait_for_selector": map[string]any{
				"type": "string",
				"description": "CSS selector to wait for before capturing, when content loads late. " +
					"Requires render_js. Omit unless a first attempt came back missing the content.",
			},
		},
		Required: []string{"url"},
		Run:      s.run,
	}, true
}

type scraper struct {
	secrets  func(name string) (string, bool)
	audit    audit.Recorder
	runID    string
	endpoint string // ScrapingAnt's API; a field so tests can point it at a stub server
}

func (s scraper) run(ctx context.Context, args map[string]any) (string, error) {
	target, ok := args["url"].(string)
	if !ok || strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("url must be a non-empty string")
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("url must be an absolute http(s) URL, got %q", target)
	}

	// Fail closed and explain: the tool is only registered when the key exists, so this
	// means it was removed or rotated away mid-run.
	key, ok := s.secrets(ScrapeSecretName)
	if !ok || key == "" {
		return fmt.Sprintf("scrape unavailable: no %q secret is stored; the operator adds it with "+
			"`agent config set-secret %s <token>`", ScrapeSecretName, ScrapeSecretName), nil
	}

	renderJS := boolArg(args, "render_js", true)
	q := url.Values{}
	q.Set("url", target)
	q.Set("browser", strconv.FormatBool(renderJS))
	if c, _ := args["proxy_country"].(string); strings.TrimSpace(c) != "" {
		q.Set("proxy_country", strings.ToUpper(strings.TrimSpace(c)))
	}
	if sel, _ := args["wait_for_selector"].(string); strings.TrimSpace(sel) != "" {
		if !renderJS {
			return "wait_for_selector requires render_js to be true (there is no browser to wait in)", nil
		}
		q.Set("wait_for_selector", sel)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	// Header, not the query string: an error path that echoes the request URL can never
	// leak the credential.
	req.Header.Set("x-api-key", key)

	client := &http.Client{Timeout: scrapeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		s.record(u.Host, renderJS, audit.CapabilityFailed, map[string]any{"error_class": "transport"})
		// Errors come back as content (like shell) so the model can adapt rather than abort.
		return fmt.Sprintf("scrape failed: %v", err), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScrapeBytes))
	if err != nil {
		s.record(u.Host, renderJS, audit.CapabilityFailed, map[string]any{"error_class": "response_read"})
		return fmt.Sprintf("scrape failed reading response: %v", err), nil
	}

	if resp.StatusCode != http.StatusOK {
		s.record(u.Host, renderJS, audit.CapabilityFailed, map[string]any{
			"error_class": "http_status",
			"status":      resp.StatusCode,
		})
		return scrapeError(resp.StatusCode, body), nil
	}

	s.record(u.Host, renderJS, audit.CapabilityExercised, nil)

	out := string(body)
	if !boolArg(args, "return_html", false) {
		text, err := extractText(strings.NewReader(out))
		if err != nil {
			return fmt.Sprintf("scrape fetched the page but could not parse its HTML: %v", err), nil
		}
		out = text
	}
	return wrapUntrusted(target, truncateForContext(out)), nil
}

// maxScrapeBytes caps the response read into memory. Rendered pages are much larger than
// the text the model ends up seeing; extraction and truncateForContext shrink it further.
const maxScrapeBytes = 5 << 20 // 5 MiB

// record logs one paid call so spend is reconstructable from the audit log. It records the
// target host and whether a browser was used (the cost driver) — never the API key, and
// never the full URL, matching how the broker summarizes a secret-bearing fetch.
func (s scraper) record(host string, renderJS bool, outcome audit.CapabilityOutcome, extra map[string]any) {
	if s.audit == nil {
		return
	}
	summary := host + " [secret:" + ScrapeSecretName + "]"
	if renderJS {
		summary += " [browser]"
	}
	s.audit.Record(audit.NewCapabilityEvent(outcome, s.runID, "scrape", summary, extra))
}

// scrapeError turns a non-200 into a message the model can act on. ScrapingAnt returns a
// JSON body with a "detail" field explaining the failure, which is more informative than
// any mapping we could hardcode, so it is passed through alongside the interpretation.
func scrapeError(status int, body []byte) string {
	var hint string
	switch status {
	case http.StatusForbidden:
		hint = "the ScrapingAnt API key was rejected — the operator should check the `" +
			ScrapeSecretName + "` secret. Do not retry."
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		hint = "ScrapingAnt rejected the request parameters. Check the URL and options; do not retry unchanged."
	case http.StatusConflict:
		hint = "ScrapingAnt's concurrency limit was reached. Retry once after a short pause."
	case http.StatusLocked:
		hint = "the target site blocked the request. Retrying rarely helps; try render_js=true " +
			"or a proxy_country, or report that the site is not scrapable."
	case http.StatusTooManyRequests:
		hint = "the ScrapingAnt account is out of credits or rate-limited. Do not retry — tell the user."
	case http.StatusNotFound:
		hint = "the target page does not exist (404)."
	default:
		if status >= 500 {
			hint = "ScrapingAnt had a server-side error. Retry at most once."
		} else {
			hint = "unexpected response from ScrapingAnt."
		}
	}
	detail := strings.TrimSpace(string(body))
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}
	msg := fmt.Sprintf("scrape failed: ScrapingAnt returned %d — %s", status, hint)
	if detail != "" {
		msg += "\nresponse: " + detail
	}
	return msg
}

// boolArg reads an optional boolean argument, tolerating the string forms some models emit.
func boolArg(args map[string]any, name string, def bool) bool {
	switch v := args[name].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
