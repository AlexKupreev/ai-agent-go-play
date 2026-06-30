# Implementation Plan

The actionable, phased plan for evolving this repo from a ReAct CLI into the self-extending
agent described in [`design.md`](design.md). Each phase is shippable on its own and leaves the
agent working. Do them in order — the build-ordering rule in Phase 2 is hard.

**How to read this:** each phase has a *Goal*, concrete *Tasks* (with the files they touch),
*Acceptance* criteria, and *Risks/notes*. Boxes are unchecked work.

**One hard rule (repeated from design §5):** the capability broker + sandbox (Phase 2) must
land **before** `author_tool` (Phase 3). A self-authoring agent without a broker is an RCE
service with an LLM picking the payloads — true even with trusted users, because the *content*
steering the model is untrusted.

---

## Phase 0 — Decouple the provider (the unblocking refactor)

**Goal:** the agent loop speaks neutral types; OpenAI lives behind an adapter. Nothing
user-visible changes. This is the highest-leverage step — every later phase assumes it.

**Why first:** today `internal/agent/agent.go` imports `openai-go` directly (client, messages,
tool defs, usage), and `internal/logger` logs OpenAI types too. Provider-agnosticism and the
headless engine both depend on cutting this coupling.

**Status: DONE** (commit `dc72492`). Build + vet clean; only `internal/provider/openai` imports
the SDK. Adapter + provider mapping covered by unit tests.

**Tasks**

- [x] Create `internal/provider` with neutral types (`provider/types.go`, `provider/provider.go`):
  `Role`, `ContentBlock` (`Text` / `ToolCall{ID,Name,Input}` / `ToolResult{CallID,Output,IsError}`),
  `Message`, `ToolDef`, `Usage`, `StopReason`, `ResponseFormat`, `StepRequest`, `StepResponse`
  (+ `Text()`/`ToolCalls()` accessors), and `type Provider interface { Step(...) }`.
  (`ToolChoice`/`Capabilities()`/streaming deferred — not needed yet.)
- [x] `internal/provider/openai`: adapter implementing `Provider`, holding the `openai-go`
  client. Owns all mapping (messages, tool defs, `ParallelToolCalls`, structured-output
  `ResponseFormat`, usage, stop reasons). The only package that imports `openai-go`.
- [x] Refactor `internal/agent/agent.go`: embedded `openai.Client` → `provider.Provider`; the
  ReAct loop appends/reads neutral types; `buildToolDefs` returns `[]provider.ToolDef`.
  `cmd/run.go` constructs the provider once and injects it into planner + executor.
- [x] `internal/logger` was already neutral (its methods take `any`); fed neutral types now,
  no SDK import. No change needed.
- [x] `Plan`/structured output preserved as a neutral `provider.ResponseFormat`; the OpenAI
  adapter renders it to `ResponseFormatJSONSchema`.

**Acceptance** — met:

- `internal/agent`, `internal/logger`, `internal/tools` no longer import `openai-go` (grep clean).
- ReAct loop + planner logic unchanged; structured planner output path intact. *(Live API
  round-trip not exercised — no key in this environment; verify with `agent run` before relying
  on it.)*
- A second adapter (e.g. Anthropic) can be added without touching `internal/agent`.

**Risks/notes:** the neutral `ResponseFormat` is kept minimal (name + description + schema +
strict) so other vendors can map it. Anthropic adapter intentionally not built yet.

---

## Phase 1 — Solidify the kernel + `run_code`, gate destructive actions

**Goal:** a clean provider-neutral kernel with the current built-ins, plus lightweight
self-extension via `run_code`, and a confirmation gate on destructive operations.

**Status: MOSTLY DONE** (commit pending). `run_code` + destructive-shell gate built and
unit-tested; only the optional rename and full config knobs remain.

**Tasks**

- [ ] (Optional, tidy) rename `internal/agent` → `internal/engine` once the loop is
    provider-neutral, to match design §6. Cosmetic; deferred.
