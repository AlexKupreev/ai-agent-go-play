# Full deletion & file management — the destructive half of the management plane

Wiring the **irreversible-delete** and **file-removal** operations the stores already support but
nothing exposes. Two coupled gaps: a session can be *archived* (`DELETE /sessions/{id}`) but never
*purged* or *restored* over any surface, and there is no way to remove a tracked file — the artifact
manifest is append-only. This is the destructive sibling of the (read-only) management plane built in
Phase 4e.

Companion to [`file-upload.md`](file-upload.md) (the ingest side — this doc is the *removal* side),
[`../adr/spaces.md`](../adr/spaces.md) §9 (space deletion, same archive-vs-purge question), and the
session model ([`plan.md`](plan.md) Phase 4f).

**Status: §2 (session purge + restore) is BUILT and shipped across all clients** — the engine
(`Engine.PurgeSession`/`RestoreSession`), HTTP (`DELETE /sessions/{id}/purge`,
`POST /sessions/{id}/restore`), the `api.Client`, the `agent session list|purge|restore` CLI, the
`/purge` command in `agent chat --addr` and Telegram, id validation, and the `session_purged`
audit event. `Store` now carries `Purge`/`Restore` (promoted from `FileStore`). §3 (file removal +
provenance-aware reaper) remains **requirements + options, not built** — it lands with the
[`file-upload.md`](file-upload.md) ingest slice (§4 sequencing).

---

## 0. Current state (traced in code)

- **Session store already has the primitives, unwired.** `session.FileStore`
  (`internal/session/session.go`) implements `Delete` (archives `<id>.json` →
  `archive/<id>.json`), `Restore` (archive → live), and `Purge` (irreversible `os.Remove` of the
  live *and* archived file). **Only `Delete` is reachable** — via `Engine.CloseSession` →
  `DELETE /sessions/{id}`. `Purge` and `Restore` are called by **nothing** outside their own tests;
  the doc comment on `Purge` says it is "for a future management plane's 'really delete'". That
  future is this doc.
- **Close is asymmetric.** `Engine.CloseSession` (`internal/api/engine.go:337`) *archives* the
  transcript (recoverable) but *hard-deletes* the scratch cache — the `onSessionClose` reaper in
  `serve.go:166` does `os.RemoveAll(sessionScratchDir(id))`. So a "recoverable close" already loses
  its artifacts irrecoverably. Harmless today (agent artifacts are re-derivable); a hazard once user
  uploads exist (see §3).
- **Files are append-only.** `artifact.Manifest` (`internal/artifact/manifest.go`) has
  `Append`/`List`/`Render` but **no `Remove`**. The bytes live in the session scratch dir; there is
  no verb to delete one file or drop its entry. `record_artifact` only adds. `Origin`
  (`agent`|`user`) is recorded but unused by any lifecycle.
- **No `Restore` path** means "archive" is, in practice, a slow delete: closed sessions accumulate in
  `sessions/archive/` forever with no supported way back (the doc says "move the file back by hand").

**Net:** the delete/restore *logic* exists at the store layer and is tested; the gap is the
engine/API/client/CLI wiring and one missing manifest verb.

---

## 1. Requirements

- **R1 — Irreversible session purge.** A supported way to remove a session's bytes for good (live or
  archived), across API + CLI, distinct from archive-close.
- **R2 — Restore closes the archive loop.** `Restore` becomes reachable, so archive is genuinely
  reversible (as [`../environment.md`](../environment.md) already claims) rather than a manual file move.
- **R3 — File removal.** A verb to delete a single tracked file (bytes + manifest entry), usable by
  the model and the human.
