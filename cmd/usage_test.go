package cmd

import (
	"strings"
	"testing"
	"time"

	"ai-agent-go-play/internal/provider"
)

func TestHumanInt(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{12431, "12,431"},
		{1000000, "1,000,000"},
		{-12431, "-12,431"},
	} {
		if got := humanInt(tc.in); got != tc.want {
			t.Errorf("humanInt(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSubUsage(t *testing.T) {
	a := provider.Usage{InputTokens: 100, OutputTokens: 20, CachedTokens: 5}
	b := provider.Usage{InputTokens: 350, OutputTokens: 60, CachedTokens: 12}
	got := subUsage(a, b)
	if got.InputTokens != 250 || got.OutputTokens != 40 || got.CachedTokens != 7 {
		t.Fatalf("subUsage = %+v, want {250 40 7}", got)
	}
}

func TestFormatUsage(t *testing.T) {
	// With cached tokens and multiple steps.
	got := formatUsage(provider.Usage{InputTokens: 12431, OutputTokens: 3210, CachedTokens: 1024}, 4, 6200*time.Millisecond)
	for _, want := range []string{"12,431 in", "3,210 out", "1,024 cached", "4 steps", "6.2s"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatUsage() = %q, missing %q", got, want)
		}
	}

	// Singular step, no cached segment.
	got = formatUsage(provider.Usage{InputTokens: 10, OutputTokens: 2}, 1, time.Second)
	if strings.Contains(got, "cached") {
		t.Errorf("formatUsage() = %q, should omit cached when zero", got)
	}
	if !strings.Contains(got, "1 step ") { // singular, with trailing space before the middot separator
		t.Errorf("formatUsage() = %q, want singular '1 step'", got)
	}
}
