// Package provider defines the vendor-neutral seam between the agent kernel and
// any LLM vendor. The kernel speaks only these types; one adapter per vendor
// (see internal/provider/openai) maps them to that vendor's SDK/wire format.
//
// This is the "portable subset" across vendors: chat + function/tool calling.
package provider

import (
	"encoding/json"
	"strings"
)

// Role is the author of a message in the conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// BlockKind tags a ContentBlock's variant.
type BlockKind string

const (
	BlockText       BlockKind = "text"
	BlockToolCall   BlockKind = "tool_call"   // model -> us
	BlockToolResult BlockKind = "tool_result" // us -> model
)

// ToolCall is a request from the model to invoke a tool.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"` // raw JSON arguments, as emitted by the model
}

// ToolResult is the outcome of executing a ToolCall, fed back to the model.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

// ContentBlock is one piece of a message: text, a tool call, or a tool result.
// Exactly one of the pointer fields is set, selected by Kind.
type ContentBlock struct {
	Kind       BlockKind   `json:"kind"`
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
}

// Message is one turn in the conversation.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// SystemText builds a system message from plain text.
func SystemText(s string) Message {
	return Message{Role: RoleSystem, Content: []ContentBlock{{Kind: BlockText, Text: s}}}
}

// UserText builds a user message from plain text.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{{Kind: BlockText, Text: s}}}
}

// AssistantText builds an assistant message from plain text.
func AssistantText(s string) Message {
	return Message{Role: RoleAssistant, Content: []ContentBlock{{Kind: BlockText, Text: s}}}
}

// ToolResultMessage builds a tool-role message carrying a single result.
func ToolResultMessage(callID, output string, isErr bool) Message {
	return Message{Role: RoleTool, Content: []ContentBlock{{
		Kind:       BlockToolResult,
		ToolResult: &ToolResult{CallID: callID, Output: output, IsError: isErr},
	}}}
}

// ToolDef is a tool as the model sees it: name + description + JSON-Schema for
// inputs. (Separate from how the tool is executed.)
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"` // full JSON-Schema object
}

// ResponseFormat requests structured (JSON-schema-constrained) output.
type ResponseFormat struct {
	Name        string
	Description string
	Schema      map[string]any
	Strict      bool
}

// Usage reports token accounting for one step.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
}

// StopReason is why the model stopped generating, normalized across vendors.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolCalls StopReason = "tool_calls"
	StopMaxTokens StopReason = "max_tokens"
	StopOther     StopReason = "other"
)

// StepRequest is one model turn. The kernel owns the loop; the provider owns one step.
type StepRequest struct {
	Model             string
	Messages          []Message
	Tools             []ToolDef
	ResponseFormat    *ResponseFormat // nil unless structured output is wanted
	MaxTokens         int64           // 0 = let the provider default
	ParallelToolCalls bool            // false = one tool call per step
}

// StepResponse is the result of one model turn.
type StepResponse struct {
	Content []ContentBlock // text and/or one-or-more tool-call blocks
	Stop    StopReason
	Usage   Usage
}

// Text returns the concatenated text blocks of the response.
func (r StepResponse) Text() string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Kind == BlockText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// ToolCalls returns the tool-call blocks of the response.
func (r StepResponse) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, c := range r.Content {
		if c.Kind == BlockToolCall && c.ToolCall != nil {
			calls = append(calls, *c.ToolCall)
		}
	}
	return calls
}
