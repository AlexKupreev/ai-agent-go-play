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

const executorPrompt = `You are a helpful AI agent with access to a shell and the web.

When given a task:
1. Think through what steps are needed
2. Use tools to execute each step — shell for local operations, web_search to find information, web_fetch to read a specific page
3. Observe the output and adjust if something fails
4. Once done, provide a concise summary of what you did and the result

Always explain briefly what you're about to do before each tool call.`

const plannerPrompt = `You are a planning agent. Your job is to clarify and refine a task before any execution happens. You do NOT execute the task yourself.

When given a task:
1. Check for typos, ambiguous names, or unclear references — if something looks misspelled or could refer to multiple things, use ask_user to confirm before proceeding
2. Identify anything that cannot be resolved without human input (e.g. preferences, credentials, target environment)
3. Use web_search or web_fetch only to resolve technical ambiguity (e.g. confirming an API name, a package name) — never to answer the task itself
4. Once everything is clear, output a single refined task description that an execution agent can act on without further questions

Rules:
- Never answer or partially complete the task — your only output is a refined task description
- When in doubt about a name or term, ask the user rather than assuming
- Your final response must be the refined task description only, with no preamble or explanation`

type Agent struct {
	client       openai.Client // value type, not pointer — that's how this SDK works
	model        string
	verbose      bool
	systemPrompt string
	tools        []tools.Tool
	log          *logger.Logger
}

func newAgent(apiKey, model, systemPrompt string, verbose bool, agentTools []tools.Tool, log *logger.Logger) *Agent {
	if model == "" {
		model = defaultModel
	}
	return &Agent{
		client:       openai.NewClient(option.WithAPIKey(apiKey)),
		model:        model,
		verbose:      verbose,
		systemPrompt: systemPrompt,
		tools:        agentTools,
		log:          log,
	}
}

// NewExecutor creates an agent that executes tasks using shell and web tools.
func NewExecutor(apiKey, workDir, model string, verbose bool, log *logger.Logger) *Agent {
	return newAgent(apiKey, model, executorPrompt, verbose, []tools.Tool{
		tools.NewShell(workDir),
		tools.WebSearchDDG,
		tools.WebFetch,
	}, log)
}

// NewPlanner creates an agent that clarifies and refines a task before execution.
// It has no shell access — only web research and the ability to ask the user questions.
func NewPlanner(apiKey, model string, verbose bool, log *logger.Logger) *Agent {
	return newAgent(apiKey, model, plannerPrompt, verbose, []tools.Tool{
		tools.WebSearchDDG,
		tools.WebFetch,
		tools.AskUser,
	}, log)
}

// Run executes the ReAct loop and returns the final text answer.
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	a.log.LogStart(userInput)

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(a.systemPrompt),
		openai.UserMessage(userInput),
	}

	toolDefs := a.buildToolDefs()

	for i := range maxIterations {
		a.log.LogRequest(i, messages)

		start := time.Now()
		resp, err := a.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:             openai.ChatModel(a.model),
			Messages:          messages,
			Tools:             toolDefs,
			ParallelToolCalls: openai.Bool(false),
		})
		if err != nil {
			return "", fmt.Errorf("OpenAI error: %w", err)
		}
		durationMs := time.Since(start).Milliseconds()

		choice := resp.Choices[0]
		a.log.LogResponse(i, choice.Message.Content, choice.Message.ToolCalls, resp.Usage, durationMs)

		if len(choice.Message.ToolCalls) == 0 {
			return choice.Message.Content, nil
		}

		if a.verbose && choice.Message.Content != "" {
			fmt.Fprintln(os.Stderr, choice.Message.Content)
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

	return "", fmt.Errorf("reached max iterations (%d) without a final answer", maxIterations)
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
