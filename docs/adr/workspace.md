# Workspace — concept & roadmap

Defines **workspace**: the directory sandbox the agent *lives and works in* — the container holding
the guidelines common to everything under it — as distinct from the **config-dir** (*who the agent
is*) above it. The two form a **two-scope model** — **config-dir → workspace** — the workspace
inheriting the config-dir and able to override it, the layered-config pattern pi and Claude Code use.

**This is built, and its behavior is now canonical in [`../environment.md`](../environment.md)**
("Two scopes: config-dir and workspace"). This doc is retained as the **design record** and for the
remaining roadmap (§6–§7). Companion to [`prompts.md`](prompts.md) (the prompt-tier consumer) and
[`subagents.md`](subagents.md) (scouts / worktree isolation).

> **Deferred: a third scope (projects).** An earlier design added a **project** — a named sub-scope
> *within* a workspace, switchable mid-conversation — making a three-scope model. It was built and
> then reverted to keep the model simple; its design is preserved in
> [`../deferred/projects.md`](../deferred/projects.md). References to it below are marked *(deferred)*.

**Implementation status.** The workspace anchor (`resolveWorkspace`/`--workspace`) for the shell
`workDir` and the tier-gated prompt tier (§5) are **built** (stages B–C). What remains: sub-agent
targets / worktree isolation (§4, [`subagents.md`](subagents.md)) and settling the parent-walk bound
(§6).

---

## 0. Two scopes — identity and target