- [x] Add a `run_code` built-in tool (`internal/tools/runcode.go`): the model writes a short Lua
    snippet (gopher-lua) the engine runs and returns. Compute-only sandbox — base/table/string/
    math libs, `os`/`io`/`debug`/`package` and code-loading stripped, context-timeout abort, no
    host functions. Flagged in code that it graduates to the broker/sandbox in Phase 2.
- [x] Add a destructive-action approval hook for `shell` (`internal/tools/destructive.go`,
    `shell.go`): heuristic detection (rm/mv/dd/overwrite/sudo/kill/destructive-git/pkg-removal/
    pipe-to-shell) → `ConfirmFunc` gate (`StdinConfirm` in CLI) before running. Shell stays a
    trusted built-in; this is a guardrail, not removal. Injectable confirm for tests.
- [~] Config knobs: `model`/`verbose` already flags; `run_code` timeout is a constructor param.
    `maxIterations` is still a const — make configurable when needed.

**Acceptance**

- [x] `run_code` computes and returns values (arithmetic, strings, arrays, maps, `result`
    fallback); `os`/`io` access errors; runaway loops time out. Covered by `runcode_test.go`.
- [x] A destructive shell command triggers confirmation; read-only does not; decline blocks the
    run. Covered by `shell_test.go` + `destructive` table test.
- [ ] Live end-to-end (model actually invokes `run_code`) — needs an API key; not yet exercised.

**Risks/notes:** the destructive heuristic is best-effort (false positives only cost a prompt),
*not* a security boundary — the real boundary is Phase 2's broker. `run_code` deliberately has
no host functions yet.

---

## Phase 2 — Capability broker + gopher-lua sandbox + audit log  *(gate for Phase 3)*

**Goal:** a deny-by-default execution environment for *machine-authored* code, and an
append-only audit log. This is the boundary the whole project is built around.

**Status: CORE DONE** (commit pending). The boundary exists and is unit-tested; live wiring of
the broker into the run flow lands with Phase 3 (when authored tools actually request caps).

**Tasks**

- [x] `internal/capability`: `Capability{Kind, Hosts, PathPrefix, Tools}` for
    `http_get`/`read_file`/`write_file`/`call_tool`/`clock`/`random`; `GrantContext{Run, Granted,
    Tier}` with `Safe|Balanced|Permissive`; allowlist matching (host glob, path-prefix containment,
    tool name/`*`); `Broker` whose every method = check grant + allowlist → execute → audit
    (denials audited too). HTTP capped at 1 MiB; `ToolCaller` injected to avoid an import cycle.
- [x] `internal/audit`: append-only `Recorder` (`MemoryRecorder` for tests, `JSONLRecorder` for
    disk). *(Deviation from plan: JSONL now; the richer multi-variant store / SQLite is deferred —
    see open question. The broker records `capability_exercised` / `capability_denied`.)*
- [x] `internal/sandbox` (`luaglue.go`): fresh `LState` per call; globals built **only** from the
    grant (host funcs installed per granted capability, else absent); `os`/`io`/`debug`/`package`/
    code-loaders stripped; context-timeout abort. `input` passed in, result returned.
    *(Hard op-count hook not added — context-timeout covers runaway loops for now.)*
- [x] Wire `run_code` to execute through `luaglue` with an empty grant (no host funcs).
- [ ] Live wiring: per-run `JSONLRecorder` + a `Broker` (with a registry-backed `ToolCaller`)
    threaded into the run. **Deferred to Phase 3**, where authored tools first need real grants.

**Acceptance** — core met (unit-tested):

- [x] A script cannot reach network/filesystem/tools unless its grant includes the capability;
    ungranted host functions are simply **absent** (`type(http_get) == "nil"`); granted-but-out-of-
    allowlist calls are **denied** by the broker and raise in the script.
