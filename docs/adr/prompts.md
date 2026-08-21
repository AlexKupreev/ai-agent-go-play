# Prompt composition — design & roadmap

How the agent's system prompt is assembled, and how an operator customizes it. Adopts pi's
`SYSTEM.md` / `AGENTS.md` mechanism as a **two-tier model** — global (config-dir) + workspace, the
workspace overriding global. The workspace tier depends on the concept defined in
[`workspace.md`](workspace.md); this doc owns the prompt-composition mechanics. Companion to
[`subagents.md`](subagents.md) (per-agent-type prompts share the same seam) and
[`../usage.md`](../usage.md) (config-dir).

**§0–§2 are built** (stages A–C): the `composeSystemPrompt` seam, config-dir + workspace
`SYSTEM.md`/`AGENTS.md` (alias `CLAUDE.md`), the tier gate, and `--no-context-files` /
`--context-file`. **Their behavior is now canonical in
[`../environment.md`](../environment.md#prompt-customization-systemmd--agentsmd)** ("Prompt
customization"); §0–§2 below are retained as the design/mechanics record. §3–§4 (per-agent-type
prompts, sub-agent inheritance) remain roadmap.

---

## 0. One seam, assembled once

All prompt customization funnels through a single pure helper:

```go
// composeSystemPrompt returns the system prompt body. If replaceWith != "" it stands in for
// base entirely; otherwise base is used. appends are concatenated after, in order, each under a
// labelled separator. No I/O — callers (the cmd layer) read files and pass strings.
func composeSystemPrompt(base, replaceWith string, appends ...string) string
```

It is called **once at executor construction**, and its result becomes `a.systemPrompt`. The
per-turn `systemMessage()` still appends the date at request time (unchanged), so the stable prefix
stays stable and day-granularity **prompt caching is preserved**. This is a deliberate divergence
from pi, which injects context files *before every turn* (flexible, but cache-hostile). Trade-off:
edits to the files take effect on the next executor build (a new `run`, or a `chat` restart / a
future `/reload`), not mid-session.

**Layering:** `internal/agent` stays pure — it never reads the config-dir. The `cmd` layer resolves
paths, reads the files, and passes their contents into `ExecutorConfig` (new fields
`SystemPromptOverride string`, `PromptAppends []string`). The agent package only concatenates.

---

## 1. Base prompt

`executorPrompt` (+ `selfDocsPromptNote` when self-docs are wired), as today. Any tightening of the
base wording (conciseness / no-fabrication / prefer-reversible norms) is a separate, optional change
that does not depend on this design.

---

## 2. Two-tier customization (config-dir + workspace)

Two file mechanisms, each resolved at **both** tiers — global `<config-dir>/` and workspace
`<workspace>/` (see [`workspace.md`](workspace.md) for how the workspace root is found):

| File | Effect | Maps to |
|---|---|---|
| `SYSTEM.md` | **replaces** the base prompt entirely (operator owns the whole prompt) | `replaceWith` |
| `AGENTS.md` (alias `CLAUDE.md`) | **appended** as operator/workspace instructions | `appends` |

