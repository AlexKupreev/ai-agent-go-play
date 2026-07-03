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
- **First frontend (4e):** ~~Telegram vs web.~~ **DECIDED: Telegram** (thin, good for the
    unattended/mobile approval case; web is more work for the same payoff first). Built in 4e-6 (bot
    logic behind a `Transport` interface; live transport via the `go-telegram-bot-api` SDK) — see
    `internal/frontend/telegram`.
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

**4c, 4d, and 4e-1…4e-6 are complete — Phase 4e is done.** The live Telegram transport
(`telegram.NewHTTPTransport`, `go-telegram-bot-api` SDK) is now implemented too. Frontend fork
**resolved: Telegram.** Earlier note (now satisfied): implement `telegram.NewHTTPTransport` (Bot
API long-poll + send) when a bot token is in hand, then the remaining housekeeping (commit timestamps,
push, markdownlint). *(4e-2, per-user data ownership, was dropped — the engine stays single-user; see
the multi-user decision above.)*

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

#### 4e-3 — Tool review / revoke over the API  *(DONE)*

- [x] `DELETE /tools/{name}` → `Registry.Revoke` (404 if absent), emitting a new `tool_revoked` audit
    event (closes the revoke-audit gap noted in `tools.md`). `GET /tools/{name}` detail that *includes*
    the impl source + smoke test (`ToolDetailView`, omitted from the listing). `Client.ToolDetail` /
    `Client.RevokeTool`; `agent tool revoke --addr` routes to a running engine (default still edits the
    local catalog file directly).
- [x] `NewServer` gained an `audit.Recorder` param (nil-able) for management-plane effects; `serve`
    opens **one process-wide** `audit.jsonl` (`~/.config/ai-agent/audit.jsonl`) for it — the first
    process-wide recorder, which 4e-4 generalizes to all runs and adds a browse endpoint over.
- *Acceptance:* a tool revoked over the API drops from `List`/the live set and the revoke is audited ✅.
    Tests: `TestHTTP_RevokeTool`, `TestHTTP_ToolDetail`, `TestClient_RevokeTool`.

#### 4e-4 — Central audit log + browse over the API  *(DONE)*

- [x] `audit.Reader` (`Tail(n, Filter{Run,Type})`, oldest-first, n≤0 ⇒ all) implemented by both
    `MemoryRecorder` (in-memory) and `JSONLRecorder` (re-reads its file; `path` tracked), sharing
    `tailMatching` over an `audit.Filter`. `audit.Recorders` fans one event to several sinks.
- [x] `serve` now shares the **process-wide** `~/.config/ai-agent/audit.jsonl` across runs: each run's
    events fan out (`audit.Recorders{sessionRec, central}`) to both the session transcript **and** the
    central log (`newServeRunner` gained a `central` param). The 4e-3 process-wide recorder is now the
    read side of `GET /audit` too.
- [x] `GET /audit?run=&type=&limit=` (`internal/api/audit.go`, empty ⇒ [] not null); `Client.Audit`;
    `agent audit --addr` (`cmd/audit.go`). `NewServer` gained a nil-able `audit.Reader` param. This makes
    the audit log the single review surface for everything effectful (capability use, `tool_authored`,
    `tool_revoked`, `memory_write`).
- *Acceptance:* effects from a run are browsable over the API ✅ (live-verified end-to-end: revoke →
    central log → `/audit` + `agent audit`). Tests: `audit_test.go` (`Tail`/`Filter`/`Recorders`, both
    backings), `TestHTTP_Audit`, `TestHTTP_AuditAbsentWithoutReader`, `TestClient_Audit`.
- *Note:* run metadata + central audit is the likely **SQLite tipping point** (design §9) — swap the
    JSON/JSONL backings behind their existing interfaces when it bites, not before. `JSONLRecorder.Tail`
    re-reads the whole file per call, which is the natural pressure point for that swap.

#### 4e-5 — Approval events on the stream (poll → push)  *(DONE)*

