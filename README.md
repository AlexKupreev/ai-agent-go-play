# ai-agent-go-play

Learn how to write an AI agent in golang. Written by a human.

It starts as a small ReAct CLI agent and is growing toward a **self-extending, provider-agnostic
assistant** that can author its own tools at runtime, for a small trusted user base on a
low-resource box. See the design docs:

- [`docs/design.md`](docs/design.md) — the concrete, Go-grounded design for this repo (decisions, current state, target architecture).
- [`docs/plan.md`](docs/plan.md) — the phased implementation plan and next steps.
- [`self-extending-agent-design.md`](self-extending-agent-design.md) — the implementation-agnostic vision and trade-off analysis.

## Requirements

- Go 1.25+
- An OpenAI API key

## Setup

```bash
# Download dependencies
go mod tidy

# Save your OpenAI API key (stored in ~/.config/ai-agent/config.json)
go run . config set-key sk-...
```

## Running

```bash
# Run directly without building (slower, recompiles every time)
go run . run list all go files in this project
```

Or build a persistent binary first:

```bash
# Build a binary in the current directory
go build -o agent .
./agent run what is the current git status
```

To install system-wide so you can run `agent` from anywhere:

```bash
# Installs the binary to ~/go/bin
go install .

agent run what is the current git status
```

Make sure `~/go/bin` is on your PATH. If `agent` is not found after install, add this to your `~/.bashrc` or `~/.zshrc`:

```bash
export PATH="$PATH:$HOME/go/bin"
```

Then reload: `source ~/.bashrc`

## How it works

A task first goes through a **planner** sub-agent that clarifies and refines it (asking you
questions if something is ambiguous), then an **executor** agent runs it via a ReAct loop
(Reason + Act):

1. Your task and available tools are sent to the LLM
2. The LLM either calls a tool (e.g. runs a shell command, searches the web) or gives a final answer
3. Tool results are fed back to the LLM, which continues until the task is done

Tool calls and their output are printed to stderr so they don't pollute the final answer on stdout.

## Project structure

```text
main.go                  entry point
cmd/
  root.go                CLI wiring (cobra)
  config.go              `agent config set-key` command
  run.go                 `agent run` command
internal/
  agent/agent.go         ReAct loop
  tools/tools.go         Tool interface
  tools/shell.go         Shell execution tool
```
