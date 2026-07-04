package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/projects"
)

// NewListProjectsTool returns the `list_projects` built-in: the agent enumerates the named
// projects it can recall and (later) switch into, so it can resolve a project the user refers
// to by intent ("the articles from last time") against the stored titles/descriptions rather
// than guessing (projects.md §3–§4). Read-only, trusted, not sandbox-exposed. Omitted when no
// projects root is wired (see ExecutorConfig.ProjectsRoot).
func NewListProjectsTool(root string) Tool {
	return Tool{
		Name: "list_projects",
		Description: "List your named projects — persistent workspaces you can recall by intent. Each has a " +
			"title, an optional description, and when it was last active. Use this to find the project the " +
			"user means (e.g. 'the reading list from last time') before continuing that work.",
		Parameters: map[string]any{},
		Required:   []string{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			ps, err := projects.List(root)
			if err != nil {
				return fmt.Sprintf("could not list projects: %v", err), nil
			}
			if len(ps) == 0 {
				return "no projects yet", nil
			}
			var b strings.Builder
			for _, p := range ps {
				fmt.Fprintf(&b, "- %s (uid:%s)", p.Title, p.UID)
				if p.Description != "" {
					fmt.Fprintf(&b, " — %s", p.Description)
				}
				if !p.LastActive.IsZero() {
					fmt.Fprintf(&b, " [last active %s]", p.LastActive.Format("2006-01-02"))
				}
				b.WriteByte('\n')
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

// NewCreateProjectTool returns the `create_project` built-in: the agent promotes work worth
// keeping into a named, persistent project — minting <slug>-<uid>/ under the projects root,
// seeding its marker, and (with from_paths) moving scratch artifacts in (projects.md §3–§4).
// It is a *side-effecting* trusted built-in (mkdir + registry write), so it is human-gated
// and audited, and is not exposed to sandboxed code. Omitted when no projects root is wired.
//
// Creation does not itself re-anchor the workspace — switching into the new project is the
// separate, tier-gated switch step (projects.md P3); this tool reports the created project so
// that switch can act on it.
func NewCreateProjectTool(root string, gate HumanGate, rec audit.Recorder, runID string) Tool {
	return Tool{
		Name: "create_project",
		Description: "Create a new named project — a persistent workspace you can recall in later sessions. " +
			"Use it when work is worth keeping (not a throwaway one-off). Give a short `title` and an optional " +
			"`description` so you can find it later by intent. Pass `from_paths` to move existing scratch files " +
			"into the new project (promotion). This asks the user for approval before creating anything.",
		Parameters: map[string]any{
			"title":       map[string]any{"type": "string", "description": "short human name for the project (e.g. \"shared reading list\")"},
			"description": map[string]any{"type": "string", "description": "optional one-line summary to aid later recall"},
			"from_paths": map[string]any{
				"type":        "array",
				"description": "optional scratch files/dirs to move into the new project",
				"items":       map[string]any{"type": "string"},
			},
		},
		Required: []string{"title"},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			title, _ := args["title"].(string)
			if strings.TrimSpace(title) == "" {
				return "create_project: a title is required", nil
			}
			desc, _ := args["description"].(string)
			fromPaths := stringSlice(args["from_paths"])

			detail := fmt.Sprintf("title: %s", strings.TrimSpace(title))
			if strings.TrimSpace(desc) != "" {
				detail += fmt.Sprintf("\ndescription: %s", strings.TrimSpace(desc))
			}
			if len(fromPaths) > 0 {
				detail += fmt.Sprintf("\nmove in: %s", strings.Join(fromPaths, ", "))
			}
			ok, err := gate.Approve(ctx, ApprovalRequest{
				Kind:   "project.create",
				Title:  "Create project",
				Detail: detail,
				RunID:  runID,
			})
			if err != nil {
				return fmt.Sprintf("create_project: approval failed: %v", err), nil
			}
			if !ok {
				return "create_project: not approved by the user", nil
			}

			p, err := projects.Create(root, projects.CreateOptions{
				Title:       title,
				Description: desc,
				FromPaths:   fromPaths,
			})
			if err != nil {
				return fmt.Sprintf("create_project failed: %v", err), nil
			}
			if rec != nil {
				rec.Record(audit.Event{
					Type:   audit.EventProjectCreated,
					Run:    runID,
					Fields: map[string]any{"uid": p.UID, "title": p.Title, "path": p.Path},
				})
			}
			msg := fmt.Sprintf("created project %q (uid:%s) at %s", p.Title, p.UID, p.Path)
			if len(fromPaths) > 0 {
				msg += fmt.Sprintf("; moved in %s", strings.Join(fromPaths, ", "))
			}
			return msg, nil
		},
	}
}

// NewSwitchProjectTool returns the `switch_project` built-in: the agent makes a named project
// the active workspace mid-conversation — subsequent shell commands run there and the project's
// own prompt tier loads under the §5 trust gate (projects.md P3, §7). doSwitch is the executor's
// re-anchor+reload seam (nil-checked by the caller: the tool is wired only when it is available).
// The switch is audited (project_switched). Trusted, not sandbox-exposed. Resolving is
// conservative — an ambiguous title is reported back so the agent asks rather than guesses.
func NewSwitchProjectTool(root string, doSwitch func(dir string) error, rec audit.Recorder, runID string) Tool {
	return Tool{
		Name: "switch_project",
		Description: "Switch into one of your named projects, making it the active workspace: subsequent shell " +
			"commands run there and the project's own instructions load. Identify it by uid (exact) or by a word " +
			"or phrase from its title. Run list_projects first if unsure which one the user means; if a title is " +
			"ambiguous, ask the user rather than guessing (a wrong switch loads the wrong context).",
		Parameters: map[string]any{
			"project": map[string]any{"type": "string", "description": "the project to switch to: its uid, or a word/phrase from its title"},
		},
		Required: []string{"project"},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			query, _ := args["project"].(string)
			if strings.TrimSpace(query) == "" {
				return "switch_project: name the project to switch to (uid or title)", nil
			}
			p, err := projects.Resolve(root, query)
			if err != nil {
				var amb *projects.AmbiguousError
				switch {
				case errors.As(err, &amb):
					return "switch_project: " + amb.Error() + " — ask the user which one, or switch by uid", nil
				case errors.Is(err, projects.ErrNotFound):
					return fmt.Sprintf("switch_project: no project matches %q; run list_projects to see what exists", query), nil
				default:
					return fmt.Sprintf("switch_project: %v", err), nil
				}
			}
			if err := doSwitch(p.Path); err != nil {
				return fmt.Sprintf("switch_project failed: %v", err), nil
			}
			if rec != nil {
				rec.Record(audit.Event{
					Type:   audit.EventProjectSwitched,
					Run:    runID,
					Fields: map[string]any{"uid": p.UID, "title": p.Title, "path": p.Path},
				})
			}
			return fmt.Sprintf("switched to project %q (uid:%s); shell now runs in %s", p.Title, p.UID, p.Path), nil
		},
	}
}
