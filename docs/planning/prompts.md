# Prompt composition — design & roadmap

How the agent's system prompt is assembled, and how an operator customizes it. Adopts pi's
`SYSTEM.md` / `AGENTS.md` mechanism as a **two-tier model** — global (config-dir) + project
(workspace), project overriding global. The workspace tier depends on the concept defined in
[`workspace.md`](workspace.md); this doc owns the prompt-composition mechanics. Companion to
[`subagents.md`](subagents.md) (per-agent-type prompts share the same seam) and
[`../usage.md`](../usage.md) (config-dir). Roadmap, **not** current behavior — nothing here is built
yet.

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

Two file mechanisms, each resolved at **both** tiers — global `<config-dir>/` and project
`<workspace>/` (see [`workspace.md`](workspace.md) for how the workspace root is found):

| File | Effect | Maps to |
|---|---|---|
| `SYSTEM.md` | **replaces** the base prompt entirely (operator owns the whole prompt) | `replaceWith` |
| `AGENTS.md` (alias `CLAUDE.md`) | **appended** as operator/project instructions | `appends` |

**Precedence — project over global** (pi's rule):

- `SYSTEM.md`: a workspace `SYSTEM.md` wins outright over a config-dir one (replace ⇒ last writer).
- `AGENTS.md`: config-dir first, then workspace, **concatenated** (project has the last word). For the
  CLI, the workspace tier collects files walking **up** parent dirs (bounded per `workspace.md` §6).
- If both `AGENTS.md` and `CLAUDE.md` exist *in the same directory*, load one (prefer `AGENTS.md`) —
  don't silently concatenate two files that likely duplicate.

Full assembly order: `SYSTEM.md` override (or `executorPrompt`) → `selfDocsPromptNote` (if docs) →
config-dir `AGENTS.md` → workspace `AGENTS.md`. A `--no-context-files` flag disables **all** file
loading (parity with pi's `-nc`), useful for reproducible runs and debugging.

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

- `replace` (default for specialists like `researcher` / `scout`) — the body **is** the whole prompt
  (`composeSystemPrompt(base="", replaceWith=body)`); a clean, standalone prompt.
- `append` — the body is appended to the parent/base prompt (for a `general-purpose` type that
  inherits the executor's behavior): `composeSystemPrompt(base=parentPrompt, "", body)`.

Same helper, so the three features are one code path.

**Open sub-question:** do sub-agents inherit the config-dir `AGENTS.md`? Proposed default: `append`
types do (they're extensions of the main agent), `replace` specialists do **not** (they're meant to
be narrow and self-contained) unless the type opts in. Confirm when building.

---

## 4. The workspace tier

The workspace half of §2 rests on the [`workspace.md`](workspace.md) concept — how the workspace root
is resolved (CLI: cwd + parent walk; `serve`: process cwd in v1, a per-run field as the extension) and
how trust gates it. This doc consumes that concept; it doesn't redefine it. The only prompt-specific
note: the workspace is the **first consumer** of the concept, so shipping project prompt files and
shipping the workspace resolver are the same unit of work.

---

## 5. Tasks (when built)

- [ ] `internal/agent/agent.go` — `composeSystemPrompt` helper; call it in `NewExecutor` and the
  sub-agent factory; `ExecutorConfig` gains `SystemPromptOverride string` + `PromptAppends []string`.
- [ ] `cmd/` — read `SYSTEM.md` / `AGENTS.md` (alias `CLAUDE.md`) from the config-dir **and** the
  resolved workspace (`workspace.md`), apply the §2 precedence and the tier gate, pass contents into
  `ExecutorConfig`; add `--no-context-files`.
- [ ] `AgentType.PromptMode` (`replace` | `append`) wired through the sub-agent factory (§3).
- [ ] Tests: override replaces; append concatenates in order; missing files are a no-op; alias
  precedence; `--no-context-files` yields the bare base prompt; caching prefix unchanged when files
  absent.
- [ ] Docs: document the `SYSTEM.md`/`AGENTS.md` file names in reference docs once shipped — as part
  of the consolidated `docs/environment.md` (see `workspace.md` §7), not scattered into `usage.md`.
