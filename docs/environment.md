# Runtime environment — identity, target, trust, customization

The single reference for **the environment a run executes in**: who the agent is, what it is
acting on, how trusted it is, and how its prompt is customized. For the day-to-day *how do I run
it*, see [`usage.md`](usage.md); for *why* the model is shaped this way, [`design.md`](design.md)
and [`security.md`](security.md).

- [Two scopes: config-dir and workspace](#two-scopes-config-dir-and-workspace)
- [Trust tier — the safety dial](#trust-tier--the-safety-dial)
- [Prompt customization (SYSTEM.md / AGENTS.md)](#prompt-customization-systemmd--agentsmd)
- [Sub-agent types (agents/\*.md)](#sub-agent-types-agentsmd)
- [Configuration & environment reference](#configuration--environment-reference)
- [Files on disk](#files-on-disk)

---


## Two scopes: config-dir and workspace

The environment a run executes in is layered from two scopes. Each answers a different question,
and the workspace **inherits from** the config-dir — it can add to or override the config-dir's
guidelines, never the reverse.

- **config-dir — *who the agent is.*** Its identity and durable state: memory, the tool catalog,
  the audit log, and the global prompt files. It is the only scope that carries identity, and it
  is **always trusted** (it is the agent's own state). Two agents that must share nothing get two
  config-dirs.
- **workspace — *what the agent is acting on.*** The directory the agent lives and works in: the
  shell's working directory and the workspace-wide prompt files (guidelines for whatever is under
  it). A workspace is trusted only **above** the `safe` tier; a checkout can be hostile.

**Identity never flows down into a target.** Memory, the tool catalog, and the audit log stay
config-dir-scoped — a workspace does *not* get its own. Switching what the agent works on is a
workspace change; giving it a different identity is a config-dir change.

| Scope | Answers | Holds | Guideline layer | Trust |
| --- | --- | --- | --- | --- |
| **config-dir** | who the agent *is* | memory, tool catalog, audit, global prompt files | 1 — always applied | always trusted |
| **workspace** | what it *acts on* | shell cwd, workspace prompt files | 2 — inherits config-dir | trusted only above `safe` |

> **Deferred: named projects.** An earlier design added a third scope — a *project*, a named,
> recallable sub-scope *within* a workspace that the agent could switch into mid-conversation
> (with its own guideline overlay and artifact/session home). That feature has been set aside to
> keep the model simple; its design is preserved in [`deferred/projects.md`](deferred/projects.md)
> for a later revisit. Today there is exactly config-dir + workspace.

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
| `SYSTEM.md` | **Replaces** the built-in base prompt (executor) | workspace wins outright over config-dir (replace ⇒ last writer) |
| `AGENTS.md` (alias `CLAUDE.md`) | **Appended** as operator instructions (executor) | config-dir first, then workspace, concatenated (workspace has the last word) |
| `PLANNER.md` | **Replaces** the built-in planner prompt (the clarify/refine pass) | workspace wins outright over config-dir |
| `CRITIC.md` | **Replaces** the built-in critic prompt (the deliberate critique loop's verdict pass) | workspace wins outright over config-dir |

`PLANNER.md` tunes the planner (`agent run`, and every deliberate `agent chat` / session turn);
`CRITIC.md` tunes the critic (the critique loop in a deliberate `agent chat` / session turn). The
planner's structured Plan and the critic's Verdict are each enforced by a JSON schema regardless,
so an override can restyle the pass but can't break its contract. Both are re-read on each
deliberate turn / run (and on `/reload`), so edits take effect without a rebuild.

Precedence is **workspace over config-dir**, matching pi. When both `AGENTS.md` and `CLAUDE.md`
exist in one directory, `AGENTS.md` wins (the alias is not also appended). The workspace tier is
**tier-gated**: on `safe` it does not auto-load unless you pass `--workspace` explicitly (an
untrusted checkout's `AGENTS.md` lands *in the system prompt*, so it is a prompt-injection vector).

Two escape hatches:

- `--context-file <path>` (repeatable) — extra prompt file(s) appended last, **always honored
  regardless of tier** (you named them). A missing named file is an error (unlike absent tier
  files, which are a no-op).
- `--no-context-files` — ignore all `SYSTEM.md` / `AGENTS.md` / `PLANNER.md` / `CRITIC.md` /
  `--context-file` loading **and** the workspace/config-dir `agents/*.md` sub-agent types, and
  run on the bare built-in base prompts + built-in agent types (reproducible runs, debugging).

---

## Sub-agent types (agents/\*.md)

The agent can delegate to **sub-agents** via a `spawn_agent(type, task)` tool: it builds a child
executor of the named type, runs it to a final answer, and gets the text back. Types are declared
as `agents/<name>.md` files under **both anchors** (`<config-dir>/agents/` global, then
`<workspace>/agents/`) — the same layout pi and Claude Code use, so those files drop in.

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
one (**workspace > global > built-in**). Like the prompt tier, the workspace `agents/` dir is
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
| `--workspace` (global flag) | — | Directory the agent acts on: shell cwd + workspace prompt files & agent types. Default: process cwd. |
| `--context-file` (global flag, repeatable) | — | Extra prompt file(s) appended last, always loaded regardless of tier. |
| `--no-context-files` (global flag) | — | Ignore all `SYSTEM.md`/`AGENTS.md`/`PLANNER.md`/`CRITIC.md`/`--context-file` **and** `agents/*.md`; run on the bare base prompts + built-in agent types. |
| `--sessions-dir` (global flag) | `AI_AGENT_SESSIONS_DIR` | Per-run transcripts (one subdir per run). Default `<config-dir>/runs`, so separate `--config-dir` agents share nothing. |
| `openai_key` | — | OpenAI API key. |
| `openai_base_url` | `AI_AGENT_OPENAI_BASE_URL` | Base URL for the OpenAI-compatible API. Empty ⇒ the real OpenAI API; set it to point at a local llama.cpp/Ollama/vLLM server, OpenRouter, or a proxy (`config set-base-url`). |
| `model` | `AI_AGENT_MODEL` (`--model` flag wins) | Default model (built-in default `gpt-4o-mini`). |
| `tier` | `AI_AGENT_TIER` (`--tier` flag wins) | Default trust tier (built-in default `balanced`). |
| `verbose` | `AI_AGENT_VERBOSE` (`--verbose`/`--quiet` flag wins) | Default trace verbosity (built-in default off). Gates only the live CLI tool-call trace; the on-disk transcript is always written. `chat` is quiet by default and has a live `/verbose [on\|off]` toggle. |
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
| `<config-dir>/sessions/<id>.json` | Persisted conversations (one file per session — the resumable session **store**, agent state). |
| `<config-dir>/sessions/archive/<id>.json` | Closed conversations, **archived not deleted** (`/end` / `DELETE /sessions/{id}`). Excluded from the resumable listing; move one back up a level to resume it. |
| `<config-dir>/session-scratch/<id>/` | Deliberate `serve` turns: a session's disk-backed artifact cache + `manifest.json`, persistent across turns/restarts (keyed by session id). Reaped when the session is closed; cache-with-fallback keeps a stale/absent file correct otherwise. |
| `<config-dir>/runs/<run-id>/` | Per-run transcripts (**logs**) + `info.json` (final run metadata), unless overridden by `--sessions-dir`. Distinct from `sessions/` above. |
| `<config-dir>/SYSTEM.md`, `AGENTS.md`, `PLANNER.md`, `CRITIC.md` | Global prompt customization (optional; `PLANNER.md`/`CRITIC.md` override the planner/critic). |
| `<config-dir>/agents/<name>.md` | Global sub-agent type definitions (optional). |

Under the **workspace** (the directory the agent acts on; default the cwd), optional and tier-gated:

| Path | What |
| --- | --- |
| `<workspace>/SYSTEM.md`, `AGENTS.md` (alias `CLAUDE.md`), `PLANNER.md`, `CRITIC.md` | Workspace prompt customization (`PLANNER.md`/`CRITIC.md` override the planner/critic). |
| `<workspace>/agents/<name>.md` | Workspace sub-agent type definitions. |

Under the **runs dir** (default `<config-dir>/runs`, override with `--sessions-dir` /
`AI_AGENT_SESSIONS_DIR`):

| Path | What |
| --- | --- |
| `<runs-dir>/<run-id>/` | Per-run transcript: `run.jsonl`, `audit.jsonl`, `artifacts/`, `info.json`. |

All are created on first use; deleting them resets the corresponding state. One exception: closing
a **session** archives it under `sessions/archive/` rather than removing it (so a mistaken `/end`
is recoverable) — delete an archived file by hand to reclaim its space.
