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
