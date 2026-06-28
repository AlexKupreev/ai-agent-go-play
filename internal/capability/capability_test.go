package capability

import (
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
