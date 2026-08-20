# Tool system — architecture, solutions, and trade-offs

How tools are modelled, stored, and surfaced to the model, with the **benefit / drawback** of each
design choice. Complements [`plan.md`](planning/plan.md) Phase 3 (the staged build) and
[`security.md`](security.md) (the boundary authored tools run behind). Update this as the design
moves through stages 3a–3e.

**Status:** Phase 3 (**3a–3e**) is implemented. 3a = `ToolSpec` + `Registry`
(`internal/tools/spec.go`, `internal/tools/registry.go`). 3b = live wiring: the executor
(`internal/agent/agent.go`) builds `Broker → LuaGlue → Registry` per run, resolves tool calls via
`Agent.dispatch`, and audits every brokered effect. 3c = `author_tool`
(`internal/tools/authortool.go`): the agent authors, tests, and registers a tool mid-run. 3d =
search-gated tool defs for large catalogs. 3e = `revoke`/`list` CLI (`cmd/tool.go`) + code-hash
dedup. Remaining: SQLite store and async approval are post-Phase-3 (see deferral table).

---

## The two-tier model

There are two kinds of tool, deliberately kept as **separate types**:

1. **Built-ins** — `tools.Tool` (`internal/tools/tools.go`): hand-written Go, trusted, ambient
   authority. Roughly two dozen, in six groups:

   | Group | Tools |
   | --- | --- |
   | World | `shell`, `web_search`, `web_fetch`, `scrape` |
   | Compute & self-extension | `run_code`, `author_tool` |
   | Human-in-the-loop | `ask_user` |
   | Memory & context | `remember`, `recall`, `list_spaces`, `create_space`, `switch_space`, `space_notes`, `update_space_notes`, `record_artifact` |
   | Recall of past work | `list_sessions`, `search_sessions`, `read_session` |
   | Introspection | `status`, `usage`, `recent_activity`, `tool_catalog`, `read_self_docs` |

   Plus `spawn_agent`, which lives in the agent package (so `tools` need not import `agent`) but
   registers as a built-in like the rest. Several are **conditional** — registered only when their
   dependency is wired, so the model is never offered a tool that must fail (`scrape` needs a
   stored `scrapingant` secret; the memory tools a store; `record_artifact` a manifest; the
   session tools a session store; `spawn_agent` an agent catalog).
2. **Registered tools** — `tools.ToolSpec` (`internal/tools/spec.go`): agent-authored (or
   natively-registered) tools held in a `Registry`. Authored ones run sandboxed + brokered.

**Solution:** the executor computes each iteration's tool defs as *built-ins ++ registry tools,
in registration order* (`Agent.buildToolDefs`).

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
The engine is single-user (one shared trust domain, design §1), so `User` and `Shared` behave
identically — both persist to the one shared catalog.

- **Benefit:** the agent can author a throwaway tool for one run (`Ephemeral`) without polluting the
  durable catalog, or persist a useful one.
- **Note:** the `User`/`Shared` enum values both exist in the code but resolve to the same shared
  store. Per-user isolation was considered (an earlier Phase 4e draft) and **dropped** — the trusted
  users share one catalog. The latent `User` value is harmless; treat persisted tools as shared.

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

The catalog is **config-dir-scoped**, not workspace-scoped: it is part of the agent's identity
(`<config-dir>/tools.json`), shared across every workspace the agent works in, and unaffected by
`--workspace`. The audit log follows the same split. **Memory does not** — it and the spaces live
at `<workspace>/.agent/`, the one deliberate exception, so a workspace change swaps the agent's
notes but not its tools; see
[`environment.md`](environment.md#two-scopes-config-dir-and-workspace). (Declarative **sub-agent
types** are read from both the config-dir and the workspace — `agents/*.md`,
workspace-over-global — because a type is prompt/config, not persisted authored state.)

### Search and catalog-size gating

`Registry.Search` is token-overlap ranking over `name + description`, top-k, zero-overlap excluded.
The executor (`Agent.selectRegistryTools`) offers **all** registry tools while the catalog is small
(≤ `maxInlineTools` = 12) and switches to **top-k for the run's task** above that — always unioned
with run-local **ephemeral** tools so a just-authored tool stays callable even in a big catalog.

- **Benefit:** zero dependencies, deterministic, and a large catalog cannot flood the context
  window. The query is the fixed run task, so the selection is stable across iterations (only grows
  when a tool is authored), keeping the prompt cache useful. Emitted in registration order.
- **Drawback:** no stemming/synonyms — "convert" won't match "conversion" — so a relevant tool can
  miss the cut in a large catalog. The ephemeral-union covers same-run authoring; better recall waits
  for BM25-lite/embeddings. Ranking *order* doesn't matter here, only *inclusion*.

### Dedup by code hash

`Register` returns the existing tool when another name has the same code hash (script hash =
`lang\0source`); `author_tool` then points the model at the existing tool instead of creating a
duplicate.

- **Benefit:** the catalog doesn't accumulate identical logic under different names; re-authoring is
  idempotent.
- **Drawback:** two tools with identical code but different *descriptions/schemas* are treated as one
  (the hash ignores the model face). Rare in practice, and the first-registered description wins;
  documented so it isn't surprising.

### Revoke / list (CLI + API)

`agent tool list` and `agent tool revoke <name>` (`cmd/tool.go`) operate on the persistent catalog;
a revoked tool drops from `List` and therefore the live tool set on the next run. Ephemeral tools
need no revoke — they die with the run.

