# Spaces — switchable data contexts (design & roadmap)

How the agent holds **several ongoing contexts** — "my English lessons", "the tax stuff", "project
X" — each accumulating its own memory and artifact references, switchable within or across
conversations. This is the **data-only** successor to the reverted, cwd-based Projects
([`../deferred/projects.md`](../deferred/projects.md)): a space is a *scope for the agent's own
data*, **not** a working directory. That single simplification removes the trust/containment
questions that sank Projects.

Companion to [`../design.md`](../design.md) (§1 single trusted box), [`memory.md`](../memory.md), and
the session model ([`../planning/plan.md`](../planning/plan.md) Phase 4f). **Status (2026-07-11):
P1 + P2 + P3-guidance are BUILT** (`internal/space`, `memory.ScopedStore`, the five space tools,
`Session.Space` sticky + POST/PATCH, `/space` in local/remote chat + Telegram, `--space`, guidance
injected into the system prompt, and the workspace-local move of memory to
`<workspace>/.agent/`). Per-space **artifact manifests** (the rest of P3) and P4 remain
roadmap. Reference docs: `usage.md` §Spaces, `environment.md` files table, `memory.md`.

---

## Governing decision (2026-07-08) — workspace-local, for simplicity

**This supersedes the config-dir / global-scope / committable-file discussion below** (§2's
"Config-dir, not workspace", §3's config-dir paths, §4's global layer). Those were explored and
rejected *for now* as too complex. The v1 decision:

- **Memory, guidance, and spaces live in the workspace**, under `<workspace>/.agent/` — `memory.json`,
  `guidance.md`, and `spaces/<id>/`. There is **no config-dir memory, no global/identity layer, and no separate
  "committable" mechanism.**
- **Per-workspace by construction.** A workspace has its own memory + its own spaces; switching
  workspace switches the whole data set. This is what "per-workspace-tuned agents shouldn't share all
  memory" asked for, achieved by *where the files live* rather than by a scope system.
- **Committing is free.** The data is files in the workspace, so `git add` shares/versions them and
  `.gitignore` keeps them private — no feature required.
- **Deferred (complicate later, on real need):** a config-dir global/identity layer for cross-workspace
  facts; a distinct private-vs-committable split; SQLite. The `memory.Store` interface remains the
  seam for all of them.

Two operational notes: (1) for `serve`/Telegram, point `--workspace` at a **persistent** dir so
memory survives restarts (it no longer rides the config-dir volume); (2) memory is now keyed by
workspace, so two `--config-dir` agents in the *same* workspace share it.

The sections below record the fuller design space that was considered; read them as background, with
the box above as the actual v1 choice.

---

## 0. The model in one paragraph

A **space** is a named data scope: short **guidance** (always loaded when the space is
active), its own **memory** entries, and its own **artifact references**. Exactly one space is
**active per session** (sticky, reusing the per-session model/tier machinery from
[`../api-transport.md`](../api-transport.md)); a session inherits its space and can switch **explicitly**
mid-conversation with `/space <name>` (or the `switch_space` tool when asked in natural language).
Memory and artifacts written while a space is active belong to it; a top-level **global** scope
holds everything unscoped and is visible in every space. A space is **not** a workspace/cwd — the
shell's directory is unchanged.

---

## 1. Motivation

The single shared memory store answers "what do I know?" but not "what do I know *about this*?" For
recurring, stateful relationships — tutoring (recall my level + progress), a standing assistant with
several parallel threads — the user wants the agent to **resume the right context** without dragging
in unrelated facts, and to keep each thread's artifacts together. Two needs:

- **Explicit reference** ("look at the results from the tax space") — served once memory/artifacts
  are scoped and listable.
- **Implicit resume** ("start my next Polish lesson") — served by the space's always-loaded guidance
  (the per-space "profile"): push context, not a pull the model must remember to do.

A single global profile (one context) would be simpler, but the user has **multiple** parallel
contexts, so spaces are the right shape. Auto-detecting which space a message is about is deferred
(§9) — v1 switches explicitly.

