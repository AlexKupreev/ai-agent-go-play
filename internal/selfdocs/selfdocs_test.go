package selfdocs

import (
	"testing"
	"testing/fstest"
)

func testDocs(t *testing.T) *Docs {
	t.Helper()
	fsys := fstest.MapFS{
		"README.md":                      {Data: []byte("# ai-agent\n\nAn agent you run.")},
		"docs/usage.md":                  {Data: []byte("# Usage — operating the agent\n\nHow to run and the trust tiers.")},
		"docs/security.md":               {Data: []byte("# Security solutions\n\nApprovals and the capability broker.")},
		"self-extending-agent-design.md": {Data: []byte("# Self-Extending Agent — Design Notes\n\nThe sandbox trade-offs.")},
		"docs/notes.txt":                 {Data: []byte("not markdown, ignored")},
	}
	d, err := New(fsys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestNew_ListOrderKindsAndTitles(t *testing.T) {
	d := testDocs(t)
	if d.Len() != 4 {
		t.Fatalf("Len = %d, want 4 (the .txt is ignored)", d.Len())
	}
	list := d.List()

	// Reference docs come before the vision doc.
	last := KindReference
	for _, in := range list {
		if last == KindVision && in.Kind == KindReference {
			t.Fatalf("reference doc %q sorted after a vision doc; order = %+v", in.Topic, list)
		}
		last = in.Kind
	}

	// The vision doc is classified and titled from its first heading.
	var found bool
	for _, in := range list {
		if in.Topic == "self-extending-agent-design" {
			found = true
			if in.Kind != KindVision {
				t.Errorf("vision doc kind = %q, want vision", in.Kind)
			}
			if in.Title != "Self-Extending Agent — Design Notes" {
				t.Errorf("vision doc title = %q", in.Title)
			}
		}
	}
	if !found {
		t.Fatal("vision doc missing from list")
	}
}

func TestGet(t *testing.T) {
	d := testDocs(t)

	for _, topic := range []string{"usage", "USAGE", "usage.md", "docs/usage.md"} {
		body, err := d.Get(topic)
		if err != nil {
			t.Fatalf("Get(%q): %v", topic, err)
		}
		if body == "" || body[:1] != "#" {
			t.Errorf("Get(%q) returned unexpected body %q", topic, body)
		}
	}

	// The vision alias resolves to the long filename.
	if _, err := d.Get("vision"); err != nil {
		t.Errorf("Get(\"vision\") alias: %v", err)
	}

	// Unknown topic errors and lists what's available.
	if _, err := d.Get("nope"); err == nil {
		t.Error("Get(unknown) returned nil error")
	}
}

func TestSearch(t *testing.T) {
	d := testDocs(t)

	hits := d.Search("trust tiers", 5)
	if len(hits) == 0 || hits[0].Topic != "usage" {
		t.Fatalf("Search(\"trust tiers\") top hit = %+v, want usage first", hits)
	}

	hits = d.Search("capability broker approvals", 5)
	if len(hits) == 0 || hits[0].Topic != "security" {
		t.Fatalf("Search(\"capability broker approvals\") top hit = %+v, want security first", hits)
	}

	if got := d.Search("zzzznotoken", 5); got != nil {
		t.Errorf("Search(no overlap) = %+v, want nil", got)
	}
}

func TestNew_NilFS(t *testing.T) {
	d, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	if d.Len() != 0 {
		t.Fatalf("New(nil).Len() = %d, want 0", d.Len())
	}
}
