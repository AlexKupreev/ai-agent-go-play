package cmd

import (
	"fmt"
	"strings"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/guidance"
)

// workspaceGuidanceStore returns the user-managed global guidance store for a workspace.
// Guidance lives with workspace data, not config-dir operator prompts.
func workspaceGuidanceStore(workDir string, rec audit.Recorder) *guidance.FileStore {
	return guidance.NewFileStore(guidancePath(workDir), "global", rec)
}

// withGuidance appends user guidance after operator prompt files, in specificity order:
// workspace, active space, then session. The current turn remains the final user message.
func withGuidance(appends []string, workspace, spaceNote, session string) []string {
	out := make([]string, 0, len(appends)+3)
	out = append(out, appends...)
	if section := guidanceSection("Workspace guidance", workspace); section != "" {
		out = append(out, section)
	}
	if spaceNote != "" {
		out = append(out, spaceNote)
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
