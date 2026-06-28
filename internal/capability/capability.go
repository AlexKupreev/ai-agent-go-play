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

// pathAllowed reports whether path resolves to within prefix.
func pathAllowed(prefix, path string) bool {
	if prefix == "" {
		return false
	}
	ap, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	pp, err := filepath.Abs(prefix)
	if err != nil {
		return false
	}
	ap, pp = filepath.Clean(ap), filepath.Clean(pp)
	return ap == pp || strings.HasPrefix(ap, pp+string(os.PathSeparator))
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
