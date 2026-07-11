package api

import "testing"


// TestSummarizeBrief proves the one-line brief rendering every frontend shares: first
// content line, blank lines skipped, a "(label)" revision marker kept as a prefix.
func TestSummarizeBrief(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain brief", "Do the thing.\n\nContext:\nlots of detail\n\nSuccess criteria: ...", "Do the thing."},
		{"leading blank lines", "\n\nRefined task here\nmore", "Refined task here"},
		{"labelled revision", "(revision 1)\nDo it better.\n\nContext: ...", "(revision 1) Do it better."},
		{"critic note", "note: verdict shortfall — re-planning", "note: verdict shortfall — re-planning"},
		{"label only", "(revision 2)\n\n", "(revision 2)"},
		{"empty", "", ""},
	} {
		if got := SummarizeBrief(tc.in); got != tc.want {
			t.Errorf("%s: SummarizeBrief = %q, want %q", tc.name, got, tc.want)
		}
	}
}
