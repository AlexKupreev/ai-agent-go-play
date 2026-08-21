package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/guidance"
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

// do adds the recovery path that raw net/http connection errors omit. Context cancellation
// is left untouched because it means the caller stopped the operation, not that the engine
// needs starting or its address repairing.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient().Do(req)
	if err == nil || req.Context().Err() != nil {
		return resp, err
	}
	return nil, fmt.Errorf("engine at %s is unavailable: %w; check --addr (or its configured alias) and start it with `agent serve --addr <host:port>`", c.BaseURL, err)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
func (c *Client) Reload(ctx context.Context) (ReloadDiff, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/reload", nil)
	if err != nil {
		return ReloadDiff{}, err
	}
	resp, err := c.do(req)
	if err != nil {
		return ReloadDiff{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return ReloadDiff{}, fmt.Errorf("reload: %s", msg)
	}
	var out ReloadDiff
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ReloadDiff{}, fmt.Errorf("reload response: %w", err)
	}
	return out, nil
}

// EffectiveConfig returns the secret-safe configuration snapshot used by the next run.
func (c *Client) EffectiveConfig(ctx context.Context) (EffectiveConfig, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/config/effective", nil)
	if err != nil {
		return EffectiveConfig{}, err
	}
	resp, err := c.do(req)
	if err != nil {
		return EffectiveConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EffectiveConfig{}, fmt.Errorf("effective config: %s", resp.Status)
	}
	var out EffectiveConfig
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return EffectiveConfig{}, err
	}
	return out, nil
}

// Status returns live engine resources and effective configuration. A nil sessionID
// requests engine-only status; a non-nil id adds that live session's explicit overlay.
func (c *Client) Status(ctx context.Context, sessionID *string) (StatusResponse, error) {
	path := "/status"
	if sessionID != nil {
		q := url.Values{}
		q.Set("session_id", *sessionID)
		path += "?" + q.Encode()
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return StatusResponse{}, err
	}
	resp, err := c.do(req)
	if err != nil {
		return StatusResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return StatusResponse{}, fmt.Errorf("status: %s", message)
	}
	var out StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return StatusResponse{}, err
	}
	return out, nil
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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

// GetGuidance reads one explicit guidance scope. target is required for space/session
// and ignored for global guidance.
func (c *Client) GetGuidance(ctx context.Context, scope guidance.Scope, target string) (GuidanceDocument, error) {
	req, err := c.newRequest(ctx, http.MethodGet, guidancePathFor(scope, target), nil)
	if err != nil {
		return GuidanceDocument{}, err
	}
	return c.doGuidance(req, scope, target)
}

// SetGuidance replaces one explicit guidance scope. An empty value is an idempotent clear.
func (c *Client) SetGuidance(ctx context.Context, scope guidance.Scope, target, text string) (GuidanceDocument, error) {
	body, _ := json.Marshal(putGuidanceRequest{Guidance: text})
	req, err := c.newRequest(ctx, http.MethodPut, guidancePathFor(scope, target), bytes.NewReader(body))
	if err != nil {
		return GuidanceDocument{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doGuidance(req, scope, target)
}

func (c *Client) doGuidance(req *http.Request, scope guidance.Scope, target string) (GuidanceDocument, error) {
	resp, err := c.do(req)
	if err != nil {
		return GuidanceDocument{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return GuidanceDocument{}, fmt.Errorf("%s guidance %q: %s", scope, target, message)
	}
	var out GuidanceDocument
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return GuidanceDocument{}, err
	}
	return out, nil
}

// ListSpaces returns body-redacted space metadata, newest-updated first.
func (c *Client) ListSpaces(ctx context.Context) ([]SpaceView, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/spaces", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, responseError("list spaces", resp)
	}
	var out []SpaceView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetSpace returns one space's body-redacted metadata by canonical id.
func (c *Client) GetSpace(ctx context.Context, id string) (SpaceView, error) {
	req, err := c.newRequest(ctx, http.MethodGet, spacePath(id), nil)
	if err != nil {
		return SpaceView{}, err
	}
	resp, err := c.do(req)
	if err != nil {
		return SpaceView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SpaceView{}, responseError("show space "+id, resp)
	}
	var out SpaceView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SpaceView{}, err
	}
	return out, nil
}

// CreateSpace creates a named space and returns its body-redacted metadata.
func (c *Client) CreateSpace(ctx context.Context, name string) (SpaceView, error) {
	body, _ := json.Marshal(createSpaceRequest{Name: name})
	req, err := c.newRequest(ctx, http.MethodPost, "/spaces", bytes.NewReader(body))
	if err != nil {
		return SpaceView{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return SpaceView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return SpaceView{}, responseError("create space", resp)
	}
	var out SpaceView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SpaceView{}, err
	}
	return out, nil
}

func responseError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("%s: %s", action, message)
}

// UploadFile stores a user-provided file in the session's working area over POST
// /sessions/{id}/files and returns where it landed. name is the file's original (untrusted)
// name — the engine sanitizes it; source says where it came from (e.g. "telegram upload") and
// shows up in the agent's artifact manifest. The body is streamed, so a large file is not held
// in memory.
func (c *Client) UploadFile(ctx context.Context, sessionID, name, source string, r io.Reader) (UploadInfo, error) {
	// Build the multipart body on a pipe so the file streams straight through to the wire.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		// CloseWithError(nil) is Close: the reader sees a clean EOF when everything was
		// written, and the real error otherwise (so a failed copy can't look like a short file).
		var err error
		defer func() { _ = pw.CloseWithError(err) }()
		if err = mw.WriteField("source", source); err != nil {
			return
		}
		var part io.Writer
		if part, err = mw.CreateFormFile("file", name); err != nil {
			return
		}
		if _, err = io.Copy(part, r); err != nil {
			return
		}
		err = mw.Close()
	}()

	req, err := c.newRequest(ctx, http.MethodPost, "/sessions/"+sessionID+"/files", pr)
	if err != nil {
		return UploadInfo{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.do(req)
	if err != nil {
		return UploadInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The engine's upload errors are actionable (too large, unknown session), so surface
		// the body rather than just the status line.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return UploadInfo{}, fmt.Errorf("upload to session %s: %s: %s", sessionID, resp.Status, strings.TrimSpace(string(detail)))
	}
	var out UploadInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return UploadInfo{}, err
	}
	return out, nil
}

// ListSessions returns the engine's sessions, newest-updated first.
func (c *Client) ListSessions(ctx context.Context) ([]session.Info, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/sessions", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("stop run %s: %s", runID, resp.Status)
	}
	return nil
}
