package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, code string) string {
	t.Helper()
	tool := NewRunCode(2 * time.Second)
	out, err := tool.Run(context.Background(), map[string]any{"code": code})
	if err != nil {
		t.Fatalf("Run returned hard error: %v", err)
	}
	return out
}

func TestRunCode_Arithmetic(t *testing.T) {
	if got := run(t, "return 1 + 2 * 3"); got != "7" {
		t.Errorf("got %q, want 7", got)
	}
}

func TestRunCode_StringRaw(t *testing.T) {
	if got := run(t, `return "hello"`); got != "hello" {
		t.Errorf("got %q, want hello (unquoted)", got)
	}
}

func TestRunCode_Boolean(t *testing.T) {
	if got := run(t, "return 1 < 2"); got != "true" {
		t.Errorf("got %q, want true", got)
	}
}

func TestRunCode_Array(t *testing.T) {
	if got := run(t, "return {10, 20, 30}"); got != "[10,20,30]" {
		t.Errorf("got %q, want [10,20,30]", got)
	}
}

func TestRunCode_Map(t *testing.T) {
	got := run(t, `return {a = 1, b = 2}`)
	var m map[string]float64
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("result %q not JSON object: %v", got, err)
	}
	if m["a"] != 1 || m["b"] != 2 {
		t.Errorf("got %v, want a=1 b=2", m)
	}
}

func TestRunCode_GlobalResultFallback(t *testing.T) {
	if got := run(t, "result = 5 + 5"); got != "10" {
		t.Errorf("got %q, want 10", got)
	}
}

func TestRunCode_StringLib(t *testing.T) {
	if got := run(t, `return string.upper("abc")`); got != "ABC" {
		t.Errorf("got %q, want ABC", got)
	}
}

func TestRunCode_OSBlocked(t *testing.T) {
	got := run(t, "return os.time()")
	if !strings.Contains(got, "error") {
		t.Errorf("expected os access to error, got %q", got)
	}
}

func TestRunCode_IOBlocked(t *testing.T) {
	got := run(t, `return io.open("/etc/passwd")`)
	if !strings.Contains(got, "error") {
		t.Errorf("expected io access to error, got %q", got)
	}
}

func TestRunCode_ParseError(t *testing.T) {
	got := run(t, "this is not lua !!!")
	if !strings.Contains(got, "error") {
		t.Errorf("expected parse error, got %q", got)
	}
}

func TestRunCode_Timeout(t *testing.T) {
	tool := NewRunCode(150 * time.Millisecond)
	start := time.Now()
	out, err := tool.Run(context.Background(), map[string]any{"code": "while true do end"})
	if err != nil {
		t.Fatalf("Run returned hard error: %v", err)
	}
	if !strings.Contains(out, "error") {
		t.Errorf("expected timeout error, got %q", out)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}
