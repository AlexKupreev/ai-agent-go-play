# User file upload — attaching files across CLI, Telegram, and web

Letting the **user** hand the agent a file. Two shapes by frontend: on the CLI you **point to a
file** already on disk; on Telegram/web you **upload bytes** that must be stored somewhere the
executor can read. This is the concrete first slice of the `origin: user` registration in
[`chat-planner.md`](../adr/chat-planner.md) §D4 — but it stands on its own and should ship **decoupled**
from the (still-unbuilt) planner/manifest pipeline.

Companion to [`chat-planner.md`](../adr/chat-planner.md) (the artifact manifest this eventually feeds),
[`workspace.md`](../adr/workspace.md) (the scope an attachment belongs to; the *(deferred)* per-project scope
is [`../deferred/projects.md`](../deferred/projects.md)),
and [`resume.md`](resume.md) (attachments must survive a resumed session). **Status: requirements +
options, not decided.** Reviewable artifact before code.

---

## 0. Current state (what exists today)

Traced in code, so the gap is concrete, not assumed:

- **CLI chat** (`cmd/chat.go`): the executor runs in `workDir` — the resolved workspace root
  (`resolveWorkspace`, `cmd/workspace.go`). There is **no attach affordance**; a user
  can only *mention* a path inside message text, and the executor reaches it only if its shell can
  read that path.
- **Telegram** (`internal/frontend/telegram/`): `mapUpdate` (`transport_http.go:67`) maps **text
  only**. `Update` carries `Message{Text}` or `Callback` (`telegram.go:26–47`) — inbound
  `Document`/`Photo` updates are **silently dropped**. No file path reaches the engine.
- **Web / HTTP API** (`internal/api/http.go`): `POST /sessions/{id}/turns` (`http.go:47`) is
  **text-only** (`PostTurn(sessionID, text)`, `engine.go:245`). No multipart/upload route exists.
- **Storage**: the `record_artifact` tool + artifact manifest from [`chat-planner.md`](../adr/chat-planner.md)
  §D4 are **designed, not built**. There is **no per-session file area** today; the only file home is
  `workDir`. Sessions (`internal/session/session.go`) persist *message history only* — the doc
  comment is explicit that "nothing unserializable is persisted" — so there is no existing hook that
  owns files-per-session.

**Net:** every frontend needs a new inbound path, and there is no storage abstraction to land bytes
in yet. Both have to be built.

---

## 1. Requirements

What any accepted design must satisfy:

- **R1 — CLI: point, don't upload.** On the CLI the file is already on disk; attaching is naming a
  path, not transferring bytes. (Open: reference in place vs. copy into a store — §3 D2.)
- **R2 — Telegram/web: upload into scope storage.** The bytes arrive over the wire and must be
  persisted into the session's/project's attachment area before the turn runs, then referenced.
- **R3 — Executor can read the file.** Whatever the store, the path handed to the executor must be
  readable by its shell/tools under the active tier (sandbox-aware — §5).
- **R4 — The agent is *told*, not force-fed.** A turn references the attachment (path + type + size,
  maybe a one-line shape note) rather than dumping the file's bytes into context (chat-planner §D3:
  filesystem is working memory, references travel, not payloads).
- **R5 — Scope-correct.** An attachment belongs to a workspace scope and a session, and must
  land in the right one. *(Per-project scoping is [deferred](../deferred/projects.md); until it
  returns, the scope is simply the workspace.)*
- **R6 — Survives resume.** A resumed session ([`resume.md`](resume.md)) must still find its
  attachments — so storage is keyed by something stable (session id / scope), not process-local.
- **R7 — Cross-frontend consistent.** A file uploaded via Telegram and one pointed-to via CLI should
  present to the agent the same way (same reference shape), so the executor logic is frontend-blind.
- **R8 — Safe by construction.** No arbitrary-path writes; uploads confined to the attachment area;
  size/type limits on the wire; auditable if we treat writes as effectful (chat-planner §8).

