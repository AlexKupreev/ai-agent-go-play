package openai

import (
	"encoding/json"
	"testing"

	"ai-agent-go-play/internal/provider"

	oai "github.com/openai/openai-go"
)

// marshalUnion renders an OpenAI message param to its wire JSON as a generic map,
// so tests assert the shape the API actually receives.
func marshalUnion(t *testing.T, u oai.ChatCompletionMessageParamUnion) map[string]any {
	t.Helper()
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	return m
}

func TestToMessages_SystemUserTool(t *testing.T) {
	msgs := []provider.Message{
		provider.SystemText("be helpful"),
		provider.UserText("hello"),
		provider.ToolResultMessage("call_1", "42", false),
	}

	out := toMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3", len(out))
	}

	sys := marshalUnion(t, out[0])
	if sys["role"] != "system" || sys["content"] != "be helpful" {
		t.Errorf("system message wrong: %v", sys)
	}

	usr := marshalUnion(t, out[1])
	if usr["role"] != "user" || usr["content"] != "hello" {
		t.Errorf("user message wrong: %v", usr)
	}

	tool := marshalUnion(t, out[2])
	if tool["role"] != "tool" || tool["content"] != "42" || tool["tool_call_id"] != "call_1" {
		t.Errorf("tool message wrong: %v", tool)
	}
}

func TestToMessages_AssistantWithToolCalls(t *testing.T) {
	msg := provider.Message{
		Role: provider.RoleAssistant,
		Content: []provider.ContentBlock{
			{Kind: provider.BlockText, Text: "calling a tool"},
			{Kind: provider.BlockToolCall, ToolCall: &provider.ToolCall{
				ID: "call_9", Name: "shell", Input: json.RawMessage(`{"command":"ls"}`),
			}},
		},
	}

	out := toMessages([]provider.Message{msg})
	got := marshalUnion(t, out[0])

	if got["role"] != "assistant" {
		t.Fatalf("role = %v, want assistant", got["role"])
	}
	if got["content"] != "calling a tool" {
		t.Errorf("content = %v, want %q", got["content"], "calling a tool")
	}

	calls, ok := got["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %v, want one call", got["tool_calls"])
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call_9" || call["type"] != "function" {
		t.Errorf("tool call meta wrong: %v", call)
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "shell" || fn["arguments"] != `{"command":"ls"}` {
		t.Errorf("tool call function wrong: %v", fn)
	}
}

func TestToMessages_AssistantNoTextOmitsContent(t *testing.T) {
	// An assistant turn that is only a tool call must not send content:"".
	msg := provider.Message{
		Role: provider.RoleAssistant,
		Content: []provider.ContentBlock{
			{Kind: provider.BlockToolCall, ToolCall: &provider.ToolCall{
				ID: "c1", Name: "t", Input: json.RawMessage(`{}`),
			}},
		},
	}
	got := marshalUnion(t, toMessages([]provider.Message{msg})[0])
	if _, present := got["content"]; present {
		t.Errorf("content should be omitted when empty, got: %v", got["content"])
	}
}

func TestToTools(t *testing.T) {
	defs := []provider.ToolDef{{
		Name:        "shell",
		Description: "run a command",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"command": map[string]any{"type": "string"}},
			"required":   []string{"command"},
		},
	}}

	out := toTools(defs)
	if len(out) != 1 {
		t.Fatalf("got %d tools, want 1", len(out))
	}

	b, err := json.Marshal(out[0])
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}
	if m["type"] != "function" {
		t.Errorf("tool type = %v, want function", m["type"])
	}
	fn := m["function"].(map[string]any)
	if fn["name"] != "shell" || fn["description"] != "run a command" {
		t.Errorf("function meta wrong: %v", fn)
	}
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Errorf("parameters missing/wrong: %v", fn["parameters"])
	}
}

func TestFromMessage(t *testing.T) {
	m := oai.ChatCompletionMessage{
		Content: "here you go",
		ToolCalls: []oai.ChatCompletionMessageToolCall{{
			ID: "call_7",
			Function: oai.ChatCompletionMessageToolCallFunction{
				Name:      "web_search",
				Arguments: `{"q":"golang"}`,
			},
		}},
	}

	blocks := fromMessage(m)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Kind != provider.BlockText || blocks[0].Text != "here you go" {
		t.Errorf("text block wrong: %+v", blocks[0])
	}
	if blocks[1].Kind != provider.BlockToolCall || blocks[1].ToolCall == nil {
		t.Fatalf("tool-call block wrong: %+v", blocks[1])
	}
	tc := blocks[1].ToolCall
	if tc.ID != "call_7" || tc.Name != "web_search" || string(tc.Input) != `{"q":"golang"}` {
		t.Errorf("tool call wrong: %+v", tc)
	}
}

func TestFromMessage_EmptyContentNoTextBlock(t *testing.T) {
	m := oai.ChatCompletionMessage{
		ToolCalls: []oai.ChatCompletionMessageToolCall{{
			ID:       "c1",
			Function: oai.ChatCompletionMessageToolCallFunction{Name: "t", Arguments: "{}"},
		}},
	}
	blocks := fromMessage(m)
	if len(blocks) != 1 || blocks[0].Kind != provider.BlockToolCall {
		t.Errorf("empty content should yield only the tool-call block, got: %+v", blocks)
	}
}

func TestMapStop(t *testing.T) {
	cases := map[string]provider.StopReason{
		"stop":           provider.StopEndTurn,
		"tool_calls":     provider.StopToolCalls,
		"function_call":  provider.StopToolCalls,
		"length":         provider.StopMaxTokens,
		"content_filter": provider.StopOther,
		"":               provider.StopOther,
	}
	for in, want := range cases {
		if got := mapStop(in); got != want {
			t.Errorf("mapStop(%q) = %q, want %q", in, got, want)
		}
	}
}
