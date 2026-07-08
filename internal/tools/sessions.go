package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ai-agent-go-play/internal/session"
)

// SessionReader is the read side of the session store the session tools need — the durable
// record of past conversations. *session.FileStore satisfies it. Read-only by design: these
// tools let the agent look back at earlier conversations ("what did we decide yesterday?"),
// never modify them.
type SessionReader interface {
	List() ([]session.Info, error)
	Get(id string) (session.Session, error)
}

// SessionToolDeps configures the session-reading tools. CurrentID, when set, is the
// in-progress session's id — marked "(current)" in listings so the agent doesn't treat its own
// live conversation as a separate past one.
type SessionToolDeps struct {
	Reader    SessionReader
	CurrentID string
}

const (
	sessionListLimit   = 30    // most-recent sessions a listing shows
	sessionSearchScan  = 200   // most-recent sessions search will load + score (bound the work)
	sessionSearchK     = 5     // top matches search returns
	sessionReadCharCap = 12000 // cap read_session output so one huge transcript can't flood context
)

// NewSessionTools returns the read-only session-introspection built-ins (list/search/read),
// letting the agent revisit earlier conversations. Trusted, not sandbox-exposed (like recall
// and status). The caller omits them by passing no reader.
func NewSessionTools(deps SessionToolDeps) []Tool {
	return []Tool{
		newListSessionsTool(deps),
		newSearchSessionsTool(deps),
		newReadSessionTool(deps),
	}
}

func newListSessionsTool(deps SessionToolDeps) Tool {
	return Tool{
		Name: "list_sessions",
		Description: "List your recent stored conversations (sessions): each one's id, title, turn " +
			"count, and when it was last active, newest first. Use it to find an earlier conversation " +
			"to revisit — then read_session to open one, or search_sessions to find one by topic.",
		Parameters: map[string]any{},
		Required:   []string{},
		Run: func(_ context.Context, _ map[string]any) (string, error) {
			infos, err := deps.Reader.List()
			if err != nil {
				return "", fmt.Errorf("list sessions: %w", err)
			}
			if len(infos) == 0 {
				return "No stored sessions yet.", nil
			}
			var b strings.Builder
			for i, in := range infos {
				if i >= sessionListLimit {
					fmt.Fprintf(&b, "… and %d older (use search_sessions to find a specific one)\n", len(infos)-sessionListLimit)
					break
				}
				b.WriteString(sessionLine(in, deps.CurrentID))
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func newSearchSessionsTool(deps SessionToolDeps) Tool {
	return Tool{
		Name: "search_sessions",
		Description: "Find earlier conversations (sessions) by topic: given a query, returns the " +
			"best-matching stored sessions with their id, title, and a short snippet, ranked by " +
			"relevance. Use it for 'the conversation where we discussed X'; then read_session to open one.",
		Parameters: map[string]any{
			"query": map[string]any{"type": "string", "description": "words describing the conversation to find"},
		},
		Required: []string{"query"},
		Run: func(_ context.Context, args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			terms := tokenize(strings.TrimSpace(query))
			if len(terms) == 0 {
				return "", errors.New("search_sessions needs a non-empty query")
			}
			infos, err := deps.Reader.List()
			if err != nil {
				return "", fmt.Errorf("list sessions: %w", err)
			}
			type hit struct {
				info    session.Info
				score   int
				snippet string
			}
			var hits []hit
			for i, in := range infos {
				if i >= sessionSearchScan {
					break
				}
				sess, err := deps.Reader.Get(in.ID)
				if err != nil {
					continue
				}
				text := renderSessionText(sess)
				score := overlapCount(terms, tokenize(in.Title+" "+text))
				if score > 0 {
					hits = append(hits, hit{in, score, sessionSnippet(text)})
				}
			}
			if len(hits) == 0 {
				return "No stored session matched that query.", nil
			}
			// Highest score first; ties broken by recency (List is already newest-first).
			sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
			var b strings.Builder
			for i, h := range hits {
				if i >= sessionSearchK {
					break
				}
				b.WriteString(sessionLine(h.info, deps.CurrentID))
				fmt.Fprintf(&b, "    %s\n", h.snippet)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func newReadSessionTool(deps SessionToolDeps) Tool {
	return Tool{
		Name: "read_session",
		Description: "Read the full text of one stored conversation by its id (from list_sessions or " +
			"search_sessions): the user/assistant turns, so you can recall what was discussed or decided. " +
			"Long transcripts are truncated.",
		Parameters: map[string]any{
			"id": map[string]any{"type": "string", "description": "the session id to read"},
		},
		Required: []string{"id"},
		Run: func(_ context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				return "", errors.New("read_session needs a session id")
			}
			sess, err := deps.Reader.Get(id)
			if err != nil {
				if errors.Is(err, session.ErrNotFound) {
					return fmt.Sprintf("No session with id %q (use list_sessions to see valid ids).", id), nil
				}
				return "", fmt.Errorf("read session %s: %w", id, err)
			}
			text := renderSessionText(sess)
			if text == "" {
				return fmt.Sprintf("Session %s has no readable content.", id), nil
			}
			if len(text) > sessionReadCharCap {
				text = text[:sessionReadCharCap] + "\n… (truncated)"
			}
			return fmt.Sprintf("Session %s (%s):\n%s", id, sess.UpdatedAt.Format("2006-01-02 15:04"), text), nil
		},
	}
}

// sessionLine renders one session's listing row, marking the current one.
func sessionLine(in session.Info, currentID string) string {
	mark := ""
	if in.ID == currentID {
		mark = " (current)"
	}
	title := in.Title
	if title == "" {
		title = "(untitled)"
	}
	return fmt.Sprintf("%s%s — %q — %d %s — last active %s\n",
		in.ID, mark, title, in.Turns, itemWordTurns(in.Turns), in.UpdatedAt.Format("2006-01-02 15:04"))
}

// renderSessionText renders a stored conversation to a role-labelled plain-text transcript
// (text blocks + tool-result output), skipping empty turns.
func renderSessionText(s session.Session) string {
	var b strings.Builder
	for _, m := range s.Messages {
		var line strings.Builder
		for _, c := range m.Content {
			if c.Text != "" {
				line.WriteString(c.Text)
			}
			if c.ToolResult != nil {
				line.WriteString(c.ToolResult.Output)
			}
		}
		if t := strings.TrimSpace(line.String()); t != "" {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, t)
		}
	}
	return b.String()
}

// sessionSnippet returns a short one-line preview of a session's text for search results.
func sessionSnippet(text string) string {
	s := strings.Join(strings.Fields(text), " ") // collapse whitespace/newlines
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// overlapCount counts how many query terms appear in hay.
func overlapCount(terms, hay map[string]struct{}) int {
	score := 0
	for t := range terms {
		if _, ok := hay[t]; ok {
			score++
		}
	}
	return score
}

// itemWordTurns is the singular/plural of "turn".
func itemWordTurns(n int) string {
	if n == 1 {
		return "turn"
	}
	return "turns"
}
