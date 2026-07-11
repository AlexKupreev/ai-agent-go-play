package capability

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostAllowed(t *testing.T) {
	pats := []string{"example.com", "*.api.test"}
	allow := []string{"example.com", "EXAMPLE.com", "v1.api.test", "deep.v1.api.test"}
	deny := []string{"evil.com", "example.com.evil.com", "api.test", "notexample.com"}
	for _, h := range allow {
		if !hostAllowed(pats, h) {
			t.Errorf("hostAllowed(%q) = false, want true", h)
		}
	}
	for _, h := range deny {
		if hostAllowed(pats, h) {
			t.Errorf("hostAllowed(%q) = true, want false", h)
		}
	}
}

func TestPathAllowed(t *testing.T) {
	base := t.TempDir()
	if !pathAllowed(base, filepath.Join(base, "a", "b.txt")) {
		t.Error("path under prefix should be allowed")
	}
	if !pathAllowed(base, base) {
		t.Error("the prefix itself should be allowed")
	}
	if pathAllowed(base, filepath.Join(base, "..", "escape.txt")) {
		t.Error("path escaping the prefix must be denied")
	}
	if pathAllowed("", "/anything") {
		t.Error("empty prefix must deny")
	}
}

func TestPathAllowed_SymlinkEscapeDenied(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0600); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the allowed dir pointing at the outside dir.
	link := filepath.Join(allowed, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// Textually under the prefix, but resolves outside it → must be denied.
	if pathAllowed(allowed, filepath.Join(link, "secret.txt")) {
		t.Error("path via a symlink escaping the prefix must be denied")
	}
	// A normal not-yet-existing file directly under the prefix stays allowed.
	if !pathAllowed(allowed, filepath.Join(allowed, "new.txt")) {
		t.Error("a new file directly under the prefix should be allowed")
	}
}

func TestToolAllowed(t *testing.T) {
	if !toolAllowed([]string{"a", "b"}, "b") {
		t.Error("listed tool should be allowed")
	}
	if toolAllowed([]string{"a"}, "b") {
		t.Error("unlisted tool should be denied")
	}
	if !toolAllowed([]string{"*"}, "anything") {
		t.Error("wildcard should allow any tool")
	}
}

func TestGrantHas(t *testing.T) {
	g := &GrantContext{Granted: []Capability{{Kind: Clock}}}
	if !g.Has(Clock) {
		t.Error("Has(Clock) = false, want true")
	}
	if g.Has(Random) {
		t.Error("Has(Random) = true, want false")
	}
	var nilGrant *GrantContext
	if nilGrant.Has(Clock) {
		t.Error("nil grant must not have capabilities")
	}
}

func TestParseTier(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Tier
		ok   bool
	}{
		{"safe", TierSafe, true},
		{"balanced", TierBalanced, true},
		{"permissive", TierPermissive, true},
		{"", "", false},
		{"bogus", "", false},
		{"Safe", "", false}, // case-sensitive
	} {
		got, err := ParseTier(tc.in)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("ParseTier(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("ParseTier(%q) = %q, nil; want error", tc.in, got)
		}
	}
}

// TestClampTier verifies the ceiling semantics: a request may match or go safer than the
// ceiling, but a looser request is clamped down to it.
func TestClampTier(t *testing.T) {
	for _, tc := range []struct {
		requested, ceiling, want Tier
	}{
		{TierPermissive, TierBalanced, TierBalanced}, // looser than ceiling ⇒ clamped
		{TierPermissive, TierSafe, TierSafe},         // clamped all the way down
		{TierSafe, TierBalanced, TierSafe},           // safer than ceiling ⇒ allowed
		{TierBalanced, TierBalanced, TierBalanced},   // equal ⇒ unchanged
		{TierBalanced, TierPermissive, TierBalanced}, // request wins when under a looser ceiling
		{TierPermissive, TierPermissive, TierPermissive},
	} {
		if got := ClampTier(tc.requested, tc.ceiling); got != tc.want {
			t.Errorf("ClampTier(%q, ceiling %q) = %q, want %q", tc.requested, tc.ceiling, got, tc.want)
		}
	}
}

func TestCapabilitySecretPlacement(t *testing.T) {
	ok := []struct{ in, where, key string }{
		{"header:x-api-key", "header", "x-api-key"},
		{"header:Authorization", "header", "Authorization"},
		{"query:token", "query", "token"},
	}
	for _, c := range ok {
		w, k, err := (Capability{SecretIn: c.in}).SecretPlacement()
		if err != nil || w != c.where || k != c.key {
			t.Errorf("%q → (%q,%q,%v), want (%q,%q,nil)", c.in, w, k, err, c.where, c.key)
		}
	}
	for _, bad := range []string{"", "x-api-key", "cookie:c", "header:", ":k"} {
		if _, _, err := (Capability{SecretIn: bad}).SecretPlacement(); err == nil {
			t.Errorf("SecretPlacement(%q) should error", bad)
		}
	}
}

func TestCapabilityValidate(t *testing.T) {
	// No secret: always valid regardless of kind.
	if err := (Capability{Kind: ReadFile}).Validate(); err != nil {
		t.Errorf("non-secret cap should validate: %v", err)
	}
	// Well-formed secret on http_get: valid.
	if err := (Capability{Kind: HTTPGet, Secret: "s", SecretIn: "header:k"}).Validate(); err != nil {
		t.Errorf("valid secret cap rejected: %v", err)
	}
	// Secret on a non-http_get cap: rejected.
	if err := (Capability{Kind: ReadFile, Secret: "s", SecretIn: "header:k"}).Validate(); err == nil {
		t.Error("secret on a read_file cap should be rejected")
	}
	// secret_in without secret name: rejected.
	if err := (Capability{Kind: HTTPGet, SecretIn: "header:k"}).Validate(); err == nil {
		t.Error("secret_in without a secret name should be rejected")
	}
	// Bad placement: rejected.
	if err := (Capability{Kind: HTTPGet, Secret: "s", SecretIn: "cookie:c"}).Validate(); err == nil {
		t.Error("bad placement should be rejected")
	}
}
