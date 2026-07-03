# Runtime environment — identity, target, trust, customization

The single reference for **the environment a run executes in**: who the agent is, what it is
acting on, how trusted it is, how its prompt is customized, and how all of that is configured.
For the day-to-day *how do I run it*, see [`usage.md`](usage.md); for *why* the model is shaped
this way, [`design.md`](design.md) and [`security.md`](security.md).

- [Two anchors: config-dir vs workspace](#two-anchors-config-dir-vs-workspace)
- [Trust tier — the safety dial](#trust-tier--the-safety-dial)
- [Prompt customization (SYSTEM.md / AGENTS.md)](#prompt-customization-systemmd--agentsmd)
- [Sub-agent types (agents/\*.md)](#sub-agent-types-agentsmd)
- [Configuration & environment reference](#configuration--environment-reference)
- [Files on disk](#files-on-disk)

---

## Two anchors: config-dir vs workspace

A run is anchored by **two** directories with distinct jobs. Keeping them separate is the
two-tier model pi and Claude Code use, and it is what stops "the project I'm working on" from
leaking into "who this agent is."

| | **config-dir** | **workspace** |
| --- | --- | --- |
| Answers | *who* the agent is (identity/state) | *what* it is acting on (the project) |
| Holds | config, tool catalog, memory, audit log, **global** prompt files + agent types | the shell tool's working directory, **project** prompt files + agent types |
| Scope | one per agent | one per run/session; can differ each run |
| Set by | `--config-dir` / `AI_AGENT_CONFIG_DIR` (default `~/.config/ai-agent`) | `--workspace` / process cwd |
| Trust | always trusted (the agent's own state) | trusted only above `safe` — a checkout can be hostile (see the tier gate below) |

The load-bearing rule: **a workspace anchors context and targets, never identity.** Memory, the
tool catalog, and the audit log stay config-dir-scoped — a workspace does *not* get its own. Two
agents that must share nothing get **two config-dirs** (see
[Running multiple independent agents](usage.md#running-multiple-independent-agents)); switching
what a single agent works on is a **workspace** change.

Today the workspace resolves to the `--workspace` directory (validated, absolutized) or the
process cwd; there is no parent-directory walk to a project root yet. `serve` uses one workspace
(its process cwd) for every run it handles.

---

## Trust tier — the safety dial

The tier decides which capabilities an agent-authored tool may use **without asking a human**,
and whether an untrusted workspace's files auto-load. It is the main autonomy control: conservative
when the agent runs unattended, looser when you are watching.

| Tier | Auto-approves | Prompts for | Workspace files |
| --- | --- | --- | --- |
| `safe` | nothing | every capability | **not** auto-loaded (config-dir globals still load) |
| `balanced` *(default)* | side-effect-free reads (clock, random, read file) | writes, network fetches, calling other tools | auto-loaded |
| `permissive` | everything | nothing *(full autonomy — only when watched)* | auto-loaded |

Set it per run with `--tier`, or as a default with `agent config set-tier`. The tier only decides
whether a human must say yes first; a tool's own allowlist (hosts / paths / callable tools) still
bounds what it can touch regardless of tier. An explicit `--workspace` or `--context-file`
authorizes those files even on `safe` — you named them, so they are trusted.

---

## Prompt customization (SYSTEM.md / AGENTS.md)

The system prompt is composed once at construction (so the prompt-cache prefix stays stable) from
the built-in base plus operator/project files, read from **both anchors**:

| File | Effect | Combined across tiers |
| --- | --- | --- |
| `SYSTEM.md` | **Replaces** the built-in base prompt | project (workspace) wins outright over global (config-dir) |
| `AGENTS.md` (alias `CLAUDE.md`) | **Appended** as operator/project instructions | global first, then project, concatenated (project has the last word) |

Precedence is **project over global**, matching pi. When both `AGENTS.md` and `CLAUDE.md` exist in
one directory, `AGENTS.md` wins (the alias is not also appended). The workspace tier is
**tier-gated**: on `safe` it does not auto-load unless you pass `--workspace` explicitly (an
untrusted checkout's `AGENTS.md` lands *in the system prompt*, so it is a prompt-injection vector).

Two escape hatches:

- `--context-file <path>` (repeatable) — extra prompt file(s) appended last, **always honored
  regardless of tier** (you named them). A missing named file is an error (unlike absent tier
  files, which are a no-op).
- `--no-context-files` — ignore all `SYSTEM.md` / `AGENTS.md` / `--context-file` loading and run on
  the bare built-in base prompt (reproducible runs, debugging).

---

## Sub-agent types (agents/\*.md)

The agent can delegate to **sub-agents** via a `spawn_agent(type, task)` tool: it builds a child
executor of the named type, runs it to a final answer, and gets the text back. Types are declared
as `agents/<name>.md` files under **both anchors** (`<config-dir>/agents/` global, then
`<workspace>/agents/` project) — the same layout pi and Claude Code use, so those files drop in.

A file is Markdown with a YAML frontmatter header; the body is the sub-agent's system prompt:

```markdown
---
description: researches a question using only web tools
tools: web_search, web_fetch      # comma/space-separated; "*" = inherit the parent's tools
model: gpt-4o                      # optional model override
parallel: false                   # if true, tools must be read-only
prompt_mode: replace               # "replace" (default) | "append" (inherit parent AGENTS.md)
---
You are a focused research assistant. Answer only from what the tools return…
```

Built-in types (`researcher`, `general-purpose`) are always available; a same-named file overrides
one (**project > global > built-in**). Like the prompt tier, the project `agents/` dir is
**tier-gated** (a `safe` agent does not auto-load a checkout's agent definitions — their bodies are
sub-agent system prompts, i.e. injection surface — unless `--workspace` is explicit);
`--no-context-files` leaves only the built-ins. A malformed or invalid file (e.g. a `parallel` type
that inherits all tools) is a hard error, not a silent skip.

After editing prompt or agent-type files, pick them up without a restart: **`/reload`** in
`agent chat`, or **`agent reload --addr`** (⇢ `POST /reload`) against a running `agent serve` (see
[`usage.md`](usage.md#customizing-the-agent--prompts--agent-types)). Try variants side by side with
**`agent eval`** (see [`usage.md`](usage.md#comparing-configurations--agent-eval)).

---

## Configuration & environment reference

Config file: `<config-dir>/config.json` (created by `config set-*`).

| Config key / flag | Env override | Meaning |
| --- | --- | --- |
| `--config-dir` (global flag) | `AI_AGENT_CONFIG_DIR` | Agent identity dir: config/tools/memory/audit + global prompt files & agent types. Default `~/.config/ai-agent`. |
| `--workspace` (global flag) | — | Project the agent acts on: shell cwd + project prompt files & agent types. Default: process cwd. |
| `--context-file` (global flag, repeatable) | — | Extra prompt file(s) appended last, always loaded regardless of tier. |
| `--no-context-files` (global flag) | — | Ignore all `SYSTEM.md`/`AGENTS.md`/`--context-file`; run on the bare base prompt. |
| `--sessions-dir` (global flag) | `AI_AGENT_SESSIONS_DIR` | Per-run transcripts (one subdir per run). Default `~/.local/share/ai-agent/sessions`. |
| `openai_key` | — | OpenAI API key. |
| `model` | `--model` flag | Default model (built-in default `gpt-4o-mini`). |
| `tier` | `--tier` flag | Default trust tier (built-in default `balanced`). |
| `engines` | — | Map of alias → engine `host:port` for `--addr` (managed by `config set-engine`/`rm-engine`/`engines`). |
| `telegram_token` | `AI_AGENT_TELEGRAM_TOKEN` | Telegram bot token; empty ⇒ bot disabled. |
| `telegram_allowed_users` | `AI_AGENT_TELEGRAM_ALLOWED_USERS` | Allowed Telegram user ids (env is comma-separated). |

Precedence everywhere: **flag > env > config value > built-in default**.

---

## Files on disk

Under the **config dir** (default `~/.config/ai-agent`, overridable with `--config-dir` /
`AI_AGENT_CONFIG_DIR`):

| Path | What |
| --- | --- |
| `<config-dir>/config.json` | API key, default model/tier, engine aliases, Telegram settings. |
| `<config-dir>/tools.json` | Persisted agent-authored tool catalog. |
| `<config-dir>/memory.json` | Long-term memory store. |
| `<config-dir>/audit.jsonl` | Process-wide audit log (written by `serve`). |
| `<config-dir>/sessions/<id>.json` | Persisted conversations (one file per session). |
| `<config-dir>/SYSTEM.md`, `AGENTS.md` | Global prompt customization (optional). |
| `<config-dir>/agents/<name>.md` | Global sub-agent type definitions (optional). |

Under the **workspace** (the project dir; default the cwd), optional and tier-gated:

| Path | What |
| --- | --- |
| `<workspace>/SYSTEM.md`, `AGENTS.md` (alias `CLAUDE.md`) | Project prompt customization. |
| `<workspace>/agents/<name>.md` | Project sub-agent type definitions. |

Under the **sessions dir** (default `~/.local/share/ai-agent/sessions`, override with
`--sessions-dir` / `AI_AGENT_SESSIONS_DIR`):

| Path | What |
| --- | --- |
| `<sessions-dir>/<run-id>/` | Per-run transcript: `run.jsonl`, `audit.jsonl`, `artifacts/`. |

All are created on first use; deleting them resets the corresponding state.
