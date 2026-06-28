// Package capability is the security boundary for agent-authored code. Authored
// tools have no ambient authority: every effect goes through the Broker, which
// checks the call against the tool's granted capabilities and audits it.
//
// Deny by default. A capability the grant does not contain simply does not exist
// for that execution.
package capability

import (
	"os"
	"path/filepath"
	"strings"
)

// Kind names a class of effect a tool may be permitted to perform.
type Kind string

const (
	HTTPGet   Kind = "http_get"
	ReadFile  Kind = "read_file"
	WriteFile Kind = "write_file"
	CallTool  Kind = "call_tool"
	Clock     Kind = "clock"
	Random    Kind = "random"
)

// Capability is a single grant. The relevant allowlist field depends on Kind.
type Capability struct {
	Kind Kind `json:"kind"`

	Hosts      []string `json:"hosts,omitempty"`       // HTTPGet: host patterns ("example.com", "*.example.com")
	PathPrefix string   `json:"path_prefix,omitempty"` // ReadFile/WriteFile: paths must live under this prefix
	Tools      []string `json:"tools,omitempty"`       // CallTool: allowed tool names ("*" = any)
}

// Tier is the default-allow policy band for a run (generalizes pi's
// safe/balanced/permissive). It governs which escalations auto-approve.
type Tier string

const (
	TierSafe       Tier = "safe"
	TierBalanced   Tier = "balanced"
	TierPermissive Tier = "permissive"
)

// GrantContext is what is live for one execution: the run it belongs to, the
// capabilities granted, and the policy tier.
type GrantContext struct {
	Run     string
	Granted []Capability
	Tier    Tier
}

// find returns the granted capability of the given kind, if any.
func (g *GrantContext) find(kind Kind) (Capability, bool) {
	if g == nil {
		return Capability{}, false
	}
	for _, c := range g.Granted {
		if c.Kind == kind {
			return c, true
		}
	}
	return Capability{}, false
}

// Has reports whether the grant contains a capability of the given kind.
func (g *GrantContext) Has(kind Kind) bool {
	_, ok := g.find(kind)
	return ok
}

// hostAllowed matches a host against patterns: exact, or "*.suffix".
func hostAllowed(patterns []string, host string) bool {
	host = strings.ToLower(host)
	for _, p := range patterns {
		p = strings.ToLower(p)
		if p == host {
			return true
		}
		if strings.HasPrefix(p, "*.") && strings.HasSuffix(host, p[1:]) {
			return true
		}
	}
	return false
}

// pathAllowed reports whether path resolves to within prefix. Symlinks are
// resolved on both sides so a link *inside* the prefix cannot point outside it
// (e.g. /allowed/link -> /etc, then read /allowed/link/passwd) — checking only
// the textual prefix would let that escape.
func pathAllowed(prefix, path string) bool {
	if prefix == "" {
		return false
	}
	pp, err := resolvePath(prefix)
	if err != nil {
		return false
	}
	ap, err := resolvePath(path)
	if err != nil {
		return false
	}
	return ap == pp || strings.HasPrefix(ap, pp+string(os.PathSeparator))
}

// resolvePath makes path absolute and resolves symlinks. The target of a write
// may not exist yet, so it resolves the longest existing ancestor and re-appends
// the remaining (not-yet-existing) components — enough to catch a symlinked
// ancestor that redirects out of an allowed prefix.
func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)

	rest := ""
	cur := abs
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, rest), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil // reached the root with nothing existing
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// toolAllowed reports whether name is in the allowlist ("*" matches any).
func toolAllowed(list []string, name string) bool {
	for _, t := range list {
		if t == "*" || t == name {
			return true
		}
	}
	return false
}
