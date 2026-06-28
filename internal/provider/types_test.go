package provider

import (
	"encoding/json"
	"testing"
)

func TestStepResponseText(t *testing.T) {
	r := StepResponse{Content: []ContentBlock{
		{Kind: BlockText, Text: "hello "},
		{Kind: BlockToolCall, ToolCall: &ToolCall{Name: "x"}},
		{Kind: BlockText, Text: "world"},
	}}
	if got := r.Text(); got != "hello world" {
		t.Errorf("Text() = %q, want %q", got, "hello world")
	}
}

func TestStepResponseToolCalls(t *testing.T) {
	r := StepResponse{Content: []ContentBlock{
		{Kind: BlockText, Text: "thinking"},
		{Kind: BlockToolCall, ToolCall: &ToolCall{ID: "a", Name: "one", Input: json.RawMessage(`{}`)}},
		{Kind: BlockToolCall, ToolCall: &ToolCall{ID: "b", Name: "two", Input: json.RawMessage(`{"k":1}`)}},
	}}
	calls := r.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(calls))
	}
	if calls[0].Name != "one" || calls[1].Name != "two" {
		t.Errorf("tool call order/names wrong: %+v", calls)
	}
}

func TestMessageConstructors(t *testing.T) {
	if m := SystemText("s"); m.Role != RoleSystem || len(m.Content) != 1 || m.Content[0].Text != "s" {
		t.Errorf("SystemText wrong: %+v", m)
	}
	if m := UserText("u"); m.Role != RoleUser || m.Content[0].Text != "u" {
		t.Errorf("UserText wrong: %+v", m)
	}
	m := ToolResultMessage("call_3", "out", true)
	if m.Role != RoleTool || m.Content[0].ToolResult == nil {
		t.Fatalf("ToolResultMessage wrong: %+v", m)
	}
	tr := m.Content[0].ToolResult
	if tr.CallID != "call_3" || tr.Output != "out" || !tr.IsError {
		t.Errorf("tool result wrong: %+v", tr)
	}
}
