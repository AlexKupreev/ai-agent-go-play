# Project Design / Spec

The concrete, Go-grounded design for **this** repository. It is the implementation companion
to [`../self-extending-agent-design.md`](../self-extending-agent-design.md), which is the
implementation-agnostic vision. Where the vision doc explores options abstractly, this doc
records **what is decided for this project, what exists today, and what to build next.**

- **Vision doc** = the menu and the trade-off analysis.
- **This doc** = the order we placed, grounded in the current Go code.

---

## 1. Deployment & trust model (read this first — it sizes everything else)

- **Small, trusted user base: the author and family.** Not a public, multi-tenant service.
- **Frontends are for convenience, not exposure.** Web / Telegram / CLI are all welcome; they
  are peer clients of one engine. No frontend is "the" frontend.
- **What is *not* trusted:** the **content the agent ingests** (a web page or data file can
  carry a prompt-injection payload) and the **code the agent authors at runtime**. These are
  the only genuinely untrusted elements in the system.
- **Therefore the broker + sandbox exist for blast-radius limiting, auditability, and
  injection defense — proportionate to a private box — not for hostile-user isolation.**
  Multi-tenant escape, per-user auth boundaries, and public-service hardening are **non-goals**.

This is the load-bearing context for §2 and §5: human-invoked / built-in tools are trusted;
**machine-authored** tools are sandboxed and brokered.

---

## 2. Decision: Go, in this repo

Build the self-extending agent in **Go**, evolving the existing prototype here — not a
from-scratch Rust rewrite, and not a language split of this repo.

**Why Go wins for *this* project** (full argument in the vision doc; summary here):

- **The trust model above** removes Rust's biggest edge (hard memory isolation / pure-Rust
  untrusted substrate as a *must-have*). On a private box with trusted users, the capability
  broker and audit log — not memory isolation — are the load-bearing parts, and that discipline
  is language-independent.
