# Usage — operating the agent

A practical guide to running and operating the agent day-to-day. For *why* it is built
this way, see [`design.md`](design.md) and [`security.md`](security.md); this file is
the *how*.

- [Install & configure](#install--configure)
- [Two ways to run](#two-ways-to-run)
- [Command reference](#command-reference)
- [Trust tiers — the safety dial](#trust-tiers--the-safety-dial)
- [Approvals — how risky actions are gated](#approvals--how-risky-actions-are-gated)
- [Self-authored tools](#self-authored-tools)
- [Long-term memory](#long-term-memory)
- [Self-documentation](#self-documentation)
- [Audit log](#audit-log)
- [Token usage](#token-usage)
- [Conversations over the API (sessions)](#conversations-over-the-api-sessions)
- [Telegram frontend (optional)](#telegram-frontend-optional)
- [Running multiple independent agents](#running-multiple-independent-agents)
  - [Engine aliases](#engine-aliases)
- [Configuration & environment reference](#configuration--environment-reference)
- [Files on disk](#files-on-disk)

---

## Install & configure

Requirements: **Go 1.25+** and an **OpenAI API key**.

```bash
go build -o agent .          # or: go install .  (puts `agent` on ~/go/bin)
./agent config set-key sk-...
```

Optional defaults (both overridable per-run with `--model` / `--tier`):

```bash
./agent config set-model gpt-4o        # default model (built-in default: gpt-4o-mini)
./agent config set-tier balanced       # default trust tier (built-in default: balanced)
```

Everything is stored under `~/.config/ai-agent/` — see [Files on disk](#files-on-disk).

---

## Two ways to run

The engine is the same in both cases; only the front door differs.

### 1. One-shot CLI — `agent run`

Runs a single task in the current process and streams progress to your terminal. The
task first goes through a **planner** (which may ask you clarifying questions on stdin),
then an **executor** ReAct loop. Risky actions prompt you inline on stdin.

```bash
./agent run summarize the go files in this repo
```

Best for interactive, at-the-keyboard use.

### 2. Headless engine — `agent serve` + clients

`agent serve` runs the executor as a long-lived HTTP+SSE service bound to
`127.0.0.1:8080`. Other processes drive it as **peer clients** — the CLI (`agent
client`), and (optionally) the Telegram bot. Risky actions **park** for an approval that
can be answered remotely, so runs work unattended.

```bash
# terminal 1 — start the engine
./agent serve

# terminal 2 — drive a run against it
./agent client "check disk usage and report the biggest dirs"
```

`serve` runs the executor directly (no interactive planner — there is no stdin to ask
on). Best for unattended / mobile / multi-frontend use.

---

## Command reference

Run `./agent <command> --help` for the authoritative flag list. Summary:

| Command | What it does |
| --- | --- |
| `agent run <task>` | One-shot run in this process (planner + executor), stdin approvals. |
| `agent chat` | Interactive multi-turn REPL, retained context. Local by default; `--addr` drives a `serve` engine's persistent session. |
| `agent serve` | Start the headless HTTP+SSE engine. |
| `agent client <task>` | Start & stream a run on a running engine; prompts for approvals. |
| `agent stop <run-id>` | Cancel a run on a running engine (kill switch). |
| `agent audit` | Browse the engine's audit log. |
| `agent tool list` | List persisted, agent-authored tools. |
| `agent tool revoke <name>` | Remove an authored tool. |
| `agent config set-key\|set-model\|set-tier` | Save API key / default model / default tier. |
| `agent config set-engine\|rm-engine\|engines` | Name an engine address as an alias for `--addr` (and list/remove them). |

Every command that talks to a running engine takes `--addr`, which accepts a literal
`host:port` **or** an alias saved with `agent config set-engine` (see [engine
aliases](#engine-aliases)).

### `agent run <task>`

```bash
./agent run --model gpt-4o --tier safe delete the build cache
./agent run --verbose <task>     # also print tool calls + results to stderr
```

Flags: `--model`, `--tier` (override config for this run), `--verbose`.

### `agent chat`

An interactive REPL — type a message, get a reply, keep going, with the **conversation
history retained across turns** (like a chat CLI). It has two modes: **local** (the default)
and **remote** (`--addr`).

**Local mode** runs the executor in *this* process. The tool catalog and long-term memory are
shared with the rest of this agent (scoped by `--config-dir`). Its audit trail, though, goes
to the **per-run transcript** (`<sessions-dir>/<run-id>/audit.jsonl`), not the process-wide
`audit.jsonl` that only `agent serve` writes and `agent audit` reads — see [Audit log](#audit-log).

```bash
./agent chat                 # executor-only conversation
./agent chat --plan          # refine each message through the planner first (experimental)
./agent chat --tier safe     # per-session model/tier, same flags as `run`
```

- **Commands:** `/reset` clears the conversation and starts fresh; `/exit` (or Ctrl-D)
  quits. **Ctrl-C** cancels the *current* turn and returns you to the prompt.
- **Planner toggle (`--plan`):** off by default (a straight conversation). When on, each
  message is refined by the planner before execution, exactly like `agent run` — useful
  for one-shot-style tasks, heavier for back-and-forth chat. It's experimental; try both.
- The agent's tool activity is streamed to stderr; each turn's final answer goes to stdout.

**Remote mode (`--addr`)** drives a running `agent serve` engine as a peer client instead of
an in-process executor. The conversation is now a **persistent, server-side [session](#conversations-over-the-api-sessions)**:
it survives quitting and can be resumed here (`--session`) or from another client — this is
the SSH → Telegram continuity the local mode can't give (local history lives in one process).
Each line is a turn on the engine; commands run where the engine runs. `--addr` takes a
`host:port` or an [alias](#engine-aliases).

```bash
./agent chat --addr 127.0.0.1:8080          # start a new session on the engine
./agent chat --addr home                    # same, addressing the engine by alias
./agent chat --addr home --list             # list resumable sessions, then exit
./agent chat --addr home --session <id>     # resume an existing conversation
```

- **Commands (remote):** `/reset` starts a fresh session (closing the old one); `/end`
  closes the current session; `/exit` (or Ctrl-D) **detaches** and leaves it resumable
  (the resume command is printed on exit). **Ctrl-C** cancels the current turn (stopping the
  remote run) and returns you to the prompt.
- Approvals are the engine's **shared** queue: an escalation you don't answer here is the
  same one visible to `agent audit`, other clients, and Telegram — not a local stdin gate.
- `--model` / `--tier` / `--plan` are **local-mode only**; in remote mode those are fixed by
  how `serve` was started.

### `agent serve`

```bash
./agent serve --addr 127.0.0.1:8080 --model gpt-4o --tier balanced
```

Flags: `--addr` (listen address; keep it on `127.0.0.1`), `--model`, `--tier`. Prints a
couple of `curl` snippets on startup. Shares one tool catalog, one memory store, and one
process-wide audit log across all runs it serves.

### `agent client <task>` / `agent stop <run-id>`

```bash
./agent client --addr 127.0.0.1:8080 "run the tests and summarize failures"
./agent stop --addr 127.0.0.1:8080 <run-id>
```

`client` starts a run, streams its events to your terminal, and polls the engine for
parked approvals to prompt you. **Ctrl+C** cancels the *remote* run and detaches. `stop`
is the same kill switch as a `POST /runs/{id}/cancel`.

### `agent audit`

See [Audit log](#audit-log).

### `agent tool list` / `agent tool revoke <name>`

See [Self-authored tools](#self-authored-tools).

---

## Trust tiers — the safety dial

The tier decides which capabilities an agent-authored tool may use **without asking
you**. It is the main autonomy control — set it conservative when the agent runs
unattended, looser when you are watching.

| Tier | Auto-approves | Prompts for |
| --- | --- | --- |
| `safe` | nothing | every capability |
| `balanced` *(default)* | side-effect-free reads (clock, random, read file) | writes, network fetches, calling other tools |
| `permissive` | everything | nothing *(full autonomy — use only when watched)* |

The tier only decides whether a human must say yes first; a tool's own allowlist
(hosts / paths / callable tools) still bounds what it can touch regardless of tier. Set
it per run with `--tier`, or as a default with `agent config set-tier`.

---

## Approvals — how risky actions are gated

Some actions always route through a human-approval gate: **destructive shell commands**
and **capability escalations** beyond the current tier. How the prompt reaches you
depends on the front door:

- **`agent run`** — prompts inline on stdin (`proceed? [y/N]`).
- **`agent serve` + `agent client`** — the action *parks* on the engine; `client` polls
  and prompts you in the terminal, then sends the decision back.
- **`agent serve` + Telegram** — the escalation is pushed onto the run's event stream and
  rendered as an **Approve / Deny** inline keyboard.

Under the hood every parked request is also visible at `GET /approvals` and resolvable
with `POST /approvals/{id}` (`{"approved": true|false}`), and it is emitted onto the
run's SSE stream as `approval_requested` / `approval_resolved`. A cancelled or abandoned
run never executes the gated action.

---

## Self-authored tools

The agent can write new tools for itself at runtime (`author_tool`): it validates the
spec, runs it past the approval gate (per the current tier), smoke-tests it in the
sandbox, and registers it. Persisted tools live in `~/.config/ai-agent/tools.json` and
are available to later runs.

Manage them:

```bash
./agent tool list                      # name, version, scope, #caps, description
./agent tool revoke reverse_string     # remove from the local catalog
```

Against a running engine, revoke over the API so the *live* engine (and its audit log)
reflect it, not just the on-disk file:

```bash
./agent tool revoke reverse_string --addr 127.0.0.1:8080
```

Over the API you can also inspect a tool's full source: `GET /tools`, `GET
/tools/search?q=`, `GET /tools/{name}` (includes implementation source), `DELETE
/tools/{name}`.

---

## Long-term memory

The agent has `remember` / `recall` built-ins backed by a persistent store at
`~/.config/ai-agent/memory.json`. A fact remembered in one run is recallable by later
runs. Every write is audited (`memory_write`). There is no CLI surface for it yet — it is
driven by the agent during a run.

---

## Self-documentation

The agent can read its **own** documentation to answer questions about how it works — its
tools, trust tiers, approvals, memory, and APIs — instead of guessing. The docs are
**embedded in the binary** (`go:embed`), so this works regardless of the working directory
and on a deployed box where the repo isn't present, via a `read_self_docs` built-in.

The embedded set is the **reference docs** (README + `docs/*.md`: usage, design, security,
tools, memory, api-transport — how it works *now*) plus the **vision doc**
(`self-extending-agent-design.md` — design intent and trade-offs). Docs are tagged: the agent
treats `[reference]` as authoritative about current behavior and `[vision]` as design intent
that may include not-yet-built ideas. **Planning/scratchpad docs** (`docs/planning/`) are
deliberately *not* embedded, so the agent never mistakes the roadmap for a current capability.

There's no separate CLI for it — it's a tool the agent uses during a run (e.g. ask it "how do
your trust tiers work?" and it reads `usage.md` rather than guessing). `read_self_docs` is
read-only, trusted, and not exposed to sandboxed authored tools.

---

## Audit log

Everything effectful is recorded to an append-only audit log: capability use
(`capability_exercised` / `capability_denied`), tool authoring/revocation
(`tool_authored` / `tool_revoked`), memory writes (`memory_write`), and per-run token
usage (`run_usage` — see [Token usage](#token-usage)).

Under `agent serve` there is **one process-wide log** at
`~/.config/ai-agent/audit.jsonl`, browsable over the API:

```bash
./agent audit --addr 127.0.0.1:8080                     # all events, oldest first
./agent audit --type tool_revoked                       # filter by event type
./agent audit --run <run-id> --limit 50                 # last 50 events for one run
```

Flags: `--addr`, `--run`, `--type`, `--limit` (0 = all). Each `agent run` / `serve` run
also keeps its own transcript under `~/.local/share/ai-agent/sessions/<run-id>/`.

---

## Token usage

Every run reports how many tokens it spent. **Tokens only — no cost** (a price table would
go stale; compute cost externally from these counts if you need it).

- **`agent run`** and **`agent chat`** print a summary line to stderr at the end of each
  run/turn:

  ```text
  · 12,431 in / 3,210 out (1,024 cached) · 4 steps · 6.2s
  ```

  In chat it's the turn's usage, followed by the running session total. `in`/`out` are
  input/output tokens; `cached` (shown only when non-zero) is the cached-input portion of
  `in`; `steps` is the number of model calls.
- **`agent client`** and **`agent chat --addr`** print the same line after a run/turn,
  read from the engine.
- **Over the API:** `GET /runs/{id}` (and `agent client`'s underlying `RunStatus`) include
  a `usage` object and `steps` count, populated when the run ends.
- **In the audit log:** each completed run (and session turn — a turn is a run) records a
  `run_usage` event, so token spend is reviewable historically:

  ```bash
  ./agent audit --type run_usage --addr 127.0.0.1:8080
  ```

Totals are the sum of the per-step usage the model reports; the per-step numbers are also
in each run's transcript (`run.jsonl`).

---

## Conversations over the API (sessions)

`agent chat` keeps context in one local process. To have a **multi-turn conversation
against `agent serve`** — retained across turns, surviving restarts, and resumable from
any frontend — the engine exposes *sessions*. A session is a persisted conversation; a
*turn* is a run whose executor is seeded with the session's history, so it streams and
parks approvals exactly like any run.

The ergonomic way to drive a session from the terminal is **[`agent chat --addr`](#agent-chat)**
(start / `--list` / `--session <id>` to resume). The raw HTTP surface below is what it (and
the Telegram bot) sit on:

```bash
# create a session
curl -s -XPOST http://127.0.0.1:8080/sessions            # → {"session_id":"…"}
# run a turn; stream its reply on the returned run id
curl -s -XPOST http://127.0.0.1:8080/sessions/<sid>/turns -d '{"text":"hello"}'   # → {"run_id":"…"}
curl -N  http://127.0.0.1:8080/runs/<run_id>/events
# list / terminate
curl -s http://127.0.0.1:8080/sessions
curl -XDELETE http://127.0.0.1:8080/sessions/<sid>
```

Sessions persist as one JSON file per session under `<config-dir>/sessions/`, so they
survive a `serve` restart and are shared by every client of that engine (this is the
mechanism behind the Telegram bot's per-chat conversations, and how the same conversation
could later be resumed from a different device). The stored history is the conversation
only — the system prompt is re-seeded from current code each turn.

## Telegram frontend (optional)

A Telegram bot can act as a peer client of the engine. Each chat maps to a **session**
(a persistent conversation, below): a message runs a turn with retained context, events
stream back, and a parked approval becomes an Approve/Deny inline keyboard. Chat commands:
**`/new`** starts a fresh session, **`/end`** terminates the current one (a session is also
created automatically on your first message). It is **entirely optional**: with no token
set, the engine runs exactly as normal.

Enable it by supplying a token (config *or* env; env wins) plus an allowlist of Telegram
user ids that may drive the engine:

```bash
# via env (takes precedence over config)
export AI_AGENT_TELEGRAM_TOKEN=123456:abcdef
export AI_AGENT_TELEGRAM_ALLOWED_USERS=11111111,22222222
./agent serve
```

There is no `config set-` command for these yet; to persist them, add `telegram_token`
(string) and `telegram_allowed_users` (array of ints) to `~/.config/ai-agent/config.json`
directly.

Security model: **auth lives in the bot** — the engine trusts localhost and stays bound
to `127.0.0.1`; only the bot faces the network. The allowlist is **fail-closed** (empty ⇒
the bot rejects everyone). The bot only needs *outbound* network access to
`api.telegram.org` (it long-polls Telegram; nothing needs to reach into your box).

The live transport uses the Telegram Bot API (`github.com/go-telegram-bot-api/telegram-bot-api/v5`)
and long-polls for updates, so it needs outbound access to `api.telegram.org`. If the token
is rejected or unreachable, `serve` logs `telegram: connect: … — running without the bot`
and the engine runs normally; the Bot API handshake happens in the background so it never
delays startup.

---

## Running multiple independent agents

Each agent's state — config, tool catalog, memory, and audit log — lives under one
**config directory** (default `~/.config/ai-agent`). Point separate `agent` invocations
at separate config directories and they share nothing: different tools, different
memory, different audit trail.

The directory is set (in precedence order) by the global `--config-dir` flag, the
`AI_AGENT_CONFIG_DIR` env var, or the default. To run two independent agents on one
box, start two `serve` processes on two ports, each with its own config dir:

```bash
# agent "work"
./agent --config-dir ~/.config/ai-agent/work serve --addr 127.0.0.1:8080 --tier safe

# agent "home" (env form)
AI_AGENT_CONFIG_DIR=~/.config/ai-agent/home ./agent serve --addr 127.0.0.1:8081 --tier balanced
```

Then address each by port: `./agent client --addr 127.0.0.1:8080 "..."`. Configure each
one separately by passing the same `--config-dir` to `config`/`tool`/`audit`:

```bash
./agent --config-dir ~/.config/ai-agent/work config set-key sk-...
./agent --config-dir ~/.config/ai-agent/work tool list
```

Per-run **transcripts** default to a shared `~/.local/share/ai-agent/sessions/` (each run
in its own uniquely-named subdir, so they never collide). To keep each agent's
transcripts separate too, give each its own `--sessions-dir` (or `AI_AGENT_SESSIONS_DIR`):

```bash
./agent --config-dir ~/.config/ai-agent/work \
        --sessions-dir ~/.local/share/ai-agent/work/sessions \
        serve --addr 127.0.0.1:8080
```

They run as separate OS processes, so they're isolated at every level (state *and* crash
domain) and you can restart one without touching the other.

### Engine aliases

Once you have more than one engine (or just don't want to type `127.0.0.1:8081`), name
each address so `--addr` can take the name instead:

```bash
./agent config set-engine work 127.0.0.1:8080
./agent config set-engine home 127.0.0.1:8081
./agent config engines                 # list them
./agent config rm-engine work          # remove one
```

Then any engine-facing command accepts the alias:

```bash
./agent chat  --addr home "…"
./agent client --addr work "run the tests"
./agent audit --addr home
./agent tool revoke reverse_string --addr work
```

An `--addr` value that isn't a known alias is used verbatim, so literal `host:port` always
works. Aliases live in `config.json` under the resolving `--config-dir`, so each config dir
keeps its own alias book.

## Configuration & environment reference

Config file: `<config-dir>/config.json` (created by `config set-*`; default config dir
`~/.config/ai-agent`).

| Config key / flag | Env override | Meaning |
| --- | --- | --- |
| `--config-dir` (global flag) | `AI_AGENT_CONFIG_DIR` | Directory holding this agent's config/tools/memory/audit. Default `~/.config/ai-agent`. |
| `--sessions-dir` (global flag) | `AI_AGENT_SESSIONS_DIR` | Directory for per-run transcripts (one subdir per run). Default `~/.local/share/ai-agent/sessions`. |
| `openai_key` | — | OpenAI API key. |
| `model` | `--model` flag | Default model (built-in default `gpt-4o-mini`). |
| `tier` | `--tier` flag | Default trust tier (built-in default `balanced`). |
| `engines` | — | Map of alias → engine `host:port` for `--addr` (managed by `config set-engine`/`rm-engine`/`engines`). |
| `telegram_token` | `AI_AGENT_TELEGRAM_TOKEN` | Telegram bot token; empty ⇒ bot disabled. |
| `telegram_allowed_users` | `AI_AGENT_TELEGRAM_ALLOWED_USERS` | Allowed Telegram user ids (env is comma-separated). |

Precedence: `--config-dir` flag > env > default; likewise model/tier: `--flag` > config
value > built-in default.

---

## Files on disk

The first four live under the **config dir** (default `~/.config/ai-agent`, overridable
with `--config-dir` / `AI_AGENT_CONFIG_DIR`):

| Path | What |
| --- | --- |
| `<config-dir>/config.json` | API key, default model/tier, engine aliases, Telegram settings. |
| `<config-dir>/tools.json` | Persisted agent-authored tool catalog. |
| `<config-dir>/memory.json` | Long-term memory store. |
| `<config-dir>/audit.jsonl` | Process-wide audit log (written by `serve`). |
| `<config-dir>/sessions/<id>.json` | Persisted conversations (one file per session). |
| `<sessions-dir>/<run-id>/` | Per-run transcript: `run.jsonl`, `audit.jsonl`, `artifacts/`. Sessions dir defaults to `~/.local/share/ai-agent/sessions` (override with `--sessions-dir`). |

All are created on first use; deleting them resets the corresponding state.
