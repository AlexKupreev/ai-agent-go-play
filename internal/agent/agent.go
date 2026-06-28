package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

const maxIterations = 20
const defaultModel = "gpt-4o-mini"

const executorPrompt = `You are a helpful AI agent with access to a shell and the web.

When given a task:
1. Think through what steps are needed
2. Use tools to execute each step — shell for local operations, run_code for calculations and data shaping (sandboxed Lua), web_search to find information, web_fetch to read a specific page
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
	provider       provider.Provider
	model          string
	verbose        bool
	systemPrompt   string
	responseFormat *provider.ResponseFormat
	tools          []tools.Tool
	log            *logger.Logger
}

func newAgent(p provider.Provider, model, systemPrompt string, verbose bool, agentTools []tools.Tool, log *logger.Logger) *Agent {
	if model == "" {
		model = defaultModel
	}
	return &Agent{
		provider:     p,
		model:        model,
		verbose:      verbose,
		systemPrompt: systemPrompt,
		tools:        agentTools,
		log:          log,
	}
}

// NewExecutor creates an agent that executes tasks using shell, code, and web tools.
func NewExecutor(p provider.Provider, workDir, model string, verbose bool, log *logger.Logger) *Agent {
	return newAgent(p, model, executorPrompt, verbose, []tools.Tool{
		tools.NewShell(workDir, tools.StdinConfirm),
		tools.NewRunCode(5 * time.Second),
		tools.WebSearchDDG,
		tools.WebFetch,
	}, log)
}

// NewPlanner creates an agent that clarifies and refines a task before execution.
// It has no shell access — only web research and the ability to ask the user questions.
// Its final response is a structured Plan enforced via JSON schema.
func NewPlanner(p provider.Provider, model string, verbose bool, log *logger.Logger) *Agent {
	a := newAgent(p, model, plannerPrompt, verbose, []tools.Tool{
		tools.WebSearchDDG,
		tools.WebFetch,
		tools.AskUser,
	}, log)
	a.responseFormat = &planResponseFormat
	return a
}

// Run executes the ReAct loop and returns the final text answer.
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	a.log.LogStart(userInput)

	messages := []provider.Message{
		provider.SystemText(a.systemPrompt),
		provider.UserText(userInput),
	}

	toolDefs := a.buildToolDefs()

	for i := range maxIterations {
		a.log.LogRequest(i, messages)

		start := time.Now()
		resp, err := a.provider.Step(ctx, provider.StepRequest{
			Model:          a.model,
			Messages:       messages,
			Tools:          toolDefs,
			ResponseFormat: a.responseFormat,
		})
		if err != nil {
			return "", fmt.Errorf("provider error: %w", err)
		}
		durationMs := time.Since(start).Milliseconds()

		text := resp.Text()
		toolCalls := resp.ToolCalls()
		a.log.LogResponse(i, text, toolCalls, resp.Usage, durationMs)

		if len(toolCalls) == 0 {
			return text, nil
		}

		if a.verbose && text != "" {
			fmt.Fprintln(os.Stderr, text)
		}

		// Append the assistant turn (text + tool calls) before its results.
		messages = append(messages, provider.Message{Role: provider.RoleAssistant, Content: resp.Content})

		for _, call := range toolCalls {
			if a.verbose {
				fmt.Fprintf(os.Stderr, "\n[tool: %s] %s\n", call.Name, string(call.Input))
			}

			result, err := a.executeTool(ctx, call)
			if err != nil {
				result = fmt.Sprintf("tool error: %v", err)
			}

			if a.verbose {
				fmt.Fprintf(os.Stderr, "[result] %s\n", result)
			}
			a.log.LogToolResult(call.Name, call.ID, string(call.Input), result)

			messages = append(messages, provider.ToolResultMessage(call.ID, result, err != nil))
		}
	}

	return "", fmt.Errorf("reached max iterations (%d) without a final answer", maxIterations)
}

func (a *Agent) executeTool(ctx context.Context, call provider.ToolCall) (string, error) {
	for _, t := range a.tools {
		if t.Name == call.Name {
			args := map[string]any{}
			if len(call.Input) > 0 {
				if err := json.Unmarshal(call.Input, &args); err != nil {
					return "", fmt.Errorf("invalid tool args: %w", err)
				}
			}
			return t.Run(ctx, args)
		}
	}
	return "", fmt.Errorf("unknown tool: %s", call.Name)
}

func (a *Agent) buildToolDefs() []provider.ToolDef {
	defs := make([]provider.ToolDef, len(a.tools))
	for i, t := range a.tools {
		required := t.Required
		if required == nil {
			// Default: all parameters required. Sorted because map iteration
			// order is random — an unstable schema would vary the tool defs
			// between runs and defeat provider prompt caching.
			required = make([]string, 0, len(t.Parameters))
			for name := range t.Parameters {
				required = append(required, name)
			}
			sort.Strings(required)
		}

		defs[i] = provider.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": t.Parameters,
				"required":   required,
			},
		}
	}
	return defs
}