Over the API (Phase 4e-3): `GET /tools/{name}` returns a `ToolDetailView` (the listing fields plus
the impl **source** and smoke **test**, which the listing omits); `DELETE /tools/{name}` revokes and
records a `tool_revoked` audit event (name, code hash, scope, version). `agent tool revoke --addr`
routes to a running engine so the *live* set — and the process-wide `audit.jsonl` — reflect the
revoke, not just the on-disk catalog. `api.Client.ToolDetail` / `RevokeTool` are the peer methods.

- **Benefit:** a human management path without a UI, single-binary-friendly; over the API the revoke
  hits the live engine's shared registry, so an in-flight run drops the tool at its next iteration's
  tool-def recompute (not just on the next run), and the removal is audited.
- **Drawback:** the agent still has no *self*-revoke meta-tool (only humans revoke). Cheap to add if
  it bites.

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

## `author_tool`: the authoring pipeline

**File:** `internal/tools/authortool.go`. A built-in `tools.Tool` whose `Run` is the only path that
turns model output into a registered tool. Every step runs **host-side** — the model supplies the
spec as arguments, never the control flow:

`validate → approve → smoke-test (under exactly the requested caps) → register → audit`.

**Host-global reference (in the tool description, so the model has it while authoring).** The Lua
body reads its args from `input` and has only the string/table/math libs — plus one host global per
*granted* capability (an ungranted one is `nil`; each raises a Lua error on failure/denial, which
fails the test):

| Global | Signature | Capability |
|---|---|---|
| `http_get` | `http_get(url) -> string` (response body) | `http_get` (allowlisted hosts) |
| `read_file` | `read_file(path) -> string` (contents) | `read_file` (path prefix) |
| `write_file` | `write_file(path, content)` (no return) | `write_file` (path prefix) |
| `call_tool` | `call_tool(name, args_table) -> string` | `call_tool` (allowlisted tools) |
| `now` | `now() -> number` (Unix seconds) | `clock` |
| `random` | `random(n) -> string` (n random bytes, hex) | `random` |

`run_code` holds no capabilities, so none of these exist in it — it is pure computation. The base
executor prompt carries a worked analytics example (fetch a CSV via `http_get`, parse it) so the
whole authoring loop is demonstrated, not just described.

- **Benefit:** the model can extend itself, but a tool only becomes callable after it **parses**, a
  human has **approved** any capability beyond the tier, and its **own test passed under exactly the
  caps it will hold**. Rejections come back as content so the model can fix and retry; nothing
  partial is registered.
- **Drawback:** more model-facing surface and a multi-step gate; a determined model can still author
  a *useless-but-valid* tool (passing a trivial test). The test gate is a quality wall, not a proof
  of correctness.

### Approve-then-test ordering

The smoke test runs **after** approval, under a grant of exactly the requested caps.

- **Benefit:** no capability is exercised before a human says yes — testing-first would run real
  effects (a write, a fetch) under caps not yet approved.
- **Drawback:** a tool can be approved and *then* fail its test (effort "wasted" on a prompt).
  Acceptable: approval is about authority, the test is about correctness; conflating them would leak
  authority.

### Tier-gated approval (`capability.Tier.AutoApproves`)

The tier decides which caps auto-approve vs. prompt: Permissive = all; Balanced = side-effect-free
reads (`clock`/`random`/`read_file`) auto, the rest prompt; Safe = prompt for everything.

- **Benefit:** one user-tunable autonomy dial. Routine tools self-serve; risky ones wait for a human
  — and with no approval channel (unattended), an over-tier cap simply rejects (safe default).
- **Drawback:** the policy is a coarse per-kind table, not per-target risk (an `http_get` to a
  benign host is treated like any other). The cap's allowlist still bounds blast radius; finer
  policy can come later.

### Shared script contract (`WrapScript` / `WrapTest`)

The tool body is the inside of `function tool(input) ... end`. Runtime runs `WrapScript` (call
`tool(input)` and return its value); the smoke test runs `WrapTest` (same `tool` in scope, the test
calls it and `return true`).

- **Benefit:** the test exercises *exactly* the code that will run in production — no drift between
  "tested" and "deployed". One contract for both.
- **Drawback:** the body must follow the `input`-in/`return`-out convention (it can't be an arbitrary
  multi-return chunk), and a syntax error surfaces against the wrapped form. A small constraint the
  tool description spells out to the model.

### Per-iteration tool defs (the mid-run visibility fix)

`buildToolDefs` is recomputed each loop iteration, so a tool authored on step *n* is offered on step
*n+1*.

- **Benefit:** "author then call it in the same run" works, and because the registry list is
  append-only and stable, the serialized prefix is unchanged until a tool is added — prompt cache
  stays warm.
- **Drawback:** a tiny rebuild per iteration. Negligible at this catalog size; the stable ordering is
  what keeps it cache-safe rather than a cache-buster.

---

## What's intentionally deferred

| Item | Stage | Why not now |
| --- | --- | --- |
| ~~Live wiring into the run loop~~ | 3b | **done** — broker/sandbox/registry threaded into `cmd/run.go` + executor |
| ~~`author_tool` pipeline (validate→approve→test→register→audit)~~ | 3c | **done** — `internal/tools/authortool.go` |
| ~~Catalog-size-gated tool defs~~ | 3d | **done** — `Agent.selectRegistryTools` |
| ~~Revoke/list CLI + dedup by `CodeHash`~~ | 3e | **done** — `cmd/tool.go`, `Register` dedup |
| BM25-lite / embedding search | post-3 | token overlap suffices for a small catalog |
| ~~Revoke over the API + revoke audit event~~ | 4e-3 | **done** — `DELETE /tools/{name}`, `tool_revoked`, `GET /tools/{name}` detail |
| SQLite store | post-3 | only when catalog+audit+event log want one transactional store |
| ~~Per-user isolation (`User` ≠ `Shared`)~~ | — | **dropped** — engine is single-user (design §1) |
