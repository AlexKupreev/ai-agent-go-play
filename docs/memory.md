# Long-term memory — architecture, solutions, and trade-offs

How the agent's cross-run memory is modelled, stored, and surfaced, with the **benefit / drawback**
of each design choice. Implements [`plan.md`](planning/plan.md) Phase 4d. Complements
[`tools.md`](tools.md) (the tool system it plugs into) and [`security.md`](security.md) (the trust
tier it lives in).

**Status:** Phase 4d is implemented. `internal/memory` (`Store` + `MemoryStore`), the
`remember`/`recall` built-ins (`internal/tools/memory.go`), wiring in
`internal/agent/agent.go` (`NewExecutor`), and persistent stores in `cmd/run.go` + `cmd/serve.go`.
Acceptance — *write a fact in one run, recall it in a later run* — is covered by
`internal/agent/memory_e2e_test.go` and `internal/memory/memory_test.go`.

---

## Where memory sits in the trust model

Memory is a **trusted built-in**, the same tier as `shell`/`web_*` — not a brokered capability.

- **Benefit:** matches the deployment trust model (design §1): on a private, single-user box the
  *engine* writing notes is trusted, so memory needs no sandbox or capability grant. It is the
  simplest thing that meets the goal and reuses the existing built-in plumbing.
- **Drawback:** authored (sandboxed) Lua tools cannot read or write memory — `remember`/`recall`
  are `Trusted` and **not** `Exposed`, so `call_tool` can't reach them (same posture as `shell`).
  If a future authored tool legitimately needs to persist state, exposing a read-only `recall` to
  the sandbox is a deliberate, separate decision (it's idempotent, like the exposed `web_fetch`),
  not something that leaks in by default.

`broker.Trusted` already returns true for every built-in (`_, ok := a.byName[name]`), so adding the
tools to the built-in slice classifies them trusted-and-unexposed automatically — no broker change.

---

## Two verbs, not one multiplexed tool

`remember` (write) and `recall` (read) are separate `tools.Tool`s rather than one `memory` tool
with an `op` field.

- **Benefit:** matches the existing single-purpose built-in convention (`web_search`/`web_fetch`),
  and each gets a clean, unambiguous JSON schema — the model isn't choosing an `op` enum and then a
  conditional set of fields. `recall` is read-only; `remember` is the only writer.
- **Drawback:** two entries in the tool list instead of one. Negligible at this surface size, and
  the clarity is worth it.

`recall` resolves in priority order: exact `key` → relevance `query` (token-overlap `Search`) →
recent listing (capped by `limit`, default 5). **A `query` that matches nothing falls back to the
recent listing** rather than reporting "nothing" — see the drawback below.

- **Benefit:** one tool covers "fetch this exact fact", "find facts about X", and "what do you
  know?" without three tools. The default cap keeps a large store from flooding the context.
- **Drawback:** `Search` is **token-overlap**, so a `query` sharing no literal words with the
  stored note misses — a different language/script (a Russian query over an English note), a
  synonym, or an unexpectedly-phrased fact. That produced a real false negative (an active-space
  `user.level` = "…A1" went unfound under a Russian query), so `recall` now **falls back to
  listing recent entries on a query miss** (`no direct match for …; the N most recent entries…`):
  for a small personal store the agent reads the few results and finds the one it wants. The real
  fix (embeddings / semantic search) waits for the SQLite migration; the fallback is the cheap
  mitigation until then.
- **Drawback:** the precedence is implicit — giving both `key` and `query` silently prefers `key`.
  Documented in the tool description so the model isn't surprised.

---

## `Store`: interface + in-memory implementation

`Put / Get / Search / List / Delete`, backed by `MemoryStore` (a `map[string]Entry` + mutex),
mirroring the tool `Registry` deliberately.

- **Benefit:** the same shape the codebase already uses — token-overlap `Search`, atomic JSON
  persistence, an interface so SQLite can replace it later without touching callers. One pattern to
  learn, not two. `Delete` exists on the interface for the future management plane (Phase 4e) even
  though no built-in calls it yet.
- **Drawback:** `Delete` is currently dead from the agent's side (no `forget` tool in v1) — a small
  bit of unused surface. Kept because the management plane will want it and it costs nothing.

**Upsert by key.** `Put` overwrites an existing key in place.

- **Benefit:** the agent updates a fact (`user.editor`) by re-using its key; no duplicate
  accumulation, no separate update path.
- **Drawback:** no history — overwriting loses the previous value. Acceptable for notes; the audit
  log records that a write happened (key + tags), and full value-versioning is a non-goal here.

### Persistence: JSON file

`NewPersistentStore(path)` loads at startup; every mutation writes the whole file back atomically
(temp + rename, `0600`, parent `0700`) — identical to the tool catalog.

- **Benefit:** single-binary-friendly, human-readable, diffable; crash-safe via atomic rename;
  consistent with `tools.json` and `config.json`. `<workspace>/.agent/memory.json` — memory is
  **workspace-local** (spaces ADR governing decision): each workspace has its own store, and
  per-space shards live beside it under `.agent/spaces/<id>/memory.json`.
- **Drawback:** rewrites the whole file on each write — fine at the expected size (tens–hundreds of
  notes), not thousands. That scale is exactly the SQLite trigger (design §9). Single-process
  assumption: the in-process mutex covers goroutines, not multiple processes sharing the file.

### Cross-run sharing in `serve`

`agent serve` builds **one** `MemoryStore` and shares it across every run (like the registry), so a
fact remembered in one run is recallable by later runs and any future frontend.

- **Benefit:** this *is* the 4d acceptance criterion, and it falls out of sharing one instance —
  no per-run reload needed.
- **Drawback:** all runs on one engine share one global keyspace (no per-run or per-user
  namespacing). Correct for the single-user deployment (§1) — the trusted users share one memory.
  Per-user scoping was considered (an earlier Phase 4e draft) and **dropped**; revisit only if the
  trust model ever widens to untrusted users.

### Space scoping (`ScopedStore`)

With a **space** active on a session (usage.md §Spaces), the cmd layer hands `remember`/`recall` a
`memory.ScopedStore`: writes go to the active space's own shard
(`.agent/spaces/<id>/memory.json`), reads merge the space over the global store (the space
shadows global on key collisions). The tools' signatures are untouched — they still take a
plain `memory.Store` — and with no active space the global store is used directly, exactly the
pre-spaces behavior. Sharding also bounds the rewrite-whole-file cost of `Put` to one space's
file. In `serve`, one shard instance per space is shared process-wide (a cache in cmd), so
concurrent sessions in the same space serialize writes through one store.

---

## Audit: writes recorded, reads not

`remember` emits a `memory_write` audit event (key + tags, not the value); `recall` emits nothing.

- **Benefit:** mirrors the broker (effects are audited, queries aren't) — the append-only log shows
  what the agent chose to persist and when, which is the reviewable surface that matters. Omitting
  the value keeps a possibly-large note out of the audit line and avoids duplicating it.
- **Drawback:** you can't reconstruct *what* was stored from the audit log alone (only that a key
  was written) — you'd read `memory.json` for the value. Reads being unaudited means you can't tell
  from the log which facts a run consulted. Both acceptable: the store is the source of truth for
  contents, and read-tracking isn't worth the log volume here.

---

## System-prompt nudge

The executor prompt tells the model it has cross-run memory: `recall` at the start of a task,
`remember` durable facts, don't store secrets.

- **Benefit:** memory is useless if the model never reaches for it; the nudge makes recall-first and
  save-worthy-facts the default behavior without any host-side orchestration.
- **Drawback:** it's a suggestion, not a guarantee — the model may forget to recall or over-save.
  A stronger version (auto-injecting relevant memories at run start) is noted below as a deliberate
  v2, kept out of v1 to hit the acceptance criterion minimally and keep prompt-cache behavior
  predictable.

---

## What's intentionally deferred

| Item | When | Why not now |
| --- | --- | --- |
| Auto-recall (inject top-k relevant notes into the run's context at start) | v2 | v1 is tool-driven to keep prompt-cache behavior predictable; add once the store proves out |
| `forget` tool (agent-driven delete) + delete audit event | Phase 4e | `Store.Delete` exists; surface it with the management plane |
| Expose read-only `recall` to the sandbox | when an authored tool needs it | idempotent like `web_fetch`, but a deliberate boundary decision, not a default |
| ~~Per-run / per-user namespacing~~ | — | **dropped** — single-user box shares one keyspace (design §1) |
| Value versioning / history | non-goal | upsert is enough for notes; audit records that a write happened |
| SQLite backing | post-Phase-4 | when catalog + audit + memory want one transactional store (design §9) |