- [x] Every brokered call (and denial) is recorded to the audit log.
- [ ] Full run/event log replay into a transcript — comes with the richer store (Phase 4-ish).

**Risks/notes:** memory-DoS is the weak axis with in-process Lua — context-timeout fires today;
add an op-count hook if abuse appears. Keep the broker surface *narrow*; a single over-broad
capability undoes the tier. `http_post` intentionally not added until a tool needs it.

---

## Phase 3 — Tool registry + `author_tool` + tool-search  *(true self-extension)*

**Goal:** the agent promotes ephemeral code into named, scoped, tested, capability-bounded
tools that persist and are discoverable.

Split into shippable sub-phases 3a–3e. Each leaves the agent working.

### Integration model (the key architectural decision)

Keep the existing `tools.Tool` shape for built-ins — do **not** refactor them into `ToolSpec`.
The executor gains a `Registry` alongside its built-in `[]Tool`:

- `buildToolDefs` each iteration = built-ins (incl. `author_tool`) `++` registry tools, in
  **registration order** (append-only, stable). Recomputing per iteration is cache-safe
  *because* the order is stable and append-only: the serialized prefix is byte-identical until a
  new tool is added, then it just grows. This achieves "append, never rebuild" with no shared
  mutable state and no callback into the running loop.
- `executeTool` matches a built-in first; else resolves in the registry → `Script` runs via
  `sandbox.LuaGlue` with the tool's resolved grant; `Native` calls its handler.

Only *authored* tools go through the sandbox/broker; built-ins are unchanged from Phase 0–2.

### Decisions (settled — pi gives no precedent: its core has no sandbox, no test gate, and runs extensions with **ambient authority**, so our second, sandboxed tier is a deliberate divergence and these are our calls)

