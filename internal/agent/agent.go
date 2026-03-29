package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/tools"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const maxIterations = 20
const defaultModel = "gpt-4o-mini"

const systemPrompt = `You are a helpful AI agent with access to a shell and the web.

When given a task:
1. Think through what steps are needed
2. Use tools to execute each step — shell for local operations, web_search to find information, web_fetch to read a specific page
3. Observe the output and adjust if something fails
4. Once done, provide a concise summary of what you did and the result

Always explain briefly what you're about to do before each tool call.`

type Agent struct {
	client  openai.Client // value type, not pointer — that's how this SDK works
	model   string
	verbose bool
	tools   []tools.Tool
	log     *logger.Logger
}

func New(apiKey string, workDir string, model string, verbose bool, log *logger.Logger) *Agent {
	if model == "" {
		model = defaultModel
	}
	return &Agent{
		client:  openai.NewClient(option.WithAPIKey(apiKey)),
		model:   model,
		verbose: verbose,
		tools:   []tools.Tool{tools.NewShell(workDir), tools.WebSearchDDG, tools.WebFetch},
		log:     log,
	}
}

func (a *Agent) Run(ctx context.Context, userInput string) error {
	a.log.LogStart(userInput)

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(userInput),
	}

	toolDefs := a.buildToolDefs()

	for i := range maxIterations {
		a.log.LogRequest(i, messages)

		start := time.Now()
		resp, err := a.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:              openai.ChatModel(a.model),
			Messages:           messages,
			Tools:              toolDefs,
			ParallelToolCalls:  openai.Bool(false),
		})
		if err != nil {
			return fmt.Errorf("OpenAI error: %w", err)
		}
		durationMs := time.Since(start).Milliseconds()

		choice := resp.Choices[0]
		a.log.LogResponse(i, choice.Message.Content, choice.Message.ToolCalls, resp.Usage, durationMs)

		if choice.Message.Content != "" {
			fmt.Println(choice.Message.Content)
		}

		if len(choice.Message.ToolCalls) == 0 {
			return nil
		}

		messages = append(messages, choice.Message.ToParam())

		for _, call := range choice.Message.ToolCalls {
			if a.verbose {
				fmt.Fprintf(os.Stderr, "\n[tool: %s] %s\n", call.Function.Name, call.Function.Arguments)
			}

			result, err := a.executeTool(ctx, call)
			if err != nil {
				result = fmt.Sprintf("tool error: %v", err)
			}

			if a.verbose {
				fmt.Fprintf(os.Stderr, "[result] %s\n", result)
			}
			a.log.LogToolResult(call.Function.Name, call.ID, call.Function.Arguments, result)

			messages = append(messages, openai.ToolMessage(result, call.ID))
		}
	}

	return fmt.Errorf("reached max iterations (%d) without a final answer", maxIterations)
}

func (a *Agent) executeTool(ctx context.Context, call openai.ChatCompletionMessageToolCall) (string, error) {
	for _, t := range a.tools {
		if t.Name == call.Function.Name {
			var args map[string]any
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return "", fmt.Errorf("invalid tool args: %w", err)
			}
			return t.Run(ctx, args)
		}
	}
	return "", fmt.Errorf("unknown tool: %s", call.Function.Name)
}

func (a *Agent) buildToolDefs() []openai.ChatCompletionToolParam {
	defs := make([]openai.ChatCompletionToolParam, len(a.tools))
	for i, t := range a.tools {
		required := make([]string, 0, len(t.Parameters))
		for name := range t.Parameters {
			required = append(required, name)
		}

		defs[i] = openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters: openai.FunctionParameters{
					"type":       "object",
					"properties": t.Parameters,
					"required":   required,
				},
			},
		}
	}
	return defs
}