- [x] `ApprovalQueue` gained an emitter hook (`SetEmitter(func(runID string, ev Event))`, wired in
    `serve` to `Engine.PublishToRun`). `Approve` emits `approval_requested` when it parks; the *run's own
    goroutine* emits `approval_resolved` the instant it receives the decision (ordered ahead of the
    terminal `done`, so it can't be dropped by the hub closing). `Engine.PublishToRun(runID, ev)`
    broadcasts into a run's hub (+ replay history), no-op on unknown/closed. New event kinds +
    `Event.ApprovalID`/`Approved` (requested reuses `Tool`=category, `Text`=title, `Input`=detail). The
    existing `POST /approvals/{id}` still resolves.
- [x] **Unified the run id** so routing works: `Runner.Run` now takes the engine's `runID`, threaded
    into the executor (and `logger.NewWithID`) — so the session dir, event stream, audit `Run`, and
    approval `RunID` all key off one id (previously the executor minted its own via `logger.New`, which
    would have mis-routed the push). CLI `printEvent` renders the two new kinds.
- *Acceptance:* a parked escalation appears as a stream event on its run; resolving it emits the
    resolution event ✅. Test: `TestApprovalEmitter_PushesOntoRunStream` (real `Engine` + emitter,
    race-clean).

#### 4e-6 — Telegram frontend as a peer client *(resolves the frontend fork — Telegram)*  *(DONE; live transport added)*

- [x] `internal/frontend/telegram`: `Bot` drives a `Client` (the slice of `api.Client` it needs — a peer,
    like `agent client`, no special access). A message starts a run and streams its events back to the
    chat; a parked approval (the 4e-5 `approval_requested` stream event) becomes an **Approve / Deny
    inline keyboard** whose callback is wired to `Client.Resolve`. No chat↔run map needed — the callback
    data carries the approval id, and each run's stream goroutine captures its chat.
- [x] **Transport behind an interface** so the frontend is testable with no live bot: `Transport`
    (Updates/Send/Answer) + `Update`/`Message`/`Callback`/`Button`, exercised by `telegram_test.go`
    (race-clean: full approval loop, auth rejection for message + callback, callback parsing). The **live
    transport** (`NewHTTPTransport`) is now implemented with the `go-telegram-bot-api/v5` SDK (long-poll
    `getUpdates`; `sendMessage` + inline keyboard; `answerCallbackQuery`).
- [x] **Optional + activated by token:** `serve` starts the bot (in a goroutine, so the Bot API
    handshake never delays listening) only when a token is set (config `telegram_token` / env
    `AI_AGENT_TELEGRAM_TOKEN`); no token, or a rejected/unreachable token, ⇒ engine runs unchanged
    (logs and continues).
- [x] **Auth lives in the frontend:** allowlist of Telegram user ids (config `telegram_allowed_users` /
    env `AI_AGENT_TELEGRAM_ALLOWED_USERS`), **fail-closed** (empty ⇒ reject everyone). Engine stays bound
    to `127.0.0.1`; only the bot faces the network. (No engine-level auth on the localhost single-user
    box — design §1.)
- *Acceptance (Phase 4):* a family member drives a session from Telegram against one engine; an
    escalation surfaces in the chat and is recorded; a run can be cancelled mid-flight from the
    CLI/API/bot. **Met:** logic covered by the fake-transport e2e test, and the live transport is wired
    (needs a real bot token + egress to exercise against Telegram itself).

**Risks/notes:** keep frontends thin — *all* policy (approval, audit) lives in the engine; the bot must
contain zero policy, only rendering + relaying. Cancellation is cooperative (stops at the next
boundary), fine given `maxIterations` bounds runs. Approval UX (design §9 open question): the tier dial
already suppresses routine prompts; 4e-5 makes escalations ambient rather than polled; keep them
batched/inline, never a nagging loop. The async `Approver` (4a) is the contract the queue (4c) and this
management plane build on.

---

## Phase 4f — Interactive chat + persistent conversations (sessions)  *(DONE)*

Added after 4e when the deployment story surfaced two needs: a Claude-Code-style REPL, and
cross-device conversation continuity (start on SSH, continue on Telegram).

- **Continuable executor.** `*agent.Agent` now retains its conversation across `Run` calls
  (`a.messages` = the conversation *without* the system prompt, which is prepended fresh each request
  so prompt edits apply on resume). `Restore`/`Messages`/`Reset` expose it. Single-shot callers (`run`,
  the stateless `/runs` path) are unaffected — they build a fresh agent per task.
- **Local REPL:** `agent chat` (`cmd/chat.go`) — multi-turn, `/reset`/`/exit`, Ctrl-C cancels a turn,
  `--plan` toggles a per-turn planner (default off; experimental, configurable per the "don't know
  what's better" call).
- **Sessions over the API (disk-backed).** `internal/session` (`Store` + `FileStore`, one JSON file per
  session under `<config-dir>/sessions/`, SQLite later). **A turn is a run whose executor is seeded with
  the session's history**, so the entire run/hub/SSE/approval/audit machinery is reused. Engine:
  `EnableSessions` + `StartSession`/`ListSessions`/`CloseSession`/`PostTurn` (per-session mutex; shared
  `launch` spine). Endpoints `POST /sessions`, `GET /sessions`, `DELETE /sessions/{id}`,
  `POST /sessions/{id}/turns`. `Client` gained the four peer methods. *Only the message history is
  persisted; the live executor is rebuilt per turn — nothing unserializable is stored.*
- **Telegram is now session-based:** chat→session map, `/new` / `/end` commands, a message is a turn.
- **`agent chat --addr` — DONE.** The REPL now has a remote mode: with `--addr` it drives a running
  engine's persistent session (`StartSession`/`PostTurn`/`StreamEvents`/`CloseSession`) instead of an
  in-process executor, so the conversation is server-side and resumable (`--list`, `--session <id>`) —
  the SSH→Telegram continuity payoff. `/reset` starts a fresh session, `/end` closes, `/exit` detaches.
  Ctrl-C cancels the current turn (stops the remote run). Approval prompts reuse the engine's shared
  queue via a stdin scanner shared with the REPL (no dual-reader race). `cmd/chat_remote.go`.
- **Engine aliases — DONE.** `--addr` now accepts a `host:port` **or** an alias saved with
  `agent config set-engine <alias> <host:port>` (`rm-engine`/`engines` to manage; stored as
  `Config.Engines`). `resolveAddr` resolves aliases and passes literals through; wired into every
  engine-facing command (`chat`/`client`/`stop`/`audit`/`tool revoke`). Tests: `TestResolveAddr`,
  `TestAttachSession`, `TestListRemoteSessions_Empty`. Live-verified end-to-end (alias → new session →
  list → resume → `/end`).
- *Deferred:* context-window trimming for long sessions.

---

## Phase 5 — Reserve tier (only if isolation needs grow)

Not planned work; pull from here only when a concrete need appears.

- [ ] `internal/sandbox/wasm` (wazero): hard memory-capped, capability-gated escalation tier —
    if a tool ever needs stronger isolation than `luaglue` gives.
- [ ] Own web-search + analytics primitives — if a second provider or stronger isolation forces
    leaving vendor-native search behind.
- [ ] Anthropic (or other) provider adapter — when actually swapping/AB-testing models.

---

## Phase 6 — Self-awareness (token accounting, self-documentation, introspection)

**Goal:** the agent knows things about *itself* — what it's spending, what it can do, and
what it has done — so it can report accurately, reason under limits, and answer questions
about its own operation instead of guessing. Grew out of a 2026-07-02 discussion.

**Current state (the seams to build on):**

- `provider.Usage` (input/output/cached tokens) is captured per step and flows into
  `EvResponse` events + the run transcript (`internal/agent/observer.go`), but is **not**
  aggregated, surfaced, audited, or fed back to the model.
- The agent has **no** access to its own docs and **no** self-status tool; asked about its
  tier/config/capabilities it hallucinates.

### 6a — Token accounting  *(DONE)*

**Tokens only; cost deliberately out of scope.** A price table goes stale — add it later
behind a config `model_prices` map, computing from the same totals. `provider.Usage` already
carries input/output/**cached** tokens, so the cached-discount data is there if cost is added.

- [x] **Aggregate** per run/turn: `agent.UsageObserver` (`internal/agent/observer.go`) sums
    input/output/cached `Usage` and counts steps from `EvResponse` events. The API `Engine`
    fans one alongside the run's hub in `launch`, so every Runner/TurnRunner is covered; the
    CLI adds one to its observer list. (Chat gets the per-turn delta by snapshotting the
    session-wide accumulator; a turn is a run, so session turns aggregate too.)
- [x] **Surface** through the three existing seams (no new transport):
    - CLI/chat end-of-turn stderr line via `cmd/usage.go` `formatUsage`, e.g.
      `· 12,431 in / 3,210 out (1,024 cached) · 4 steps · 6.2s`. `agent client` and
      `agent chat --addr` print the same line from `RunStatus`.
    - `api.RunInfo` gained `Usage` + `Steps`, set when the run ends → `GET /runs/{id}`,
      `ListRuns`, `agent client`.
    - `run_usage` audit event per completed run/turn (`audit.EventRunUsage`), recorded by the
      engine via `SetAuditRecorder` (wired in `serve` to the process-wide log) → `GET /audit`,
      `agent audit --type run_usage`. *(A distinct `turn_usage` / session-cumulative event was
      not added — a turn is already a run, so it emits `run_usage`; session-cumulative can come
      with 6c/6d.)*
- [x] *Acceptance:* met. `TestUsageObserver_Accumulates`, `TestRunUsage_AggregatedIntoInfoAndAudit`
    (RunInfo totals + `run_usage` event equal the summed per-step usage), `cmd` formatting tests.
    Live-verified over `serve`: `GET /runs/{id}` carries `usage`, `GET /audit?type=run_usage`
    lists the event.

### 6b — Self-documentation (the agent can read its own docs)  *(DONE)*

**Corpus decision (2026-07-02):** embed **reference docs + the vision doc**, not planning docs.
Rather than exclude-by-omission, the risk being managed is the agent mistaking *planned* for
*implemented*; the vision doc is included (tagged) so it can align tool-authoring with the
intended philosophy. Planning/scratchpad docs (`plan.md`, `resume.md`) were **moved to
`docs/planning/`** so a flat `docs/*.md` embed glob excludes them structurally (and separates
planning from reference docs). Kinds: `reference` (authoritative about current behavior) and
`vision` (design intent, may include not-yet-built ideas).

- [x] `go:embed README.md docs/*.md self-extending-agent-design.md` in `main.go` (the flat
    glob does **not** descend into `docs/planning/`); passed to `cmd` via `SetSelfDocs`, then
    into every executor. Available regardless of cwd / on Fly.io.
- [x] `internal/selfdocs`: `Docs` reads the embedded FS, deriving a topic per file, a `Kind`
    (reference/vision), and a title from the first heading; `List`/`Get`/`Search` (token
    overlap), a `vision` alias for the long filename. `internal/tools/selfdocs.go`
    `read_self_docs` built-in (topic → body, query → ranked list, none → listing tagged by
    kind). Trusted, read-only, **not** sandbox-exposed. Omitted when no doc set is wired.
- [x] System-prompt note (`selfDocsPromptNote`) appended when docs are present: consult
    `read_self_docs` for self-questions; `[reference]` is current truth, `[vision]` is not-yet.
- [x] *Acceptance:* met. `internal/selfdocs` + `internal/tools` unit tests, and
    `internal/agent/selfdocs_e2e_test.go` (a scripted run reads a doc and the body reaches the
    model; the tool is omitted when nil). Live-verified the embed set by grepping the binary
    (reference + vision present; `plan.md`/`resume.md` bodies absent).

### 6c — Introspection tools (expose 6a/6b + live state to the model)  *(DONE)*

- [x] **`status` tool — DONE.** Reports identity (model, tier, run id, build version), counts
    (#authored tools, #memory entries), **and host resources** (CPU count + load, RAM free/total,
    disk free/total, process RSS, Go heap + goroutines, host uptime). `internal/hoststat`
    (`Read(path)` — best-effort via Linux `/proc` + `runtime` + `syscall.Statfs`, zero fields
    where unavailable), `internal/buildinfo` (`Version`, `-ldflags`-overridable),
    `internal/tools/status.go` (`NewStatusTool`). Wired unconditionally in `NewExecutor` from
    existing params (no signature change); read-only, trusted, not sandbox-exposed. Host status
    is a distinct self-awareness axis from `read_self_docs` (live state vs docs); apt for the
    low-resource-box target (design §11). *Not a new capability* — the agent can already `shell`
    out to `df`/`free`; this is the structured, reliable convenience. Tests:
    `internal/hoststat`, `internal/tools/status_test.go`, `internal/agent/status_e2e_test.go`
    (offered + returns a live report). Live-verified on this box (8 cores, load, RAM/disk, RSS,
    uptime). *(config-dir dropped from v1 — a cmd concept the executor doesn't hold; add via a
    param if a real need appears.)*
- [x] **`usage` tool — DONE.** Reports token spend **this session** (across turns) and **today**
    (across all runs). **Design decision (2026-07-02): derive from the audit log, not live
    accumulators** — every run/turn already emits `run_usage`, so session/day totals are sums over
    those persisted events: restart-safe, cross-session, no accumulator state to keep in sync (only
    the in-flight run isn't counted until it ends). `internal/usage` (`Ledger.Session`/`Today` over
    an `audit.Reader`; `Record` — the single writer of the event shape, used by both engine and
    CLI); `internal/tools/usage.go` (`NewUsageTool`, `UsageContext{SessionID, Ledger}`). Enabling
    change: `run_usage` is now **tagged with the session id** (threaded `sessionID` through
    `Engine.launch` + `TurnRunner.RunTurn`). `agent run` now also appends `run_usage` to the
    process-wide log so **today** includes CLI runs. **Human surface:** `agent usage` /
    `agent usage --session <id>`, and a `today:` line after `agent run`. Tests
    (`internal/usage`, `internal/tools`, agent wiring e2e) + live-verified (session-tagged events,
    `agent usage` today/session counts). *(Local `agent chat` keeps its in-process per-turn/session
    line; the model-facing tool is wired on `serve`/`run` where the ledger is.)*
- [x] **`recent_activity` tool — DONE.** The agent reviews its own recorded activity from the
    audit log (capabilities used, tools authored/revoked, memory saved, token usage), filterable
    by `type`/`run` — "what have I done?". Over an `audit.Reader` (`NewExecutor` gained an
    `auditReader` param): serve passes the process-wide log (cross-run); `run`/`chat` pass their
    per-run recorder. Omitted when nil. `internal/tools/introspect.go`.
- [x] **`tool_catalog` tool — DONE.** Lists the agent's authored tools with capabilities + scope
    (optionally searched) so it reuses an existing tool rather than re-authoring a duplicate. Built
    unconditionally from the registry `NewExecutor` already has. `internal/tools/introspect.go`.
- All read-only, trusted, not sandbox-exposed. Tests: `internal/tools/introspect_test.go` +
    `internal/agent/introspect_e2e_test.go` (catalog always offered; recent_activity gated on a
    reader).

### 6d — Budget + context-window awareness (later)

- [ ] Token **budget** per run/session: soft warning fed into context at ~80%, optional hard
    stop — a dial alongside the trust tier. Builds on 6a's totals.
- [ ] Context-window awareness: know the model's context limit + current fill so the agent
    can summarize under pressure. This is the deferred **context-window trimming** item
    (Phase 4f), upgraded from blind truncation to the agent noticing and acting.

**Risks/notes:** keep the model-facing self-tools read-only and un-sandboxed (introspection,
not effect). Token totals are cheap and always-on; cost/budget are opt-in extras. Don't let a
system-prompt self-summary drift from the docs — generate it or keep it a pointer.

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

**The experimentation track A–F is DONE.** Prompts + workspace (A–C), sub-agent types + the live
foreground `spawn_agent` tool (D + E), and the experimentation loop (F: `/reload` hot-reload +
`agent eval` compare harness) all shipped — model-driven delegation works, new organizations can be
tried by editing agent-type files, and prompt/model/organization variants can be measured side by side.

**Next candidates** (pick per priority): **Stage G — docs consolidation** (surface workspace vs
config-dir + the new `reload`/`eval` commands in `usage.md`/`README.md`, and the deferred
`docs/environment.md`); the **UX & plumbing** cluster below (verbosity default, transcript location,
unified human-in-the-loop); or pulling in **Phase 6d** (token budget dial + context-window awareness).

**Phase 6d is deferred** — budget + context-window awareness (soft warning at ~80%, optional hard
stop, context-window trimming) is a self-contained dial that reads 6a/`usage` totals; pull it in
after the experimentation track (A–F), not before. See the *Deferred* note in the sequenced backlog.

*Note:* `NewExecutor` now takes an `ExecutorConfig` struct (done — was 13 positional args across
~16 callers). Add A's prompt fields, 6d's budget dep, and any future dep as a **field**, not a
positional param.

*(Phases 0–4f and 6a–6c are complete; the historical "start at Phase 0" note that once lived here
is superseded.)*

---

## Beyond the phased plan

- **Sub-agents** (one agent delegating to others — declarative agent *types* + a `spawn_agent` tool,
  after the pi/Claude-Code model): design and roadmap in [`subagents.md`](subagents.md). Decision
  reached: **in-engine child agents** are the default (foreground-sequential is safe for any type;
  a **parallel** batch path is gated to read-only types), with **cross-engine delegation** (separate
  `serve` per agent, via an `Isolation` field) in reserve for standing specialists / distinct trust
  tiers. Not multi-tenant isolation — that remains a non-goal. Nothing built yet; the near-term
  buildable slice is **parallel read-only executors** (§4 of that doc).

- **Prompt composition** (operator/project customization of the system prompt, after pi's
  `SYSTEM.md`/`AGENTS.md`): design in [`prompts.md`](prompts.md). One `composeSystemPrompt` seam feeds
  three features — the base prompt, config-dir+workspace `AGENTS.md`/`SYSTEM.md` (two-tier, project
  overrides global), and per-agent-type prompts (`PromptMode`). Read once at construction to preserve
  prompt caching.

- **Workspace** (the project the agent acts on, vs the config-dir = the agent's identity): concept in
  [`workspace.md`](workspace.md). Two-tier, pi-compatible; today only a bare `workDir` (shell cwd)
  exists. CLI resolves the workspace as cwd + parent walk; `serve` uses the process cwd in v1 with a
  per-run `workspace` field as the designed-for extension. Workspace prompt files are **tier-gated**
  (an untrusted checkout can't inject into a `safe` agent). First consumer is prompt composition.

### Sequenced backlog

The three docs above each carry a scoped task list; this is the **build order across them**, with
dependencies. Two roughly independent tracks — **prompts + workspace** (A–C) and **sub-agents** (D–F)
— joined only where `PromptMode` reuses the prompt seam. The `ExecutorConfig` struct refactor (the
unblocker for every field addition below) is **done**.

*Ordering — experimentation-first (chosen).* This sequence optimizes for **experimentation surface**:
land the foreground `spawn_agent` tool (E) and the iterate/measure loop (F) early, so new subagent
*organizations* (critic, debate, refine loops, hierarchies) and prompt variants can be tried by editing
files — **no Go per topology**. The alternative — *ship-first* — would build parallel research fan-out
first (one impressive feature) but bakes a single topology into Go and back-loads the flexibility. We
**deferred fan-out** instead (see below): it's a latency optimization and a special case of a parallel
`spawn_agents` batch, so it costs nothing to postpone.

**A — Prompt composition core** (config-dir / global tier) · *no deps* · [`prompts.md`](prompts.md) §0–§2  *(DONE)*
- [x] `composeSystemPrompt` helper (`internal/agent/agent.go`, pure: base + optional override + labelled
    appends); called once in `NewExecutor`; `ExecutorConfig` += `SystemPromptOverride`, `PromptAppends`.
    A `SYSTEM.md` override replaces the base; the self-docs note re-attaches after it; `AGENTS.md` bodies
    append last. Folded in at construction, so the cached prefix stays stable.
- [x] `cmd/prompts.go`: `loadConfigDirPrompts` reads `<config-dir>/SYSTEM.md` + `AGENTS.md` (alias
    `CLAUDE.md`, AGENTS.md preferred when both present); persistent `--no-context-files` flag short-circuits
    to the bare base. Wired into `run`, `chat`, and `serve` (read once at startup, shared across runs).
- [x] tests: `composeSystemPrompt` table (override replaces, appends in order, blanks skipped);
    `NewExecutor` sends the composed prompt (override + ordered appends) and the bare base when no files;
    `cmd` file-loading (alias precedence, missing = no-op, `--no-context-files` gate).
- *Ships:* operator-level global prompt customization. *(Workspace tier is B/C.)*

**B — Workspace concept** · *no deps* · [`workspace.md`](workspace.md) §2  *(DONE)*
- [x] `resolveWorkspace()` in `cmd/workspace.go` — persistent `--workspace` flag (validated dir, made
    absolute) > process cwd; wired into `run`, `chat`, and `serve` (replaces the raw `os.Getwd()`).
    **No parent walk yet:** the upward walk collects project *files* (stage C) and its stop bound is an
    open question (`workspace.md` §6), so the resolver returns the cwd/override itself.
- [x] the resolved workspace threads into the shell tool's `workDir` via `ExecutorConfig.WorkDir`.
- [x] tests: `cmd/workspace_test.go` (cwd default, flag override made absolute, missing dir / a file → error).
- **`--context-file` deferred to C:** it's the tier-gate escape hatch for *prompt-file loading* (§5), which
  stage C builds — adding the flag now would be a no-op, so it lands with the code that honors it.
- *Ships:* first-class, overridable workspace; generalizes today's bare `workDir`.

**C — Workspace prompt tier** · *deps A + B* · [`prompts.md`](prompts.md) §2, [`workspace.md`](workspace.md) §5  *(DONE)*
- [x] `loadConfigDirPrompts` → `loadPrompts(workspace, tier)` in `cmd/prompts.go`: loads the config-dir
    (global) tier, then the workspace (project) tier, merged project > global — a workspace `SYSTEM.md`
    wins outright over a config-dir one, `AGENTS.md` bodies concatenate global-then-project. Rewired into
    `run`/`chat`/`serve` (each already resolved `workDir` + `tier`). **Single resolved workspace dir, no
    parent walk yet** (its stop bound is still open, `workspace.md` §6).
- [x] tier gate (`loadWorkspaceTier`): `safe` does not auto-load workspace files, but an explicit
    `--workspace` authorizes them; a workspace that resolves to the config dir isn't loaded twice.
- [x] `--context-file` flag (persistent, repeatable): extra prompt file(s) appended last, always honored
    regardless of tier; a missing named file is an error (unlike absent tier files, which are a no-op).
- [x] tests: project>global precedence, `SYSTEM.md` project-wins, safe-tier gate (+ explicit override),
    workspace==config-dir dedup, `--context-file` append + missing-file error, `--no-context-files` gate.
- *Ships:* pi-compatible project `AGENTS.md` / `SYSTEM.md` + explicit `--context-file`.

**D — Sub-agent types** · *`PromptMode` deps A* · [`subagents.md`](subagents.md) §2–§3  *(DONE — `internal/agent/agenttype.go`, `agenttype_test.go`)*
- [x] `agenttype.go`: `AgentType`, `AgentCatalog` (register/get/list, re-register overrides in place), built-in `researcher` + `general-purpose` (`scout` deferred until a read-only file/shell tool exists — see the Stage D note in `subagents.md` §2), `Parallel ⇒ read-only` validation at load (inherit-all rejected for parallel — may include writers). *(`agents/*.md` YAML-frontmatter loading → E, with its cmd wiring; use a real YAML parser for pi compatibility.)*
- [x] `newSubAgent(parent, AgentType, obs)` factory + `selectSubAgentTools` (tool subset from `a.byName`/`a.tools`); `readOnlyBuiltins` set marks read-only built-ins. Inherited tools (`["*"]`) are a subset of the parent's built-ins minus a denylist (`subAgentExcluded` = `spawn_agent`); empty ⇒ read-only default; child gets no `responseFormat`, no registry/sandbox in v1
- [x] `AgentType.PromptMode` (`replace`|`append`) via the compose seam (A) — `append` inherits the parent's config-dir/workspace `AGENTS.md` (baked into `parent.systemPrompt`), `replace` specialists don't
- [x] *Acceptance:* `agenttype_test.go` — validate matrix (parallel-write reject, inherit-all reject, prompt-mode), built-in catalog + register-override, the three `selectSubAgentTools` branches, and `newSubAgent` replace/append prompt + model inherit + no-responseFormat/registry. `go test -race` clean.
- *Ships:* declarative agent types + foreground sub-agent construction. (No `ExecutorConfig`/cmd wiring yet — that lands with `spawn_agent` in E.)

**E — Foreground `spawn_agent` tool** (the experimentation unlock) · *deps D; `PromptMode` from A* · [`subagents.md`](subagents.md) §3  *(DONE — `cmd/agents.go`, `internal/agent/agenttype.go`, `spawn_test.go`, `agents_test.go`)*
- [x] `agents/*.md` YAML-frontmatter loader (`cmd/agents.go`: `loadAgentCatalog`/`parseAgentFile`/`splitFrontmatter`; real parser — `go.yaml.in/yaml/v3` now a **direct** dep) + cmd resolution of global (`<config-dir>/agents/`) then project (`<workspace>/agents/`) dirs, project>global>built-in override, tier-gated like the prompt tier (safe won't auto-load a checkout's agents without `--workspace`; `--no-context-files` ⇒ built-ins only). Wired into `run`/`chat`/`serve`; `ExecutorConfig.AgentCatalog` (nil ⇒ `spawn_agent` omitted).
- [x] trusted host built-in `spawn_agent(type, task)` (`newSpawnAgentTool` in the agent pkg, so no `tools→agent` import cycle) → builds a child via `newSubAgent` (D), runs it foreground to a final answer, returns the text; **no concurrency**. In `a.byName` ⇒ broker-Trusted but never Exposed, so sandboxed `call_tool` can't start a sub-run. Description enumerates the available types.
- [x] spawn-depth budget (`ExecutorConfig.SpawnDepth`, `Agent.spawnDepth`; cmd default `defaultSpawnDepth = 1`): `spawn_agent` refuses at ≤0, `newSubAgent` hands the child `depth-1`. In v1 children carry no spawn tool, so it only bites the coordinator, but the budget threads down for the nested case.
- [x] child events labelled: `Event.SubAgent` + `labelSubAgent` observer wrapper; `CLIObserver` indents/labels a sub-run's lines (`  ↳ <type>`).
- [x] *Acceptance:* `spawn_test.go` — foreground delegation (child runs as its own step between coordinator steps, result threads back), type-restricted child (`researcher` sees only `web_search`/`web_fetch`, never `shell`/`spawn_agent`/`author_tool`), depth-guard refusal, unknown-type recovery, omitted-without-catalog; `agents_test.go` — frontmatter/tools parsing, global+override, project>global + safe-tier gate, `--no-context-files`, invalid-file hard error. `go test -race` clean; live-verified via `serve` (valid file binds, invalid parallel+write fails fast).
- *Ships:* model-driven delegation — try new subagent **organizations** (critic, debate, refine, hierarchy) via prompts + agent-type files, no Go per topology.

**F — Experimentation loop** (ergonomics that make E worth it) · *deps A, E*  *(DONE — `cmd/reload.go`, `cmd/eval.go`, `reload_test.go`, `eval_test.go`)*
- [x] prompt / agent-type **hot-reload** so file edits skip a restart: `chat` grows `/reload` (rebuilds the executor via a `buildExecutor` closure, carrying the conversation forward with `Messages`/`Restore`; a malformed file keeps the current executor). `serve` holds prompts+catalog behind a reloadable, lock-guarded `promptState` (each run snapshots the current values; a concurrent reload can't change a running executor's prompt) with a `POST /reload` route mounted on a thin outer mux wrapping the api server (file-path/tier logic stays in `cmd`, api package stays transport-agnostic). `Client.Reload` + `agent reload --addr` as the remote counterpart.
- [x] a tiny **eval / compare harness**: `agent eval <task>` runs the task under N variants from a YAML file (`--variants`) and/or a quick model sweep (`--models`); each variant overrides ambient defaults (model/tier/workspace/context_files/no_context_files), builds a fresh executor (no planner, shared catalog+memory), and the report is a tabwriter table (variant, effective model, steps, tokens, duration, status) + each variant's full output. Per-variant errors are captured so the rest still report; Ctrl+C stops after the current variant.
- *Ships:* a tight edit → run → measure loop for prompt and organization experiments.

**G — Docs consolidation** · *deps all; ship time* · [`workspace.md`](workspace.md) §7, [`prompts.md`](prompts.md) §5  *(DONE — `docs/environment.md`, edits to `usage.md`/`README.md`/`design.md`/`tools.md`)*
- [x] `design.md` / `tools.md`: workspace vs config-dir (reference) — `design.md` §1 "Two anchors — identity vs target"; `tools.md` notes the catalog is config-dir-scoped (vs sub-agent types, read from both tiers). Both link to `environment.md`.
- [x] new `docs/environment.md`: consolidates config-dir vs workspace, trust tier, prompt customization (SYSTEM.md/AGENTS.md), sub-agent types (agents/*.md), and the config/env + files-on-disk reference tables. `usage.md`'s config/env + files sections shrink to a pointer; it gains operational sections for prompt/agent-type customization, hot-reload, and `agent eval`. `README.md` links `environment.md` and surfaces the new commands. `environment.md` is auto-embedded (`//go:embed docs/*.md`), so `read_self_docs` includes it.

*Deferred (designed, unscheduled):* **parallel read-only executors / fan-out** — `FanOutResearch`,
`workerObs`, `Event.Worker`, `RunResearchTurn`, `cmd/chat.go --research`, `golang.org/x/sync`
([`subagents.md`](subagents.md) §4); a latency optimization and a special case of a parallel
`spawn_agents` batch tool (§5). Also cross-engine `Isolation: "engine"` delegation (§7), the `serve`
per-run `workspace` field, and `Phase 6d` (budget dial), postponed earlier.

### UX & plumbing (current code; independent of stages A–G)

Small, self-contained items surfaced in review — not blocked on the sub-agent / prompt work.

- [ ] **Verbosity setting.** The intermediate trace is the `CLIObserver` (`internal/agent/observer.go`);
  quiet mode simply doesn't attach it — `ask_user` prompts and the final answer bypass the observer, so
  they still show. Manage like `model`/`tier`: a `Config.Verbose` field + `config set-verbose` +
  `--verbose`/`--quiet` flag (precedence flag > env > config > default), plus a live `/verbose` toggle in
  `chat` via a verbosity-gated observer wrapper. **`chat` becomes quiet by default** (matching `run`);
  `--verbose` or `/verbose on` restores the trace. The full transcript is unaffected — always on disk.
- [ ] **Transcript location = share-nothing.** The run transcript (`run.jsonl`) defaults to the shared
  data dir (`~/.local/share/ai-agent/sessions/<runID>/`), so agents on different `--config-dir`s
  **co-mingle** transcripts — inconsistent with "separate config dirs share nothing" (and with
  `audit.jsonl`, already under the config-dir). Default the transcript base under the config-dir in a
  **distinct** subfolder (`<config-dir>/runs/`), keeping `--sessions-dir`/env as the override, and keep
  `<config-dir>/sessions/` for the resumable session store (don't overload the name). Fold into
  `environment.md` (stage G).
- [ ] **Unified human-in-the-loop across all clients.** Approvals (`y/N`) and `ask_user` (free text) are
  the same primitive — pause the run, route a prompt to the frontend that owns it, block for the answer —
  but only approvals use the async, queue-backed `Approver` seam (`internal/tools/approval.go`);
  `ask_user` uses raw stdin and is wired to the **planner only**. Unify: generalize `Approver` into one
  human-gate returning a structured response (bool for approve, string for ask), back it with the same
  per-frontend impls (stdin for CLI, queue for serve/API/Telegram), and give the **executor** `ask_user`
  through it. Gain: a running task can ask a clarifying question mid-run over *any* client, not just the
  CLI planner.
