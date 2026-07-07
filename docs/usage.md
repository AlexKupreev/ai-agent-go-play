# Usage — operating the agent

A practical guide to running and operating the agent day-to-day. For *why* it is built
this way, see [`design.md`](design.md) and [`security.md`](security.md); for the runtime
**environment** model — config-dir vs workspace, tiers, prompt/agent-type customization,
and the full config/env/files reference — see [`environment.md`](environment.md). This file
is the *how*.

- [Install & configure](#install--configure)
- [Two ways to run](#two-ways-to-run)
- [Command reference](#command-reference)
- [Trust tiers — the safety dial](#trust-tiers--the-safety-dial)
- [Approvals & questions — the human-in-the-loop gate](#approvals--questions--the-human-in-the-loop-gate)
- [Deliberate turns & the artifact cache](#deliberate-turns--the-artifact-cache)
- [Customizing the agent — prompts & agent types](#customizing-the-agent--prompts--agent-types)
- [Comparing configurations — `agent eval`](#comparing-configurations--agent-eval)
- [Self-authored tools](#self-authored-tools)
- [Long-term memory](#long-term-memory)
- [Self-documentation](#self-documentation)
- [Self-status](#self-status)
- [Audit log](#audit-log)
- [Token usage](#token-usage)
- [Conversations over the API (sessions)](#conversations-over-the-api-sessions)
- [Telegram frontend (optional)](#telegram-frontend-optional)
- [Running multiple independent agents](#running-multiple-independent-agents)
  - [Engine aliases](#engine-aliases)
- [Configuration & environment reference](#configuration--environment-reference)

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
./agent config set-verbose on          # default trace verbosity (built-in default: off)
```

To use a local or third-party OpenAI-compatible endpoint (llama.cpp, Ollama, vLLM, OpenRouter,
a proxy) instead of the OpenAI API, point the client at its base URL — the rest of the agent is
unchanged (`AI_AGENT_OPENAI_BASE_URL` overrides it per run):

```bash
./agent config set-base-url http://127.0.0.1:11434/v1   # e.g. a local Ollama server
./agent config set-base-url ""                          # clear it — back to the OpenAI API
```

The CLI tool-call trace is off by default; turn it on per run with `--verbose` (or off with
`--quiet`), or as a default with `config set-verbose` / `AI_AGENT_VERBOSE`. In `chat` it is
also togglable live with `/verbose [on|off]`. When shown, the whole trace is rendered in **grey**
and the model's intermediate reasoning is wrapped in a bounded `╭─ thinking ─ … ╰─` block, so it
reads as work-in-progress and stays visually distinct from the final answer (which prints in the
default colour). Colour is emitted only to a real terminal — piped/redirected output stays plain
(honours `NO_COLOR`). The trace is display-only — the full transcript is always written to disk
regardless.

Everything is stored under `~/.config/ai-agent/` — see [`environment.md`](environment.md#files-on-disk).

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

A one-shot `agent client` run (`POST /runs`) runs the executor directly. **Session turns**
(`agent chat --addr` / Telegram) are **deliberate by default** — a context-aware planner →
executor → critic loop — with clarifying questions routed through the approval queue instead
of stdin; `serve --no-plan` / `--no-critique` turn those off (see [`agent serve`](#agent-serve)).
Best for unattended / mobile / multi-frontend use.

---

## Command reference

Run `./agent <command> --help` for the authoritative flag list. Summary:

| Command | What it does |
| --- | --- |
| `agent run <task>` | One-shot run in this process (planner + executor), stdin approvals. |
| `agent chat` | Interactive multi-turn REPL, retained context. Deliberate (planner→executor→critic) by default; `--no-plan`/`--no-critique` to simplify. Local by default; `--addr` drives a `serve` engine's persistent session. `/reload` re-reads prompt/agent-type files. |
| `agent serve` | Start the headless HTTP+SSE engine. Deliberate session turns by default (`--no-plan`/`--no-critique`/`--max-revisions`). |
| `agent client <task>` | Start & stream a run on a running engine; prompts for approvals. |
| `agent eval <task>` | Run one task under N config variants and compare outputs + token usage. |
| `agent prompts show` | Print the composed effective prompt (executor by default; `--planner`/`--critic`/`--all`) without a run — honors `--workspace`/`--tier`/`--context-file`/`--no-context-files`. |
| `agent reload` | Tell a running engine to re-read its prompt + agent-type files (no restart). |
| `agent stop <run-id>` | Cancel a run on a running engine (kill switch). |
| `agent audit` | Browse the engine's audit log. |
| `agent usage` | Show token totals — today, or `--session <id>` — from the audit log. |
| `agent tool list` | List persisted, agent-authored tools. |
| `agent tool revoke <name>` | Remove an authored tool. |
| `agent config set-key\|set-base-url\|set-model\|set-tier\|set-verbose` | Save API key / OpenAI-compatible base URL / default model / default tier / default trace verbosity. |
| `agent config set-engine\|rm-engine\|engines` | Name an engine address as an alias for `--addr` (and list/remove them). |

Every command that talks to a running engine takes `--addr`, which accepts a literal
`host:port` **or** an alias saved with `agent config set-engine` (see [engine
aliases](#engine-aliases)).

### `agent run <task>`

```bash
./agent run --model gpt-4o --tier safe delete the build cache
./agent run --verbose <task>     # also print tool calls + results to stderr
```

Flags: `--model`, `--tier` (override config for this run), `--verbose`. The global
`--workspace` flag applies here too.

### `agent chat`

An interactive REPL — type a message, get a reply, keep going, with the **conversation
history retained across turns** (like a chat CLI). It has two modes: **local** (the default)
and **remote** (`--addr`).

**Local mode** runs the executor in *this* process. The tool catalog and long-term memory are
shared with the rest of this agent (scoped by `--config-dir`). Its audit trail, though, goes
to the **per-run transcript** (`<sessions-dir>/<run-id>/audit.jsonl`), not the process-wide
`audit.jsonl` that only `agent serve` writes and `agent audit` reads — see [Audit log](#audit-log).

```bash
./agent chat                 # deliberate: planner → executor → critic (the default)
./agent chat --no-plan       # bare executor with retained context (a straight conversation)
./agent chat --no-critique   # planner → executor, but skip the critic re-plan loop
./agent chat --tier safe     # per-session model/tier, same flags as `run`
```

- **Deliberate by default.** Each message runs the full pipeline: a **planner** refines it
  into a brief (using the conversation so far), the **executor** runs the brief, and a
  **critic** judges the answer against the plan's success criteria and re-plans a shortfall
  (up to `--max-revisions`, default 1). `--no-plan` drops to a bare executor with retained
  history; `--no-critique` keeps the planner but skips the critic loop. The brief and any
  critique notes are printed to stderr, distinct from the final answer on stdout.
- **Commands:** `/new` (alias `/reset`) clears the conversation and starts fresh; `/model
  [id]` and `/tier [safe|balanced|permissive]` show or switch the model / trust tier for the
  session (no arg prints the current value) — in `--no-plan` the executor is rebuilt in place
  carrying the conversation, in deliberate mode the change takes effect on the next turn;
  `/attach <path>` registers a local file as an artifact the agent can read by path (deliberate
  mode only); `/verbose [on|off]` toggles the tool-call trace; `/reload` re-reads the prompt files
  and agent-type catalog (in the default deliberate mode prompts are re-read every turn, so
  `/reload` is a no-op there; in `--no-plan` it rebuilds the executor *without losing the
  conversation* — a malformed file is reported and the current setup kept); `/exit` (or
  Ctrl-D) quits. **Ctrl-C** cancels the *current* turn and returns you to the prompt.
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

- **Commands (remote):** `/new` (alias `/reset`) starts a fresh session (closing the old
  one); `/end` closes the current session; `/exit` (or Ctrl-D) **detaches** and leaves it resumable
  (the resume command is printed on exit). **Ctrl-C** cancels the current turn (stopping the
  remote run) and returns you to the prompt.
- Approvals are the engine's **shared** queue: an escalation you don't answer here is the
  same one visible to `agent audit`, other clients, and Telegram — not a local stdin gate.
  A deliberate session turn's planner clarification (`ask_user`) arrives through this same
  queue, so the engine can deliberate remotely, not just on the CLI.
- `--model` / `--tier` and the `--no-plan` / `--no-critique` dials are **local-mode only**;
  in remote mode the model, tier, and whether turns are deliberate are fixed by how `serve`
  was started.

### `agent serve`

```bash
./agent serve --addr 127.0.0.1:8080 --model gpt-4o --tier balanced
./agent serve --no-plan            # session turns run the bare executor (no planner/critic)
./agent serve --no-critique        # planner on, critic re-plan loop off
```

Flags: `--addr` (listen address; keep it on `127.0.0.1`), `--model`, `--tier`, and the
deliberate-turn dials `--no-plan` / `--no-critique` / `--max-revisions` (default 1). Prints a
couple of `curl` snippets on startup. Shares one tool catalog, one memory store, and one
process-wide audit log across all runs it serves.

**Session turns are deliberate by default** (planner → executor → critic; `--no-plan` implies
no critique). This affects **session turns** (`/sessions/{id}/turns`, i.e. `chat --addr` and
Telegram), *not* one-shot `POST /runs`, which always runs the executor directly. The planner's
clarifying questions route through the shared approval queue, so deliberation works over any
frontend. Each session gets a disk-backed scratch dir + artifact manifest that persists across
turns and restarts (see [Deliberate turns & the artifact cache](#deliberate-turns--the-artifact-cache)).

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

## Approvals & questions — the human-in-the-loop gate

Two kinds of interaction pause a run and route a prompt to whoever owns it: an **approval**
(yes/no — **destructive shell commands** and **capability escalations** beyond the current
tier) and a **question** (free text — the executor's `ask_user`, when a task is ambiguous).
Both go through one gate, so a running task can also *ask you something mid-run*, not just
ask permission. How the prompt reaches you depends on the front door:

- **`agent run` / `agent chat`** — prompts inline on stdin (`proceed? [y/N]` for an approval,
  a free-text line for a question).
- **`agent serve` + `agent client` / `chat --addr`** — the request *parks* on the engine; the
  client polls, prompts you in the terminal, and sends the decision or answer back.
- **`agent serve` + Telegram** — pushed onto the run's event stream: an approval becomes an
  **Approve / Deny** inline keyboard; a question is sent as a prompt whose next chat reply is
  delivered as the answer.

Under the hood every parked request is visible at `GET /approvals` (each carries a `mode` of
`approval` or `question`) and resolved with `POST /approvals/{id}` — `{"approved": true|false}`
for an approval, `{"answer": "…"}` for a question. It is also emitted onto the run's SSE stream
as `approval_requested` / `approval_resolved` or `question_requested` / `question_answered`. A
cancelled or abandoned run never executes the gated action (and an unanswered question surfaces
its cancellation to the model).

---

## Deliberate turns & the artifact cache

A **deliberate** turn (the default in `agent chat` and `serve` session turns) is a small
pipeline rather than a single executor call:

1. **Planner** — reads the conversation so far and the artifact manifest (below), and refines
   your message into a **brief** (a refined task + context + data references + success
   criteria). It may ask a clarifying question, which routes to stdin (local) or the approval
   queue (remote/Telegram). Tune it with `PLANNER.md`.
2. **Executor** — runs the brief in a fresh executor (no accumulated tool-call cruft in the
   conversation), streaming its activity and producing the answer.
3. **Critic** (unless `--no-critique`) — judges the answer against the brief's success
   criteria. If it falls short, the planner re-plans with the critic's gaps as added context
   and the executor runs the revised brief — up to `--max-revisions` cycles (default 1), after
   which the best answer is delivered with a note. Tune it with `CRITIC.md`.

The rendered brief and critique notes are surfaced distinctly — to stderr on the CLI, and as
`brief` events on the SSE stream (`agent serve`) so a frontend can show the deliberation.
`--no-plan` skips the whole pipeline and runs a bare executor with retained history (`--no-plan`
implies no critique).

### The artifact cache

So a turn doesn't have to stuff a downloaded dataset or a computed result into the conversation
(and re-fetch it next turn), a deliberate turn gets a **disk-backed scratch directory** and an
**artifact manifest**:

- **`record_artifact`** — a built-in the executor calls after writing a sizeable intermediate
  (a fetched dataset, a cleaned CSV, a computed file) to the scratch dir: it registers the
  file's path, source, and a one-line shape note in the manifest so it's tracked and reusable.
- **`/attach <path>`** (CLI chat) — registers a file *you* provide into the manifest
  (`origin: user`), so the agent reads it by path like any other artifact. Explicit attach
  only — the agent never sniffs your prose for filenames.
- The planner sees the manifest each turn and references existing artifacts by path (with the
  source as a re-fetch fallback) instead of re-deriving them.

**Where it lives.** In `agent serve`, the scratch dir + manifest are **per session** and
persist across turns and restarts, keyed by session id, at
`<config-dir>/session-scratch/<session-id>/` (see [Files on disk](environment.md#files-on-disk)).
In local `agent chat`, they live under the run's transcript dir and last for the process. There
is no automatic reaper yet — a cache-with-fallback keeps a stale or absent file correct (the
manifest records the source to re-fetch from), so pruning `session-scratch/` by hand is safe.

---

## Customizing the agent — prompts & agent types

You can shape the agent's behaviour with files, read from **two directories**: the
**config-dir** (who the agent is — global, always trusted) and the **workspace** (what it's
acting on — per-run, trusted only above `safe`). The full model, precedence rules, and the
tier gate live in [`environment.md`](environment.md); the operational summary:

- **System prompt / operator instructions.** `SYSTEM.md` **replaces** the built-in base prompt;
  `AGENTS.md` (alias `CLAUDE.md`) is **appended** as instructions. `PLANNER.md` **replaces** the
  built-in planner prompt (the clarify/refine pass — `agent run`, and every deliberate `agent
  chat` / session turn). `CRITIC.md` **replaces** the built-in critic prompt (the critique loop's
  verdict pass in a deliberate `agent chat` / session turn). Drop any in the config-dir (global)
  and/or the workspace; workspace wins over global. `--context-file <path>` (repeatable) appends
  an extra file regardless of tier; `--no-context-files` ignores **all** of the above (both tiers,
  `SYSTEM`/`AGENTS`/`PLANNER`/`CRITIC`, agent types, and `--context-file`) for a reproducible run
  on the bare base prompts. The `PLANNER.md`/`CRITIC.md` overrides only re-style the pass; the
  planner's Plan and the critic's Verdict are enforced by JSON schema, so an override can't break
  the contract.
- **Sub-agent types.** Declare delegatable agents as `agents/<name>.md` (YAML frontmatter +
  a body that is the sub-agent's system prompt) under the config-dir and/or workspace. The agent
  reaches them through a `spawn_agent(type, task)` tool; built-in types `researcher` and
  `general-purpose` are always available, and a same-named file overrides one. See
  [`environment.md`](environment.md#sub-agent-types-agentsmd) for the frontmatter fields.

```bash
./agent --workspace ~/proj run "…"          # loads ~/proj/{SYSTEM,AGENTS}.md + ~/proj/agents/*
./agent run --context-file ./STYLE.md "…"   # append one extra prompt file, any tier
./agent run --no-context-files "…"          # bare built-in prompt only
```

The workspace tier is **tier-gated**: on `safe`, workspace files (a possibly-hostile checkout)
don't auto-load unless you pass `--workspace` explicitly. Config-dir files always load.

To see the **composed** prompt a given configuration actually produces — base + tier policy +
tool roster + your `SYSTEM.md`/`AGENTS.md` layers, assembled exactly as a run would but without
calling the model — use `agent prompts show` (add `--planner` / `--critic` / `--all`; it honors
`--workspace` / `--tier` / `--context-file` / `--no-context-files`):

```bash
./agent prompts show --tier safe            # the executor prompt as `safe` composes it
./agent --workspace ~/proj prompts show --all   # executor + planner + critic for that workspace
```

### Hot-reload — no restart

After editing any of these files, pick up the changes in place:

- **`agent chat`** — type **`/reload`** (rebuilds the executor, keeps the conversation).
- **`agent serve`** — **`agent reload --addr <engine>`** (or `curl -XPOST <engine>/reload`)
  re-reads the files so the *next* run uses them. A malformed file is rejected (HTTP 400) and
  the engine keeps its current configuration; in-flight runs are unaffected.

```bash
./agent reload --addr 127.0.0.1:8080     # or --addr <alias>
```

## Comparing configurations — `agent eval`

To decide *which* prompt, model, or agent-type set works best, run one task under several
configurations and compare them side by side instead of guessing:

```bash
./agent eval "summarize this repo" --models gpt-4o-mini,gpt-4o     # quick model sweep
./agent eval "summarize this repo" --variants variants.yaml        # full control
```

`--models` makes one variant per model. `--variants` points at a YAML list where each variant
overrides the ambient defaults with any of `model`, `tier`, `workspace`, `context_files`,
`no_context_files`, an inline `system_prompt` / `agents_md` (so a variant needn't ship a file),
per-variant `limits` (the same keys as the config `limits` block, layered over it), and
`spawn_depth` — the file-backed equivalent of a set of run flags:

```yaml
- name: baseline
- name: with-project-prompt
  context_files: [./PROMPT.md]
- name: terse-inline               # inline prompt, no file needed
  system_prompt: |
    You are a terse assistant. Answer in one sentence.
- name: deeper-loops               # vary a limit for this variant only
  model: gpt-4o
  limits: { max_iterations: 40 }
```

Each variant runs the executor directly (no planner) with a fresh context, sharing this agent's
tool catalog and memory. The report is a table (variant, effective model, steps, tokens,
duration, status) followed by each variant's full output; a variant that errors is captured so
the rest still report, and **Ctrl+C** stops after the current variant.

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

The embedded set is the **reference docs** (README + `docs/*.md`: usage, environment, design,
security, tools, memory, api-transport — how it works *now*) plus the **vision doc**
(`self-extending-agent-design.md` — design intent and trade-offs). Docs are tagged: the agent
treats `[reference]` as authoritative about current behavior and `[vision]` as design intent
that may include not-yet-built ideas. **Design-record and planning docs** (`docs/adr/` — the
rationale for shipped features; `docs/planning/` — specs for not-yet-built work; `docs/deferred/`
— shelved designs) are deliberately *not* embedded, so the agent never mistakes a roadmap or a
decision record for a current capability.

There's no separate CLI for it — it's a tool the agent uses during a run (e.g. ask it "how do
your trust tiers work?" and it reads `usage.md` rather than guessing). `read_self_docs` is
read-only, trusted, and not exposed to sandboxed authored tools.

---

## Self-status

The agent has a `status` tool that reports its own live state, so it can answer "how am I
configured?" or "how much headroom does this machine have?" accurately. It returns:

- **Identity:** model, trust tier, current run id, and build version.
- **Counts:** how many authored tools and memory entries it has.
- **Host resources:** CPU count and load average, RAM free/total, disk free/total, the
  process's RSS, Go heap/goroutines, and host uptime.
- **State on disk:** how much space its own state occupies — the run transcripts (`runs/`),
  sessions (live + archived), the session scratch cache, the tool catalog, memory, and the
  audit log — each as an entry count + total bytes. So it can answer "what's using my disk?"
  and notice, e.g., an accumulating `runs/` tree (which has no auto-reaper by design). The
  walk is best-effort and budget-bounded, so a huge tree can't stall the tool (a capped total
  is shown with a leading `≥`).

Host figures are read live (Linux `/proc` + Go runtime; fields it can't read are omitted).
This isn't a new capability — the agent can already `shell` out to `df`/`free`/`uptime` — but
a structured, reliable convenience, and it matters most on a small box where knowing the
headroom before starting heavy work is useful. Like the other introspection tools, `status`
is a tool the agent invokes during a run (no separate CLI), read-only and not sandbox-exposed.

The build version defaults to `dev`; stamp a real one at build time with
`-ldflags "-X ai-agent-go-play/internal/buildinfo.Version=$(git describe --tags --always)"`.

The agent has two more introspection tools in the same vein (model-facing, read-only, no CLI):
`recent_activity` lets it review its own recorded activity from the audit log (capabilities used,
tools authored/revoked, memory saved, token usage — "what have I done recently?"), and
`tool_catalog` lists the tools it has authored with their capabilities, so it reuses an existing
one instead of writing a duplicate.

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
also keeps its own transcript under `<config-dir>/runs/<run-id>/`.

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

### Session-wide and day-wide totals

Because every run/turn writes a `run_usage` event (tagged with the session id for session
turns), **session-wide** and **day-wide** totals are just sums over those persisted events —
restart-safe and spanning every run, with no separate accumulators. Both `agent serve` and
`agent run` append to the one process-wide `audit.jsonl`, so "today" covers CLI runs too.

- **For you:** `agent usage` reports today's total across all runs; `agent usage --session <id>`
  reports one conversation's total across its turns.

  ```bash
  ./agent usage                       # today: 118,900 in / 27,400 out across 14 run(s)
  ./agent usage --session <id>        # session <id>: 22,180 in / 5,340 out across 6 turn(s)
  ```

  (`agent usage` reads the local `audit.jsonl` under `--config-dir`.) `agent run` also prints a
  `today:` line after each run.
- **For the agent:** a `usage` tool lets it query its own spend this session and today, so it
  can reason about how much it's using. The in-flight run is included only once it finishes
  (it's summed from the log). The `usage` tool is available on `agent serve` runs; local
  `agent chat` shows its own per-turn and session totals instead.

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

**Closing a session archives it, it doesn't destroy it.** `/end` (and `DELETE /sessions/{id}`)
moves the conversation to `<config-dir>/sessions/archive/<id>.json` rather than deleting it, so
a mistaken close is recoverable: move the file back up to `<config-dir>/sessions/` and resume
it with `agent chat --addr <engine> --session <id>`. Archived sessions drop out of the resumable
listing (`--list`, `GET /sessions`). The session's scratch cache (`session-scratch/<id>/`) *is*
removed on close — it holds large, re-derivable artifacts, and the manifest records each one's
source so a later turn re-fetches it if needed. (There is no built-in "purge for real" surface
yet; delete an archived file by hand to reclaim its space.)

Each completed run/turn also writes a compact `info.json` (task, state, result, token usage,
timings) into its transcript dir, so a run's status survives both the engine's in-memory
retention cap and a restart: `GET /runs/{id}` falls back to it once the live run is evicted
(its live SSE event replay is not reconstructable, so streaming an evicted run still 404s).

## Telegram frontend (optional)

A Telegram bot can act as a peer client of the engine. Each chat maps to a **session**
(a persistent conversation, below): a message runs a turn with retained context, events
stream back, and a parked approval becomes an Approve/Deny inline keyboard. Chat commands:
**`/new`** (alias **`/reset`**) starts a fresh session, **`/end`** terminates the current one
(a session is also created automatically on your first message), and **`/reload`** re-reads the
engine's prompt files + agent-type catalog (effective from your next message — the same
management action as `agent reload`, gated by the bot's allowlist). The `/new` + `/reset` +
`/end` verbs match the CLI chat REPL. It is **entirely optional**: with no token set, the
engine runs exactly as normal.

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

Per-run **transcripts** default to `<config-dir>/runs/` (each run in its own uniquely-named
subdir), so pointing two agents at different `--config-dir`s already keeps their transcripts
separate — no extra flag needed. Override the location with `--sessions-dir` (or
`AI_AGENT_SESSIONS_DIR`) if you want transcripts somewhere other than under the config dir:

```bash
./agent --config-dir ~/.config/ai-agent/work \
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

The full reference — the config-dir vs workspace model, the trust tier, prompt/agent-type
customization, the complete config-key / env-var table, and every file the agent reads or
writes on disk — lives in **[`environment.md`](environment.md)**. In brief:

- Config lives in `<config-dir>/config.json` (created by `config set-*`; default config dir
  `~/.config/ai-agent`, set by `--config-dir` / `AI_AGENT_CONFIG_DIR`).
- Per-run transcripts default to `<config-dir>/runs/<run-id>/` (`--sessions-dir`
  / `AI_AGENT_SESSIONS_DIR`), so separate `--config-dir` agents share nothing.
- Precedence everywhere: **flag > env > config value > built-in default**.

See [`environment.md`](environment.md#configuration--environment-reference) for the tables.
