package tools

import (
	"context"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/capability"
)

// scriptSpec builds a minimal valid script-backed ToolSpec for tests.
func scriptSpec(name, desc, src string, scope Scope) ToolSpec {
	return ToolSpec{
		Name:        name,
		Description: desc,
		InputSchema: map[string]any{"type": "object"},
		Impl:        Impl{Kind: ImplScript, Lang: "lua", Source: src},
		Scope:       scope,
	}
}

func TestRegister_AssignsProvenance(t *testing.T) {
	r := NewMemoryRegistry()
	got, err := r.Register(scriptSpec("adder", "adds numbers", "return input.a + input.b", ScopeEphemeral))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
	if got.CodeHash == "" {
		t.Error("code hash not assigned")
	}
	if got.seq == 0 {
		t.Error("seq not assigned")
	}
}

func TestRegister_RejectsInvalid(t *testing.T) {
	r := NewMemoryRegistry()
	cases := map[string]ToolSpec{
		"bad name":       scriptSpec("Bad Name", "d", "x", ScopeEphemeral),
		"empty desc":     scriptSpec("ok", "", "x", ScopeEphemeral),
		"script no src":  {Name: "ok", Description: "d", InputSchema: map[string]any{}, Impl: Impl{Kind: ImplScript, Lang: "lua"}},
		"native no func": {Name: "ok", Description: "d", InputSchema: map[string]any{}, Impl: Impl{Kind: ImplNative}},
		"nil schema":     {Name: "ok", Description: "d", Impl: Impl{Kind: ImplScript, Lang: "lua", Source: "x"}},
	}
	for label, spec := range cases {
		if _, err := r.Register(spec); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

func TestReRegister_BumpsVersionKeepsPosition(t *testing.T) {
	r := NewMemoryRegistry()
	first, _ := r.Register(scriptSpec("alpha", "first", "return 1", ScopeEphemeral))
	r.Register(scriptSpec("beta", "second", "return 2", ScopeEphemeral))
	second, _ := r.Register(scriptSpec("alpha", "updated", "return 3", ScopeEphemeral))

	if second.Version != 2 {
		t.Errorf("version = %d, want 2", second.Version)
	}
	if second.seq != first.seq {
		t.Errorf("seq changed on re-register: %d -> %d", first.seq, second.seq)
	}
	if second.CodeHash == first.CodeHash {
		t.Error("code hash should change when source changes")
	}
	// Order is still alpha, beta (alpha kept its earlier position).
	names := listNames(r.List(ScopeAny))
	if want := []string{"alpha", "beta"}; !equal(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
}

func TestList_StableRegistrationOrderAndScopeFilter(t *testing.T) {
	r := NewMemoryRegistry()
	r.Register(scriptSpec("one", "d", "return 1", ScopeEphemeral))
	r.Register(scriptSpec("two", "d", "return 2", ScopeShared))
	r.Register(scriptSpec("three", "d", "return 3", ScopeEphemeral))

	if names := listNames(r.List(ScopeAny)); !equal(names, []string{"one", "two", "three"}) {
		t.Errorf("all = %v", names)
	}
	if names := listNames(r.List(ScopeEphemeral)); !equal(names, []string{"one", "three"}) {
		t.Errorf("ephemeral = %v", names)
	}
	if names := listNames(r.List(ScopeShared)); !equal(names, []string{"two"}) {
		t.Errorf("shared = %v", names)
	}
}

func TestRegister_DedupsByCodeHash(t *testing.T) {
	r := NewMemoryRegistry()
	r.Register(scriptSpec("alpha", "first", "return 42", ScopeEphemeral))

	got, err := r.Register(scriptSpec("beta", "second", "return 42", ScopeEphemeral))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if got.Name != "alpha" {
		t.Errorf("dedup should return the existing tool (alpha), got %q", got.Name)
	}
	if _, ok := r.Get("beta"); ok {
		t.Error("identical-code tool should not be registered under a new name")
	}
	if n := len(r.List(ScopeAny)); n != 1 {
		t.Errorf("catalog should hold 1 tool after dedup, got %d", n)
	}
}

func TestGetAndRevoke(t *testing.T) {
	r := NewMemoryRegistry()
	r.Register(scriptSpec("gone", "d", "x", ScopeEphemeral))

	if _, ok := r.Get("gone"); !ok {
		t.Fatal("expected to find tool")
	}
	if !r.Revoke("gone") {
		t.Error("revoke should report true for existing tool")
	}
	if _, ok := r.Get("gone"); ok {
		t.Error("tool should be gone after revoke")
	}
	if r.Revoke("gone") {
		t.Error("revoke should report false for missing tool")
	}
}

func TestSearch_RanksByOverlap(t *testing.T) {
	r := NewMemoryRegistry()
	r.Register(scriptSpec("weather", "fetch current weather forecast", "return 1", ScopeEphemeral))
	r.Register(scriptSpec("currency", "convert currency exchange rates", "return 2", ScopeEphemeral))
	r.Register(scriptSpec("notes", "store personal notes", "return 3", ScopeEphemeral))

	got := listNames(r.Search("weather forecast", 5))
	if len(got) == 0 || got[0] != "weather" {
		t.Errorf("top result = %v, want weather first", got)
	}
	if got := r.Search("blockchain", 5); len(got) != 0 {
		t.Errorf("no-overlap query should return nothing, got %v", listNames(got))
	}
	if got := r.Search("currency", 0); len(got) != 1 {
		t.Errorf("k<=0 should return all matches, got %d", len(got))
	}
}

func TestPersistence_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "tools.json")

	r1, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	spec := scriptSpec("persisted", "survives restart", "return 42", ScopeShared)
	spec.RequiredCaps = []capability.Capability{{Kind: capability.HTTPGet, Hosts: []string{"example.com"}}}
	if _, err := r1.Register(spec); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Ephemeral tools must NOT be persisted.
	r1.Register(scriptSpec("temp", "ephemeral", "return 0", ScopeEphemeral))

	r2, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloaded, ok := r2.Get("persisted")
	if !ok {
		t.Fatal("persisted tool missing after reload")
	}
	if reloaded.Impl.Source != "return 42" {
		t.Errorf("source = %q", reloaded.Impl.Source)
	}
	if len(reloaded.RequiredCaps) != 1 || reloaded.RequiredCaps[0].Kind != capability.HTTPGet {
		t.Errorf("required caps not round-tripped: %+v", reloaded.RequiredCaps)
	}
	if _, ok := r2.Get("temp"); ok {
		t.Error("ephemeral tool should not have been persisted")
	}
}

func TestPersistence_NativeNotWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.json")
	r1, _ := NewPersistentRegistry(path)
	_, err := r1.Register(ToolSpec{
		Name:        "native_shared",
		Description: "native handler",
		InputSchema: map[string]any{"type": "object"},
		Scope:       ScopeShared,
		Impl:        Impl{Kind: ImplNative, Native: func(context.Context, map[string]any) (string, error) { return "ok", nil }},
	})
	if err != nil {
		t.Fatalf("register native: %v", err)
	}
	// Reload: a native handler cannot serialize, so it must not reappear from disk.
	r2, _ := NewPersistentRegistry(path)
	if _, ok := r2.Get("native_shared"); ok {
		t.Error("native tool should not be persisted to the catalog")
	}
}

// --- helpers ---

func listNames(specs []ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