---

## 2. What a space is NOT (the simplification vs Projects)

The reverted Projects tied a project to a **filesystem workspace** (`<home>/projects/<slug>/`, cwd
switching, containment-based trust, "who vouches for this checkout"). A space carries **none** of
that:

- **No cwd change.** The shell's working directory is set by `--workspace` as today, independent of
  the active space. (A per-space cwd can attach later as a field if a real need appears — but it is
  out of v1, deliberately.)
- **No filesystem-containment trust model.** A space is the agent's own data under the config dir;
  it introduces no new trust surface. The tier gate is unchanged.

So a space is purely: *which memory namespace + which artifact set + which guidance is active.* This is
the whole feature, and why it's a fraction of Projects' cost.

### Config-dir (agent-global), not workspace-scoped

Spaces live under the **config dir** and are shared across **all** workspaces of that agent — because
**space and workspace are orthogonal axes**: workspace = *which files* the agent acts on (shell cwd,
workspace-tier prompts); space = *which memory/artifact/guidance bucket* is active. The motivating cases
("English lessons", "the tax stuff") have **no filesystem workspace at all** — the family-box
deployment (design §1) runs one `serve` / one config-dir where cwd is barely relevant — so tying a
space to a workspace would be wrong for exactly the case spaces exist for.

This is also consistent with the two-anchor model: a space is a **partition of the agent's memory**,
and memory lives in the config dir (the agent's identity), so spaces do too. Per-`--config-dir`
share-nothing then falls out for free — two agents get separate spaces. Being global across
workspaces leaks nothing: switching is explicit and only the **active** space's memory is loaded
(+ global), so you are never in two at once and inactive spaces are never in context. Being in
workspace `~/repos/foo` with space `english` active is allowed and harmless — independent dials.

**Not covered (deliberately):** a space whose *data physically lives inside a repo* (travels with the
checkout, git-ignored, per-repo) is the reverted workspace-sub-scope direction — revisit only on a
concrete need. Auto-switching a space when you `cd` into a repo is the deferred auto-switch (§9).

---

## 3. Storage — and the "memory.json will get huge" question

**The concern is real.** Today `internal/memory.MemoryStore` holds every entry in RAM and, on each
`Put` (`remember`), **rewrites the whole `memory.json`** (marshal-all + atomic temp-rename). So write
cost is O(n) in total entries and the total work to accumulate n facts is quadratic; the whole store
is also resident in RAM. Fine for a family box with hundreds of memory entries; a long-lived agent that
accumulates thousands would feel it.

Spaces give a natural fix — **shard by space** — so we adopt it rather than piling every space's
entries into the one growing file:

```
<config-dir>/
  memory.json                     # GLOBAL scope (unscoped/default; the existing file — no migration)
  spaces/
    <space-id>/
      space.json                  # metadata: name, guidance (the per-space profile), timestamps
      memory.json                 # THIS space's memory entries
      artifacts.json              # THIS space's artifact manifest (reuses internal/artifact)
```

Consequences:

- A `remember` in a space rewrites only **that space's** `memory.json` — bounded to one space's
  size, not the whole corpus. Global writes touch only the global file.
- Loading a space loads only its file (+ global), so RAM tracks the active space, not everything.
- Switching is cheap (open a different small store).
- **The filesystem is the space registry** — `spaces/` is a directory of spaces (mirrors the session
  store's dir-of-files); `list_spaces` reads it, no separate index to keep in sync.

**When even this bites, migrate to SQLite** — the codebase's stated end goal (design §9;
`tools.md` / `memory.md` already flag the SQLite tipping point). Memory + spaces is a strong trigger:
per-entry upsert instead of file-rewrite, indexed/space-scoped queries, and FTS for search — with a
space becoming a *column*, not a directory. Crucially this is a **swap behind the existing
`memory.Store` interface** (Get/Put/Search/List/Delete), so callers don't change. We build sharded
JSON now (single-binary, consistent with the other stores) and keep the interface the seam.

*(Decision made on the "guess if optimal" question: **sharded-JSON now**, SQLite when search or a
single space's size bites. Not one growing file, and not SQLite pre-emptively.)*

**Alternatives weighed (2026-07-08), sharded-JSON chosen:**

- **`modernc.org/sqlite`** (pure-Go, so it survives the `CGO_ENABLED=0` static build — the cgo
  `mattn/go-sqlite3` does not) — SQL + FTS + a `space_id` column, and the stated end-goal of one
  transactional store. Rejected *for now* only for its weight (notably larger binary + slower builds);
  it stays the migration target.
- **`bbolt`** (pure-Go B+tree KV, small) — per-key writes with buckets mapping 1:1 to spaces; the
  neatest technical fit, but KV-only (no relational/audit consolidation) and still a new dep.
- **Sharded JSON (chosen)** — no new dependency, keeps the lean static binary, and bounds each
  `remember` rewrite to one space's file. Its residual cost (rewrite a space's whole file per `Put`,
  active space resident in RAM) is acceptable at family scale, and the `memory.Store` interface makes
  the eventual swap to SQLite/bbolt a one-package change — so nothing here forecloses it.

---

## 4. Memory scoping

- **`remember`** writes to the **active space** (its `spaces/<id>/memory.json`); with no active space,
  to **global** (`memory.json`) — today's behavior, unchanged.
- **`recall` / `search`** return **active-space ∪ global**, so a space sees its own entries plus shared
  facts, but not another space's. Global-only when no space is active.
- **Existing entries need no migration** — they live in the global `memory.json` and stay globally
  visible (decision #2). A user/agent can promote a global fact into a space by re-saving it there.
- Implementation: a small `ScopedStore` composing the active-space store over the global store
  (`Put` → active; `Get/Search/List` → merge, active shadowing global on key collisions). The
  `remember`/`recall` tools keep taking a `memory.Store`; the cmd/engine layer hands them the scoped
  view for the active space. No tool change.

---

## 5. Active space = per session, switched explicitly

**Per-session, sticky** — reusing the machinery shipped for per-session model/tier
([`../api-transport.md`](../api-transport.md)): `session.Session` gains `SpaceID`; settable at
`POST /sessions` and `PATCH /sessions/{id}`, merged per turn. Several sessions can share a space (the
data is the space's, not the session's).

**Validated where it is set** (2026-08-21): unlike the tier, which is stored as a request and clamped
per turn, a sticky space is resolved against the store by `POST`/`PATCH` and stored as its canonical
id — an unknown one is a 400 naming the available spaces, not a broken next turn. The engine core
keeps knowing nothing of the spaces directory: `cmd/serve.go` injects a resolver (`SetSpaceResolver`)
the way it injects the run store and the session-close hook.

**Switching mid-session is explicit** (no intent-guessing in v1):

- **`/space <name>`** REPL command — re-points the active space **from that message forward**.
  `/space` alone shows the current one; `/space list` lists them. Also `agent chat --space <name>` to
  start in one.
- **`switch_space(id)` / `create_space(name)` / `list_spaces`** model-facing tools — so *"let's switch
  to the Polish project"* in natural language works, and so a space can be created on the fly.
- Telegram: a `/space` command mirrors the REPL (a chat maps to a session, which carries the space).

**Mid-session semantics:** facts/artifacts saved before a switch keep the old space; after, the new
one — which is correct, they *were* about the old space. The conversation transcript itself is not
re-scoped (a session can span spaces); only new memory/artifact writes follow the active space.

---

## 6. Tool surface

All trusted, not sandbox-exposed (like `remember`/`status`). Wired when a space store is present
(nil ⇒ omitted, like every optional dep).

| Tool | Does |
|---|---|
| `list_spaces` | names, ids, guidance preview, last-active; marks the current one |
| `create_space(name)` | make a new space, return its id |
| `switch_space(id)` | set the session's active space (effective from the next write/turn) |
| `space_guidance` / `update_space_guidance(text)` | read / replace the active space's guidance (the per-space profile; size-capped so it cannot bloat the always-on prompt) |

`remember` / `recall` are unchanged in signature — they operate on the scoped view (§4). The active
space's **guidance** is injected into the system prompt at session start (like `AGENTS.md`, but
agent-writable and per-space — this is the "profile" the earlier discussion converged on, resolved
here as a space field rather than a standalone file).

---

## 7. Staging

- [x] **P1 — spaces exist + switch.** *(DONE 2026-07-11)* `internal/space` store (dir-of-dirs,
  `space.json` metadata) + `list_spaces`/`create_space`/`switch_space` + `Session.Space` sticky
  (+ `POST`/`PATCH`) + `/space` command (local, remote, Telegram) + `agent chat --space`.
- [x] **P2 — memory scoping.** *(DONE 2026-07-11)* The `ScopedStore` (active ∪ global);
  `remember` → active, `recall` → merged; per-space `spaces/<id>/memory.json`. In `serve`, one
  shard instance per space is shared process-wide so concurrent sessions serialize writes.
  Mid-turn `switch_space` persists through the session store; the engine re-reads the session
  before saving the turn history so the switch isn't clobbered.
- [~] **P3 — artifacts + guidance.** Guidance **DONE 2026-07-11**, renamed 2026-08-21: the
  guidance blob (capped at `space.MaxGuidanceChars`) + `space_guidance`/`update_space_guidance` +
  injection into the system prompt
  (as a labelled append, so prompt composition is unchanged). Per-space `artifacts.json`
  manifest **deferred** — the artifact cache is currently session-scoped (chat-planner §D5);
  attach it per space when a real cross-session artifact need appears.
- [ ] **P4 (deferred) — SQLite backend** behind `memory.Store` when scale/search bites (§3); and any
  per-space cwd, if ever wanted.

---

## 8. Decisions (settled)

- **Data scope, not a workspace** — no cwd change (§2).
- **Active space per session, sticky** — reuses the model/tier session-sticky machinery (§5).
- **Explicit switch only** in v1 (`/space`, `switch_space`); no auto-detection (§9).
- **Global default** — unscoped/existing memory stays visible everywhere; no migration (§4).
- **Sharded JSON now, SQLite when it bites** — behind the `memory.Store` interface (§3); chosen over
  `modernc.org/sqlite` and `bbolt` this round to avoid a new dependency and keep the `CGO_ENABLED=0`
  static binary lean (§3 alternatives).
- **Workspace-local (v1)** — memory, guidance, and spaces live under `<workspace>/.agent/`, per-workspace by
  construction; committing/gitignoring is free. Config-dir global layer deferred. (See the governing
  decision at the top; it supersedes the earlier "config-dir, agent-global" exploration in §2.)
- **Name: "space"** (distinct from the reverted "projects" and from Go's `context`).

## 9. Open questions

- **Auto-switch by intent** — detecting the space from what the user is discussing (implicit switch).
  Real value for the tutoring case, but it's the hard/risky part (wrong guess scopes memory wrongly).
  Deferred; explicit `/space` first, measure whether auto is even wanted.
- **Deleting/merging spaces** — archive-on-delete (like sessions) vs purge; merging two spaces'
  memory. Add when a real need appears.
- **Cross-space search** — a `recall --all-spaces` escape hatch to search everything. Probably wanted
  eventually; trivial once scoping exists.
- **Guidance size discipline** — always-loaded guidance must stay short; do we hard-cap, or let the
  agent self-summarize (the `/compact` skill) when it grows? Lean: hard char cap + a nudge.
- **Per-space cwd** — if a space ever *should* pin a directory, it attaches as a `space.json` field;
  explicitly out of v1.

## 10. Non-goals

- **Multi-tenant isolation** — spaces are one user's organizational buckets on a trusted box, not a
  security boundary (design §1). A user with shell access reaches every space's files regardless.
- **Workspace/cwd switching** — that was Projects; deliberately dropped (§2).
- **Automatic context detection** — v1 is explicit (§9).
