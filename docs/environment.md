# Runtime environment — identity, target, trust, customization

The single reference for **the environment a run executes in**: who the agent is, what it is
acting on, how trusted it is, how its prompt is customized, and how all of that is configured.
For the day-to-day *how do I run it*, see [`usage.md`](usage.md); for *why* the model is shaped
this way, [`design.md`](design.md) and [`security.md`](security.md).

- [Three scopes: config-dir, workspace, project](#three-scopes-config-dir-workspace-project)
- [Trust tier — the safety dial](#trust-tier--the-safety-dial)
- [Prompt customization (SYSTEM.md / AGENTS.md)](#prompt-customization-systemmd--agentsmd)
- [Sub-agent types (agents/\*.md)](#sub-agent-types-agentsmd)
- [Projects — named sub-scopes within a workspace](#projects--named-sub-scopes-within-a-workspace)
- [Configuration & environment reference](#configuration--environment-reference)
- [Files on disk](#files-on-disk)

---


## Three scopes: config-dir, workspace, project

The environment a run executes in is layered from three nested scopes. Each answers a different
question, and each **inherits from the one above it** — an inner scope can add to or override the
guidelines of an outer one, never the reverse.

- **config-dir — *who the agent is.*** Its identity and durable state: memory, the tool catalog,
  the audit log, and the global prompt files. It is the only scope that carries identity, and it is
  **always trusted** (it is the agent's own state). Two agents that must share nothing get two
  config-dirs.
- **workspace — *what the agent is acting on.*** The directory the agent lives and works in: the
  shell's working directory, workspace-wide prompt files (guidelines common to everything under it),
  and — when no project is active — the home for that session's artifacts and sessions. A workspace
  is trusted only **above** the `safe` tier; a checkout can be hostile.
- **project — *a named corner of the workspace.*** A recallable sub-scope the agent can switch into
  mid-conversation. It inherits the workspace's guidelines and may override or append its own, and it
  keeps its own artifacts and sessions apart from the workspace's scratch.

**Identity never flows down into a target.** Memory, the tool catalog, and the audit log stay
config-dir-scoped — a workspace or project does *not* get its own. Switching what the agent works on
is a workspace/project change; giving it a different identity is a config-dir change.

| Scope | Answers | Holds | Guideline layer | Trust |
| --- | --- | --- | --- | --- |
| **config-dir** | who the agent *is* | memory, tool catalog, audit, global prompt files | 1 — always applied | always trusted |
| **workspace** | what it *acts on* | shell cwd, workspace prompt files, default artifact/session home when no project | 2 — inherits config-dir | trusted only above `safe` |
| **project** | a named sub-scope *within* the workspace | project prompt overrides/appends, its own artifacts + sessions | 3 — inherits workspace, wins | inherits the workspace's trust |

> **Built vs. designed.** Layers 1–2 exist today: `loadPrompts` (`cmd/prompts.go`) composes the
> config-dir tier with the single active workspace/project directory. The full **workspace → project
> inheritance** (a project layering *on top of* its enclosing workspace's guidelines rather than
> replacing them) is designed, not yet built — today the active directory composes only with the
> config-dir, not with an enclosing workspace root.

### Where artifacts and sessions live *(layout provisional)*

Work products live on disk, scoped to whichever level owns them, under the `.agent/` marker dir (the
same convention as the project marker `.agent/project.md`; [`projects.md`](planning/projects.md)). The exact
paths below are a working sketch, not settled — `<workspace>` is the workspace root, distinct from the
active `workDir` (which re-anchors to the project dir once a project is switched into).

- **Workspace scratch** (no project active) — keyed per session, so concurrent or resumed sessions
  don't collide:
  - sessions:  `<workspace>/.agent/sessions/<session-id>/`
  - artifacts: `<workspace>/.agent/sessions/<session-id>/artifacts/`
- **Project** — durable and shared across the sessions that work in that project:
  - project root: `<workspace>/projects/<slug>-<uid>/`
  - sessions:     `<workspace>/projects/<slug>-<uid>/.agent/sessions/<session-id>/`
  - artifacts:    `<workspace>/projects/<slug>-<uid>/.agent/artifacts/`

Each stored artifact is described by a **manifest** entry — metadata about the file itself (schema,
shape, description) and about its origin (for a web source: site, etag, media type, …) — so the
planner can reason about what exists without opening the bytes (see [`planning/chat-planner.md`](planning/chat-planner.md) §D4).

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
| `SYSTEM.md` | **Replaces** the built-in base prompt (executor) | project (workspace) wins outright over global (config-dir) |
| `AGENTS.md` (alias `CLAUDE.md`) | **Appended** as operator/project instructions (executor) | global first, then project, concatenated (project has the last word) |
| `PLANNER.md` | **Replaces** the built-in planner prompt (the pre-execution clarify/refine pass) | project (workspace) wins outright over global (config-dir) |

`PLANNER.md` tunes only the planner (`agent run`, `agent chat --plan`); the planner's structured
Plan output is enforced by a JSON schema regardless, so an override can't break the plan contract.
It is re-read on each planned turn / run (and on `/reload`), so edits take effect without a rebuild.

Precedence is **project over global**, matching pi. When both `AGENTS.md` and `CLAUDE.md` exist in
one directory, `AGENTS.md` wins (the alias is not also appended). The workspace tier is
**tier-gated**: on `safe` it does not auto-load unless you pass `--workspace` explicitly (an
untrusted checkout's `AGENTS.md` lands *in the system prompt*, so it is a prompt-injection vector).

Two escape hatches:

- `--context-file <path>` (repeatable) — extra prompt file(s) appended last, **always honored
  regardless of tier** (you named them). A missing named file is an error (unlike absent tier
  files, which are a no-op).
- `--no-context-files` — ignore all `SYSTEM.md` / `AGENTS.md` / `PLANNER.md` / `--context-file`
  loading and run on the bare built-in base prompts (reproducible runs, debugging).

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

## Projects — named sub-scopes within a workspace

A **project** is a named sub-scope *within* a workspace (the third scope above), so a conversation
can recall it *by intent* (*"the articles from last time"*) and switch into it without you ever
naming a path. It inherits the workspace's common guidelines and adds its own, and keeps its own
artifacts and sessions — a named corner of the sandbox the agent can **find again**, not a
replacement for it.

Projects live under the home workspace, and **the filesystem is the registry** — there is no
separate index to fall out of sync:

```
<home-workspace>/
  projects/
    articles-a3f9c1/
      .agent/project.md      # title, uid, description, timestamps (YAML frontmatter)
      … project artifacts …
    health-analysis-7b2e04/
      .agent/project.md
  … scratch work lives at the workspace root, un-promoted …
```

The folder is `<slug>-<uid>`: the **uid is the stable identity** (retitle freely without moving
the folder or breaking references), the slug a human convenience. `<home>/projects/` sits under a
workspace you already authorized, so switching into a project inherits that trust by
**containment** — and the switch still re-runs the [tier gate](#trust-tier--the-safety-dial) on
the target's prompt files, so a just-scaffolded project's `AGENTS.md` doesn't auto-load on `safe`.

The agent works the loop **scratch → promote → recall → switch** through three trusted built-in
tools (never exposed to sandboxed `call_tool`), offered whenever the registry is enabled:

| Tool | Does |
| --- | --- |
| `list_projects()` | Enumerate the registry (uid, title, description, last-active) — how the agent knows what exists. |
| `create_project(title, description?)` | Mint `<slug>-<uid>/`, seed the marker, switch into it. **Human-gated + audited.** Promotion = the same call moving scratch artifacts in. |
| `switch_project(uid \| title)` | Resolve (uid exact → title exact → title substring; ambiguous is *reported*, not guessed) and re-anchor the workspace + reload the project prompt tier. **Audited.** |

**CLI control** (projects.md §6) — three modes, alongside `--workspace`:

| Flag / config | Effect |
| --- | --- |
| *(default)* | Registry active at `<workspace>/projects`; the agent works at the home root until it creates/switches into a project. |
| `--no-project` (or config `projects: false`) | **Flat-repo mode:** no registry, no project tools, the workspace *is* the repo. The pi-faithful "I cd'd in, just act on this" path. |
| `--project <uid\|title\|path>` | Activate a project **at launch** — the workspace becomes that project's directory (a path is used directly; a uid/title is resolved against the registry). The registry root stays the home one, so `switch_project` can still reach siblings. |
| config `projects_root` | Point the registry somewhere other than `<workspace>/projects`. |

Precedence: `--no-project` wins outright; an explicit `--project` forces projects on (overriding
config `projects: false`); `--no-project` together with `--project` is an error. Set the config
defaults with `agent config set-projects <on|off>` / `set-projects-root <path>`.

---

## Configuration & environment reference

Config file: `<config-dir>/config.json` (created by `config set-*`).

| Config key / flag | Env override | Meaning |
| --- | --- | --- |
| `--config-dir` (global flag) | `AI_AGENT_CONFIG_DIR` | Agent identity dir: config/tools/memory/audit + global prompt files & agent types. Default `~/.config/ai-agent`. |
| `--workspace` (global flag) | — | Project the agent acts on: shell cwd + project prompt files & agent types. Default: process cwd. |
| `--context-file` (global flag, repeatable) | — | Extra prompt file(s) appended last, always loaded regardless of tier. |
| `--no-context-files` (global flag) | — | Ignore all `SYSTEM.md`/`AGENTS.md`/`PLANNER.md`/`--context-file`; run on the bare base prompts. |
| `--no-project` (global flag) | — | Flat-repo mode: no named-project registry and no list/create/switch_project tools. |
| `--project` (global flag) | — | Activate a project at launch by uid, title, or path; the workspace becomes its directory. |
| `projects` | `--no-project` flag | Whether the project registry is enabled by default (built-in default on). Set with `config set-projects`. |
| `projects_root` | — | Registry location; default `<workspace>/projects`. Set with `config set-projects-root`. |
| `--sessions-dir` (global flag) | `AI_AGENT_SESSIONS_DIR` | Per-run transcripts (one subdir per run). Default `<config-dir>/runs`, so separate `--config-dir` agents share nothing. |
| `openai_key` | — | OpenAI API key. |
| `model` | `--model` flag | Default model (built-in default `gpt-4o-mini`). |
| `tier` | `--tier` flag | Default trust tier (built-in default `balanced`). |
| `verbose` | `--verbose`/`--quiet` flag, `AI_AGENT_VERBOSE` env | Default trace verbosity (built-in default off). Gates only the live CLI tool-call trace; the on-disk transcript is always written. `chat` is quiet by default and has a live `/verbose [on\|off]` toggle. |
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
| `<config-dir>/runs/<run-id>/` | Per-run transcripts (**logs**), unless overridden by `--sessions-dir`. Distinct from `sessions/` above. |
| `<config-dir>/SYSTEM.md`, `AGENTS.md`, `PLANNER.md` | Global prompt customization (optional; `PLANNER.md` overrides the planner). |
| `<config-dir>/agents/<name>.md` | Global sub-agent type definitions (optional). |

Under the **workspace** (the project dir; default the cwd), optional and tier-gated:

| Path | What |
| --- | --- |
| `<workspace>/SYSTEM.md`, `AGENTS.md` (alias `CLAUDE.md`), `PLANNER.md` | Project prompt customization (`PLANNER.md` overrides the planner). |
| `<workspace>/agents/<name>.md` | Project sub-agent type definitions. |
| `<workspace>/projects/<slug>-<uid>/.agent/project.md` | A named project's registry marker (title, uid, timestamps). Disabled by `--no-project`; relocated by `projects_root`. |

Under the **runs dir** (default `<config-dir>/runs`, override with `--sessions-dir` /
`AI_AGENT_SESSIONS_DIR`):

| Path | What |
| --- | --- |
| `<runs-dir>/<run-id>/` | Per-run transcript: `run.jsonl`, `audit.jsonl`, `artifacts/`. |

All are created on first use; deleting them resets the corresponding state.
