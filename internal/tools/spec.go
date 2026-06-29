package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"

	"ai-agent-go-play/internal/capability"
)

// Scope controls a tool's lifetime and persistence.
type Scope string

const (
	// ScopeAny is the zero value; as a List/filter argument it means "all scopes".
	ScopeAny Scope = ""
	// ScopeEphemeral tools live in memory and die with the run that authored them.
	ScopeEphemeral Scope = "ephemeral"
	// ScopeUser tools persist for the invoking user. On the single-user CLI this
	// collapses to ScopeShared (same JSON catalog); the distinction exists for a
	// future multi-frontend deployment.
	ScopeUser Scope = "user"
	// ScopeShared tools persist in the JSON catalog and load at startup.
	ScopeShared Scope = "shared"
)

// persistent reports whether tools of this scope survive across runs.
func (s Scope) persistent() bool { return s == ScopeUser || s == ScopeShared }

// ImplKind distinguishes how a registered tool executes.
type ImplKind string

const (
	// ImplScript runs authored source in the sandbox under the tool's grant.
	ImplScript ImplKind = "script"
	// ImplNative runs a Go handler registered programmatically at startup.
	// Native impls are never persisted (a func cannot be serialized); they are
	// re-registered each boot by the host.
	ImplNative ImplKind = "native"
)

// Impl is a tool's execution face. Exactly one kind is populated.
type Impl struct {
	Kind ImplKind `json:"kind"`

	// Script fields.
	Lang   string `json:"lang,omitempty"`   // e.g. "lua"
	Source string `json:"source,omitempty"` // sandbox source

	// Native handler — set in code, never serialized.
	Native func(ctx context.Context, args map[string]any) (string, error) `json:"-"`
}

// ToolSpec is an agent-authored (or natively-registered) tool. It has a model
// face (Name, Description, InputSchema — what the LLM sees) and an exec face
// (Impl, RequiredCaps, Scope — how and under what authority it runs).
type ToolSpec struct {
	// Model face.
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`

	// Exec face.
	Impl         Impl                    `json:"impl"`
	RequiredCaps []capability.Capability `json:"required_caps,omitempty"`
	Scope        Scope                   `json:"scope"`
	Test         string                  `json:"test,omitempty"` // smoke-test source (enforced by author_tool in 3c)

	// Provenance, assigned by the registry at registration.
	Version   int    `json:"version"`
	CreatedBy string `json:"created_by,omitempty"`
	CodeHash  string `json:"code_hash,omitempty"`

	// seq is the monotonic registration order, used to keep the live tool list
	// append-only and stable (so the serialized prefix stays cache-friendly). Not
	// persisted: it is reassigned deterministically from catalog order on load.
	seq uint64
}

// nameRe constrains tool names so they are safe as identifiers and as catalog
// keys, and never collide with shell/JSON quirks: lowercase, start with a
// letter, then letters/digits/underscores.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validName reports whether name is a legal tool name.
func validName(name string) bool { return nameRe.MatchString(name) }

// validate checks a spec is well-formed enough to register. It does NOT enforce
// the smoke-test or approval — those are the author_tool pipeline's job (3c).
func (t ToolSpec) validate() error {
	if !validName(t.Name) {
		return fmt.Errorf("invalid tool name %q (want %s)", t.Name, nameRe.String())
	}
	if t.Description == "" {
		return fmt.Errorf("tool %q: description is required", t.Name)
	}
	if t.InputSchema == nil {
		return fmt.Errorf("tool %q: input_schema is required", t.Name)
	}
	switch t.Impl.Kind {
	case ImplScript:
		if t.Impl.Lang == "" || t.Impl.Source == "" {
			return fmt.Errorf("tool %q: script impl needs lang and source", t.Name)
		}
	case ImplNative:
		if t.Impl.Native == nil {
			return fmt.Errorf("tool %q: native impl needs a handler", t.Name)
		}
	default:
		return fmt.Errorf("tool %q: unknown impl kind %q", t.Name, t.Impl.Kind)
	}
	return nil
}

// computeHash hashes the executable content so the registry can dedup and the
// audit log can reference a stable code identity. Native handlers have no
// serializable body, so they hash by name+kind.
func (t ToolSpec) computeHash() string {
	h := sha256.New()
	switch t.Impl.Kind {
	case ImplScript:
		fmt.Fprintf(h, "script\x00%s\x00%s", t.Impl.Lang, t.Impl.Source)
	default:
		fmt.Fprintf(h, "%s\x00%s", t.Impl.Kind, t.Name)
	}
	return hex.EncodeToString(h.Sum(nil))
}
