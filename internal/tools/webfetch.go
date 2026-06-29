package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// maxFetchChars limits how many characters are returned to avoid flooding the LLM context.
const maxFetchChars = 10_000

var WebFetch = Tool{
	Name:        "web_fetch",
	Description: "Fetch the text content of a web page at a given URL. Returns clean text with HTML tags removed.",
	Parameters: map[string]any{
		"url": map[string]any{
			"type":        "string",
			"description": "The URL to fetch",
		},
	},
	Run: func(ctx context.Context, args map[string]any) (string, error) {
		rawURL, ok := args["url"].(string)
		if !ok {
			return "", fmt.Errorf("url must be a string")
		}
		return fetchPage(ctx, rawURL)
	},
}

func fetchPage(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	// Only attempt HTML parsing for HTML responses
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		return fmt.Sprintf("non-HTML content (%s) — cannot extract text", contentType), nil
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Remove elements that produce noise or are not content
	doc.Find("script, style, nav, footer, header, aside, noscript").Remove()

	text := cleanText(doc.Find("body").Text())

	if len(text) > maxFetchChars {
		text = text[:maxFetchChars] + fmt.Sprintf("\n\n[truncated — %d chars total]", len(text))
	}

	return wrapUntrusted(rawURL, text), nil
}

// cleanText collapses repeated whitespace and removes blank lines
func cleanText(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
