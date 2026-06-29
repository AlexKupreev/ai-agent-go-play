# Tool system — architecture, solutions, and trade-offs

How tools are modelled, stored, and surfaced to the model, with the **benefit / drawback** of each
design choice. Complements [`plan.md`](plan.md) Phase 3 (the staged build) and
[`security.md`](security.md) (the boundary authored tools run behind). Update this as the design
moves through stages 3a–3e.

**Status:** stage **3a** is implemented — `ToolSpec` + `Registry` (`internal/tools/spec.go`,
`internal/tools/registry.go`), unit-tested, **not yet wired into the run loop** (that is 3b).

---

## The two-tier model

There are two kinds of tool, deliberately kept as **separate types**:

1. **Built-ins** — `tools.Tool` (`internal/tools/tools.go`): `shell`, `web_search`, `web_fetch`,
   `run_code`, `ask_user`, and the future `author_tool`. Hand-written Go, trusted, ambient
   authority. Unchanged since Phase 0–2.
2. **Registered tools** — `tools.ToolSpec` (`internal/tools/spec.go`): agent-authored (or
   natively-registered) tools held in a `Registry`. Authored ones run sandboxed + brokered.

**Solution:** the executor will (in 3b) compute each iteration's tool defs as *built-ins ++
registry tools, in registration order*.

- **Benefit:** built-ins stay simple and don't pay the sandbox/spec tax; the append-only ordering
  keeps the serialized tool-def prefix byte-stable, which is friendly to provider prompt caching.
  Only untrusted (authored) code crosses the security boundary.
- **Drawback:** two shapes to reason about, and a small amount of glue (`executeTool` checks
  built-ins first, then the registry). We accepted this over unifying everything into `ToolSpec`
  because forcing trusted built-ins through the sandbox path would be all cost, no benefit.

---

## `ToolSpec`: model face vs exec face

A spec separates **what the model sees** from **how it runs**:

- *Model face* — `Name`, `Description`, `InputSchema`. This is all that is rendered into the tool
  definitions the LLM receives.
- *Exec face* — `Impl`, `RequiredCaps`, `Scope`, `Test`, plus registry-assigned provenance
  (`Version`, `CreatedBy`, `CodeHash`, internal `seq`).

- **Benefit:** the model cannot see or influence the execution authority (caps, impl) — it only
  describes intent. Provenance lets the audit log and dedup reference a stable code identity.
- **Drawback:** more fields to populate; the `author_tool` pipeline (3c) must fill the exec face
  host-side, never from model-controlled input.

### Impl kinds: `Script` vs `Native`

| Kind | Body | Authority | Persisted? |
| --- | --- | --- | --- |
| `Script` | `Lang` + `Source` (Lua) | sandbox + broker grant | yes (catalog) |
| `Native` | Go `func` handler | in-process (registered by host) | **no** |

- **Benefit:** `Script` is the self-extension path — serializable, sandboxed, capability-bounded.
  `Native` is an escape hatch for host-provided dynamic tools that need real Go but still want to
  live in the registry/search surface.
- **Drawback:** a `Native` handler can't be serialized, so native tools must be re-registered every
  boot and are silently skipped by catalog persistence. This is enforced (`save()` only writes
  `Script` + persistent scope) and tested (`TestPersistence_NativeNotWritten`), but it is an
  asymmetry to remember: *a native tool in a persistent scope still won't survive a restart on its
  own.*

### Code identity: `CodeHash`

SHA-256 over `lang\x00source` for scripts (over `kind\x00name` for natives, which have no body).

- **Benefit:** stable identity for dedup (3e) and for the `tool_authored` audit event.
- **Drawback:** native hashing is name-based, so two different native handlers with the same name
  would collide — acceptable because names are unique registry keys anyway.

---

## Scopes: lifetime and persistence

`Ephemeral` (in-memory, dies with the run) · `User` · `Shared` (both persist to the JSON catalog).
On the single-user CLI, `User` collapses to `Shared`.

