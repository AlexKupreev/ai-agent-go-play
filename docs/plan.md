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

### 3b — Live broker/sandbox wiring (activates Phase 2 in the run flow)

- [ ] In `cmd/run.go`, per run, construct `JSONLRecorder` → `Broker(rec, toolCaller)` →
    `LuaGlue(broker)` → `Registry`; inject into the executor.
- [ ] Set `broker.Trusted` = the built-in names; `broker.Exposed` = `{web_search, web_fetch}`
    only. `toolCaller` resolves a name → built-in `Run` or registry script-exec.
- *Acceptance:* `run_code` (and soon authored tools) execute through the real broker with a real
    audit trail; the `call_tool` allowlist primitive becomes load-bearing.

### 3c — `author_tool` meta-tool (the pipeline)

A built-in `tools.Tool` whose `Run` performs, host-side (not model-controllable):

- [ ] 1. **Validate** — `name` regex, `input_schema` is an object, `code` parses (LuaGlue
    parse-only). Syntax errors return to the model to retry.
- [ ] 2. **Approve** — caps beyond the run's tier → `ConfirmFunc`; declined → reject.
- [ ] 3. **Smoke-test** — run the mandatory `test` in the sandbox under a grant of **exactly the
    approved caps**; the assertion must hold, else reject.
- [ ] 4. **Register** at scope (assigns version/seq); appears in the next iteration's tool defs
    automatically (3a's append-only list).
- [ ] 5. **Audit** `ToolAuthored{code_hash, caps, scope}` (event type already defined).
- *Acceptance:* the agent hits a gap, authors a tool, passes its test, and calls it in the same
    run; parse-fail and test-fail both reject; cap-beyond-tier prompts; the lifecycle is in the log.

### 3d — Tool-search

- [ ] `Registry.Search(query, k)` — BM25-lite over name+description (regex/token overlap first;
    embeddings deferred). Executor includes built-ins always; for registered tools, include all
    while the catalog is small (≤ ~12), else top-k by search against the refined task.
- *Acceptance:* a large catalog does not flood the context window; relevant tools still surface.

### 3e — Lifecycle polish

- [ ] `revoke` surfaced (CLI subcommand or authored path); revoked tools drop from the live set.
- [ ] Dedup by code hash; ephemeral scope already dies with the run.

### Cross-phase test (no live API)

- [ ] A fake provider that emits an `author_tool` call then a call to the new tool drives the
    whole pipeline end-to-end in a unit test.

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

**Tasks**

- [ ] `internal/api`: a headless transport (HTTP/SSE or JSON-RPC) exposing run/step/stream,
    tool list/search, and the approval queue. CLI becomes one client of this.
- [ ] Long-term memory the agent maintains (notes/store), surfaced as a built-in.
- [ ] Management plane: approve/deny escalations, review/revoke tools, browse the audit log.
- [ ] Web + Telegram frontends as thin clients; design the approval UX so escalation prompts
    don't become nagging.

**Acceptance**

- The same run can be driven from CLI and from a second frontend against one engine.
- Escalation approvals surface in the chosen frontend and are recorded.

**Risks/notes:** keep frontends thin — all policy lives in the engine. Approval UX is an open
question (design §9); start minimal.

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
- **Config:** keep secrets in `~/.config/ai-agent/`; add provider selection when the second
  adapter lands.
- **Audit-first:** once Phase 2 exists, every effectful path writes to the log — treat a missing
  audit record as a bug.
- **Don't speculatively build** Phase 5 items, multi-tenant isolation, or hard memory caps —
  they are non-goals for this deployment (design §1, §5).

---

## Immediate next step

**Phase 0, task 1:** create `internal/provider` with the neutral types and the `Provider`
interface, then move OpenAI behind `internal/provider/openai`. Everything else unblocks from
there.
</content>
