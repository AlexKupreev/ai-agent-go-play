// Package openai adapts the OpenAI Chat Completions API to the vendor-neutral
// provider.Provider port. It is the only package in the tree that imports the
// OpenAI SDK; the kernel never sees these types.
package openai

import (
	"context"
	"fmt"
	"strings"

	"ai-agent-go-play/internal/provider"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// Client is an OpenAI-backed provider.Provider.
type Client struct {
	client oai.Client
}

// New builds an OpenAI provider from an API key.
func New(apiKey string) *Client {
	return &Client{client: oai.NewClient(option.WithAPIKey(apiKey))}
}

// Step runs one model turn against the Chat Completions API.
func (c *Client) Step(ctx context.Context, req provider.StepRequest) (provider.StepResponse, error) {
	params := oai.ChatCompletionNewParams{
		Model:    oai.ChatModel(req.Model),
		Messages: toMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		params.Tools = toTools(req.Tools)
		// parallel_tool_calls is only valid alongside tools.
		params.ParallelToolCalls = oai.Bool(req.ParallelToolCalls)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = oai.Int(req.MaxTokens)
	}
	if req.ResponseFormat != nil {
		params.ResponseFormat = toResponseFormat(*req.ResponseFormat)
	}

	resp, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return provider.StepResponse{}, err
	}
	if len(resp.Choices) == 0 {
		return provider.StepResponse{}, fmt.Errorf("openai: response had no choices")
	}

	choice := resp.Choices[0]
	return provider.StepResponse{
		Content: fromMessage(choice.Message),
		Stop:    mapStop(choice.FinishReason),
		Usage: provider.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			CachedTokens: resp.Usage.PromptTokensDetails.CachedTokens,
		},
	}, nil
}

// toMessages maps neutral messages to OpenAI message params.
func toMessages(msgs []provider.Message) []oai.ChatCompletionMessageParamUnion {
	out := make([]oai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleSystem:
			out = append(out, oai.SystemMessage(textOf(m)))
		case provider.RoleUser:
			out = append(out, oai.UserMessage(textOf(m)))
		case provider.RoleAssistant:
			am := oai.ChatCompletionAssistantMessageParam{}
			if t := textOf(m); t != "" {
				am.Content.OfString = oai.String(t)
			}
			for _, b := range m.Content {
				if b.Kind == provider.BlockToolCall && b.ToolCall != nil {
					am.ToolCalls = append(am.ToolCalls, oai.ChatCompletionMessageToolCallParam{
						ID: b.ToolCall.ID,
						Function: oai.ChatCompletionMessageToolCallFunctionParam{
							Name:      b.ToolCall.Name,
							Arguments: string(b.ToolCall.Input),
						},
					})
				}
			}
			out = append(out, oai.ChatCompletionMessageParamUnion{OfAssistant: &am})
		case provider.RoleTool:
			for _, b := range m.Content {
				if b.Kind == provider.BlockToolResult && b.ToolResult != nil {
					out = append(out, oai.ToolMessage(b.ToolResult.Output, b.ToolResult.CallID))
				}
			}
		}
	}
	return out
}

// fromMessage maps an OpenAI response message to neutral content blocks.
func fromMessage(m oai.ChatCompletionMessage) []provider.ContentBlock {
	var blocks []provider.ContentBlock
	if m.Content != "" {
		blocks = append(blocks, provider.ContentBlock{Kind: provider.BlockText, Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		blocks = append(blocks, provider.ContentBlock{
			Kind: provider.BlockToolCall,
			ToolCall: &provider.ToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: []byte(tc.Function.Arguments),
			},
		})
	}
	return blocks
}

func toTools(defs []provider.ToolDef) []oai.ChatCompletionToolParam {
	out := make([]oai.ChatCompletionToolParam, len(defs))
	for i, d := range defs {
		out[i] = oai.ChatCompletionToolParam{
			Function: oai.FunctionDefinitionParam{
				Name:        d.Name,
				Description: oai.String(d.Description),
				Parameters:  oai.FunctionParameters(d.InputSchema),
			},
		}
	}
	return out
}

func toResponseFormat(rf provider.ResponseFormat) oai.ChatCompletionNewParamsResponseFormatUnion {
	return oai.ChatCompletionNewParamsResponseFormatUnion{
		OfJSONSchema: &oai.ResponseFormatJSONSchemaParam{
			JSONSchema: oai.ResponseFormatJSONSchemaJSONSchemaParam{
				Name:        rf.Name,
				Description: oai.String(rf.Description),
				Strict:      oai.Bool(rf.Strict),
				Schema:      rf.Schema,
			},
		},
	}
}

func mapStop(finishReason string) provider.StopReason {
	switch finishReason {
	case "tool_calls", "function_call":
		return provider.StopToolCalls
	case "stop":
		return provider.StopEndTurn
	case "length":
		return provider.StopMaxTokens
	default:
		return provider.StopOther
	}
}

func textOf(m provider.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Kind == provider.BlockText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
