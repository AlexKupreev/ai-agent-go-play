package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	maxEvidenceCalls         = 32
	maxEvidenceSources       = 8
	maxEvidenceInputRunes    = 256
	maxEvidenceResultRunes   = 512
	maxEvidenceSourceRunes   = 256
	maxEvidenceURLBytes      = 1024
	maxEvidenceEnvelopeBytes = 16 * 1024
)

// EvidenceOutcome is the runtime-observed outcome of one tool call. It says whether the
// tool returned successfully; it does not attest that returned information is true.
type EvidenceOutcome string

const (
	EvidenceSuccess EvidenceOutcome = "success"
	EvidenceError   EvidenceOutcome = "error"
)

// ExecutionEvidence is the bounded, metadata-only record of one executor attempt.
type ExecutionEvidence struct {
	Attempt   int            `json:"attempt"`
	Calls     []ToolEvidence `json:"calls"`
	Truncated bool           `json:"truncated"`
}

// ToolEvidence describes one runtime-observed tool call without retaining its raw output.
type ToolEvidence struct {
	Sequence      int              `json:"sequence"`
	Tool          string           `json:"tool"`
	Outcome       EvidenceOutcome  `json:"outcome"`
	InputSummary  string           `json:"input_summary,omitempty"`
	ResultSummary string           `json:"result_summary,omitempty"`
	Sources       []EvidenceSource `json:"sources,omitempty"`
	ErrorClass    string           `json:"error_class,omitempty"`
}

// EvidenceSource is source metadata conservatively extracted from a successful web call.
// Titles originate on the web and remain untrusted data when rendered for the critic.
type EvidenceSource struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// EvidenceRecorder derives execution evidence from events emitted by a single executor
// attempt. It intentionally never stores raw result bodies or raw errors.
type EvidenceRecorder struct {
	mu        sync.Mutex
	attempt   int
	calls     []ToolEvidence
	pending   map[string]int
	truncated bool
}

// NewEvidenceRecorder creates an empty recorder for attempt (initial execution is attempt 0).
func NewEvidenceRecorder(attempt int) *EvidenceRecorder {
	return &EvidenceRecorder{attempt: attempt, pending: make(map[string]int)}
}

// Emit implements Observer. Tool starts establish ordering; matching results fill in outcome
// metadata. Planner and critic events, and all non-tool events, are ignored.
func (r *EvidenceRecorder) Emit(e Event) {
	if e.Call == nil || (e.Kind != EvToolStart && e.Kind != EvToolResult) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	switch e.Kind {
	case EvToolStart:
		if len(r.calls) >= maxEvidenceCalls {
			r.truncated = true
			return
		}
		input, cut := summarizeEvidenceInput(e.Call.Name, e.Call.Input)
		r.truncated = r.truncated || cut
		r.calls = append(r.calls, ToolEvidence{
			Sequence:     len(r.calls) + 1,
			Tool:         boundedToolName(e.Call.Name),
			Outcome:      EvidenceError,
			InputSummary: input,
			ErrorClass:   "incomplete_call",
		})
		r.pending[e.Call.ID] = len(r.calls) - 1

	case EvToolResult:
		i, ok := r.pending[e.Call.ID]
		if !ok || i >= len(r.calls) {
			// A result whose start was dropped by a call bound remains represented by the
			// envelope's truncated marker; never manufacture reordered evidence for it.
			r.truncated = true
			return
		}
		delete(r.pending, e.Call.ID)
		call := &r.calls[i]
		call.Sources = nil
		if e.IsError {
			call.Outcome = EvidenceError
			call.ErrorClass = classifyEvidenceError(e.Result)
			call.ResultSummary = "tool call failed"
			return
		}
		call.Outcome = EvidenceSuccess
		call.ErrorClass = ""
		result, sources, cut := summarizeEvidenceResult(call.Tool, e.Result, e.Call.Input)
		boundedResult, cutResult := boundRunes(result, maxEvidenceResultRunes)
		call.ResultSummary = boundedResult
		call.Sources = sources
		r.truncated = r.truncated || cut || cutResult
	}
}

// Snapshot returns a detached immutable-by-convention copy. It applies the serialized
// envelope bound last, preserving call order and dropping only the tail when necessary.
func (r *EvidenceRecorder) Snapshot() ExecutionEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := ExecutionEvidence{Attempt: r.attempt, Calls: make([]ToolEvidence, 0, len(r.calls)), Truncated: r.truncated}
	for _, original := range r.calls {
		call := original
		call.Sources = append([]EvidenceSource(nil), original.Sources...)
		candidate := out
		candidate.Calls = append(append([]ToolEvidence(nil), out.Calls...), call)
		encoded, _ := json.Marshal(candidate)
		if len(encoded) > maxEvidenceEnvelopeBytes {
			out.Truncated = true
			break
		}
		out.Calls = append(out.Calls, call)
	}
	// A call-count overflow can leave pending entries for events we deliberately ignored.
	if len(r.calls) >= maxEvidenceCalls && len(r.pending) > 0 {
		out.Truncated = true
	}
	return out
}

func boundedToolName(name string) string {
	name, _ = boundRunes(strings.TrimSpace(name), 128)
	return name
}

