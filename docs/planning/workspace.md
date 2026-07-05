# Workspace — concept & roadmap

Defines **workspace**: the directory sandbox the agent *lives and works in* — the container holding
the guidelines common to everything under it — as distinct from the **config-dir** (*who the agent
is*) above it and a **project** (a *named sub-scope within* the workspace, [`projects.md`](projects.md))
below it. The three form a **three-scope model** — **config-dir → workspace → project** — each
inheriting the one above and able to override it, the layered-config pattern pi and Claude Code use.
Canonical statement of the three scopes: [`../environment.md`](../environment.md). Companion to
[`prompts.md`](prompts.md) (the prompt-tier consumer) and [`subagents.md`](subagents.md) (scouts /
worktree isolation).

**Implementation status.** The workspace anchor (`resolveWorkspace`/`--workspace`) for the shell
`workDir` and the tier-gated prompt tier (§5), and — via [`projects.md`](projects.md) — named projects
switchable mid-session, are built (stages B–C). The **workspace → project guideline inheritance** (§3)
is specified here but not yet wired: a project's prompt tier currently composes with the config-dir
only, and aligning it is a parent-walk to the workspace root. Sub-agent targets / worktree isolation
(§4) also remain to build.

---

## 0. Three scopes — identity, sandbox, sub-scope

Three nested scopes, each inheriting the one above and able to override it. This doc owns the middle
one (**workspace**); the config-dir is *who the agent is*, and a **project**
([`projects.md`](projects.md)) is a named sub-scope *within* a workspace.

| | **config-dir** | **workspace** | **project** |
|---|---|---|---|
| Answers | *who* the agent is (identity) | *what* it acts on — the sandbox it lives in | a *named corner* of the workspace |
| Holds | config, tool catalog, memory, audit, **global** prompt files | shell cwd, **workspace-common** prompt files, scratch home, scout target | **project-specific** prompt overrides, its own artifacts/sessions |
| Scope | one per agent (`--config-dir`, default `~/.config/ai-agent`) | one per run/session — the fixed sandbox root | switchable within the workspace; the active working dir re-anchors to it |
| Trust | always trusted (the agent's own state) | trusted only above `safe` (§5) — a checkout can be hostile | inherits the workspace's trust (+ the §5 gate re-fires on switch) |
| Guideline layer | 1 — always | 2 — inherits config-dir | 3 — inherits workspace, wins |

The load-bearing boundary: a workspace (and a project) anchors **context and targets**, never
**identity**. Memory, tools, and audit stay config-dir-scoped — a workspace does *not* get its own
memory or catalog (that's the config-dir's job, and per-project separation is already achieved by
pointing at a different config-dir). See §4.

**Container vs. active dir.** The workspace is the *fixed* sandbox root for a session; switching into a
project moves the *active working directory* to a dir nested under it and layers that project's
guidelines on top — the workspace itself doesn't change. (The persistent middle workspace tier is the
one piece still to wire — §3; the model here is the target both docs describe.)

---

## 1. `workDir` today (current behavior, to clarify)

There is no workspace concept in the code yet. The only related thing is `workDir` — **the shell
tool's working directory** — and it is set, not discovered:

- `run` / `chat`: `workDir = os.Getwd()` (the CLI's cwd at launch).
- `serve`: `workDir` is fixed at process start (`serveDeps.workDir`), the same for every run the
  engine handles.

No parent-directory walking, no project root, no "current project" — `workDir` is just *where shell
commands execute*. The workspace concept **generalizes and names** this: `workDir` becomes the shell
face of a first-class workspace, and the workspace additionally anchors prompt context (§4).

---

## 2. Resolving the workspace, per frontend

- **CLI (`run` / `chat`)** — workspace = the launch **cwd**, walking **up parent directories** to
  collect project files (the pi/Claude-Code ergonomic: `cd` into a repo and it's picked up). This is
  a faithful pi port.
- **`serve`** — **v1: the process cwd is the single workspace** for the whole engine (simple,
  coherent, matches "cd here, then serve"). *Extension, designed-for not built:* a per-run/per-session
  `workspace` field on the run request, so one long-lived engine can serve multiple projects. The
  resolver is written to accept an explicit workspace so this is an additive change, not a reshape.

The config-dir resolves identically regardless of frontend (global tier, always).

---

## 3. Precedence — inner scope over outer

Wherever more than one scope supplies something (prompt files today; agent types later), the **inner
scope overrides or augments the outer** — **config-dir (global) → workspace (common) → project
(specific)**, the layered-config rule pi uses:

- append-style inputs (`AGENTS.md`): outer-to-inner, concatenated (the project has the last word).
- replace-style inputs (`SYSTEM.md`, an agent type of the same name): the innermost present wins.

**Implementation to align.** The precedence above is three-tier by design. The current code composes
only two — config-dir + the single active directory (`loadPrompts(workspace, tier)`, `cmd/prompts.go`)
— so a project's active dir does not yet inherit an enclosing workspace root's common guidelines.
Closing that gap is a walk from the active dir up to the workspace root (§6's parent-walk bound).
Mechanics live in [`prompts.md`](prompts.md) §2–§3; this doc owns only the concept.

---

## 4. What a workspace anchors (and what it does not)

**Does:**

- **Workspace prompt context** — the workspace-common tier of `AGENTS.md` / `CLAUDE.md` / `SYSTEM.md`
  (`prompts.md`), which projects inherit and add their own tier on top of. First and primary consumer.
- **Sub-agent targets** — a `scout` / repo-reader operates "on *this* workspace" (`subagents.md` §2).
- **Worktree isolation** (later) — `AgentType.Isolation: "worktree"` needs a workspace/git root as the
  thing to snapshot (`subagents.md`).

**Does not (v1):** scope memory, tools, or audit — those remain config-dir-scoped (agent identity).
Keeping this boundary is what stops "workspace" from ballooning into a second config-dir. If a genuine
per-project memory/tool need appears, it's a deliberate future decision (§6), not a default.

---

## 5. Trust — the reason this isn't just "load some files"

The config-dir is as trusted as the stored API key; its files always load. A **workspace can be an
untrusted checkout**, so a project `AGENTS.md` is a prompt-injection vector (worse than fenced web
content — it lands *in the system prompt*). pi accepts this (it trusts the cwd); we shouldn't
unconditionally, given deny-by-default.

Rule: **workspace file auto-loading is tier-gated.**

- `safe` — do **not** auto-load workspace prompt files (config-dir globals still load).
- `balanced` / `permissive` — auto-load workspace files (pi-compatible interactive behavior).
- An explicit `--context-file <path>` / `--workspace <dir>` is always honored regardless of tier (the
  user named it, so it's authorized).

This keeps the pi ergonomic for interactive trusted use while never letting an arbitrary repo inject
into a `safe`-tier agent.

---

## 6. Open questions

- **`serve` multi-project / mid-session switch / who-vouches** — **resolved in
  [`projects.md`](projects.md):** named projects live under `<home>/projects/<slug>-<uid>/` (the
  filesystem *is* the registry), a per-session active project is switchable via a tier-gated
  `switch_project`, and trust is supplied by **containment** (the project sits under an
  already-authorized home workspace) plus the §5 gate on switch. See that doc's §5/§7.
- **Parent-walk bound** — stop the upward walk at a git root? a filesystem root? a `.agent`/marker
  file? (pi walks to the home/root; bounding at a VCS root is safer.) *(Still open — and now
  load-bearing for the §3 middle tier: the walk from a project's active dir up to its workspace root is
  exactly what layers the workspace-common guidelines under the project's, so its bound defines where
  "the workspace" ends.)*
- **Ever scope memory/tools?** — v1 says no (§4). Revisit only if one running agent must keep
  genuinely separate per-project memory that config-dir separation can't serve.

---

## 7. Tasks (when built)

- [x] `cmd/workspace.go` — `resolveWorkspace()` returning the workspace root: persistent `--workspace`
  override (validated dir, absolutized) > process cwd, for both CLI and serve. **No parent walk yet**
  (the walk collects project *files* — stage C — and its bound is open, §6); `--context-file` lands
  with stage C, where it gates prompt loading.
- [x] Thread the workspace into (a) the shell tool's `workDir` (`ExecutorConfig.WorkDir`, wired in
  `run`/`chat`/`serve`) and (b) project prompt-file loading (`prompts.md` §2) with the §5 tier gate —
  **stage C, done**: `loadPrompts(workspace, tier)` in `cmd/prompts.go` loads the workspace tier
  project-over-global, gated by `loadWorkspaceTier` (`safe` skips auto-load unless `--workspace` is
  explicit; config-dir isn't double-loaded), plus the `--context-file` escape hatch.
- [x] Update `design.md` / `tools.md` (reference) to describe workspace vs config-dir once shipped.
  **Done (stage G):** `design.md` §1 gains a "Two anchors — identity vs target" paragraph; `tools.md`
  notes the catalog is config-dir-scoped (vs sub-agent types read from both). Both link to
  `environment.md`.
- [x] **Consolidate** config-dir + workspace + tier + keys/env into a new reference doc
  `docs/environment.md` (the single "runtime environment" home): who the agent is (config-dir), what it
  acts on (workspace), how trusted (tier), how configured (env). **Done (stage G):** `environment.md`
  written (also covers prompt files + agent types); `usage.md`'s config/env + files-on-disk sections
  shrink to a pointer, and it gains operational sections for prompt/agent-type customization + `eval`.