- **`gopher-lua` already gives Rhai's safety *and* Luau's familiarity in one option.** It is
  pure Go, so the substrate is memory-safe (no C core, no UB — Rhai's prized property), *and*
  it runs Lua 5.1, which has top-tier LLM training presence (Luau's prized property). The only
  thing it lacks vs Luau is a hard per-instance memory cap — a non-issue here (§5).
- **`wazero` is the escape hatch if isolation ever matters more.** Pure-Go WASM, no CGo: a
  hard-isolated, memory-capped tier available without a C toolchain. Hold it in reserve;
  likely never needed for this deployment.
- **Phase 1 already exists in Go** (kernel loop, a provider, the tool loop, real tools, a
  planner sub-agent). Restarting in Rust discards working code for guarantees we've scoped out.

**When to revisit:** only if this ever opens up to untrusted users / multi-tenancy. Then a
Rust + Wasmtime sandbox can sit as a **sibling module/service** behind the same broker — no
need to split the repo now.

---

## 3. Following pi (pi.dev): blueprint, not artifact

pi is a minimal TypeScript/Node coding-agent harness (headless JSON-RPC/SDK modes, many
providers, self-extension via TypeScript extensions, minimal core). We adopt its **shape**.

| pi principle | Adopt? | How it lands here |
|---|---|---|
| Headless engine + JSON-RPC/SDK; frontends are peer clients | **Yes** | Web/Telegram/CLI all talk to one headless engine; CLI is not special. |
| Provider-agnostic, swappable models | **Yes** | Introduce a `Provider` port; today the loop is hard-wired to OpenAI. |
| Minimal core + extensibility-as-architecture | **Yes** | Self-authored tools (`author_tool`) instead of a fat built-in surface. |
| Primitives over prescriptive workflows | **Yes** | Agent composes capability-gated primitives; few opinionated flows. |
| Shell/files as first-class tools | **Yes — keep** | Shell stays a real built-in capability; we *add* general-purpose tools and frontends, not remove shell. |
| Trusts the human user with the machine | **Yes (for built-ins)** | Trusted users → built-in/human-invoked tools need no sandbox. |
| Extension code runs with **ambient authority** | **No (for authored tools)** | The *agent-authored* tier is sandboxed + brokered for blast-radius + audit, since that code is machine-written and injection-influenceable. |
| Extend in **TypeScript / V8** | **No** | Too heavy for ~2 GB; use **gopher-lua** (glue) + **wazero** (reserve), not V8. |

The one meaningful divergence: **pi has a single trust tier (everything the user runs is
trusted). We have two** — trusted built-ins (incl. shell), and a sandboxed tier for tools the
*agent writes at runtime*. Same minimal-core, agent-builds-capabilities spirit; one extra
boundary around machine-authored code.

---

## 4. Current state (what exists today)

A working ReAct CLI agent. Honest inventory:

| Area | Today | File |
|---|---|---|
| Agent loop | ReAct loop, max 20 iterations, sequential tool calls | `internal/agent/agent.go` |
| Provider | **Hard-wired OpenAI** (`openai-go`), value-type client | `internal/agent/agent.go` |
| Tool interface | Provider-neutral `Tool{Name, Description, Parameters, Run}` | `internal/tools/tools.go` |
| Built-in tools | `shell` (workDir), `web_search` (DuckDuckGo), `web_fetch`, `ask_user` | `internal/tools/*.go` |
| Sub-agent | Planner that refines a task before execution; structured output via JSON-schema | `internal/agent/plan.go`, `agent.go` |
| Frontend | CLI only (cobra): `agent run`, `agent config set-key` | `cmd/*.go` |
| Persistence | None (no session/run log); config key in `~/.config/ai-agent/config.json` | `cmd/config.go` |
| Observability | Stderr logging of requests/tool calls/results | `internal/logger/logger.go` |

Already right (keep): the **kernel/tool split**, the **provider-neutral `Tool` shape**, the
**sub-agent pattern**, and **structured output**. These map cleanly onto the target.

Gaps vs the target:

- **OpenAI coupling.** `agent.go` imports `openai-go` directly; messages, tool defs, and the
  loop all speak OpenAI types. Needs a `Provider` port. *(Highest-leverage first refactor.)*
- **No persistence / headless API.** Needed for web/Telegram frontends and for an audit trail.
- **No capability/sandbox tier yet** — fine *until* `author_tool` exists; required before it.
- **Shell runs the agent's chosen commands directly.** Acceptable for trusted use, but note:
  an injected instruction in fetched content could drive a destructive command. Mitigate with
  the audit log + an approval gate on destructive/irreversible actions (§5) — not by removing
  shell.

---

## 5. Security model (proportionate to a private, trusted deployment)

Two tiers, matching §1:

- **Built-in / human-invoked tools (shell, web, …): trusted.** No sandbox. The user authorized
  the box; these run directly.
- **Agent-authored tools (runtime-written code): sandboxed + brokered.** This is the only code
  we don't trust, because it is machine-written and can be steered by injected content.

For the authored tier:

- **Deny by default + capability-based access.** Authored code has no ambient authority; it can
  only name host functions injected from its granted capabilities. The broker surface *is* the
  boundary for this tier.
- **Human-in-the-loop on *escalation*, not every call.** First use of a new host or a
  destructive/irreversible action → approve; routine reuse → automatic. Applies to the built-in
  `shell` too (destructive command → confirm).
- **Auditability over isolation.** Append-only log of every authored tool, capability
  exercised, and notable hostcall. Tools are revocable; scopes have lifecycles. On a private
  box, *seeing and undoing* what the agent did matters more than hard memory walls.
- **Build-ordering rule (hard):** capability broker + sandbox **before** `author_tool`. A
  self-authoring agent without a broker is an RCE service with an LLM choosing the payloads —
  true even when the *user* is trusted, because the *content* steering it is not.
- **`call_tool` must not be a sandbox-escape.** When the broker's `ToolCaller` is wired to the
  registry, `call_tool` lets authored (sandboxed) code re-enter the tool surface. If that surface
  includes the *trusted* built-ins (`shell`, `web_fetch`), an authored tool with a single
  `call_tool: ["*"]` grant gets ambient authority transitively — the boundary leaks. Rules: (a)
  `call_tool` resolves only registered/authored tools and an explicit built-in allowlist, never
  `shell` by default; (b) a built-in re-entered from the sandbox runs under the *caller's*
  grant/audit context, and its interactive confirm (e.g. destructive `shell`) cannot be the only
  gate, since nested calls have no human at the prompt; (c) `["*"]` tool grants are an escalation,
  not a default. **Mechanism (done):** the broker now classifies tools via `Trusted(name)`
  (ambient-authority built-ins) and `Exposed(name)` (deliberately opened to the sandbox). A
  `Trusted` tool is callable from `call_tool` only when both `Exposed` *and* named directly in the
  grant — a `*` never reaches one. **Phase-3 wiring requirement:** when the registry-backed
  `ToolCaller` is connected, the host MUST set `Trusted` for every built-in reachable through it
  (and `Exposed` only for the few intended), or the classification defaults to "all sandboxed" and
  the protection is moot. **v1 decision:** expose only confirm-free, read-only built-ins
  (`web_search`, `web_fetch`); `shell` stays unexposed. This sidesteps rule (b) (running a
  re-entered built-in under the caller's grant instead of its interactive confirm) — there is no
  interactive-confirm built-in in the exposed set, so it cannot arise until one is exposed.
- **The broker's allowlists must hold across indirection.** An allowlist that checks only the
  first hop is not a boundary: HTTP redirects are re-validated per hop against the host
  allowlist (done), and file paths are symlink-resolved before the prefix check so a link inside
  an allowed prefix cannot point outside it (done).

What we deliberately **skip** (non-goals): multi-tenant isolation, per-user auth, hostile-user
defense, hard per-instance memory caps (gopher-lua op/time limits + abort are enough here).

---

## 6. Target architecture (Go)

Ports-and-adapters shape (vision doc §4), as Go packages:

```
cmd/                 CLI frontend (one of several peer clients)
internal/
  engine/            headless agent loop (provider-neutral kernel)
  provider/          Provider port + adapters (openai, anthropic, …)
  tools/             Tool type, built-in primitives, registry, tool-search
  sandbox/           agent-authored tier: luaglue (gopher-lua); wasm (wazero) in reserve
  capability/        capability broker (deny-by-default for authored tools) + grant context
  store/             append-only run log, tool catalog, memory, grants
  api/               headless transport (HTTP/SSE or JSON-RPC) for frontends
```

Key types (Go renderings of vision-doc Appendix B):

- `provider.Provider` — `Step(ctx, StepRequest) (StepResponse, error)`; neutral `Message`,
  `ContentBlock` (`Text` / `ToolCall` / `ToolResult`), `ToolDef`, `Usage`, `StopReason`. The
  engine never imports `openai-go`; adapters do.
- `tools.ToolSpec` — model-facing (`ToolDef`) + execution-facing (`Impl`: `Native` |
  `Script{Lang, Source}` | `VendorNative`), plus `RequiredCaps`, `Scope`
  (`Ephemeral|User|Shared`), `Test`, `Version`.
- `tools.Registry` — `Register / Get / Search / Revoke / List`. **Search** (BM25/regex first,
  embeddings later) keeps a growing catalog out of the context window. **Append** tools to the
  live set mid-run; never rebuild (preserves prompt cache).
- `capability.Broker` — for the authored tier, every effect (`HttpGet`, `ReadFile`, `CallTool`,
  `Clock`, `Random`, …) goes through here: check grant + allowlist → execute → audit.
- `sandbox.luaglue` (gopher-lua) — default authored-tool tier: fresh `LState` per call,
  globals built only from granted capabilities, stdlib stripped
  (`os`/`io`/`debug`/`package`/`require`), instruction-count hook for timeout/abort,
  op/size limits.
- `sandbox.wasm` (wazero) — **reserve** tier: hard linear-memory cap + fuel limits +
  capability-gated host imports. Build only if isolation needs ever exceed the glue tier.
- `store` — append-only `RunEvent` log (transcript *and* audit trail), tool catalog, memory,
  grants. SQLite is the natural single-binary-friendly backing store.

---

## 7. Data collection & analytics in Go

Both target use cases are covered without Python (excluded at ~2 GB):

- **Collection** (a Go strength): goroutine-concurrent HTTP, HTML scraping (`goquery`, already
  a dependency), JSON/CSV/XML parsing, DB writes.
- **Analytics**: a small set of **curated native primitives** the agent *composes* (Go as the
  primitive language):
  - `gonum` — stats, distributions, linear algebra, regression, optimization.
  - `gota` — dataframes (filter / group-by / join / aggregate).
  - `gonum/plot` or `go-echarts` — visualizations.
  - Stream/chunk large inputs; don't load giant datasets fully into memory on a 2 GB box.
- **Escape hatch** for genuinely heavy numeric work (rarely needed): a numeric module in the
  **wazero** tier, or one vetted native binary behind a brokered capability — no host change.

---

## 8. Phased build plan (grounded in current code)

Each phase is useful on its own; do them in order. The build-ordering rule (§5) is hard.

0. **Decouple the provider.** Extract `provider.Provider`; move OpenAI specifics behind an
   adapter. Engine speaks neutral types only. *(Refactor of today's `agent.go`; unblocks
   everything — highest-leverage first step.)*
1. **Kernel + provider port + built-in tools incl. `run_code`.** Largely exists; add `run_code`
   as lightweight self-extension. Keep `shell`/web as trusted built-ins; add an approval gate on
   destructive actions.
2. **Capability broker + `luaglue` sandbox (gopher-lua) + append-only audit log.** The boundary
   around *authored* tools. Do this *before* step 3.
3. **Tool registry + `author_tool` + tool-search.** Promote ephemeral code to named, scoped,
   tested, capability-bounded tools. True self-extension.
4. **Headless engine API + memory + management plane** (approvals, review/revoke); web +
   Telegram added as peer frontends alongside the CLI.
5. *(Later, only if isolation needs grow)* **wazero tier**; **own web-search + analytics
   primitives** if a second provider or stronger isolation ever demands it.

---

## 9. Open questions (this project)

- **Provider port shape**: normalize parallel tool calls, streaming deltas, tool-call/result
  id pairing, and stop/refusal reasons across OpenAI/Anthropic.
- **Authored-tool limits**: concrete op/size/time limits + abort policy for gopher-lua.
- **`author_tool` gate**: the mandatory smoke-test contract and the dry-run grant used to run it.
- **Approval UX**: surfacing destructive-action / escalation prompts in web/Telegram without
  nagging.
- **Tool-catalog lifecycle**: dedup, TTL, review cadence for shared self-authored tools.
- **Store choice**: SQLite vs append-only file log for the run/event log + catalog + grants.