- **R4 — Provenance-aware reaping.** The close-reaper must keep `origin:user` files (they are "kept
  until explicit deletion", [`file-upload.md`](file-upload.md) §5) while still reaping `origin:agent`
  scratch. Today it reaps everything.
- **R5 — Irreversible actions confirm.** Purge and file-delete route through the existing `Approver`
  gate / a CLI confirm — they are destructive and unrecoverable (design §5 discipline).
- **R6 — Audited.** Purge and file-removal emit audit events (new `session_purged` /
  `file_removed`), so the single review surface (`GET /audit`) covers destructive management too.
- **R7 — Safe ids/paths.** Purge validates the session id shape; file-remove reuses the manifest's
  existing `withinDir` containment (`record_artifact.go`) so a remove can't escape the scratch tree.

---

## 2. Session purge & restore (R1, R2)

The store methods exist; add the four wiring layers, mirroring how `CloseSession` is already wired.

| Layer | Add | Notes |
|---|---|---|
| **Engine** | `PurgeSession(id)` → `sessions.Purge(id)` + reap scratch; `RestoreSession(id)` → `sessions.Restore(id)` | `PurgeSession` is `CloseSession`'s hard-delete sibling; it *should* reap scratch (unlike archive, there's no recovery to preserve it for). |
| **API** | `DELETE /sessions/{id}?purge=true` **or** `DELETE /sessions/{id}/purge`; `POST /sessions/{id}/restore` | Prefer a distinct `/purge` sub-path over a query flag — a destructive verb shouldn't hinge on a droppable query param. Validate `{id}` against the id regex (hex) before hitting disk (R7). |
| **Client** | `Client.PurgeSession` / `Client.RestoreSession` | Peer methods next to `CloseSession`. |
| **CLI** | `agent session purge <id>` (confirm gate, R5) and `agent session restore <id>`; optional `--purge` on the REPL `/end` | A top-level `session` command group is new; it also naturally hosts `list`/`archive`. Confirm is mandatory for purge. |

**Decision — keep archive as the default close.** `DELETE /sessions/{id}` stays *archive* (Phase 4e
behavior, recoverable); purge is the explicit escalation. This matches sessions' existing
"mistaken `/end` is recoverable" promise and the spaces ADR's archive-on-delete lean (§9).

---

## 3. File removal & provenance-aware reaping (R3, R4)

- **`Manifest.Remove(path)`** — drop the entry and delete the bytes (atomic rewrite, same pattern as
  `Append`; `withinDir` guard reused). This is the primitive the manifest is missing.
- **`remove_file(path)` built-in** — trusted, not sandbox-exposed (like `record_artifact`), reusing
  the scratch-dir containment. Human surface: `agent session files rm <path>` or a REPL `/rm`.
- **Provenance-aware reaper (R4)** — change the `onSessionClose` hook from
  `os.RemoveAll(scratchDir)` to: read the manifest, delete only `origin:agent` files (+ the manifest
  itself if empty), leave `origin:user` files in place. Until uploads exist there are no `user`
  entries, so this is a no-op change now that becomes correct-by-construction when they land. **This
  is the one change worth making even before uploads ship**, so the reaper is never retrofitted under
  a live upload feature.

---

## 4. Sequencing (recommended)

1. **Session purge + restore (§2)** — ✅ **DONE.** Smallest, self-contained, exercised already-tested
   store code; closed the archive/restore symmetry gap. Shipped on every session client (CLI REPL,
   remote REPL, Telegram, HTTP) plus the `agent session` management command.
2. **Provenance-aware reaper (§3, reaper only)** — tiny, no new surface, removes a latent hazard
   before uploads exist.
3. **File removal verb (§3, rest)** — pairs with, and is easiest to land *alongside*, the
   [`file-upload.md`](file-upload.md) ingest slice, since both touch the same manifest. Defer until
   uploads are being built so the two land as one coherent file-management story.

`remove_file` and uploads share the manifest, so building removal in isolation risks a manifest shape
that the ingest side then wants to change — hence removal waits for §1 of file-upload, while session
purge/restore (which touches no shared shape) goes now.

---

## 5. Open questions

- **Purge granularity** — purge one session vs. a `purge --archived --older-than 30d` sweep for the
  accumulating archive. Start with single-id; add the sweep when the archive actually grows.
- **Space deletion** ([`../adr/spaces.md`](../adr/spaces.md) §9) shares this exact archive-vs-purge
  shape — when spaces land, `purge`/`restore` should generalize over both sessions and spaces rather
  than duplicate. Design the CLI verb group with that in mind (`agent purge session|space <id>`).
- **Cascade** — purging a session: also purge its per-run transcripts under `runs/<id>/`? They are a
  distinct store (`--sessions-dir`) keyed by run, not session. Lean: no cascade in v1 (transcripts are
  the audit trail); revisit if privacy-delete ("erase everything about this conversation") is a real
  requirement.
- **Audit of a purge is itself a record** — purging for privacy but leaving a `session_purged` event
  (with the id) in an append-only log is a mild tension. Fine on a single trusted box; note it if the
  box ever holds data that must be truly erasable.
