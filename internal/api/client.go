package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Client is the peer-side of the HTTP+SSE transport: it drives a remote engine the
// same way the CLI would drive an in-process one. It makes the CLI (and any other
// frontend) a client of the engine rather than a special case (the Phase 4 goal).
type Client struct {
	BaseURL string // e.g. "http://127.0.0.1:8080"
	HTTP    *http.Client
}

// NewClient returns a client for the engine at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/")}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// StartRun starts a run and returns its id.
func (c *Client) StartRun(ctx context.Context, task string) (string, error) {
	body, _ := json.Marshal(startRunRequest{Task: task})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/runs", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("start run: %s", resp.Status)
	}
	var out startRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.RunID, nil
}

// StreamEvents opens the run's SSE stream and calls onEvent for each event until the
// stream ends (the run's terminal done/error event closes it) or ctx is cancelled.
func (c *Client) StreamEvents(ctx context.Context, runID string, onEvent func(Event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/runs/"+runID+"/events", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream events: %s", resp.Status)
	}

	sc := bufio.NewScanner(resp.Body)
	// Tool results can be large; lift the line cap well above the 64 KiB default.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			continue // skip malformed frame rather than abort the stream
		}
		onEvent(e)
	}
	return sc.Err()
}

// Pending lists the approvals currently parked on the engine.
func (c *Client) Pending(ctx context.Context) ([]PendingApproval, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/approvals", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list approvals: %s", resp.Status)
	}
	var out []PendingApproval
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Resolve answers a parked approval by id.
func (c *Client) Resolve(ctx context.Context, id string, approved bool) error {
	body, _ := json.Marshal(resolveApprovalRequest{Approved: approved})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/approvals/"+id, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("resolve approval %s: %s", id, resp.Status)
	}
	return nil
}
