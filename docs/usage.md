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
- [Scraping JS-rendered and bot-walled pages (`scrape`)](#scraping-js-rendered-and-bot-walled-pages-scrape)
- [Using an external API (secrets)](#using-an-external-api-secrets)
- [Long-term memory](#long-term-memory)
- [Spaces — switchable data contexts](#spaces--switchable-data-contexts)
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
| `agent reload` | Tell a running engine to re-read its prompt + agent-type files and `config.json` defaults (model, tier) — no restart. |
| `agent stop <run-id>` | Cancel a run on a running engine (kill switch). |
| `agent audit` | Browse the engine's audit log. |
| `agent session list\|purge\|restore` | Manage a running engine's persistent conversations: list resumable ones, purge one irreversibly (`-y` skips the confirm), or restore a closed (archived) one. |
| `agent usage` | Show token totals — today, or `--session <id>` — from the audit log. |
| `agent tool list` | List persisted, agent-authored tools. |
| `agent tool revoke <name>` | Remove an authored tool. |
| `agent config set-key\|set-base-url\|set-model\|set-tier\|set-verbose` | Save API key / OpenAI-compatible base URL / default model / default tier / default trace verbosity. |
| `agent config set-engine\|rm-engine\|engines` | Name an engine address as an alias for `--addr` (and list/remove them). |
| `agent config set-secret\|rm-secret\|secrets` | Store / remove / list (names only) credentials an authored tool can inject into an approved `http_get`, never model-visible. See [`adr/external-apis.md`](adr/external-apis.md). |

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
  critique notes go to stderr, distinct from the final answer on stdout — as a **one-line
  summary** by default (the refined-task line); `/verbose on` shows the full brief block
  (context, success criteria, assumptions). The disk transcript keeps the full brief either way.
- **Commands:** `/new` (alias `/reset`) clears the conversation and starts fresh; `/model
  [id]` and `/tier [safe|balanced|permissive]` show or switch the model / trust tier for the
  session (no arg prints the current value) — in `--no-plan` the executor is rebuilt in place
  carrying the conversation, in deliberate mode the change takes effect on the next turn;
  `/space [name]` shows or switches the active [space](#spaces--switchable-data-contexts)
  (`/space list` lists them, `/space -` returns to the global scope; also `--space <name>` at
  launch) — switching re-scopes memory and loads the space's notes, same rebuild semantics as
  `/model`; `/attach <path>` registers a local file as an artifact the agent can read by path (deliberate
  mode only); `/compact` summarizes the conversation so far into a compact briefing and replaces
  the working history with it, freeing context (the on-disk transcript is untouched — it only
  shrinks the *live* context; in deliberate mode the last turn is kept verbatim); `/verbose
  [on|off]` toggles the tool-call trace **and** the full brief block (off ⇒ one-line brief
  summaries); `/reload` re-reads the prompt files
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
./agent chat --addr 127.0.0.1:8080              # start a new session on the engine
./agent chat --addr home                        # same, addressing the engine by alias
./agent chat --addr home --model gpt-4o --tier safe  # new session with a sticky model/tier
./agent chat --addr home --list                 # list resumable sessions, then exit
./agent chat --addr home --session <id>         # resume an existing conversation
```

- **Commands (remote):** `/new` (alias `/reset`) starts a fresh session (closing the old
  one); `/model [id]` and `/tier [safe|balanced|permissive]` show or switch the session's sticky
  model / trust tier (no arg prints the current value), effective from the next turn; `/space
  [name]` shows or switches the session's sticky [space](#spaces--switchable-data-contexts)
  (`/space -` returns to the global scope), effective from the next turn; `/end` closes
  (archives) the current session and `/purge` deletes it for good; `/exit` (or Ctrl-D) **detaches**
  and leaves it resumable (the resume command
  is printed on exit). **Ctrl-C** cancels the current turn (stopping the remote run) and returns you
  to the prompt.
- Approvals are the engine's **shared** queue: an escalation you don't answer here is the
  same one visible to `agent audit`, other clients, and Telegram — not a local stdin gate.
  A deliberate session turn's planner clarification (`ask_user`) arrives through this same
  queue, so the engine can deliberate remotely, not just on the CLI.
- **`--model` / `--tier` are now remote-capable** as a **per-session sticky**: `--model`/`--tier` at
  launch seed a new session, and `/model`/`/tier` change it live. Both are stored on the session and
  merged per turn (turn override > session-stored > serve default); a requested tier is still
  **clamped** to the `serve --tier` ceiling, so it can go safer but never looser than the engine
  allows. A resumed session keeps its own stored values. The `--no-plan` / `--no-critique` dials
  remain **serve-side only** — whether turns are deliberate is fixed by how `serve` was started.

### `agent serve`

```bash
./agent serve --addr 127.0.0.1:8080 --model gpt-4o --tier balanced
./agent serve --no-plan            # session turns run the bare executor (no planner/critic)
./agent serve --no-critique        # planner on, critic re-plan loop off
```

Flags: `--addr` (listen address — **loopback only**: the engine has no authentication, so a
non-loopback address is refused unless you also pass `--unsafe-public`, which prints a warning
naming what that exposes; see [`security.md`](security.md) §7), `--model`, `--tier`, and the
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
- **Sending a file in Telegram** ([above](#sending-files)) — the network equivalent: the file is
  copied into the session's scratch dir and recorded the same way (`origin: user`), over
  `POST /sessions/{id}/files`.
- The planner sees the manifest each turn and references existing artifacts by path (with the
  source as a re-fetch fallback) instead of re-deriving them.

**Where it lives.** In `agent serve`, the scratch dir + manifest are **per session** and
persist across turns and restarts, keyed by session id, at
`<config-dir>/session-scratch/<session-id>/` (see [Files on disk](environment.md#files-on-disk)).
In local `agent chat`, they live under the run's transcript dir and last for the process.

**When it is cleaned.** Closing a session (`/end`, `DELETE /sessions/{id}`) reaps its scratch dir
but **keeps the files you provided** (`origin: user` — an attach or a Telegram upload); agent-derived
artifacts and untracked scratch are re-derivable, so they go. A purge deletes everything. Pruning
`session-scratch/` by hand is also safe for agent artifacts — a cache-with-fallback keeps a stale or
absent file correct (the manifest records the source to re-fetch from) — but it will take your
uploaded files with it.

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
  re-reads the files **and the `config.json` defaults (default model + tier ceiling)** so the
  *next* run uses them — retune the engine's model/tier without a restart. A malformed file or
  config (or a bad tier) is rejected (HTTP 400) and the engine keeps its current configuration
  entirely (no partial reload); in-flight runs are unaffected. Flag/env precedence is re-applied,
  so an engine launched with an explicit `--model`/`--tier` keeps that choice — only a
  config-sourced default moves.

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

## Scraping JS-rendered and bot-walled pages (`scrape`)

`web_fetch` issues a plain HTTP GET, so it comes back empty on a page that renders client-side,
and blocked on one behind a bot wall. Store a [ScrapingAnt](https://scrapingant.com) token under
the name `scrapingant` and the agent gains a `scrape` tool that fetches through a headless browser
and rotating proxies:

```bash
./agent config set-secret scrapingant sk_your_token
# or, on a deployment:  fly secrets set AI_AGENT_SECRET_SCRAPINGANT=sk_your_token -a your-app
```

The tool appears only when that secret resolves, and the token is read host-side and sent as a
header — it never reaches the model, the tool arguments, or the audit log. Options: `render_js`
(default true), `return_html`, `proxy_country`, `wait_for_selector`.

**It costs money per call**, which is why it is a separate tool rather than a flag on `web_fetch`:
the agent is told to try `web_fetch` first and not to retry scrapes in a loop, and every call
lands in the audit log as its own line, so spend is reconstructable:

```
capability_exercised  capability=scrape  arg=example.com [secret:scrapingant] [browser]
```

`[browser]` marks the calls that used JS rendering — roughly 10x the credits of a plain proxied
fetch, so it is worth seeing. Reasoning in [`adr/external-apis.md`](adr/external-apis.md) §5.

---

## Using an external API (secrets)

To let the agent call **any other keyed third-party API** (a data provider, an internal service,
…), you store the credential once and the agent authors an `http_get` tool that references it **by
name** — no per-API code, and no waiting on someone to build a `scrape`-style built-in. The
value is injected into the request host-side — it never reaches the model, the sandbox, the tool
catalog, or the audit log. This is the general path for any keyed API; no per-API code (design in
[`adr/external-apis.md`](adr/external-apis.md)).

**1. Store the token** (once):

```bash
./agent config set-secret scrapingant sk_your_token   # value stored in config.json (mode 0600)
./agent config secrets                                 # lists NAMES only — never values
./agent config rm-secret scrapingant                   # remove one
```

`config secrets` lists every secret the broker can resolve — from `config.json` *and* from the
environment (see **Deployment** below) — tagging each with its source:

```
scrapingant	(env)
weather	(config)
```

**2. Ask the agent to author a tool** that uses it (in `agent chat` / Telegram / `agent run`),
e.g. *"author a tool that scrapes a URL via ScrapingAnt using the `scrapingant` secret."* The
stored names are listed in `author_tool`'s schema, so the agent knows which keyed APIs it can
reach without being told the name (it still can never read a value). It requests an `http_get`
capability that names the secret and its placement:

```json
{ "kind": "http_get", "hosts": ["api.scrapingant.com"],
  "secret": "scrapingant", "secret_in": "header:x-api-key" }
```

`secret_in` is one of `header:<Name>`, `query:<param>`, or `bearer` (shorthand for
`Authorization: Bearer <token>`, so you store the raw token).

**3. Approve it once.** A secret-bearing capability **always** prompts — even on `permissive` —
so you confirm *this token goes to this host*:

```
Authorize tool "scrape" with elevated capabilities
http_get → api.scrapingant.com (secret "scrapingant" in header:x-api-key)
```

Approve, and the tool is registered and reusable. At call time the broker injects the value,
bounded to the cap's host allowlist (and re-checked across redirects), and audits
`http_get → api.scrapingant.com [secret:scrapingant]` — the name, never the value.

**Guardrails.** Fail-closed: if the secret isn't stored, a tool naming it is *denied*, not run
without it. If the agent pointed the secret at a different host, you'd see that host in the
approval and reject it — and even approved, the value only travels to allowlisted hosts.

**Deployment (no value on disk).** Instead of `set-secret`, supply each secret from the
environment as `AI_AGENT_SECRET_<NAME>` (uppercase name; the agent reads it lowercased). This is
the 12-factor path for an automated deploy — e.g. on Fly.io:

```bash
fly secrets set AI_AGENT_SECRET_SCRAPINGANT=sk_your_token -a your-app
```

Env wins over `config.json`, nothing is written to the state volume, and `config secrets` on
the deployed machine lists the name tagged `(env)`. See
[`../deploy/fly/README.md`](../deploy/fly/README.md).

---

## Long-term memory

The agent has `remember` / `recall` built-ins backed by a persistent store at
`<workspace>/.agent/memory.json` — memory is **workspace-local**: each workspace (the
directory the agent acts on, `--workspace` or the cwd) has its own memory, so two
differently-tuned agents in different workspaces don't share notes, and committing or
`.gitignore`-ing `.agent/` is an ordinary file decision. A fact remembered in one run is
recallable by later runs in that workspace. Every write is audited (`memory_write`).
There is no CLI surface for it yet — it is driven by the agent during a run.

Three consequences of the workspace-local move (memory previously lived at
`<config-dir>/memory.json`):

- For `serve`/Telegram, point `--workspace` at a **persistent** directory so memory survives
  restarts — on a deployment that means the mounted volume. Under `serve` the workspace is fixed
  at launch, so this is a start-up decision, not something a session can change later.
- If you had an old config-dir `memory.json`, move it to `<workspace>/.agent/memory.json` to keep
  its entries.
- **`--config-dir` no longer separates memory.** Two agents with different config dirs but the
  same workspace share one memory store and one set of spaces; separating them means separate
  workspaces (see [Running multiple independent agents](#running-multiple-independent-agents)).

---

## Spaces — switchable data contexts

A **space** is a named scope for the agent's own data — "my English lessons", "the tax
stuff" — with its **own memory** and a short, **always-loaded notes blob** (the per-space
profile). Exactly one space is active per session; with none active, memory works in the
**global scope** exactly as before. Spaces let the agent resume the right context ("start
my next Polish lesson") without dragging in unrelated facts. A space is *data only* — it
does **not** change the working directory or trust tier. Design: `docs/adr/spaces.md`
(not embedded; this section is the reference).

- **Storage:** `<workspace>/.agent/spaces/<id>/` — `space.json` (name + notes) and that
  space's `memory.json`. The directory is the registry; ids are slugs of the name.
- **Scoping:** while a space is active, `remember` writes to it and `recall` reads it
  *plus* the global scope (the space shadows global on key collisions). Unscoped facts
  stay visible everywhere.
- **Notes = deliberate context.** The active space's notes are injected into the system
  prompt every turn (like an `AGENTS.md` section, but agent-writable and per-space) —
  use them for standing guidelines, goals, and state for that context. Capped at 4000
  characters so the always-on prompt stays lean; the agent maintains them with
  `update_space_notes`. You can also edit `space.json` by hand and `/reload` is not
  needed — notes are re-read every turn.
- **Tools** (trusted, not sandbox-exposed): `list_spaces`, `create_space(name)`,
  `switch_space(space)` — so *"switch to the Polish project"* in natural language works —
  plus `space_notes` / `update_space_notes`. A switch takes effect from the next turn.
- **Commands:** local chat `/space` (show), `/space list`, `/space <name>`, `/space -`
  (back to global), and `agent chat --space <name>`; remote chat and Telegram `/space
  <name>` / `/space -` set the session sticky over `PATCH /sessions/{id}`.
- **API:** `POST /sessions` and `PATCH /sessions/{id}` accept `"space"`; `POST /runs` and
  turns accept a per-request `"space"` override. The session carries the active space
  (sticky, like model/tier); an unknown space id fails the turn with a clear error.

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

**Retrieval is section-scoped, so a self-docs read costs a screen, not a context window.**
Docs are indexed at every `##` heading (fenced code blocks excluded), and the tool has three
modes:

- `query=...` ranks **sections** across all docs and returns refs like `memory#where-memory-sits-in-the-trust-model` — not whole documents.
- `topic=usage` returns the doc's **outline** (section slugs, headings, byte counts) when the doc is over the 10,000-character cap; smaller docs come back whole.
- `topic=usage section=spaces` returns that one section. The selector matches a slug exactly, then by prefix, then by substring, then as a 1-based index, and a miss lists the doc's sections.

Every body is capped at 10,000 characters with a `[truncated — N chars total]` marker, the same
limit `web_fetch` applies. `usage.md` is ~57 KB (~14k tokens); its outline is ~1.8 KB (~450
tokens), and a typical section is 1–3 KB. The agent can also tell you *which section* it
answered from.

---

## Self-status

The agent has a `status` tool that reports its own live state, so it can answer "how am I
configured?" or "how much headroom does this machine have?" accurately. It returns:

- **Identity:** model, trust tier, current run id, and build version.
- **Counts:** how many authored tools and memory entries it has.
- **Context:** how full its context window is — the tokens the last model request used vs the
  model's window size, as a percentage — so it can notice when it's running low and wrap up or
  summarize. The window comes from a built-in table of known models, overridable per model with
  the `context_limits` config for private/renamed endpoints; an unknown model shows tokens
  without a percentage.
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

**`recent_activity`'s reach depends on the front door.** Under `agent serve` it reads the
process-wide `audit.jsonl`, so it genuinely spans past runs and sessions. Under `agent run` and
local `agent chat` it reads only *that run's* transcript audit file
(`<runs-dir>/<run-id>/audit.jsonl`), so it sees the current run and nothing else — usually
empty. Ask a `serve` engine, or read the central log with `agent audit --addr`, for history.

---

## Audit log

Everything effectful is recorded to an append-only audit log: capability use
(`capability_exercised` / `capability_denied`), tool authoring/revocation
(`tool_authored` / `tool_revoked`), memory writes (`memory_write`), and per-run token
usage (`run_usage` — see [Token usage](#token-usage)).

Under `agent serve` there is **one process-wide log** at
`~/.config/ai-agent/audit.jsonl`, browsable over the API:

```bash
./agent audit --addr 127.0.0.1:8080                       # all events, oldest first
./agent audit --addr home --type tool_revoked             # filter by event type
./agent audit --addr home --run <run-id> --limit 50       # last 50 events for one run
```

Flags: `--addr`, `--run`, `--type`, `--limit` (0 = all). Each `agent run` / `serve` run
also keeps its own transcript under `<config-dir>/runs/<run-id>/`.

**`agent audit` always reads over the API** — it has no local-file mode, so it needs a running
engine (`--addr` defaults to `127.0.0.1:8080`; without one you get a connection error). To read
the log after a `serve` has stopped, read the file directly:

```bash
jq -c 'select(.type == "tool_revoked")' ~/.config/ai-agent/audit.jsonl
```

(`agent usage`, by contrast, reads that same file locally and needs no engine.)

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
- **Context-window fill** — `agent chat` follows the usage line with how full the context is,
  so a long conversation's pressure is visible:

  ```text
  · context ~62,300 / 128,000 tokens (49%)
  ```

  It's the last request's input tokens against the model's window (from a built-in table,
  overridable with the `context_limits` config). An unknown window shows tokens without a
  percentage; the same figure is available to the agent itself via the `status` tool. Use
  `/compact` to summarize the conversation when it gets full (see [chat](#agent-chat)).
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

**Revisiting past conversations.** The agent can look back at earlier sessions through three
read-only, trusted tools (wired wherever a session store is present — `agent serve` and
`agent chat`, which reads the sessions written by serve/Telegram):

- `list_sessions` — recent conversations (id, title, turns, last-active), newest first.
- `search_sessions <query>` — the best-matching stored sessions for a topic, with snippets.
- `read_session <id>` — the full transcript of one session (long ones truncated).

So *"look at the results we discussed yesterday"* resolves to a search/read, not a guess. These
never modify a session — they only read the durable record. (For facts you want carried forward
automatically, prefer `remember`/`recall`, which is push-nothing, pull-on-demand memory.)

The ergonomic way to drive a session from the terminal is **[`agent chat --addr`](#agent-chat)**
(start / `--list` / `--session <id>` to resume). The raw HTTP surface below is what it (and
the Telegram bot) sit on:

```bash
# create a session
curl -s -XPOST http://127.0.0.1:8080/sessions            # → {"session_id":"…"}
# run a turn; stream its reply on the returned run id
curl -s -XPOST http://127.0.0.1:8080/sessions/<sid>/turns -d '{"text":"hello"}'   # → {"run_id":"…"}
curl -N  http://127.0.0.1:8080/runs/<run_id>/events
# list / close (archive) / restore / purge (irreversible)
curl -s http://127.0.0.1:8080/sessions
curl -XDELETE http://127.0.0.1:8080/sessions/<sid>            # close → archive (recoverable)
curl -XPOST   http://127.0.0.1:8080/sessions/<sid>/restore    # un-archive a closed session
curl -XDELETE http://127.0.0.1:8080/sessions/<sid>/purge      # delete for good (+ scratch cache)
```

Sessions persist as one JSON file per session under `<config-dir>/sessions/`, so they
survive a `serve` restart and are shared by every client of that engine (this is the
mechanism behind the Telegram bot's per-chat conversations, and how the same conversation
could later be resumed from a different device). The stored history is the conversation
only — the system prompt is re-seeded from current code each turn.

**Closing a session archives it, it doesn't destroy it.** `/end` (and `DELETE /sessions/{id}`)
moves the conversation to `<config-dir>/sessions/archive/<id>.json` rather than deleting it, so
a mistaken close is recoverable. Archived sessions drop out of the resumable listing (`--list`,
`GET /sessions`). The session's scratch cache (`session-scratch/<id>/`) is reaped on close —
it holds large, re-derivable artifacts, and the manifest records each one's source so a later
turn re-fetches it if needed. (The reaper keeps any user-provided files — an `/attach`
or a [Telegram upload](#sending-files); agent-materialized and unrecorded scratch go. A purge,
being a whole-session deletion, removes the cache in full.)

**Restore and purge are the recovery/destructive counterparts, on every client.** A closed
session is brought back with `agent session restore <id>` (or `POST /sessions/{id}/restore`),
after which it resumes with `agent chat --addr <engine> --session <id>`. To delete a session
for good — the conversation *and* its scratch cache, irreversibly, whether live or archived —
use `agent session purge <id>` (confirms unless `-y`), the `/purge` command in `agent chat
--addr` and Telegram, or `DELETE /sessions/{id}/purge`. A purge is recorded to the audit log as
a `session_purged` event (`agent audit --type session_purged`). `agent session list` shows the
resumable sessions. *(There is no archived-session listing yet — restore by the id shown when
you closed it; see [`planning/deletion.md`](planning/deletion.md) §5.)*

Each completed run/turn also writes a compact `info.json` (task, state, result, token usage,
timings) into its transcript dir, so a run's status survives both the engine's in-memory
retention cap and a restart: `GET /runs/{id}` falls back to it once the live run is evicted
(its live SSE event replay is not reconstructable, so streaming an evicted run still 404s).

## Telegram frontend (optional)

A Telegram bot can act as a peer client of the engine. Each chat maps to a **session**
(a persistent conversation, below): a message runs a turn with retained context, events
stream back, and a parked approval becomes an Approve/Deny inline keyboard. Chat commands:
**`/new`** (alias **`/reset`**) starts a fresh session, **`/end`** terminates (archives) the
current one, **`/purge`** deletes it for good (a session is also created automatically on your
first message), **`/model <id>`** switches the session's model (bare `/model` shows the current
one, `/model -` returns to the engine default) and **`/space <name>`** its data context (both
effective from your next message, over `PATCH /sessions/{id}`), and **`/reload`** re-reads the
engine's prompt files + agent-type catalog (effective from your next message — the same
management action as `agent reload`, gated by the bot's allowlist). The `/new` + `/reset` +
`/end` + `/purge` + `/model` + `/space` verbs match the CLI chat REPL. It is **entirely
optional**: with no token set, the engine runs exactly as normal.

A model id is **not** validated when you set it — an unknown one fails the next turn with the
provider's error, and `/model -` gets you back.

### Sending files

Send a file (or a photo) to the chat and it is stored in that session's **scratch directory**,
recorded in the [artifact cache](#the-artifact-cache) as user-provided, and a
turn runs with the caption as your message. The agent is told *where the file is*, not what is in
it — it reads what it needs with the tools it already has (`shell`, `run_code`), which is how a
CSV, log, or source file works on a text-only model. Drop a CSV in with "how many rows per
region?" and it will read it. Because the file is recorded as user-provided, closing the session
(`/end`) **keeps** it while reaping the agent's re-derivable scratch; only `/purge` deletes it.

Limits and caveats:

- **20 MB** per file (the Telegram Bot API's own download ceiling); a larger one is refused up front.
- **Images are stored, but not seen.** The model is text-only today, so the agent is told it cannot
  read image content rather than being left to invent it. The upload plumbing is already in place
  for when a vision path lands.
- The filename is sanitized to a safe basename (no traversal, no separators) and made unique, so an
  upload can neither escape the scratch dir nor overwrite an existing artifact.
- A file's contents are **untrusted input**, like fetched web content: the turn text tells the agent
  to treat them as data, never as instructions.
- If the agent has asked you a question, answer it before sending a file (the parked turn holds the
  session lock).

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

Fully separating two agents takes **two config dirs *and* two workspaces** — they hold
different halves of the state:

| Store | Scope | Separated by |
| --- | --- | --- |
| config, tool catalog, audit log, sessions, transcripts | config-dir (default `~/.config/ai-agent`) | `--config-dir` / `AI_AGENT_CONFIG_DIR` |
| **memory + spaces** (`<workspace>/.agent/`) | workspace (default: the cwd) | `--workspace` |

> **Two config-dirs pointed at the same workspace share memory and spaces.** Since memory moved
> to the workspace (see [Long-term memory](#long-term-memory)), `--config-dir` alone no longer
> separates it. Give each agent its own `--workspace` too, or they will read and write each
> other's notes. Full model: [`environment.md`](environment.md#two-scopes-config-dir-and-workspace).

To run two independent agents on one box, start two `serve` processes on two ports, each with
its own config dir **and** its own workspace:

```bash
# agent "work"
./agent --config-dir ~/.config/ai-agent/work --workspace ~/ws/work \
        serve --addr 127.0.0.1:8080 --tier safe

# agent "home" (env form for the config dir)
AI_AGENT_CONFIG_DIR=~/.config/ai-agent/home \
  ./agent --workspace ~/ws/home serve --addr 127.0.0.1:8081 --tier balanced
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
        --sessions-dir /var/log/agent-work/runs \
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
  / `AI_AGENT_SESSIONS_DIR`), so separate `--config-dir` agents share no transcripts. (Memory
  and spaces are workspace-local, so they are shared unless the agents also differ by
  `--workspace` — see `environment.md` §Two scopes.)
- Precedence everywhere: **flag > env > config value > built-in default**.

See [`environment.md`](environment.md#configuration--environment-reference) for the tables.