Two nested scopes, the inner inheriting the outer and able to override it. This doc owns the inner
one (**workspace**); the config-dir is *who the agent is*. The canonical table lives in
[`../environment.md`](../environment.md#two-scopes-config-dir-and-workspace); in short:

| | **config-dir** | **workspace** |
|---|---|---|
| Answers | *who* the agent is (identity) | *what* it acts on — the sandbox it lives in |
| Holds | config, tool catalog, memory, audit, **global** prompt files | shell cwd, **workspace-common** prompt files, scratch home, scout target |
| Scope | one per agent (`--config-dir`, default `~/.config/ai-agent`) | one per run/session — the sandbox root |
| Trust | always trusted (the agent's own state) | trusted only above `safe` (§5) — a checkout can be hostile |
| Guideline layer | 1 — always | 2 — inherits config-dir, wins |

The load-bearing boundary: a workspace anchors **context and targets**, never **identity**. Memory,
tools, and audit stay config-dir-scoped — a workspace does *not* get its own memory or catalog
(that's the config-dir's job, and per-workspace separation is already achieved by pointing at a
different config-dir). See §4.

---

## 1. `workDir` today (current behavior)

The workspace is the shell face of the shell tool's working directory, `workDir`:

- `run` / `chat`: `workDir` = the resolved workspace (launch cwd, or `--workspace`).
- `serve`: `workDir` is fixed at process start (`serveDeps.workDir`), the same for every run the
  engine handles.

`workDir` is *where shell commands execute*; the workspace **generalizes and names** it, additionally
anchoring prompt context (§4).

---

## 2. Resolving the workspace, per frontend

- **CLI (`run` / `chat`)** — workspace = the launch **cwd** (the pi/Claude-Code ergonomic: `cd` into
  a repo and it's picked up), or an explicit `--workspace`. *(Parent-directory walking to collect
  project files is not wired — the stop bound is open, §6.)*
- **`serve`** — the process cwd is the single workspace for the whole engine (simple, coherent,
  matches "cd here, then serve"). *Extension, designed-for not built:* a per-run/per-session
  `workspace` field on the run request, so one long-lived engine can serve multiple projects. The
  resolver accepts an explicit workspace, so this is additive.

The config-dir resolves identically regardless of frontend (global tier, always).

---

## 3. Precedence — inner scope over outer

Wherever more than one scope supplies something (prompt files today; agent types later), the **inner
scope overrides or augments the outer** — **config-dir (global) → workspace (common)** — the
layered-config rule pi uses:

- append-style inputs (`AGENTS.md`): outer-to-inner, concatenated (the workspace has the last word).
- replace-style inputs (`SYSTEM.md`, an agent type of the same name): the innermost present wins.

This is **built** as a two-tier compose (`loadPrompts(workspace, tier)`, `cmd/prompts.go`); mechanics
in [`prompts.md`](prompts.md) §2. *(deferred)* A third project tier nested under the workspace was
specified in [`../deferred/projects.md`](../deferred/projects.md) but never wired.

---

## 4. What a workspace anchors (and what it does not)

**Does:**

- **Workspace prompt context** — the workspace-common tier of `AGENTS.md` / `CLAUDE.md` / `SYSTEM.md`
  (`prompts.md`). First and primary consumer — **built**.
- **Sub-agent targets** — a `scout` / repo-reader operates "on *this* workspace" (`subagents.md` §2).
  *Roadmap.*
- **Worktree isolation** (later) — `AgentType.Isolation: "worktree"` needs a workspace/git root as the
  thing to snapshot (`subagents.md`). *Roadmap.*

**Does not:** scope memory, tools, or audit — those remain config-dir-scoped (agent identity).
Keeping this boundary is what stops "workspace" from ballooning into a second config-dir. If a genuine
per-workspace memory/tool need appears, it's a deliberate future decision (§6), not a default.

---

## 5. Trust — the reason this isn't just "load some files"

The config-dir is as trusted as the stored API key; its files always load. A **workspace can be an
untrusted checkout**, so a workspace `AGENTS.md` is a prompt-injection vector (worse than fenced web
content — it lands *in the system prompt*). pi accepts this (it trusts the cwd); we shouldn't
unconditionally, given deny-by-default.

Rule: **workspace file auto-loading is tier-gated** (built):

- `safe` — do **not** auto-load workspace prompt files (config-dir globals still load).
- `balanced` / `permissive` — auto-load workspace files (pi-compatible interactive behavior).
- An explicit `--context-file <path>` / `--workspace <dir>` is always honored regardless of tier (the
  user named it, so it's authorized).

This keeps the pi ergonomic for interactive trusted use while never letting an arbitrary repo inject
into a `safe`-tier agent.

---

## 6. Open questions

- **Parent-walk bound** — if the CLI walks up parent directories to collect prompt files, stop the
  walk at a git root? a filesystem root? a `.agent`/marker file? (pi walks to the home/root; bounding
  at a VCS root is safer.) *Still open — currently a single resolved workspace dir, no walk.*
- **`serve` multi-workspace / mid-session switch** — the shell's `tools.Workspace` anchor is
  re-anchorable, so a mid-session workspace switch is mechanically possible; who *vouches* for a
  workspace that arrives at runtime (trust origin) was the design problem the *(deferred)* projects
  work answered by containment ([`../deferred/projects.md`](../deferred/projects.md) §1/§5).
- **Ever scope memory/tools?** — v1 says no (§4). Revisit only if one running agent must keep
  genuinely separate per-workspace memory that config-dir separation can't serve.

---

## 7. Tasks

- [x] `cmd/workspace.go` — `resolveWorkspace()` returning the workspace root: persistent `--workspace`
  override (validated dir, absolutized) > process cwd, for both CLI and serve. **No parent walk yet**
  (§6).
- [x] Thread the workspace into (a) the shell tool's `workDir` (`ExecutorConfig.WorkDir`) and (b)
  workspace prompt-file loading (`prompts.md` §2) with the §5 tier gate — **stage C**:
  `loadPrompts(workspace, tier)` loads the workspace tier over global, gated by `loadWorkspaceTier`,
  plus the `--context-file` escape hatch.
- [x] Update `design.md` / `tools.md` (reference) to describe workspace vs config-dir — **stage G**:
  `design.md` §1 "Two scopes — identity and target"; `tools.md` notes the catalog is config-dir-scoped.
- [x] **Consolidate** config-dir + workspace + tier + keys/env into `../environment.md` — **stage G**:
  the canonical two-scope reference.
- [ ] Sub-agent targets / worktree isolation (§4) — see [`subagents.md`](subagents.md).
