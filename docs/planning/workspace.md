# Workspace — concept & roadmap

Defines **workspace**: the project/directory the agent is currently *acting on*, as distinct from the
**config-dir**, which is *who the agent is*. This is the two-tier model that pi and Claude Code use
(global config dir + per-project layer, project overriding global); adopting it is what makes this
engine pi-compatible. Companion to [`prompts.md`](prompts.md) (the first consumer) and
[`subagents.md`](subagents.md) (scouts / worktree isolation). **Built** (stages B–C): a first-class,
overridable workspace (`resolveWorkspace`/`--workspace`) that anchors the shell `workDir` and the
tier-gated project prompt tier (§5). Sub-agent targets / worktree isolation (§4) remain roadmap.

---

## 0. Two anchors, two jobs

| | **config-dir** | **workspace** |
|---|---|---|
| Answers | *who* the agent is (identity) | *what* it is acting on (the project) |
| Holds | config, tool catalog, memory, audit, **global** prompt files | shell cwd, **project** prompt files, scout target |
| Scope | one per agent (`--config-dir`, default `~/.config/ai-agent`) | one per run/session; changes as the agent moves between projects |
| Trust | always trusted (the agent's own state) | trusted only above `safe` (§5) — a checkout can be hostile |
| Today | defined & shipped (`usage.md`) | **not a concept yet** — only `workDir` (§1) |

The load-bearing boundary: a workspace anchors **context and targets**, never **identity**. Memory,
tools, and audit stay config-dir-scoped in v1 — a workspace does *not* get its own memory or catalog
(that's the config-dir's job, and per-project separation is already achieved by pointing at a
different config-dir). See §4.

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

## 3. Precedence — project over global

Wherever both tiers supply something (prompt files today; agent types later), **workspace (project)
overrides or augments config-dir (global)** — the same rule pi uses:

- append-style inputs (`AGENTS.md`): global then project, concatenated (project has the last word).
- replace-style inputs (`SYSTEM.md`, an agent type of the same name): project wins outright.

Mechanics live in [`prompts.md`](prompts.md) §2–§3; this doc owns only the concept.

---

## 4. What a workspace anchors (and what it does not)

**Does:**
- **Project prompt context** — the project tier of `AGENTS.md` / `CLAUDE.md` / `SYSTEM.md`
  (`prompts.md`). First and primary consumer.
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

- **`serve` multi-project** — the shape of a per-run `workspace` field, and how trust attaches to a
  workspace that arrives over the wire (who vouches for it?).
- **Parent-walk bound** — stop the upward walk at a git root? a filesystem root? a `.agent`/marker
  file? (pi walks to the home/root; bounding at a VCS root is safer.)
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