func summarizeEvidenceInput(tool string, raw json.RawMessage) (string, bool) {
	var args map[string]any
	if len(raw) > 0 && json.Unmarshal(raw, &args) != nil {
		return "invalid JSON arguments", false
	}
	redacted := redactEvidenceValue(args).(map[string]any)
	switch tool {
	case "web_search":
		if q, ok := redacted["query"].(string); ok {
			return boundRunes("query: "+strings.TrimSpace(q), maxEvidenceInputRunes)
		}
	case "web_fetch":
		if rawURL, ok := redacted["url"].(string); ok {
			if normalized, cut := normalizeEvidenceURL(rawURL); normalized != "" {
				summary, cutSummary := boundRunes("url: "+normalized, maxEvidenceInputRunes)
				return summary, cut || cutSummary
			}
			return "url omitted (invalid)", false
		}
	}

	keys := make([]string, 0, len(redacted))
	for k := range redacted {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	summary := "arguments: none"
	if len(keys) > 0 {
		summary = "argument names: " + strings.Join(keys, ", ")
	}
	return boundRunes(summary, maxEvidenceInputRunes)
}

func summarizeEvidenceResult(tool, result string, input json.RawMessage) (string, []EvidenceSource, bool) {
	bytesSummary := fmt.Sprintf("returned %d bytes", len(result))
	switch tool {
	case "web_search":
		sources, cut := extractSearchEvidenceSources(result)
		return fmt.Sprintf("returned %d source(s), %d bytes", len(sources), len(result)), sources, cut
	case "web_fetch":
		if source, cut := evidenceFetchURL(input); source != "" {
			return bytesSummary, []EvidenceSource{{URL: source}}, cut
		}
	}
	return bytesSummary, nil, false
}

func evidenceFetchURL(raw json.RawMessage) (string, bool) {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return "", false
	}
	args = redactEvidenceValue(args).(map[string]any)
	rawURL, _ := args["url"].(string)
	return normalizeEvidenceURL(rawURL)
}

func extractSearchEvidenceSources(result string) ([]EvidenceSource, bool) {
	lines := strings.Split(result, "\n")
	var sources []EvidenceSource
	var title string
	truncated := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if dot := strings.Index(trimmed, ". "); dot > 0 && allDigits(trimmed[:dot]) {
			title = trimmed[dot+2:]
			continue
		}
		if !strings.HasPrefix(trimmed, "URL: ") || title == "" {
			continue
		}
		if len(sources) >= maxEvidenceSources {
			truncated = true
			break
		}
		normalized, cutURL := normalizeEvidenceURL(strings.TrimSpace(strings.TrimPrefix(trimmed, "URL: ")))
		if normalized == "" {
			title = ""
			continue
		}
		boundedTitle, cutTitle := boundRunes(title, maxEvidenceSourceRunes)
		sources = append(sources, EvidenceSource{URL: normalized, Title: boundedTitle})
		truncated = truncated || cutURL || cutTitle
		title = ""
	}
	return sources, truncated
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizeEvidenceURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	u.User = nil
	u.Fragment = ""
	q := u.Query()
	for key := range q {
		if sensitiveEvidenceKey(key) {
			q.Set(key, "[REDACTED]")
		}
	}
	u.RawQuery = q.Encode()
	normalized := u.String()
	if len(normalized) <= maxEvidenceURLBytes {
		return normalized, false
	}
	// Prefer a valid, less-specific URL over cutting an encoded URL mid-component.
	u.RawQuery = ""
	normalized = u.String()
	if len(normalized) <= maxEvidenceURLBytes {
		return normalized, true
	}
	u.Path, u.RawPath = "", ""
	normalized = u.String()
	if len(normalized) > maxEvidenceURLBytes {
		return "", true
	}
	return normalized, true
}

func redactEvidenceValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, child := range value {
			if sensitiveEvidenceKey(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = redactEvidenceValue(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			out[i] = redactEvidenceValue(child)
		}
		return out
	default:
		return value
	}
}

func sensitiveEvidenceKey(key string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	normalized := b.String()
	switch normalized {
	case "password", "passwd", "secret", "clientsecret", "token", "accesstoken",
		"refreshtoken", "key", "apikey", "accesskey", "privatekey", "authorization",
		"auth", "cookie", "setcookie", "signature", "sig":
		return true
	default:
		return strings.HasSuffix(normalized, "password") ||
			strings.HasSuffix(normalized, "secret") ||
			strings.HasSuffix(normalized, "token") ||
			strings.HasSuffix(normalized, "key") ||
			strings.HasSuffix(normalized, "authorization") ||
			strings.HasSuffix(normalized, "cookie") ||
			strings.HasSuffix(normalized, "signature")
	}
}

func classifyEvidenceError(result string) string {
	lower := strings.ToLower(result)
	switch {
	case strings.Contains(lower, "context canceled"), strings.Contains(lower, "context cancelled"):
		return "canceled"
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"):
		return "timeout"
	case strings.Contains(lower, "invalid"), strings.Contains(lower, "must be"):
		return "invalid_input"
	case strings.Contains(lower, "permission"), strings.Contains(lower, "denied"), strings.Contains(lower, "forbidden"):
		return "permission_denied"
	case strings.Contains(lower, "status "):
		return "http_status"
	case strings.Contains(lower, "request failed"), strings.Contains(lower, "connection"), strings.Contains(lower, "dns"):
		return "network"
	case strings.Contains(lower, "not found"), strings.Contains(lower, "no such"):
		return "not_found"
	case strings.Contains(lower, "unknown tool"):
		return "unknown_tool"
	default:
		return "tool_error"
	}
}

func boundRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return "", s != ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	runes := []rune(s)
	return string(runes[:max]), true
}
