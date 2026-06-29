package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var WebSearchDDG = Tool{
	Name:        "web_search",
	Description: "Search the web using DuckDuckGo. Returns titles, URLs, and snippets for the top results.",
	Parameters: map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "The search query",
		},
	},
	Run: func(ctx context.Context, args map[string]any) (string, error) {
		query, ok := args["query"].(string)
		if !ok {
			return "", fmt.Errorf("query must be a string")
		}
		return searchDDG(ctx, query, 10)
	},
}

func searchDDG(ctx context.Context, query string, maxResults int) (string, error) {
	reqURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	// Without these headers DDG returns a CAPTCHA or empty response
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DDG returned status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	var results []string
	count := 0

	doc.Find(".result").Each(func(i int, s *goquery.Selection) {
		if count >= maxResults {
			return
		}

		title := strings.TrimSpace(s.Find(".result__a").Text())
		snippet := strings.TrimSpace(s.Find(".result__snippet").Text())

		href, exists := s.Find(".result__a").Attr("href")
		if !exists {
			return
		}
		resultURL := extractDDGURL(href)

		if title == "" || resultURL == "" {
			return
		}

		results = append(results, fmt.Sprintf("%d. %s\n   URL: %s\n   %s", count+1, title, resultURL, snippet))
		count++
	})

	if len(results) == 0 {
		return "No results found.", nil
	}

	return wrapUntrusted(fmt.Sprintf("web search for %q", query), strings.Join(results, "\n\n")), nil
}

// extractDDGURL pulls the real URL out of a DDG redirect like /l/?uddg=https%3A%2F%2F...
func extractDDGURL(href string) string {
	if strings.HasPrefix(href, "http") {
		return href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if uddg := u.Query().Get("uddg"); uddg != "" {
		return uddg
	}
	return href
}