Non-goals for a first cut (revisit later): OCR/parsing on ingest, thumbnailing, virus scanning,
dedup, and building the full manifest (§4 keeps this decoupled).

---

## 2. Proposed shape (sketch, not decided)

A thin **attachment store** keyed by scope + session, plus three frontend entry points that all
funnel into it, plus one turn-threading step that makes the executor aware:

```
CLI  /attach <path> ─┐
Telegram Document  ──┼─▶ attachment store ──▶ reference note injected into the turn ──▶ executor
HTTP multipart     ──┘   (bytes on disk,        ("user attached <path> (csv, 12 KB)")     (reads via
                          scope+session keyed)                                              shell/tools)
```

- **Store**: put bytes at a deterministic path; return a reference `{path, filename, size, mime,
  origin: user}`. Small enough to be a package (`internal/attach`?) the CLI loop and the engine both
  call; big enough to own path derivation, the size/type guard (R8), and cleanup.
- **Turn threading**: the reference is rendered into the turn text (or a structured turn field) so
  the executor sees *"a file is here"* without the bytes (R4). Frontend-blind (R7).
- **Later**: when the manifest lands (chat-planner §D4), the store's reference becomes a
  `record_artifact`/manifest entry with `origin: user`; until then it's just the note. **This doc's
  job is to not paint that corner badly.**

---

## 3. Decisions to make (options, with a lean)

### D1 — Storage location & scoping *(load-bearing)*

| Option | Layout | Trade |
|---|---|---|
| **A. Session-scoped** *(lean)* | `<scope>/.agent/attachments/<sessionID>/<file>` where `<scope>` = workspace root *(the active-project dir when the [deferred](../deferred/projects.md) project scope returns)* | Matches the cross-frontend session model + doc scoping; session isolation; easy cleanup + promotion; survives resume (R6). Reuses the `.agent/` directory convention. Slightly more path plumbing. |
| **B. Scope root directly** | `<workDir>/<file>` | Simplest; executor already sees `workDir`. But clutters the tree, no session isolation, awkward cleanup, name collisions. |
| **C. Shared per-scope dir** | `<scope>/.agent/attachments/<file>` | Simpler than A (no session key); but sessions in a scope see each other's files — leaks across conversations. |

**Lean: A.** It's the only one that satisfies R5+R6 cleanly and lines up with the manifest's
scope×session retention (chat-planner §D5). Cost is modest path plumbing. Open sub-question: CLI
local chat has no engine `sessionID` today (the session store is engine-side) — a local CLI session
needs a stable id to key on, or a `scratch/` fallback bucket.

### D2 — CLI: reference in place vs. copy into the store

- **Copy in** *(lean, for R7 uniformity)*: `/attach ./data.csv` copies into the attachment area, so
  CLI and Telegram/web produce identical references and the file is guaranteed inside the
  executor-readable scope (matters if the shell is sandboxed away from arbitrary cwd — §5).
- **Reference in place**: no copy; hand the executor the original absolute path. Zero duplication,
  but the path may be outside the readable scope, and it diverges from the upload frontends (R7).

**Lean: copy in**, with the original path recorded as `source` in the reference (cache-with-fallback
flavor — a lost copy can be re-read from source). Revisit if large-file duplication becomes a concern.

### D3 — How the agent learns of an attachment

- **Reference note in the turn** *(lean)*: prepend/inject *"The user attached `<path>` (`<mime>`,
  `<size>`)."* to the turn text. Frontend-blind, cheap, matches R4. The executor decides whether to
  read it.
- **Structured turn field**: extend the turn/`PostTurn` signature with an `attachments []Ref` field
  threaded to the executor seed. Cleaner typing, but touches the engine/API/session turn shape and
  every frontend — heavier. Could be the v2 of the note once the shape settles.

**Lean: note first**, migrate to a structured field if/when multiple attachments per turn or the
manifest make it worth the signature change.

