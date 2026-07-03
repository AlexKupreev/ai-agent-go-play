package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/capability"

	yaml "go.yaml.in/yaml/v3"
)

// agentsDir is the sub-directory (under the config dir and the workspace) that holds
// user-defined sub-agent types as <name>.md files (subagents.md §2). Mirrors the pi /
// Claude-Code layout so those files drop in.
const agentsDir = "agents"

// defaultSpawnDepth is the coordinator's initial delegation budget: it may spawn a leaf
// sub-agent, which (in v1) cannot spawn further. Widen deliberately if nesting is enabled.
const defaultSpawnDepth = 1

// loadAgentCatalog builds the spawnable sub-agent catalog: the built-in types, then any
// <config-dir>/agents/*.md (global), then any <workspace>/agents/*.md (project), with a
// later file overriding a same-named earlier type (project > global > built-in), matching
// pi's precedence and the prompt-tier layering (prompts.md §2).
//
// The project tier is trust-gated exactly like the workspace prompt tier (workspace.md §5):
// a `safe` agent does not auto-load an untrusted checkout's agent definitions — whose bodies
// are system prompts for spawned agents, i.e. injection surface — unless the operator named
// the workspace explicitly (--workspace). --no-context-files disables both file tiers,
// leaving only the built-in types.
func loadAgentCatalog(workspace string, tier capability.Tier) (*agent.AgentCatalog, error) {
	cat := agent.NewAgentCatalog()
	if noContextFilesFlag {
		return cat, nil
	}

	cfgDir, err := configDir()
	if err != nil {
		return nil, err
	}
	if err := loadAgentDir(cat, filepath.Join(cfgDir, agentsDir)); err != nil {
		return nil, err
	}
	if loadWorkspaceTier(workspace, cfgDir, tier) {
		if err := loadAgentDir(cat, filepath.Join(workspace, agentsDir)); err != nil {
			return nil, err
		}
	}
	return cat, nil
}

// loadAgentDir registers every <name>.md in dir into cat. A missing directory is a no-op
// (most installs have none). A malformed or invalid file is a hard error — a typo in an
// agent definition should surface, not be silently skipped.
func loadAgentDir(cat *agent.AgentCatalog, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		at, err := parseAgentFile(path)
		if err != nil {
			return fmt.Errorf("agent file %s: %w", path, err)
		}
		if err := cat.Register(at); err != nil {
			return fmt.Errorf("agent file %s: %w", path, err)
		}
	}
	return nil
}

// agentFrontmatter is the YAML header of an agents/<name>.md file. Parsed with a real YAML
// library (not hand-rolled) so the format stays drop-in compatible with pi / Claude Code.
type agentFrontmatter struct {
	Description string `yaml:"description"`
	Tools       string `yaml:"tools"`       // comma/space-separated tool names; "*" ⇒ inherit all
	Model       string `yaml:"model"`       // optional model override
	Parallel    bool   `yaml:"parallel"`    // may run concurrently ⇒ tools must be read-only
	PromptMode  string `yaml:"prompt_mode"` // "replace" (default) | "append"
}

// parseAgentFile reads one agents/<name>.md into an AgentType: the type name is the file
// stem, the YAML frontmatter supplies the metadata, and the body is the system prompt.
func parseAgentFile(path string) (agent.AgentType, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return agent.AgentType{}, err
	}
	front, body := splitFrontmatter(b)

	var fm agentFrontmatter
	if len(front) > 0 {
		if err := yaml.Unmarshal(front, &fm); err != nil {
			return agent.AgentType{}, fmt.Errorf("invalid frontmatter: %w", err)
		}
	}

	name := strings.TrimSuffix(filepath.Base(path), ".md")
	return agent.AgentType{
		Name:        name,
		Description: fm.Description,
		Prompt:      strings.TrimSpace(string(body)),
		Tools:       splitTools(fm.Tools),
		Model:       strings.TrimSpace(fm.Model),
		Parallel:    fm.Parallel,
		PromptMode:  strings.TrimSpace(fm.PromptMode),
	}, nil
}

// splitFrontmatter separates a leading `---`-fenced YAML block from the body. A file with no
// opening fence is treated as all body (no metadata). If the closing fence is missing, the
// whole remainder is taken as frontmatter (the YAML parser then reports any error). Handles
// both LF and CRLF line endings and a leading UTF-8 BOM.
func splitFrontmatter(b []byte) (front, body []byte) {
	s := bytes.TrimPrefix(b, []byte("\ufeff"))
	if !bytes.HasPrefix(s, []byte("---\n")) && !bytes.HasPrefix(s, []byte("---\r\n")) {
		return nil, b
	}
	// Drop the opening fence line, then scan for a line that is exactly "---".
	rest := s[bytes.IndexByte(s, '\n')+1:]
	for i := 0; i <= len(rest); {
		end := bytes.IndexByte(rest[i:], '\n')
		if end < 0 { // last line, no closing fence found
			if strings.TrimRight(string(rest[i:]), "\r") == "---" {
				return rest[:i], nil
			}
			return rest, nil
		}
		if strings.TrimRight(string(rest[i:i+end]), "\r") == "---" {
			return rest[:i], rest[i+end+1:]
		}
		i += end + 1
	}
	return rest, nil
}

// splitTools parses the frontmatter `tools` field into an allow-list. Accepts comma- or
// whitespace-separated names. Empty ⇒ nil (the AgentType read-only default). A lone "*" is
// preserved as the inherit-all sentinel.
func splitTools(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	if len(fields) == 0 {
		return nil
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
