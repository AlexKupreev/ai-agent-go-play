package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-agent-go-play/internal/provider"
)

// resetEvalFlags clears the eval command's flag vars between tests.
func resetEvalFlags(t *testing.T) {
	t.Helper()
	evalVariantsFlag, evalModelsFlag = "", nil
	t.Cleanup(func() { evalVariantsFlag, evalModelsFlag = "", nil })
}

func TestLoadEvalVariants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "variants.yaml")
	content := "" +
		"- name: baseline\n" +
		"- name: with-prompt\n" +
		"  context_files: [./a.md, ./b.md]\n" +
		"- name: big\n" +
		"  model: gpt-4o\n" +
		"  tier: permissive\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	vs, err := loadEvalVariants(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("got %d variants, want 3", len(vs))
	}
	if vs[1].Name != "with-prompt" || len(vs[1].ContextFiles) != 2 || vs[1].ContextFiles[0] != "./a.md" {
		t.Errorf("variant[1] = %+v, want with-prompt with [./a.md ./b.md]", vs[1])
	}
	if vs[2].Model != "gpt-4o" || vs[2].Tier != "permissive" {
		t.Errorf("variant[2] = %+v, want model gpt-4o tier permissive", vs[2])
	}
}

func TestLoadEvalVariants_EmptyFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(path, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEvalVariants(path); err == nil {
		t.Error("an empty variants list should error")
	}
}

// --models synthesizes one variant per model; combined with --variants both contribute.
func TestCollectEvalVariants_ModelsAndFileMerge(t *testing.T) {
	resetEvalFlags(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "v.yaml")
	if err := os.WriteFile(path, []byte("- name: fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evalVariantsFlag = path
	evalModelsFlag = []string{"gpt-4o", "gpt-4o-mini"}

	vs, err := collectEvalVariants()
	if err != nil {
		t.Fatal(err)
	}
	names := []string{vs[0].Name, vs[1].Name, vs[2].Name}
	want := []string{"fromfile", "gpt-4o", "gpt-4o-mini"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("variant[%d].Name = %q, want %q", i, names[i], want[i])
		}
	}
	if vs[1].Model != "gpt-4o" {
		t.Errorf("model-sweep variant should carry its model, got %q", vs[1].Model)
	}
}

func TestCollectEvalVariants_NoneErrors(t *testing.T) {
	resetEvalFlags(t)
	if _, err := collectEvalVariants(); err == nil {
		t.Error("no --variants and no --models should error")
	}
}

// An unnamed variant is given a stable positional name so the report is unambiguous.
func TestCollectEvalVariants_AutoNamesUnnamed(t *testing.T) {
	resetEvalFlags(t)
	path := filepath.Join(t.TempDir(), "v.yaml")
	if err := os.WriteFile(path, []byte("- model: gpt-4o\n- name: named\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evalVariantsFlag = path

	vs, err := collectEvalVariants()
	if err != nil {
		t.Fatal(err)
	}
	if vs[0].Name != "variant-1" {
		t.Errorf("unnamed variant[0].Name = %q, want variant-1", vs[0].Name)
	}
	if vs[1].Name != "named" {
		t.Errorf("variant[1].Name = %q, want named", vs[1].Name)
	}
}

func TestFormatEvalReport(t *testing.T) {
	results := []evalResult{
		{
			Variant: evalVariant{Name: "baseline"}, Model: "gpt-4o-mini",
			Output: "the answer is 42\n", Steps: 2, Elapsed: 3200 * time.Millisecond,
			Usage: provider.Usage{InputTokens: 1234, OutputTokens: 340},
		},
		{
			Variant: evalVariant{Name: "broken"}, Model: "gpt-4o",
			Err: errString("boom"),
		},
	}
	out := formatEvalReport(results)

	// Table has a header and both variant rows with humanized token counts.
	for _, want := range []string{"VARIANT", "baseline", "gpt-4o-mini", "1,234", "broken", "ERROR"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
	// Full-output section shows the answer and the error message.
	if !strings.Contains(out, "the answer is 42") {
		t.Errorf("report missing the variant output\n%s", out)
	}
	if !strings.Contains(out, "error: boom") {
		t.Errorf("report missing the error detail\n%s", out)
	}
}

// errString is a trivial error for table tests.
type errString string

func (e errString) Error() string { return string(e) }
