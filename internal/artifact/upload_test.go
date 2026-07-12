package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSafeName pins the containment property the upload path depends on: whatever a sender
// calls a file, what we store is a plain basename that cannot escape the scratch dir or carry
// shell metacharacters into a command the agent later builds.
func TestSafeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sales.csv", "sales.csv"},
		{"my report (final).csv", "my_report__final_.csv"},
		{"../../etc/passwd", "passwd"}, // traversal collapses to the leaf
		{"/etc/shadow", "shadow"},      // absolute path collapses to the leaf
		{"..", "upload"},               // nothing but dots ⇒ no name at all
		{".", "upload"},                //
		{"", "upload"},                 //
		{".bashrc", "bashrc"},          // no dotfiles
		{"a;b c.csv", "a_b_c.csv"},     // metacharacters and spaces neutralized
		{"a;rm -rf /.csv", "csv"},      // an embedded "/" means the leaf is all that survives
		{"data\n.csv", "data_.csv"},    // control characters neutralized
		{"..\\..\\win.ini", "win.ini"}, // a Windows-style path is still just a leaf
		{strings.Repeat("x", 200) + ".csv", strings.Repeat("x", maxNameLen-len(".csv")) + ".csv"}, // truncated in the stem, extension kept
	}
	for _, tc := range cases {
		got := SafeName(tc.in)
		if got != tc.want {
			t.Errorf("SafeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.ContainsAny(got, `/\`) || got == "." || got == ".." {
			t.Errorf("SafeName(%q) = %q, which is not a safe basename", tc.in, got)
		}
	}
}

// TestSaveUserFile checks the two inseparable halves of storing an upload: the bytes land in
// the scratch dir, and the manifest records the file as user-provided — the provenance that
// makes ReapScratch keep it when the session is closed.
func TestSaveUserFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scratch") // not pre-created: SaveUserFile makes it

	e, n, err := SaveUserFile(dir, "../sales report.csv", "telegram upload", strings.NewReader("a,b\n1,2\n"))
	if err != nil {
		t.Fatalf("SaveUserFile: %v", err)
	}
	if want := int64(len("a,b\n1,2\n")); n != want {
		t.Errorf("bytes = %d, want %d", n, want)
	}
	if e.Path != "sales_report.csv" {
		t.Fatalf("stored path = %q, want the sanitized basename sales_report.csv", e.Path)
	}
	if e.Origin != OriginUser {
		t.Errorf("origin = %q, want %q — an agent-origin upload would be reaped on close", e.Origin, OriginUser)
	}
	body, err := os.ReadFile(filepath.Join(dir, e.Path))
	if err != nil || string(body) != "a,b\n1,2\n" {
		t.Fatalf("stored file = %q, %v; want the uploaded bytes", body, err)
	}

	// The manifest is the durable record the planner reads and the reaper consults.
	m, err := New(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	entries := m.List()
	if len(entries) != 1 || entries[0].Path != "sales_report.csv" || entries[0].Source != "telegram upload" {
		t.Fatalf("manifest = %+v, want one user entry for sales_report.csv", entries)
	}

	// A second upload of the same name must not overwrite the first.
	e2, _, err := SaveUserFile(dir, "sales report.csv", "telegram upload", strings.NewReader("different"))
	if err != nil {
		t.Fatalf("second SaveUserFile: %v", err)
	}
	if e2.Path == e.Path {
		t.Fatalf("second upload reused %q — it would have clobbered the first", e2.Path)
	}
	body, _ = os.ReadFile(filepath.Join(dir, e.Path))
	if string(body) != "a,b\n1,2\n" {
		t.Errorf("first file was overwritten: %q", body)
	}
}

// TestSaveUserFileSurvivesReap is the point of recording provenance: closing a session reaps
// the agent's re-derivable scratch but keeps what the user uploaded.
func TestSaveUserFileSurvivesReap(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := SaveUserFile(dir, "notes.txt", "telegram upload", strings.NewReader("keep me")); err != nil {
		t.Fatalf("SaveUserFile: %v", err)
	}
	// An agent-derived artifact alongside it, plus untracked scratch.
	m, _ := New(filepath.Join(dir, ManifestName))
	if err := os.WriteFile(filepath.Join(dir, "derived.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Append(Entry{Path: "derived.json", Origin: OriginAgent}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmp.bin"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ReapScratch(dir); err != nil {
		t.Fatalf("ReapScratch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("user upload was reaped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "derived.json")); !os.IsNotExist(err) {
		t.Errorf("agent artifact survived the reap (err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp.bin")); !os.IsNotExist(err) {
		t.Errorf("untracked scratch survived the reap (err = %v)", err)
	}
}
