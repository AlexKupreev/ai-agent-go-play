package agent

import "testing"

func TestContextWindow(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"gpt-4o", 128_000},
		{"gpt-4o-mini", 128_000},              // prefix → gpt-4o
		{"gpt-4o-2024-08-06", 128_000},        // dated snapshot → gpt-4o
		{"gpt-4.1", 1_000_000},                // exact
		{"gpt-4.1-mini", 1_000_000},           // prefix → gpt-4.1
		{"gpt-4", 8_192},                      // exact
		{"gpt-4-0613", 8_192},                 // prefix → gpt-4 (not gpt-4o)
		{"gpt-4-turbo", 128_000},              // exact (longer prefix wins over gpt-4)
		{"gpt-4-32k", 32_768},                 // exact
		{"gpt-3.5-turbo", 16_385},             // exact
		{"o3-mini", 200_000},                  // exact (longer than o3)
		{"o1-mini", 128_000},                  // exact (differs from o1)
		{"some-local-llama", 0},               // unknown → 0
		{"", 0},                               // empty → 0
	}
	for _, tc := range cases {
		if got := ContextWindow(tc.model); got != tc.want {
			t.Errorf("ContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}