- **Smoke test runs AFTER approval, not before.** The test executes real effects under the
  requested caps; testing first would exercise a capability before the human approves it.
  Approve-first guarantees no capability is exercised without a human "yes". *(Minor divergence
  from vision-doc B.3's stated order — recorded deliberately.)*
- **Sandbox-exposed built-ins (v1): `web_search` + `web_fetch` only.** Read-only, idempotent, no
  interactive confirm — so design §5 rule (b) (running a re-entered built-in under the caller's
  grant instead of its confirm) is moot for v1. `shell` stays **unexposed** → unreachable from
  authored code. Revisit only if a real need appears.
- **Persistence (v1): JSON catalog** at `~/.config/ai-agent/tools.json`, single-binary-friendly,
  matching the existing config. **SQLite is the stated end goal** (design §6) once the catalog,
  audit log, and run/event log want one transactional store — migrate `Registry` behind its
  interface without touching callers.
- **Approval (v1): synchronous** via the existing `ConfirmFunc` (`StdinConfirm`). A cap beyond
  the run's tier prompts at authoring time; declined → rejected. The async *pending queue* /
  management UI is **Phase 4**, not here.

### 3a — `ToolSpec` + `Registry`  *(DONE — `internal/tools/spec.go`, `registry.go`, `registry_test.go`)*

- [x] `tools.ToolSpec`: model face (`Name`, `Description`, `InputSchema`) + exec face
    (`Impl: Native|Script{Lang,Source}`, `RequiredCaps []capability.Capability`, `Scope`
    `Ephemeral|User|Shared`, `Test`, `Version`, `CreatedBy`, `CodeHash`). Name regex + SHA-256
    code hash in `spec.go`.
- [x] `tools.Registry` interface + in-memory impl: `Register/Get/Search/Revoke/List(scope)`;
    monotonic seq per registration for stable ordering. Ephemeral = in-memory (dies with the
    run); Shared/User = JSON catalog (atomic write) loaded at startup; `User` collapses to `Shared`
    on single-user CLI. Native impls never persisted (skipped on save). Search = token-overlap
    (BM25-lite deferred to 3d).
- [x] *Acceptance:* register/list/revoke/search round-trip + persistence reload, unit-tested; not
    wired to the loop. Design + trade-offs documented in [`tools.md`](tools.md).

### 3b — Live broker/sandbox wiring (activates Phase 2 in the run flow)  *(DONE)*

- [x] In `cmd/run.go`, per run, construct `JSONLRecorder` (`<session>/audit.jsonl`) +
    `NewPersistentRegistry` (`~/.config/ai-agent/tools.json`); `NewExecutor` builds
    `Broker(rec) → LuaGlue(broker)` and shares the glue with `run_code`. `toolCaller` =
    `Agent.dispatch` (built-ins first, then registry Script via glue / Native via handler).
- [x] `broker.Trusted` = built-in names; `broker.Exposed` = `{web_search, web_fetch}`. The
    broker⇄dispatch cycle is broken by assigning `broker.Tools` after the agent is built.
    `buildToolDefs` now appends registry tools after built-ins (collisions skipped).
- [x] *Acceptance:* registry Script tools run through the real broker with a real audit trail;
    `call_tool`→`shell` (unexposed) is denied and audited; ungranted host funcs are absent.
    Covered by `internal/agent/executor_dispatch_test.go`. *(Live model loop needs an API key;
    not exercised here.)*

### 3c — `author_tool` meta-tool (the pipeline)  *(DONE — `internal/tools/authortool.go`)*

A built-in `tools.Tool` whose `Run` performs, host-side (not model-controllable):

- [x] 1. **Validate** — `name` regex, `input_schema` is an object, `code` + `test` parse
    (`sandbox.Parse` on the wrapped forms). Rejections return to the model as content to retry.
- [x] 2. **Approve** — caps where `!tier.AutoApproves(kind)` → `ConfirmFunc`; declined or no
    channel → reject. Tier policy added as `capability.Tier.AutoApproves`.
- [x] 3. **Smoke-test** — run the mandatory `test` in the sandbox under a grant of **exactly the
    requested caps**; must `return true`, else reject. Tool body + test share one contract via
    `tools.WrapScript`/`WrapTest` (body is callable as `tool(input)`), so the test exercises the
    real code; runtime execution uses the same wrap.
- [x] 4. **Register** at scope; **`buildToolDefs` now recomputes each iteration**, so the tool
    appears on the next step (was hoisted out of the loop — fixed).
- [x] 5. **Audit** `ToolAuthored{name, code_hash, caps, scope, version}`.
- [x] *Acceptance:* covered by `authortool_test.go` (gate-by-gate) and `authoring_e2e_test.go`
    (fake provider authors `triple`, then calls it same run; test-fail rejects; not offered before
    authoring, offered after). Logging made nil-safe so the loop runs without a disk logger.

### 3d — Tool-search  *(DONE)*

- [x] `Registry.Search(query, k)` = token-overlap over name+description (BM25-lite/embeddings still
    deferred). `Agent.selectRegistryTools`: built-ins always; registered tools — all while catalog
    ≤ `maxInlineTools` (12), else top-k by `Search(task)` **unioned with run-local ephemeral tools**
    (so same-run authoring still works in a big catalog), emitted in registration order.
- [x] *Acceptance:* `TestSelectRegistryTools_LargeCatalogTopK` — 16-tool catalog offers ≤12, the
    relevant tool surfaces, the ephemeral tool is kept, unrelated ones are dropped.

### 3e — Lifecycle polish  *(DONE)*

- [x] `revoke` surfaced via CLI (`agent tool list`, `agent tool revoke <name>` in `cmd/tool.go`),
    operating on the persistent catalog; revoked tools drop from `List`/the live set.
- [x] Dedup by code hash: `Registry.Register` returns the existing tool when another name has the
    same hash; `author_tool` tells the model to call the existing tool. Ephemeral dies with the run.
- [x] *Acceptance:* `TestRegister_DedupsByCodeHash`, `TestAuthorTool_DedupsIdenticalCode`.

### Cross-phase test (no live API)

- [x] A fake provider that emits an `author_tool` call then a call to the new tool drives the
    whole pipeline end-to-end in a unit test (`internal/agent/authoring_e2e_test.go`).

**Done in advance:** `call_tool` allowlist primitive (broker `Trusted`/`Exposed`) — a trusted
built-in is reachable from the sandbox only when explicitly exposed *and* named directly in the
grant; a `*` grant never escalates into one. 3b wires it; design §5 rule (b) is sidestepped in v1
by exposing only confirm-free built-ins.

**Risks/notes:** the smoke-test gate is the quality wall — `test` is mandatory and runs under
exactly the approved caps. Validate that authored Lua reliably round-trips (open question,
design §9).

---

## Phase 4 — Headless engine API + memory + management plane + frontends

**Goal:** the engine becomes headless and addressable; web/Telegram join the CLI as peer
clients; approvals and review/revoke get a UI.

Split into sub-phases, ordered so each is shippable and the internal seams (no external fork) come
before the transport/frontend choices (real forks, settled when reached).

### Decisions to settle when reached (not blocking 4a–4b)

- **Transport (4c):** ~~HTTP+SSE vs JSON-RPC/WebSocket.~~ **DECIDED: HTTP+SSE** (simplest, curl-able,
    streaming-friendly, single-binary). Rationale + how JSON-RPC stays addable: [`api-transport.md`](api-transport.md).
- **First frontend (4e):** Telegram vs web. Leaning Telegram (thin, good for the unattended/mobile
    approval case; web is more work for the same payoff first).
- **Multi-user model (4e) — DROPPED: single shared trust domain.** An earlier draft of 4e added a
    per-run `Owner` label, session isolation, and per-user data ownership (private memory/tools, opt-in
    sharing). That was **removed** to simplify: design §1 is a family sharing one trusted box, so owned
    /private data is ceremony without payoff, and a user with shell access could reach another's data
    anyway. The engine stays **single-user in its data model** — one shared memory keyspace, one shared
    tool catalog. Concurrent runs remain compute-independent (goroutine-per-run + fresh executor-per-run
    + concurrent-safe shared stores) but share data freely. Frontend auth (e.g. a Telegram allowlist)
    gates *who may reach the engine*, not data between trusted users. *(If the box ever opens to
    untrusted users, owner scoping + real auth attach as siblings — design §1/§5.)*
- **Store (cross-cutting):** SQLite vs keep JSONL+JSON. Migrate when catalog+audit+memory+run-metadata
    want one transactional store (design §9). A central audit reader (4e) is the likely trigger.

### 4a — Approver seam (async-ready approval)  *(DONE — `internal/tools/approval.go`)*

- [x] Replaced `ConfirmFunc`/`StdinConfirm` with an `Approver` interface
    (`Approve(ctx, ApprovalRequest) (bool, error)`): structured request (kind/title/detail/run) so a
    frontend can render it and an async approver can queue by run; `ctx` so Approve can block on a
    remote decision. `StdinApprover` (CLI), `ApproverFunc` (tests). Refactored `shell`, `author_tool`,
    `NewExecutor`. An approval **error blocks** the action (treated as not-approved).
- [x] *Acceptance:* shell decline/approve/error and author_tool decline/no-channel paths covered;
    CLI behaviour unchanged.

### 4b — Headless engine event sink  *(DONE — `internal/agent/observer.go`)*

- [x] Replaced the executor's direct `os.Stderr` prints + concrete `*logger.Logger` with an emitted
    event stream: `Observer.Emit(Event)` with a single `Event` union (`start`/`request`/`response`/
    `tool_start`/`tool_result`). `Observers` fans out; `LoggerObserver` wraps the disk log,
    `CLIObserver` reproduces the verbose trace. `NewExecutor`/`NewPlanner` now take an `Observer` +
    `runID`; `cmd/run.go` composes `LoggerObserver` (always) + `CLIObserver` (when `--verbose`).
- [x] *Acceptance:* `agent run` output is unchanged but driven through the sink; the loop has **no**
    direct stdout/stderr writes (grep-clean). `TestRun_EmitsEventSequence` asserts the event order;
    the API (4c) will attach its own observer to stream the same events.

### 4c — `internal/api` transport  *(DONE — HTTP+SSE, see [`api-transport.md`](api-transport.md))*

Built as increments. Package is split so a JSON-RPC adapter can attach to the same core later.

- [x] **Vertical slice — run/stream.** Transport-neutral core (`internal/api/engine.go`:
    `Engine.StartRun`/`Subscribe` over a `Runner`; `hub.go`: per-run `Hub` implements
    `agent.Observer`, fans events with history replay; `event.go`: wire `Event`). SSE adapter
    (`http.go`): `POST /runs`, `GET /runs/{id}/events`. `cmd/serve.go` (`agent serve`) wires a real
    executor-backed `Runner`. Tests: `http_test.go` (start + stream event sequence; unknown run = 404).
- [x] **Approval queue.** `NewExecutor` now takes an injectable `tools.Approver` (nil ⇒
    `StdinApprover`, so CLI/tests unchanged). `internal/api/approval.go`: `ApprovalQueue` implements
    `tools.Approver` — `Approve` parks a request and blocks until resolved or ctx-cancelled (cancel ⇒
    not-approved); `Pending()` snapshots; `Resolve` is single-shot. SSE adapter adds the
    `GET /approvals` and `POST /approvals/{id}` endpoints; `serve` shares one queue between executor
    and endpoints. Tests:
    `approval_test.go` (park→list→resolve for approve/deny; unknown id = 404). Design in
    [`api-transport.md`](api-transport.md).
- [x] **Tools over the API.** `internal/api/tools.go`: `GET /tools` (catalog in registration order)
    and `GET /tools/search?q=&k=` (relevance-ranked) over the `tools.Registry`; wire `ToolView`
    omits impl source. `serve` now builds **one** persistent registry shared between the executor and
    the endpoints, so a tool authored in a run is immediately visible to later runs and to the API.
    `NewServer` gained a nil-able `catalog` arg (endpoints registered only when supplied). Tests:
    `tools_test.go` (list order/fields, search ranking + missing-`q` 400, absent-without-catalog 404).
- [x] **CLI as a client** of the engine (peer to future frontends). `internal/api/client.go`:
    `Client` (`StartRun`/`StreamEvents`/`Pending`/`Resolve`) — the peer side of the same transport.
    `cmd/client.go` (`agent client <task> --addr`) starts a run on a running `serve` engine, streams
    events to the terminal (mirrors the `CLIObserver` trace), and polls `/approvals` to prompt the
    operator (SSE has no server push). Tests: `client_test.go` (full start→stream→approve loop;
    bad-status error) against a real `httptest` server.
- *Acceptance:* a run can be started and streamed over the API ✅; an escalation parks in the queue and
    is resolved by an API call ✅; the tool catalog is listable/searchable over the API ✅; the CLI
    drives a run on a separate engine process ✅.

**4c, 4d, and 4e-1 are complete.** Next: **4e-3** (tool revoke over the API). *(4e-2, per-user data
ownership, was dropped — the engine stays single-user; see the multi-user decision above.)*

### 4d — Long-term memory  *(DONE — see [`memory.md`](memory.md))*

- [x] `internal/memory`: `Store` interface (`Put/Get/Search/List/Delete`) + `MemoryStore`
    (in-memory, optional JSON-file persistence — atomic temp+rename, token-overlap `Search`),
    mirroring the tool `Registry`. Persists to `~/.config/ai-agent/memory.json`.
- [x] Built-ins `remember` / `recall` (`internal/tools/memory.go`): trusted, **not** exposed to the
    sandbox (so `call_tool` can't reach them, like `shell`). `NewExecutor` takes a `memory.Store`
    (nil ⇒ tools omitted); `serve` shares ONE store across runs so a fact from one run is recallable
    in later runs. `remember` emits a `memory_write` audit event (key+tags, not the value); reads
    aren't audited (mirrors the broker). Executor prompt nudges recall-first / save durable facts.
- [x] *Acceptance:* `internal/memory/memory_test.go` (round-trip, upsert, search, reload across
    instances) + `internal/agent/memory_e2e_test.go` (fake provider remembers in run 1, a second
    executor over the same store recalls it in run 2; write is audited).

### 4e — Management plane + a frontend

Split into shippable sub-phases, testable with the existing `httptest` pattern (no live API, no
Telegram token); only the frontend (4e-6) needs the external fork settled. The engine stays
**single-user** (one shared trust domain, design §1) — there is no owner/identity layer.

What already exists (4c): start/stream a run, the approval queue (`GET/POST /approvals`), tool
list/search (`GET /tools`), and `api.Client` (CLI as peer). The gaps 4e closes: tools can't be revoked
over the API; the audit log isn't readable over the API (per-session JSONL, no central reader);
approvals park *silently* (clients must poll); and there is no frontend beyond the CLI.

#### 4e-1 — Run lifecycle (start / list / status / cancel)  *(DONE)*

- [x] **Run metadata + lifecycle.** Engine `runs` map entry is a
    `run{hub, cancel, info RunInfo{id, task, state, startedAt, endedAt, result, error}}`; the run ctx is
    `context.WithCancel(context.Background())` (outlives the request that started it) and the goroutine
    sets terminal state on exit.
- [x] **Kill switch + listing.** `Engine.StopRun(id)` / `ListRuns()` / `RunStatus(id)` via `lookup(id)`
    (`ErrUnknownRun` if absent). Endpoints: `POST /runs/{id}/cancel`, `GET /runs`, `GET /runs/{id}`
    (404 unknown). Cancellation propagates to the next model/tool boundary.
- [x] **CLI/Ctrl+C.** `Client` gained `StopRun`, `RunStatus`, `ListRuns`; `agent stop <id> --addr`;
    `signal.NotifyContext` in `agent run` (cancels the in-process run) and `agent client` (first Ctrl+C
    cancels the *remote* run + detaches, second force-quits).
- [x] *Acceptance:* `internal/api/runs_test.go` — `TestCancelStopsRun` (kill switch ends a blocked run in
    the error state), `TestUnknownRunStatusIs404`.

*(An earlier draft of 4e-1 also added a per-run `Owner` label + session isolation + owner-scoped
approvals; that was removed — the engine is single-user, see the multi-user decision above. The old
4e-2, per-user data ownership, was dropped with it.)*

#### 4e-3 — Tool review / revoke over the API

- [ ] `DELETE /tools/{name}` → `Registry.Revoke` (404 if absent), emitting a new `tool_revoked` audit
    event (the revoke-audit gap noted in `tools.md`). Optional `GET /tools/{name}` detail that *includes*
    the impl source (omitted from the listing). `Client.RevokeTool`; extend `agent tool revoke` with an
    `--addr` path (today it edits the local catalog file directly).
- *Acceptance:* a tool revoked over the API drops from `List`/the live set and the revoke is audited.

#### 4e-4 — Central audit log + browse over the API

- [ ] Today `serve` opens a *per-run* `JSONLRecorder` inside `newServeRunner` — no queryable history.
    Add an `audit.Reader` (`Tail(n, filter)`) and a single **process-wide** recorder for `serve` (e.g.
    `~/.config/ai-agent/audit.jsonl`) shared across runs (same "one shared instance" pattern as the
    registry/memory store); per-session logs stay for the transcript.
- [ ] `GET /audit?run=&type=&limit=`; `Client.Audit`; `agent audit --addr`. This makes the audit log the
    single review surface for everything effectful (capability use, `tool_authored`, `tool_revoked`,
    `memory_write`).
- *Acceptance:* effects from a run are browsable over the API.
- *Note:* run metadata + central audit is the likely **SQLite tipping point** (design §9) — swap the
    JSON/JSONL backings behind their existing interfaces when it bites, not before.

#### 4e-5 — Approval events on the stream (poll → push)

- [ ] The UX seam. `ApprovalQueue.Approve` currently parks silently; a streaming client learns of an
    escalation only by polling `/approvals`. Give the queue an observer hook so parking/resolving
    **emits `approval_requested` / `approval_resolved` events into the run's hub**. Any streaming
    frontend then learns of an escalation in the event stream it is already reading; the existing
    `POST /approvals/{id}` resolves it. Small change, big leverage for every frontend.
- *Acceptance:* a parked escalation appears as a stream event on its run; resolving it emits the
    resolution event.

#### 4e-6 — Telegram frontend as a peer client *(resolves the frontend fork)*

- [ ] `internal/frontend/telegram` driving `api.Client` (a peer, like `agent client` — not special).
    Keeps a chat↔run mapping; a message starts a run and streams events back; a parked approval (from
    the 4e-5 stream event) becomes an **inline keyboard** (Approve / Deny) wired to `Client.Resolve`.
    This is the unattended/mobile approval case the plan leans Telegram for.
- [ ] **Auth lives in the frontend:** an allowlist of Telegram user ids (config) gating who may reach
    the engine at all. The engine stays bound to `127.0.0.1`; only the bot faces the network. (No
    engine-level auth on the localhost single-user box — design §1.)
- *Acceptance (Phase 4):* a family member drives a session from Telegram against one engine; an
    escalation surfaces in the chat and is recorded; a run can be cancelled mid-flight from the
    CLI/API/bot.

**Risks/notes:** keep frontends thin — *all* policy (approval, audit) lives in the engine; the bot must
contain zero policy, only rendering + relaying. Cancellation is cooperative (stops at the next
boundary), fine given `maxIterations` bounds runs. Approval UX (design §9 open question): the tier dial
already suppresses routine prompts; 4e-5 makes escalations ambient rather than polled; keep them
batched/inline, never a nagging loop. The async `Approver` (4a) is the contract the queue (4c) and this
management plane build on.

---

## Phase 5 — Reserve tier (only if isolation needs grow)

Not planned work; pull from here only when a concrete need appears.

- [ ] `internal/sandbox/wasm` (wazero): hard memory-capped, capability-gated escalation tier —
    if a tool ever needs stronger isolation than `luaglue` gives.
- [ ] Own web-search + analytics primitives — if a second provider or stronger isolation forces
    leaving vendor-native search behind.
- [ ] Anthropic (or other) provider adapter — when actually swapping/AB-testing models.

---

## Cross-cutting (carry through every phase)

- **Tests:** table tests for the provider adapter mapping (Phase 0), the broker allow/deny
  matrix (Phase 2), and the `author_tool` pipeline (Phase 3).
- **Config:** keep secrets in `~/.config/ai-agent/`. `config.json` now holds `model` and `tier`
  too (`agent config set-model` / `set-tier`); precedence for each is `--model`/`--tier` flag >
  config > built-in default (`resolveModel`/`resolveTier` in `cmd/config.go`; tier default =
  `balanced`, validated by `capability.ParseTier`). The setters merge into the existing file. Add
  provider selection the same way when the second adapter lands.
- **Audit-first:** once Phase 2 exists, every effectful path writes to the log — treat a missing
  audit record as a bug.
- **Don't speculatively build** Phase 5 items, multi-tenant isolation, or hard memory caps —
  they are non-goals for this deployment (design §1, §5).

---

## Immediate next step

**Phase 0, task 1:** create `internal/provider` with the neutral types and the `Provider`
interface, then move OpenAI behind `internal/provider/openai`. Everything else unblocks from
there.
