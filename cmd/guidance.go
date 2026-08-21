package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/guidance"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/space"
)

// workspaceGuidanceStore returns the user-managed global guidance store for a workspace.
// Guidance lives with workspace data, not config-dir operator prompts.
func workspaceGuidanceStore(workDir string, rec audit.Recorder) *guidance.FileStore {
	return guidance.NewFileStore(guidancePath(workDir), "global", rec)
}

// applyRemoteGuidance resolves chat-relative space/session targets and executes through
// the engine's explicit management API.
func applyRemoteGuidance(ctx context.Context, client *api.Client, input, activeSpace, sessionID string) (guidance.CommandResult, error) {
	command, err := guidance.ParseCommand(input)
	if err != nil {
		return guidance.CommandResult{}, err
	}
	target := ""
	switch command.Scope {
	case guidance.ScopeSpace:
		if activeSpace == "" {
			return guidance.CommandResult{}, errors.New("no space is active; use /space <name> first")
		}
		target = activeSpace
	case guidance.ScopeSession:
		target = sessionID
	}
	return guidance.ApplyCommand(command,
		func(scope guidance.Scope) (string, error) {
			doc, err := client.GetGuidance(ctx, scope, target)
			return doc.Guidance, err
		},
		func(scope guidance.Scope, text string) error {
			_, err := client.SetGuidance(ctx, scope, target, text)
			return err
		},
	)
}

// withGuidance appends user guidance after operator prompt files, in specificity order:
// workspace, active space, then session. The current turn remains the final user message.
func withGuidance(appends []string, workspace, spaceGuidance, session string) []string {
	out := make([]string, 0, len(appends)+3)
	out = append(out, appends...)
	if section := guidanceSection("Workspace guidance", workspace); section != "" {
		out = append(out, section)
	}
	if spaceGuidance != "" {
		out = append(out, spaceGuidance)
	}
	if section := guidanceSection("Session guidance", session); section != "" {
		out = append(out, section)
	}
	return out
}

func guidanceSection(title, text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return fmt.Sprintf("## %s\n\n%s", title, text)
}

// applyLocalGuidance resolves the three command scopes for an in-process chat. Its
// session scope lives for the lifetime of that REPL; global and space use durable stores.
func applyLocalGuidance(input string, workspace guidance.Store, spaces *space.Store, activeSpace string, sessionText *string, rec audit.Recorder, runID string) (guidance.CommandResult, error) {
	command, err := guidance.ParseCommand(input)
	if err != nil {
		return guidance.CommandResult{}, err
	}
	return guidance.ApplyCommand(command,
		func(scope guidance.Scope) (string, error) {
			switch scope {
			case guidance.ScopeGlobal:
				return workspace.Get()
			case guidance.ScopeSpace:
				if activeSpace == "" {
					return "", errors.New("no space is active; use /space <name> first")
				}
				sp, err := spaces.Get(activeSpace)
				if err != nil {
					return "", err
				}
				return sp.Guidance, nil
			case guidance.ScopeSession:
				return *sessionText, nil
			default:
				return "", fmt.Errorf("unsupported guidance scope %q", scope)
			}
		},
		func(scope guidance.Scope, text string) error {
			switch scope {
			case guidance.ScopeGlobal:
				return workspace.Set(text)
			case guidance.ScopeSpace:
				sp, err := spaces.Get(activeSpace)
				if err != nil {
					return err
				}
				previous := sp.Guidance
				sp.Guidance = text
				if err := spaces.Save(sp); err != nil {
					return err
				}
				guidance.RecordUpdate(rec, runID, string(scope), previous, text, map[string]any{"space_id": sp.ID})
				return nil
			case guidance.ScopeSession:
				previous := *sessionText
				*sessionText = text
				guidance.RecordUpdate(rec, runID, string(scope), previous, text, map[string]any{"session_id": runID})
				return nil
			default:
				return fmt.Errorf("unsupported guidance scope %q", scope)
			}
		},
	)
}

// guidanceService is serve's workspace-aware implementation of the API management seam.
// One lock covers each read-modify-write update so concurrent management requests cannot
// lose an append. The underlying stores retain their own atomic disk writes.
type guidanceService struct {
	mu        sync.Mutex
	workspace guidance.Store
	spaces    *space.Store
	sessions  session.Store
	rec       audit.Recorder
}

func (s *guidanceService) GetGuidance(scope guidance.Scope, target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(scope, target)
}

func (s *guidanceService) SetGuidance(scope guidance.Scope, target, text string) error {
	if err := guidance.Validate(text); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.get(scope, target)
	if err != nil {
		return err
	}
	if previous == text {
		return nil
	}
	switch scope {
	case guidance.ScopeGlobal:
		// FileStore owns global audit recording so direct users of that narrow seam get
		// the same behavior. Do not record it a second time here.
		return s.workspace.Set(text)
	case guidance.ScopeSpace:
		sp, err := s.spaces.Resolve(target)
		if err != nil {
			return fmt.Errorf("%w: %v", api.ErrGuidanceTargetNotFound, err)
		}
		sp.Guidance = text
		if err := s.spaces.Save(sp); err != nil {
			return err
		}
		guidance.RecordUpdate(s.rec, "", string(scope), previous, text, map[string]any{"space_id": sp.ID})
		return nil
	case guidance.ScopeSession:
		sess, err := s.sessions.Get(target)
		if err != nil {
			return err
		}
		sess.Guidance = text
		if err := s.sessions.Save(sess); err != nil {
			return err
		}
		guidance.RecordUpdate(s.rec, "", string(scope), previous, text, map[string]any{"session_id": sess.ID})
		return nil
	default:
		return fmt.Errorf("unsupported guidance scope %q", scope)
	}
}

// get assumes s.mu is held.
func (s *guidanceService) get(scope guidance.Scope, target string) (string, error) {
	switch scope {
	case guidance.ScopeGlobal:
		if s.workspace == nil {
			return "", errors.New("workspace guidance is unavailable")
		}
		return s.workspace.Get()
	case guidance.ScopeSpace:
		if s.spaces == nil {
			return "", errors.New("space guidance is unavailable")
		}
		sp, err := s.spaces.Resolve(target)
		if err != nil {
			return "", fmt.Errorf("%w: %v", api.ErrGuidanceTargetNotFound, err)
		}
		return sp.Guidance, nil
	case guidance.ScopeSession:
		if s.sessions == nil {
			return "", api.ErrSessionsDisabled
		}
		sess, err := s.sessions.Get(target)
		if err != nil {
			return "", err
		}
		return sess.Guidance, nil
	default:
		return "", fmt.Errorf("unsupported guidance scope %q", scope)
	}
}
