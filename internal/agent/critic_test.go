package agent

import (
	"encoding/json"
	"testing"
)

func TestVerdictRoundTrip(t *testing.T) {
	t.Run("satisfied", func(t *testing.T) {
		var v Verdict
		if err := json.Unmarshal([]byte(`{"satisfied": true, "gaps": []}`), &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !v.Satisfied || len(v.Gaps) != 0 {
			t.Fatalf("got %+v, want satisfied with no gaps", v)
		}
	})
	t.Run("not satisfied with gaps", func(t *testing.T) {
		var v Verdict
		if err := json.Unmarshal([]byte(`{"satisfied": false, "gaps": ["missing delta table", "no 2025 data"]}`), &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v.Satisfied || len(v.Gaps) != 2 {
			t.Fatalf("got %+v, want unsatisfied with 2 gaps", v)
		}
	})
}

func TestVerdictSchemaStrict(t *testing.T) {
	if !verdictResponseFormat.Strict {
		t.Fatal("verdict schema must be strict")
	}
	assertStrictObject(t, "verdict", verdictResponseFormat.Schema)
}

func TestNewCriticEnforcesVerdictSchema(t *testing.T) {
	c := NewCritic(nil, "test-model", "", nil)
	if c.responseFormat != &verdictResponseFormat {
		t.Fatal("critic must enforce the verdict response format")
	}
	if len(c.tools) != 0 {
		t.Fatalf("critic should be tools-light, got %d tools", len(c.tools))
	}
}
