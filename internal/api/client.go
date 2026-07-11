package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/session"
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

// newRequest builds a request against the engine.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
}

// StartRun starts a run and returns its id. opts carries optional per-request model/tier
// overrides (the zero value inherits the engine's defaults).
func (c *Client) StartRun(ctx context.Context, task string, opts RunOptions) (string, error) {
	body, _ := json.Marshal(startRunRequest{Task: task, RunOptions: opts})
	req, err := c.newRequest(ctx, http.MethodPost, "/runs", bytes.NewReader(body))
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
	req, err := c.newRequest(ctx, http.MethodGet, "/runs/"+runID+"/events", nil)
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

// Pending lists the approvals parked on the engine for this client's session.
func (c *Client) Pending(ctx context.Context) ([]PendingApproval, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/approvals", nil)
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

// Resolve answers a parked approval by id (yes/no).
func (c *Client) Resolve(ctx context.Context, id string, approved bool) error {
	if err := c.postResolve(ctx, id, resolveApprovalRequest{Approved: approved}); err != nil {
		return fmt.Errorf("resolve approval %s: %w", id, err)
	}
	return nil
}

// Answer answers a parked ask_user question by id (free text).
func (c *Client) Answer(ctx context.Context, id, text string) error {
	if err := c.postResolve(ctx, id, resolveApprovalRequest{Answer: &text}); err != nil {
		return fmt.Errorf("answer question %s: %w", id, err)
	}
	return nil
}

// postResolve POSTs a resolution body to /approvals/{id}, shared by Resolve and Answer.
func (c *Client) postResolve(ctx context.Context, id string, body resolveApprovalRequest) error {
	b, _ := json.Marshal(body)
	req, err := c.newRequest(ctx, http.MethodPost, "/approvals/"+id, bytes.NewReader(b))
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
		return fmt.Errorf("%s", resp.Status)
	}
	return nil
}

// ToolDetail returns one catalog tool including its implementation source.
func (c *Client) ToolDetail(ctx context.Context, name string) (ToolDetailView, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/tools/"+name, nil)
	if err != nil {
		return ToolDetailView{}, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return ToolDetailView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ToolDetailView{}, fmt.Errorf("tool detail %s: %s", name, resp.Status)
	}
	var out ToolDetailView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ToolDetailView{}, err
	}
	return out, nil
}

// RevokeTool removes an authored tool from the remote engine's catalog.
func (c *Client) RevokeTool(ctx context.Context, name string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/tools/"+name, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("revoke tool %s: %s", name, resp.Status)
	}
	return nil
}

// Audit returns audit events from the remote engine's process-wide log, oldest
// first. run and typ filter (empty = wildcard); limit caps to the last N (<=0 = all).
func (c *Client) Audit(ctx context.Context, run, typ string, limit int) ([]audit.Event, error) {
	q := url.Values{}
	if run != "" {
		q.Set("run", run)
	}
	if typ != "" {
		q.Set("type", typ)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/audit"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audit: %s", resp.Status)
	}
	var out []audit.Event
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Reload tells the engine to re-read, from disk, its prompt files (SYSTEM.md/AGENTS.md/
// PLANNER.md/CRITIC.md), agent-type catalog (agents/*.md), and config.json defaults (the
// default model + tier ceiling), so edits take effect on subsequent runs without restarting
// the engine. A malformed file or config leaves the current state intact and returns an error.
func (c *Client) Reload(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/reload", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("reload: %s", msg)
	}
	return nil
}

// StartSession creates a persistent conversation on the engine and returns its id. opts
// carries an optional initial sticky model/tier (the zero value inherits the engine
// defaults; a turn may still override per-request).
func (c *Client) StartSession(ctx context.Context, opts RunOptions) (string, error) {
	body, _ := json.Marshal(startSessionRequest{RunOptions: opts})
	req, err := c.newRequest(ctx, http.MethodPost, "/sessions", bytes.NewReader(body))
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
		return "", fmt.Errorf("start session: %s", resp.Status)
	}
	var out startSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.SessionID, nil
}

// UpdateSession sets a session's sticky model/tier/space over PATCH /sessions/{id} and returns
// the updated Info. A nil pointer leaves that field unchanged; a non-nil pointer sets it (an
// empty string clears it back to the engine default / global scope). Passing nil for all is a
// read of the current values.
func (c *Client) UpdateSession(ctx context.Context, sessionID string, model, tier, space *string) (session.Info, error) {
	body, _ := json.Marshal(updateSessionRequest{Model: model, Tier: tier, Space: space})
	req, err := c.newRequest(ctx, http.MethodPatch, "/sessions/"+sessionID, bytes.NewReader(body))
	if err != nil {
		return session.Info{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return session.Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return session.Info{}, fmt.Errorf("update session %s: %s", sessionID, resp.Status)
	}
	var out session.Info
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return session.Info{}, err
	}
	return out, nil
}

// ListSessions returns the engine's sessions, newest-updated first.
func (c *Client) ListSessions(ctx context.Context) ([]session.Info, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/sessions", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list sessions: %s", resp.Status)
	}
	var out []session.Info
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// CloseSession terminates a session on the engine.
func (c *Client) CloseSession(ctx context.Context, sessionID string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/sessions/"+sessionID, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("close session %s: %s", sessionID, resp.Status)
	}
	return nil
}

// PurgeSession irreversibly removes a session on the engine (live or archived) and reaps
// its scratch cache — the destructive counterpart to CloseSession, which only archives.
func (c *Client) PurgeSession(ctx context.Context, sessionID string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/sessions/"+sessionID+"/purge", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("purge session %s: %s", sessionID, resp.Status)
	}
	return nil
}

// RestoreSession un-archives a closed session on the engine so it is resumable again.
func (c *Client) RestoreSession(ctx context.Context, sessionID string) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/sessions/"+sessionID+"/restore", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("restore session %s: %s", sessionID, resp.Status)
	}
	return nil
}

// PostTurn submits a turn to a session and returns the run id to stream for its reply. opts
// carries an optional per-turn model/tier override (the zero value inherits the session /
// engine defaults).
func (c *Client) PostTurn(ctx context.Context, sessionID, text string, opts RunOptions) (string, error) {
	body, _ := json.Marshal(postTurnRequest{Text: text, RunOptions: opts})
	req, err := c.newRequest(ctx, http.MethodPost, "/sessions/"+sessionID+"/turns", bytes.NewReader(body))
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
		return "", fmt.Errorf("post turn: %s", resp.Status)
	}
	var out postTurnResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.RunID, nil
}

// ListRuns returns this client's runs, newest first.
func (c *Client) ListRuns(ctx context.Context) ([]RunInfo, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/runs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list runs: %s", resp.Status)
	}
	var out []RunInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// RunStatus returns the metadata for one of this client's runs.
func (c *Client) RunStatus(ctx context.Context, runID string) (RunInfo, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/runs/"+runID, nil)
	if err != nil {
		return RunInfo{}, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return RunInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RunInfo{}, fmt.Errorf("run status %s: %s", runID, resp.Status)
	}
	var out RunInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return RunInfo{}, err
	}
	return out, nil
}

// StopRun cancels one of this client's runs (the kill switch).
func (c *Client) StopRun(ctx context.Context, runID string) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/runs/"+runID+"/cancel", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("stop run %s: %s", runID, resp.Status)
	}
	return nil
}