- **Benefit:** the agent can author a throwaway tool for one run (`Ephemeral`) without polluting the
  durable catalog, or promote a useful one to `Shared`. The collapse keeps single-user simple while
  leaving room for a multi-frontend deployment later.
- **Drawback:** `User`/`Shared` being the same store today means the distinction is latent — a
  reader might expect per-user isolation that doesn't exist yet. Documented here so it isn't a
  surprise.

---

## `Registry`: interface + in-memory implementation

`Register / Get / List(scope) / Search / Revoke`, backed by `MemoryRegistry`.

**Stable ordering** via a monotonic `seq` assigned at first registration. Re-registering a name
keeps its `seq` (preserves position) and bumps `Version`.

- **Benefit:** "append, never rebuild" with no shared mutable state and no callback into the running
  loop — the serialized tool list only ever grows at the tail, so prompt caching holds. Re-register
  updates in place without reshuffling earlier tools.
- **Drawback:** revoke-then-re-add gives a *new* tail position (the old `seq` is gone), so a
  revoked-and-recreated tool moves to the end. Acceptable: revoke is rare and the order only needs
  to be stable, not semantically meaningful.

**Behind an interface** so a transactional store (SQLite — the stated end goal) can replace
`MemoryRegistry` without touching callers.

- **Benefit:** callers depend on five methods, not on the storage. Migration is a swap.
- **Drawback:** the interface is the lowest common denominator; SQLite-only features (queries,
  transactions across catalog+audit) won't surface until the interface grows. Fine for now.

### Persistence: JSON catalog

`NewPersistentRegistry(path)` loads at startup; persistent-scope changes write back atomically
(temp file + rename, `0600`, `0700` parent dir).

- **Benefit:** single-binary-friendly, human-readable, diffable, matches the existing config style.
  Atomic write avoids a torn catalog on crash.
- **Drawback:** rewrites the whole file on every persistent change — fine at the expected catalog
  size (tens of tools), not at thousands. That scale is exactly the SQLite trigger. No concurrent
  multi-process access (single-process assumption); the in-process mutex covers goroutines only.

### Search

Token-overlap ranking over `name + description`, top-k, zero-overlap excluded (3d refines to
BM25-lite; embeddings deferred).

- **Benefit:** zero dependencies, deterministic, good enough to keep a small catalog from flooding
  the context window. Ties break by `seq` for reproducibility.
- **Drawback:** no stemming/synonyms — "convert" won't match "conversion". Acceptable until the
  catalog is large enough to need real ranking, which is when 3d/embeddings land.

---

## Validation boundary

`ToolSpec.validate()` checks name regex (`^[a-z][a-z0-9_]*$`), non-empty description, non-nil
input schema, and impl completeness. It does **not** enforce the smoke-test or approval.

- **Benefit:** the registry stays a storage concern; *policy* (mandatory test, cap-vs-tier approval)
  lives in the `author_tool` pipeline (3c) where the human gate is. Keeps the registry reusable for
  native/host registration that needs no approval.
- **Drawback:** registering directly (bypassing `author_tool`) skips the policy gate — so the rule
  "authored tools only enter via `author_tool`" is a convention the wiring must uphold, not
  something the registry enforces. Called out so 3b/3c keep that invariant.

---

## What's intentionally deferred

| Item | Stage | Why not now |
| --- | --- | --- |
| Live wiring into the run loop | 3b | needs the broker/sandbox threaded into `cmd/run.go` first |
| `author_tool` pipeline (validate→approve→test→register→audit) | 3c | the policy gate; depends on 3b |
| BM25-lite / embedding search | 3d | token overlap suffices for a small catalog |
| Revoke surfaced (CLI/authored) + dedup by `CodeHash` | 3e | lifecycle polish |
| SQLite store | post-3 | only when catalog+audit+event log want one transactional store |
| Per-user isolation (`User` ≠ `Shared`) | Phase 4+ | single-user CLI doesn't need it yet |
