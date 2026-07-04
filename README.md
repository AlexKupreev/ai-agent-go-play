# ai-agent-go-play

Learn how to write an AI agent in golang. Written by a human.

It starts as a small ReAct CLI agent and is growing toward a **self-extending, provider-agnostic
assistant** that can author its own tools at runtime, for a small trusted user base on a
low-resource box.

- **[`docs/usage.md`](docs/usage.md) — how to run and operate the agent (start here to use it).**
- [`docs/environment.md`](docs/environment.md) — the runtime environment: config-dir vs workspace, trust tiers, prompt/agent-type customization, config/env/files reference.
- [`docs/design.md`](docs/design.md) — the concrete, Go-grounded design (decisions, current state, target architecture).
- [`docs/planning/plan.md`](docs/planning/plan.md) — the phased implementation plan and next steps.
- [`docs/security.md`](docs/security.md) — the threat→control map (capabilities, sandbox, audit, approvals).
- [`self-extending-agent-design.md`](self-extending-agent-design.md) — the implementation-agnostic vision and trade-off analysis.

## Requirements

- Go 1.25+
- An OpenAI API key

## Quickstart

```bash
# Download dependencies and build
go mod tidy
go build -o agent .          # or: go install .  (puts `agent` on ~/go/bin)

# Save your OpenAI API key (stored in ~/.config/ai-agent/config.json)
./agent config set-key sk-...

# Run a one-shot task
./agent run what is the current git status
```

To install system-wide, `go install .` and make sure `~/go/bin` is on your `PATH`
(`export PATH="$PATH:$HOME/go/bin"`).

## Ways to run

- **One-shot CLI** — `agent run <task>` runs a task in this process (a **planner** clarifies it,
  then an **executor** ReAct loop runs it), streaming progress to your terminal and prompting you
  on stdin for risky actions.
- **Interactive chat** — `agent chat` is a multi-turn REPL that retains conversation context
  across turns (`/reset`, `/exit`; `--plan` to run the planner per message). Add `--addr` to drive
  a running engine's **persistent session** instead — the conversation lives server-side, so it
  survives quitting and can be resumed here (`--session`) or from another client like Telegram.
- **Headless engine** — `agent serve` runs the executor as a local HTTP+SSE service; `agent client
  <task>` (and, optionally, a Telegram bot) drive it as peer clients, with risky actions parking
  for a remote approval. Good for unattended use.

```bash
./agent serve                      # terminal 1: start the engine
./agent client "run the tests"     # terminal 2: drive a run against it
```

Run two independent agents (different tools + memory) on one box by starting two `serve` processes
with separate config dirs and ports:

```bash
./agent --config-dir ~/.config/ai-agent/work serve --addr 127.0.0.1:8080
AI_AGENT_CONFIG_DIR=~/.config/ai-agent/home ./agent serve --addr 127.0.0.1:8081
```

Name each engine so you can address it by alias instead of `host:port` (`agent config
set-engine home 127.0.0.1:8081` → `agent client --addr home "…"`).

**Customize & experiment.** Shape the agent with `SYSTEM.md` / `AGENTS.md` prompt files and
`agents/*.md` sub-agent types, read from the config-dir (global) and workspace (project) — see
[`docs/environment.md`](docs/environment.md). Edit them and reload in place (`/reload` in `agent
chat`, `agent reload --addr` against a running engine), and compare configurations with `agent
eval <task> --models …` / `--variants file.yaml`.

**Named projects.** In conversation the agent can keep work as **projects** it recalls by intent
and switches into mid-chat (*"back to the articles from last time"*) via `list_projects` /
`create_project` / `switch_project`, stored under `<workspace>/projects/`. Use `--no-project` for
flat-repo mode (act on the checkout directly) or `--project <uid|title|path>` to open one at
launch — see [`docs/environment.md`](docs/environment.md#projects--named-recallable-workspaces).

The full command list, trust tiers, approvals, authored tools, memory, audit log, running multiple
agents, and the optional Telegram frontend are documented in **[`docs/usage.md`](docs/usage.md)**.

## How it works

A task first goes through a **planner** sub-agent that clarifies and refines it (asking you
questions if something is ambiguous), then an **executor** agent runs it via a ReAct loop
(Reason + Act):

1. Your task and available tools are sent to the LLM.
2. The LLM either calls a tool (shell, web search, run code, author a new tool, …) or gives a final answer.
3. Tool results are fed back to the LLM, which continues until the task is done.

Effectful actions are gated by a capability broker + trust tier and recorded to an append-only
audit log; destructive actions and escalations route through a human-approval gate. See
[`docs/security.md`](docs/security.md).

## Project structure

```text
main.go                    entry point
cmd/                       CLI commands (cobra): run, chat, serve, client, eval, reload, stop, config, tool, audit, usage
internal/
  agent/                   planner + executor ReAct loop, event observers
  tools/                   built-in tools, tool registry, author_tool, sandbox contract, approvals
  capability/              capability broker + trust tiers
  sandbox/                 gopher-lua sandbox for authored tools
  memory/                  long-term memory store
  selfdocs/                the agent's own docs, embedded for read_self_docs
  hoststat/                best-effort host resource snapshot (for the status tool)
  buildinfo/               build version (ldflags-stampable), for self-reporting
  provider/                LLM provider port (OpenAI adapter under provider/openai)
  audit/                   append-only audit log (+ reader)
  api/                     headless engine: HTTP+SSE transport, run lifecycle, approval queue, client
  frontend/telegram/       optional Telegram bot (peer client; go-telegram-bot-api)
  logger/                  per-run session transcripts
```