**Precedence — workspace over global** (pi's rule):

- `SYSTEM.md`: a workspace `SYSTEM.md` wins outright over a config-dir one (replace ⇒ last writer).
- `AGENTS.md`: config-dir first, then workspace, **concatenated** (workspace has the last word). For the
  CLI, the workspace tier collects files walking **up** parent dirs (bounded per `workspace.md` §6).
- If both `AGENTS.md` and `CLAUDE.md` exist *in the same directory*, load one (prefer `AGENTS.md`) —
  don't silently concatenate two files that likely duplicate.

Full assembly order: `SYSTEM.md` override (or `executorPrompt`) → `selfDocsPromptNote` (if docs) →
config-dir `AGENTS.md` → workspace `AGENTS.md`. A `--no-context-files` flag disables **all** file
loading (parity with pi's `-nc`), useful for reproducible runs and debugging.

> **Superseded in part, 2026-08-21 — "replaces entirely" is no longer true.** A `SYSTEM.md` owns
> the *wording*, not the containment rules. The built-in base is now four named blocks, and
> `kernelPromptBlocks` re-attaches two of them — the ~2 GB runtime constraints and the
> untrusted-content rule — after an override, the way the tier-policy note and tool roster were
> always re-attached. The original decision stands for everything else, and an override that
> restates a block is detected by marker so it is not duplicated. Rationale: an all-or-nothing
> replace silently deleted half the prompt-injection defence (`review-2026-08.md` §2.2), which was
> a surprising outcome for what reads like a cosmetic knob. Current behaviour:
> [`../environment.md`](../environment.md#what-a-systemmd-override-does-and-does-not-remove) and
> [`../security.md`](../security.md) §5/§7.

**Trust.** The config-dir tier is always trusted (the agent's own state — no new trust surface). The
**workspace tier is tier-gated** (`workspace.md` §5): `safe` does not auto-load workspace files, so an
untrusted checkout's `AGENTS.md` can't inject into a `safe` agent; `balanced`/`permissive` load them
(the pi-compatible interactive behavior); an explicit `--context-file`/`--workspace` is always honored.
The `cmd` layer applies the gate when it reads files, so `internal/agent` (§0) never sees an
untrusted file it wasn't handed.

---

## 3. Per-agent-type prompts (sub-agents)

`AgentType` (see [`subagents.md`](subagents.md) §2) carries the sub-agent's prompt as its file body,
plus a `PromptMode`:

- `replace` (default for specialists like `researcher`) — the body **is** the whole prompt
  (`composeSystemPrompt(base=body, "")`); a clean, standalone prompt. *(Amended 2026-08-21: the
  same kernel blocks as §2 are re-attached — the untrusted-content rule always, the runtime
  constraints only when the child's tool subset includes `shell`/`run_code`. A replace-mode
  `agents/*.md` is an operator file with the same hazard as a `SYSTEM.md`.)*
- `append` — the body is appended to the parent/base prompt (for a `general-purpose` type that
  inherits the executor's behavior): `composeSystemPrompt(base=parent.systemPrompt, "", body)`.

Same helper, so the three features are one code path.

**Resolved (Stage D):** do sub-agents inherit the config-dir/workspace `AGENTS.md`? **`append` types do,
`replace` specialists don't** — and this falls out of the composition seam for free: `parent.systemPrompt`
already has the operator/workspace `AGENTS.md` bodies folded in at `NewExecutor` construction (stage A/C),
so an `append` sub-agent that builds on `parent.systemPrompt` inherits them automatically, while a
`replace` sub-agent (body only) does not. No extra plumbing; the default matches the proposal. So the
built-in `general-purpose` (`append`) inherits the operator's instructions; `researcher` (`replace`)
stays narrow and self-contained.

---

## 4. The workspace tier

The workspace half of §2 rests on the [`workspace.md`](workspace.md) concept — how the workspace root
is resolved (CLI: cwd + parent walk; `serve`: process cwd in v1, a per-run field as the extension) and
how trust gates it. This doc consumes that concept; it doesn't redefine it. The only prompt-specific
note: the workspace is the **first consumer** of the concept, so shipping workspace prompt files and
shipping the workspace resolver are the same unit of work.

---

## 5. Tasks (when built)

- [x] `internal/agent/agent.go` — `composeSystemPrompt` helper; called in `NewExecutor`;
  `ExecutorConfig` gains `SystemPromptOverride string` + `PromptAppends []string`. *(Sub-agent factory
  reuse is stage D/E.)*
- [x] **config-dir tier (stage A):** `cmd/prompts.go` reads `SYSTEM.md` / `AGENTS.md` (alias `CLAUDE.md`)
  from the config-dir, passes contents into `ExecutorConfig`; `--no-context-files` added.
- [ ] **workspace tier (stage C):** extend the reader to the resolved workspace (`workspace.md`), apply
  the §2 project-over-global precedence and the tier gate.
- [ ] `AgentType.PromptMode` (`replace` | `append`) wired through the sub-agent factory (§3).
- [ ] Tests: override replaces; append concatenates in order; missing files are a no-op; alias
  precedence; `--no-context-files` yields the bare base prompt; caching prefix unchanged when files
  absent.
- [x] Docs: document the `SYSTEM.md`/`AGENTS.md` file names in reference docs once shipped — as part
  of the consolidated `docs/environment.md` (see `workspace.md` §7), not scattered into `usage.md`.
  **Done (stage G):** `environment.md` "Prompt customization" section documents the file names,
  replace-vs-append semantics, project-over-global precedence, the tier gate, and the
  `--context-file`/`--no-context-files` escape hatches.