### D4 — Relationship to the manifest / planner

- **Standalone now** *(lean)*: ship the store + note **without** the manifest; the reference lives
  only in the turn. Keeps this independent of the unbuilt planner (chat-planner is "designed, not
  built").
- **Write manifest entries now**: have the store also append an `origin: user` manifest entry. Buys
  forward-compat but pulls in a manifest format that isn't finalized.

**Lean: standalone**, but design the store's `Ref` struct to be a **superset** of the future manifest
entry (`{path, origin, source, mime/description, size, timestamp}`) so adoption is a serialization
change, not a redesign.

### D5 — Scope / sequencing of the build

- **Core + CLI first** *(lean)*: attachment store + turn threading + CLI `/attach`, with tests, as
  one reviewable slice; then Telegram, then HTTP/web. Smallest safe increment; proves the store shape
  before wiring two transports to it.
- **All three at once**: store + CLI + Telegram document download + HTTP multipart in one change.
  One bigger review, more surface at once.

**Lean: staged (core + CLI first).**

---

## 4. Per-frontend notes (implementation surface)

- **CLI** (`cmd/chat.go`): add a `/attach <path>` REPL command (sibling to `/new`, `/verbose`), or a
  `--file` startup flag. Resolve → store (D2) → stash the ref so the next turn's text carries the
  note (D3).
- **Telegram** (`internal/frontend/telegram/`): extend `Update`/`Message` to carry a `Document`
  (file id, name, mime, size), teach `mapUpdate` (`transport_http.go:67`) to populate it, and add a
  transport `Download(fileID) → bytes` using the Bot API `getFile` + file endpoint (the `tgbotapi`
  lib already supports this). `handleMessage` (`telegram.go:144`) then stores the bytes and posts the
  turn with the note. Guard on `b.allow` and a size cap (R8).
- **Web/API** (`internal/api/`): add `POST /sessions/{id}/turns` **multipart** support (or a separate
  `POST /sessions/{id}/attachments` then a normal turn). Engine gains an entry that stores bytes for
  a session and returns a ref; `handlePostTurn` (`http.go`) threads it. Enforce max body size.

**Shared seam:** all three produce the same `Ref`, and the engine/CLI both render the same note (R7),
so the executor never learns which frontend an attachment came from.

---

## 5. Risks / open threads

- **Sandbox readability (R3).** Confirm whether the executor's shell/tools can read
  `<scope>/.agent/attachments/...`. If the sandbox restricts cwd, D2's "copy in" must target a path
  inside the readable root — verify against `internal/sandbox` before committing to a layout.
- **Local CLI has no session id (D1 sub-question).** The engine owns session ids; local chat doesn't
  create an engine session. Need a stable local key (or a `scratch/` bucket) so CLI attachments are
  scoped and resumable.
- **Turn shape vs. note (D3).** The note is a stopgap; if we ever want the model to reliably
  distinguish "user attached" from prose, a structured field is more robust. Cheap to start with the
  note, but log the intent.
- **Cleanup / retention.** Ties into chat-planner §D5 (scope×provenance lifecycle). User-provided
  files are "kept until explicit deletion" there — so the reaper must **not** GC these. Until the
  reaper exists, unbounded growth is a (small) known debt.
- **Size/type limits + audit (R8).** Pick wire caps per frontend; decide whether attachment writes
  are audited like other effectful paths (chat-planner §8 raises the same question for artifact
  writes).
- **Multiple files / one turn.** The note approach handles one cleanly; N-per-turn nudges toward the
  structured field (D3) sooner.

---

## 6. Smallest first slice (if/when we proceed)

Per D5's lean: `internal/attach` store (path derivation + guard + `Ref`) → CLI `/attach` → turn note
→ tests. No Telegram, no HTTP, no manifest yet. That validates D1/D2/D3 on the cheapest surface
before either transport is wired.
