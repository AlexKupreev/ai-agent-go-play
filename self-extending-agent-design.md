# Self-Extending Agent — Design Notes

A standalone, implementation-agnostic design for a **general-purpose, provider-agnostic AI
agent** that is **managed from the web (and other frontends)** and can **author its own tools
at runtime**, targeting **small, low-resource deployments**.

This document captures goals, constraints, the proposed architecture, and the option
trade-offs explored. It is a starting point for a from-scratch implementation; it does not
assume any existing codebase.

---

## 1. Goal

Build a **general-purpose autonomous agent** — not a coding/ops agent tied to a shell, and
not a single-purpose chatbot. Target use cases:

- **Web search** (answer with current information).
- **Data analysis** (operate on user-supplied data, produce results/visualizations).
- **Interactive learning / tutoring** sessions.
- …and open-ended tasks generally.

Defining properties:

1. **Managed from the web** (and ideally Telegram, CLI, etc.) — not CLI-only.
2. **Provider-agnostic** — not locked to one LLM vendor.
3. **Extensible / self-extending** — the agent can create new tools for itself at runtime
   (the property that makes "pi-like" architectures attractive).
4. **High-performance, low-resource** — runs comfortably on small machines (~2 GB RAM),
   including the tools the agent writes for itself.

---

## 2. Constraints

| Constraint | Implication |
|---|---|
| ~2 GB RAM target | No per-tool heavyweight runtimes (V8/CPython are tens of MB *each*). No on-box native toolchains (rustc/LLVM/clang) for runtime tool compilation. |
| Agent-authored tools must be cheap to create, instantiate, and run | Favor interpretation or pre-AOT-cached artifacts; instantiation in µs–ms; per-instance memory in single-digit MB. |
| High performance where it matters | Hot/compute paths must be native, not interpreted agent code. |
| Provider-agnostic | Cannot depend on any one vendor's hosted server-side tools as the foundation. |
| Self-authoring = LLM writes & runs code | Security is a first-class design problem (prompt injection can author malicious tools). |
| Small, trusted user base (private deployment) | Threat model is injection-driven misbehavior, *not* hostile multi-tenant escape. This materially relaxes the isolation requirement. |

---

## 3. Core idea

> **Code execution is the universal tool. A "named tool" is just crystallized code with a
> schema and a capability grant.**

Extensibility and self-authoring are the same feature at two lifecycles:
- **Ephemeral:** the agent writes code and runs it now.
- **Registered:** that code is given a name + input schema + capability grant, stored, and
  becomes a first-class callable tool for this and future sessions.

A second, equally important principle for the performance constraint:

> **Agent-authored code is glue (I/O-bound orchestration), not hot loops.** Make it cheap,
> tiny, and safe — not CPU-fast. Push genuinely compute-heavy work into **pre-built native
> primitives** the agent *composes*. "Fast tools" come from fast native building blocks, not
> from compiling the agent's code to native.

---

## 4. Architecture (ports & adapters)

```
                         ┌──────────────────────────────┐
   web ─┐                │            KERNEL            │
  tg ───┼──▶  Engine API │  agent loop: model ⇄ tools  │
  cli ─┘   (headless)    │  (provider-neutral)         │
                         └───────┬───────────┬──────────┘
                                 │           │
                      ┌──────────▼──┐   ┌────▼─────────────┐
                      │ Provider     │   │ Tool system      │
                      │ port         │   │  - built-ins     │
                      │ (Claude/     │   │  - registry      │
                      │  OpenAI/…)   │   │  - tool-search   │
                      └──────────────┘   └────┬─────────────┘
                                              │ every effect
                                   ┌──────────▼──────────┐
                                   │ Capability broker    │  ← deny by default
                                   │  + Sandbox(es)       │
                                   └──────────┬──────────┘
                                   net │ fs │ tools │ compute
                         ┌──────────────────────────────────┐
                         │ Stores: run log (append-only),   │
                         │ tool catalog, memory, grants     │
                         └──────────────────────────────────┘
```

### Components

- **Kernel (agent loop)** — provider-neutral. `prompt → model → (text | tool calls) →
  execute tools → feed results → repeat until done`. Knows nothing about vendors or tool
  internals.
- **Provider port** — adapters mapping the portable subset (chat + function/tool calling) to
  each LLM vendor. The kernel never names a provider.
- **Tool system** — built-in tools (search, http, memory/notes, `run_code`), a **registry**
  of self-authored/registered tools, and **tool-search** (BM25/regex/embeddings) so a large
  or growing catalog never floods the context window — only relevant tool schemas are loaded
  per turn.
- **Capability broker + sandbox(es)** — the load-bearing wall. Self-authored code has **no
  ambient authority**; every effect (open host X, read path Y, call tool Z) is a brokered
  call checked against the tool's granted capabilities, and audited.
- **Stores** — append-only run/event log (session history), tool catalog, long-term memory
  the agent maintains, and capability grants.
- **Engine API + frontends** — the engine is **headless and addressable from day one**; web
  UI, Telegram, CLI are **peer clients**, not special cases. (This is the single most
  valuable idea borrowed from pi's RPC/headless mode.)

---

## 5. Provider-agnosticism

**The portable layer across vendors is chat + function/tool calling** (a `tools` array, the
model emits tool calls, you execute and feed results back — Anthropic, OpenAI, Gemini all do
this with minor shape differences). Keep the loop above this seam.

**Trade-off (important):** vendor *hosted* server-side tools (e.g. a provider's built-in web
search or code execution) are **not portable** — different shapes, results, semantics. So
provider-agnosticism means **you own the infrastructure** for web search and the code/data
sandbox, rather than leaning on a vendor's hosted versions.

**Resolution:** define tools as **host-executed and provider-neutral**. Treat a vendor's
native server tools as an **optional accelerator behind your neutral tool interface**, never
the base — so a run behaves correctly on any provider, and a vendor tool is just a faster
path when available. Defer building your own search/sandbox until a second provider actually
forces it; until then you may back a neutral tool with whichever provider's native tool is
present.

| | Provider-agnostic | Single-vendor-native |
|---|---|---|
| Agent loop | Yours, portable | Yours (or vendor-managed) |
| Web search | You host (or vendor-native as optional) | Free hosted tool |
| Code/data sandbox | You host | Free hosted tool |
| Model swap | Config change | Locked in |
| Net effort | Higher (own 2 infra pieces) | Lower |

---

## 6. The self-extension loop

1. Agent hits a task its current tools can't do.
2. It calls a meta-tool, e.g. `author_tool(name, description, input_schema, code,
   requested_capabilities)`.
3. System **validates**: schema well-formed, code parses, passes a smoke/conformance test
   (give every authored tool a test gate).
4. System **scopes & gates**: store in the catalog at a scope (ephemeral / per-user /
   shared); any *new* capability beyond the default tier requires human approval.
5. Tool becomes callable — **appended** to the tool set (never rebuild the list mid-run; that
   destroys prompt cache) or discoverable via tool-search.
6. On call, the code runs in the sandbox with **only** its granted capabilities; every
   hostcall is audited.

The toolbox grows within a session and across sessions, but every addition is schema'd,
tested, scoped, capability-bounded, and logged.

---

## 7. Security model (the make-or-break)

A self-extending agent writes and runs new code; **prompt injection can make it author a
malicious tool.** Non-negotiables:

- **Deny by default + capability-based access.** No sandbox has network/filesystem/tool reach
  unless brokered. The **registered host-function (capability) API surface is the entire
  security boundary** — be ruthless: narrow, explicit, every effect brokered and audited. A
  single over-broad capability (`http_get(anyURL)`, `read_file(anyPath)`) undoes everything.
- **Human-in-the-loop for capability *escalation*, not every call.** First use of a new host
  or a destructive/irreversible action → approve. Routine reuse → automatic. (Generalization
  of pi's `safe`/`balanced`/`permissive` policy tiers.)
- **Total auditability.** Every authored tool, every capability exercised, every hostcall in
  the append-only log. Tools are reviewable and revocable; scopes have lifecycles (a
  session-scoped tool dies with the session; a shared one needs review).

Note on threat reality: for the **most likely** threat (a tool that over-reaches), the
**capability/import list** — not memory isolation — is what stops exfiltration. Hard
sandboxing only adds defense against (a) interpreter/runtime escape bugs and (b) clean
memory-DoS caps.

---

## 8. Runtime / sandbox for agent-authored tools — the central technical choice

Requirements: cheap to create, tiny to instantiate, low memory, safe, **no external
toolchain at runtime**. CPU throughput of agent code is *not* a requirement (it's glue).

### Options considered

**A. WASM (Wasmi / Wasmtime in Rust; wazero in Go)**
- *Pros:* hard, memory-isolated sandbox; per-instance linear-memory cap (e.g. 16–64 MB) +
  fuel/epoch CPU limits; module is KB–low-MB, instantiates in µs–ms; WASI preview2 /
  component model is **capability-based by design**; language-agnostic; native primitives can
  be reused as modules. wazero is pure-Go, no CGo, zero deps.
- *Cons:* ABI + linear-memory marshalling boundary (more integration friction); JIT
  (Wasmtime) costs compile memory/time per module — mitigate with **AOT-cache at
  registration**, or use an **interpreter** (Wasmi/Wasm3/wazero-interpreter) for zero compile
  cost.
- *Interpreter vs JIT for 2 GB:* lean interpreter for ephemeral glue tools; AOT-cache once at
  registration if you later want JIT speed for hot tools. Never compile per call.

**B. Rust-native embedded interpreter — Rhai**
- *What it is:* small, pure-Rust, dynamically-typed, tree-walking scripting language, **purpose-built for embedding and running untrusted scripts.**
- *Pros:* trivial integration (register Rust fns directly, native types, no marshalling);
  capability gating is natural (script can call only what you register); tiniest footprint;
  instant instantiation; no toolchain; built-in **operation-count, call-depth, string/array/map
  size limits, and a progress callback** for timeouts/abort; mature & stable; explicitly
  hardened for untrusted execution.
- *Cons:* **logical (in-process) isolation only** — no memory wall; safety = interpreter
  correctness + your API discipline; **no clean per-instance memory cap** (weakest axis is
  memory-DoS; mitigate via size/op limits + progress abort); tree-walking is slow at compute
  (irrelevant — compute lives in native primitives); single scripting language (not
  language-agnostic).

**C. Embedded Lua / Luau (via `mlua` in Rust)**
- *What it is:* the canonical embeddable scripting language; tiny, mature, fast bytecode VM.
  **Luau** is Roblox's Lua fork, *purpose-built to run untrusted scripts at scale* (sandbox
  mode, removed footguns, gradual typing, deterministic). `mlua` binds Lua 5.1–5.4 / LuaJIT /
  Luau in Rust.
- *Pros:* **highest LLM familiarity** of any option — the script author is an LLM, and Lua is
  enormously represented in training data (Redis, nginx/OpenResty, Neovim, game engines), so
  the agent writes correct tools more reliably; canonical embed-language maturity; capability
  gating is natural and total (hand the script a restricted `_ENV` / globals table with only
  brokered host functions); **hard per-instance memory cap** via a custom allocator (fixes
  Rhai's weakest axis); instruction-count debug hooks for CPU/timeout abort; tiny footprint;
  fast. Luau is best-in-class for *untrusted* embedded scripts.
- *Cons:* **C core** (PUC-Lua/LuaJIT) — a VM bug is potential host UB, not a safe Rust panic
  (Luau + `mlua` maturity mitigate; this is the main trade vs Rhai); **logical (in-process)
  isolation only** — no memory wall; **sandboxing footguns must be handled deliberately:**
  text-only chunk loading (`load(src, name, "t", env)` — never accept precompiled bytecode),
  strip `os`/`io`/`debug`/`package`/`require`/`dofile`/`loadfile`/raw `load`, lock the shared
  `string` metatable. **Avoid LuaJIT for untrusted code** unless its FFI is fully disabled
  (FFI = raw memory access = instant escape); its speed is unneeded anyway (hot work is native).

**D. Rust-native embedded interpreter — Rune**
- *What it is:* younger, Rust-like syntax, **bytecode VM**, pattern matching, **first-class
  async**.
- *Pros:* nicer language ergonomics; faster than Rhai (bytecode vs tree-walk); **async** lets
  a tool `await` brokered I/O without blocking a thread.
- *Cons:* younger, smaller community, more API churn; **less hardened for untrusted
  execution** than Rhai; lower LLM familiarity than Lua; slightly more memory.

**E. Embedded JS (QuickJS) — pi's choice**
- *Pros:* familiar language; capability-gated hostcalls; deterministic.
- *Cons:* heavier than Rhai/Lua/Wasmi per instance; JS ecosystem temptations; for a Rust/Go
  host it's an FFI dependency. Largely superseded by Lua/Rhai/WASM for this use.

**F. Python / Node (V8) per-tool**
- *Rejected:* tens of MB baseline **per instance**; fatal at any concurrency on 2 GB.

**G. Provider-hosted code execution (Anthropic/OpenAI)**
- *Pros:* zero infra; rich data-science libs.
- *Cons:* vendor-specific (fights provider-agnosticism); can't persist results as *your* own
  registered tools.

### Comparison (agent-tool sandbox)

| | Luau (via `mlua`) | Rhai | Rune | WASM (Wasmi/Wasmtime/wazero) |
|---|---|---|---|---|
| LLM familiarity (author is an LLM) | **Very high** | Low–moderate | Low | n/a (depends on source lang) |
| Maturity / pedigree | Canonical embed lang | Mature | Young | Mature runtimes |
| Integration | Native fns via `mlua` | Trivial — native Rust fns | Native Rust fns | ABI + linear-memory boundary |
| Footprint / startup | Tiny; instant | Tiniest; instant | Small; instant | Tiny; instant (interp) or AOT-cached |
| Capability gating | Restricted `_ENV` | Registered fns | Registered fns | Module imports |
| **Isolation** | Logical (in-process) | Logical (in-process) | Logical (in-process) | **Hard — memory-isolated** |
| Hard memory cap | **Yes (custom allocator)** | No (op/size only) | Limited | **Yes (linear-memory cap)** |
| Safe substrate | **C core → VM bug = host UB** | **Pure safe Rust** | **Pure safe Rust** | Runtime-dependent (hardened) |
| Untrusted-script pedigree | Luau: excellent | Marketed for it | Weaker | Strong |
| Language-agnostic / reuse native as modules | No | No | No | Yes |

### Decision guidance (trust-boundary driven)

- **Hostile multi-tenant SaaS** → require **WASM** (or process/container) isolation.
- **Small private box, trusted users, injection-driven threat (this project)** → an in-process
  interpreter is a defensible, much simpler default. For the dominant threat (capability
  over-grant), interpreters and WASM are equivalent — the broker discipline is what matters.
  WASM's edge (hard memory caps, escape-resistance, language-agnosticism) isn't worth its
  complexity here *yet*.
- **Co-leading defaults: Luau and Rhai.** They lead on different axes — pick by which you
  weight more:
  - **Luau (via `mlua`)** — choose for **LLM familiarity** (the agent writes correct tools
    more reliably) and a **hard per-instance memory cap** (custom allocator). Cost: C-core
    substrate (Luau hardening mitigates) and deliberate sandbox setup (text-only `load`,
    stripped stdlib, locked `string` metatable).
  - **Rhai** — choose for a **pure-Rust memory-safe substrate** and zero-footgun, trivial
    integration. Cost: lower LLM familiarity and **no hard memory cap** (op/size limits +
    progress-abort only — the weak axis on a 2 GB box).
  - Tiebreaker for a self-authoring agent: **LLM familiarity usually wins → lean Luau**, since
    "the model rarely mis-writes a tool" compounds across every authored tool. Choose Rhai if
    you weight substrate safety and a footgun-free sandbox above that.
- **Rune** only if you specifically want async + nicer syntax and accept a younger,
  less-hardened, lower-familiarity runtime.
- Keep **WASM (or a process/container sandbox) as the escalation tier**, not the default —
  route a tool there only when it needs stronger isolation than the in-process tier gives.
  Don't build two sandboxes on day one.

---

## 9. Host language

| Option | Sandbox fit | Notes |
|---|---|---|
| **Rust** | **Luau/Lua (`mlua`)**, **Rhai/Rune** (embedded), Wasmtime/Wasmi (WASM) | Best memory control + sandbox story; Rhai/Rune are native Rust, Luau via the mature `mlua` binding. **Recommended.** |
| **Go** | **wazero** (pure-Go WASM, no CGo, zero deps); `gopher-lua` (pure-Go Lua 5.1) | wazero is the cleanest WASM embedding; `gopher-lua` gives an in-process Lua tier without CGo (no Luau). |
| **Zig** | Wasmtime C API / Wasm3; Lua C lib | Works; thinner ecosystem. |

**Recommendation:** **Rust** host, with the default glue-tool tier being **Luau (via `mlua`)
or Rhai** — co-leading (Luau for LLM-familiarity + hard memory cap; Rhai for pure-Rust safe
substrate + footgun-free integration). Hold **WASM (Wasmi/Wasmtime)** in reserve as the
higher-isolation escalation tier. If Go is strongly preferred for the host, **Go + wazero**
(WASM-only) or **Go + `gopher-lua`** (in-process Lua, no Luau/CGo) are the alternatives.

### Host language vs guest language — keep them distinct

A natural question is whether the **host (the engine itself)** could be written in the same
language the agent authors tools in — e.g. Lua all the way down. It *can* (OpenResty runs
serious Lua backends), but for these constraints the host and guest should be **different
languages**, and the split is a feature, not an accident.

**The host is a different kind of program than a tool.** It's a long-running,
security-critical, concurrent network service: HTTP/SSE API for frontends, many concurrent
runs, streaming HTTP to LLM providers, a DB, the capability broker + audit log, and the
sandbox host itself. Judge a host language against *that*:

| Host need | Statically-typed systems lang (Rust/Go) | Lua as host |
|---|---|---|
| Concurrency for a network service | Goroutines / async runtimes, mature | No built-in threads; cooperative coroutines → needs OpenResty or `cqueues`/`copas` event loop |
| Backend ecosystem (HTTP server, SSE, DB, TLS) | Deep, uniformly maintained | Thinner, bespoke (luarocks/OpenResty); more glue, more gaps |
| Correctness of a complex stateful core | Compiler-enforced types | Dynamic (Luau adds gradual typing, but server tooling around Luau barely exists) |
| Deploy on a ~2 GB box | Single static binary | Interpreter + luarocks/C deps or an OpenResty image — heavier artifact |
| Trusted, security-critical core | Memory-safe + minimal C surface (Rust); simple (Go) | Managed VM, but C runtime + C bindings (TLS/DB) reintroduce a C surface |

Two points specific to this design:

- **A Lua host contradicts the high-perf-core constraint.** The host isn't compute-heavy (I/O
  orchestration, so "Lua is fast enough" is true), but hosting in Lua trades away
  static-typing robustness, the single-binary ops story, and a thick concurrency ecosystem.
- **The host/guest language split reinforces the trust boundary.** The agent self-authors
  *sandboxed tools*, never the *host*. A distinct host language keeps that boundary sharp;
  sharing one language nudges toward blurring exactly the line being protected.

**The one coherent "Lua host" architecture** (for the record): host in Lua and run each tool
as an isolated child `lua_State` (separate VM, GC, globals; restricted `_ENV`). This is the
Redis/nginx embedding pattern — elegant, one runtime, no host↔guest marshalling. But it
concentrates the *entire* system, trusted core included, on the **C-Lua substrate**
(amplifying the substrate-safety concern from "the sandbox" to "everything") and gives up the
static-typing / single-binary / concurrency-ecosystem advantages. It also can't use Luau (its
tooling is embedded-in-Roblox-shaped, not server-shaped), so no gradual typing where it would
help most. Coherent, but not the default here.

**Conclusion:** keep the conventional, proven split — **fast statically-typed host (Rust/Go)
embedding Lua/Luau as the guest extension language** (the Redis / nginx / game-engine
pattern). Host gets concurrency, type-enforced correctness, single-binary deploy, minimal
trusted surface; guest gets LLM-familiarity, a tiny embeddable VM, natural capability gating,
and a hard memory cap — behind the trust boundary.

---

## 10. Performance philosophy

- **Agent-authored code = glue** (orchestration, data shaping, branching, calling other
  tools). I/O-bound; interpreter speed is fine.
- **Hot/compute work = pre-built native primitives** you write once (Rust/Zig), exposed as
  capability-gated tools/host-functions the agent composes. Native speed, tiny incremental
  memory, nothing to compile at runtime, better auditability.

### Data-analysis exception

The one case wanting real numeric muscle. On 2 GB, **avoid** Python/Pyodide and giant
dataframe libs. Instead expose a **curated native primitive** — a small set of
dataframe/stats/plot operations written once in Rust/Zig, surfaced as capability-gated tools.
The agent composes those; heavy loops run in vetted native code.

---

## 11. Resource budget (~2 GB)

```
host engine + provider client + DB + management API   ~200–400 MB
embedded interpreter / WASM runtime (shared)           ~few MB
each agent-tool instance (capped, pooled, ephemeral)   ~1–16 MB
pre-built native primitives (numeric/parse/plot)       loaded once, shared
                                                       ─────────────────────
→ comfortable headroom for many concurrent tools on 2 GB
```

Budget-killers to avoid: per-tool V8/CPython runtimes; on-box native compilation (rustc/LLVM);
unbounded interpreter memory. Memory-DoS is the weakest axis with interpreters — enforce
size/op limits + progress-abort explicitly.

---

## 12. Phased build plan

Each stage is useful on its own; do them in order.

1. **Kernel + provider port + small built-in tool set including `run_code`.** Already gives
   lightweight self-extension ("writing a tool" = writing a function the model runs). Most of
   the value, little of the risk.
2. **Capability broker + sandbox (Luau or Rhai), deny-by-default, audit log.** Do this
   *before* step 3. (Luau: text-only `load`, stripped stdlib, locked `string` metatable,
   allocator memory cap, instruction-count abort. Rhai: op/size/depth limits + progress
   abort.)
3. **Tool registry + `author_tool` + tool-search.** Promote ephemeral code to named,
   reusable, scoped tools. True self-extension.
4. **Memory + management plane** (approvals, review/revoke, web + Telegram + CLI as peer
   frontends).
5. *(Later, if needed)* **WASM escalation tier** for higher-isolation tools; **own web-search
   + data-analysis primitives** once a second provider or stronger isolation forces it.

> Do **not** build step 3 before step 2 exists — a self-authoring agent without a capability
> broker is a remote code-execution service with an LLM choosing the payloads.

---

## 13. Summary of recommendations

- **Headless, addressable engine**; web/Telegram/CLI are peer frontends. (pi's most valuable
  idea.)
- **Provider-neutral kernel** over a chat+tool-calling provider port; vendor server-tools are
  optional accelerators, never the base.
- **"Code is the tool; named tools are crystallized code."** Self-extension = author → test →
  scope → capability-gate → register.
- **Security is the architecture:** deny-by-default capability broker, human approval for
  escalation, full audit. The registered capability API *is* the boundary.
- **Sandbox: Rust host with Luau (`mlua`) and Rhai as co-leading default tiers** — Luau for
  LLM-familiarity + hard memory cap, Rhai for pure-Rust safe substrate; lean Luau for a
  self-authoring agent unless substrate safety outweighs it. **WASM (Wasmi/Wasmtime)** as the
  escalation tier; **Go + wazero / gopher-lua** if Go-hosted.
- **Agent code is glue; hot work lives in native primitives.** Data analysis = curated native
  primitives, not Python.
- **Fits ~2 GB** by avoiding per-tool heavyweight runtimes and on-box native compilation.

---

## 14. Open questions / risks

- **Scripting language ergonomics for the LLM:** how reliably can the model author correct
  tools in the chosen language? (Lua/Luau has the strongest training-data presence and is the
  safer bet here; Rhai/Rune are less familiar — mitigate with examples, a tight stdlib of host
  functions, and the test gate.)
- **Luau vs Rhai final pick:** validate empirically — have the target model author a batch of
  representative tools in each and compare correctness/iterations; weigh against how much you
  value the pure-Rust substrate (Rhai) vs LLM-familiarity + hard memory cap (Luau).
- **Lua sandbox hardening:** confirm the concrete lock-down (text-only `load`, stripped libs,
  `string`-metatable lock, allocator cap, instruction hook) and prefer **Luau** over PUC-Lua
  for its untrusted-script pedigree; never enable the LuaJIT FFI for authored code.
- **Tool-catalog lifecycle:** dedup, TTL, review cadence for shared self-authored tools;
  preventing accumulation of stale/insecure tools.
- **Memory-DoS hardening** with in-process interpreters: concrete limits + abort policy.
- **Capability-grant UX:** how approvals surface in web/Telegram without becoming nagging.
- **Provider-neutral tool-calling edge cases:** parallel tool calls, streaming, and
  tool-result shapes differ subtly across vendors — the adapter layer must normalize these.
- **When to introduce the WASM tier / self-hosted search + sandbox** — driven by a concrete
  second-provider or stronger-isolation need, not speculatively.

---

## Appendix A — Decision log

One line per decision and its rationale. "Open" = deliberately deferred, to be settled during
implementation.

### Product & scope

| Decision | Choice | Rationale |
|---|---|---|
| Agent type | General-purpose autonomous assistant (not coding/ops, not single-purpose chatbot) | Target use cases are web search, data analysis, tutoring, open-ended tasks. |
| Frontends | Web + Telegram + CLI as **peer clients** of one headless engine | Don't special-case any frontend; the engine is addressable from day one (pi's RPC idea). |
| "pi-like" | Adopt the **blueprint**, not the artifact | pi's tool surface (shell/files) and single-user/no-auth model are wrong for a general, multi-user, web-managed assistant; its *architecture* (provider abstraction, tool loop, headless mode, append-log sessions) is right. |

### Provider strategy

| Decision | Choice | Rationale |
|---|---|---|
| Provider coupling | **Provider-agnostic** kernel over a chat + tool-calling port | Avoid vendor lock-in; enable A/B of models. |
| Vendor server-tools (hosted search/code-exec) | **Optional accelerator behind a neutral tool interface**, never the base | They don't port across vendors; the portable contract is your host-executed tool, with a vendor's native tool as a faster path when present. |
| Cost of agnosticism | You **own** search + code/data sandbox infra | The price of not depending on any vendor's hosted tools; defer building until a second provider forces it. |

### Extensibility & tools

| Decision | Choice | Rationale |
|---|---|---|
| Self-extension model | **"Code is the tool; named tools are crystallized code"** | Ephemeral code now → registered, schema'd, scoped tool later; same feature at two lifecycles. |
| Tool authoring | Meta-tool `author_tool(...)` → validate → test → scope → capability-gate → register | Every addition is schema'd, smoke-tested, scoped, capability-bounded, audited. |
| Catalog scaling | **Tool-search** (BM25/regex/embeddings); **append** tools, never rebuild the list mid-run | Keeps context small as the catalog grows; rebuilding the tool list destroys prompt cache. |

### Security

| Decision | Choice | Rationale |
|---|---|---|
| Authority model | **Deny-by-default capability broker**; the registered host-fn API *is* the boundary | A self-authoring agent + prompt injection = it can write malicious tools; the capability/import list — not memory isolation — is what stops over-reach. |
| Approvals | Human-in-the-loop on capability **escalation**, not every call | First use of a new host / destructive action → approve; routine reuse → automatic. |
| Auditability | Append-only log of every authored tool, capability, and hostcall; tools revocable, scopes have lifecycles | Reviewability and revocation are mandatory for self-modifying systems. |
| Build ordering | Broker + sandbox **before** the tool registry | A self-authoring agent without a broker is an RCE service with an LLM picking payloads. |

### Performance & resources

| Decision | Choice | Rationale |
|---|---|---|
| Agent-code performance | Treat agent-authored code as **glue (I/O-bound)**; optimize for cheap/tiny/safe, not CPU-fast | Most authored tools orchestrate; throughput rarely matters. |
| Hot/compute work | **Pre-built native primitives** the agent composes | Native speed, tiny incremental memory, nothing compiled at runtime, better auditability. |
| Data analysis | **Curated native dataframe/stats/plot primitives**, not Python/Pyodide | Python runtimes are too heavy for ~2 GB; push numeric loops into vetted native code. |
| ~2 GB fit | Avoid per-tool V8/CPython runtimes and on-box native compilation | These are the budget-killers; WASM/embedded interpreters + shared native primitives stay frugal. |

### Sandbox & languages

| Decision | Choice | Rationale |
|---|---|---|
| Default sandbox tier | **Luau (`mlua`) and Rhai — co-leading** | Luau: top LLM-familiarity + hard memory cap (custom allocator). Rhai: pure-Rust safe substrate + footgun-free integration. |
| Luau vs Rhai tiebreaker | **Lean Luau** for a self-authoring agent (Open — validate empirically) | LLM-familiarity compounds across every authored tool; pick Rhai if substrate safety + footgun-free sandbox outweighs it. |
| LuaJIT FFI | **Disabled** for authored code | FFI = raw memory access = instant sandbox escape; its speed is unneeded (hot work is native). |
| Lua hardening | Text-only `load`, stripped `os`/`io`/`debug`/`package`/`require`, locked `string` metatable, allocator cap, instruction-count abort; prefer Luau | Lua sandboxing is well-understood but footgun-prone; Luau was built for untrusted scripts. |
| Higher-isolation tier | **WASM (Wasmi/Wasmtime)** as escalation, not default | Hard memory isolation + caps when a tool needs more than in-process logical isolation; don't build two sandboxes on day one. |
| Rune | Not default | Younger, less-hardened, lower LLM-familiarity; only if async + nicer syntax is specifically wanted. |
| Embedded JS (QuickJS), Python/Node per-tool, provider-hosted code-exec | Rejected as default | QuickJS superseded by Lua/Rhai/WASM; V8/CPython too heavy per instance for 2 GB; provider-hosted is vendor-locked and not persistable as your own tool. |
| Interpreter vs JIT | **Interpreter** for ephemeral glue; **AOT-cache at registration** if JIT speed is later wanted | Zero per-call compile cost; bounded one-time cost at registration. |
| Host language | **Rust** (or Go) — statically-typed systems language | Concurrency, type-enforced correctness for the stateful core, single-binary deploy, minimal trusted surface. |
| Host ≠ guest language | **Distinct languages** (fast typed host embeds Lua/Luau guest) | Reinforces the trust boundary (agent authors sandboxed tools, never the host); the Redis/nginx/game-engine pattern. |
| Lua as the host | Rejected | Contradicts the high-perf-core constraint, thinner backend ecosystem, heavier deploy, and blurs the host/guest trust boundary; child-`lua_State` "all-Lua" variant is coherent but concentrates everything on the C substrate. |

### Open decisions (settle during implementation)

| Open item | Why deferred |
|---|---|
| Final Luau-vs-Rhai pick | Validate empirically with the target model (authoring-correctness vs substrate-safety weighting). |
| Tool-catalog lifecycle (dedup, TTL, review) | Needs real usage data to tune. |
| Capability-grant UX in web/Telegram | Must approve escalations without becoming nagging. |
| Provider-neutral tool-calling normalization | Parallel calls, streaming, tool-result shapes differ subtly per vendor. |
| When to add the WASM tier / self-hosted search + sandbox | Driven by a concrete second-provider or stronger-isolation need, not speculation. |

---

## Appendix B — Interface sketches

Stack-agnostic pseudocode (Rust-flavored). These are **shapes to convey the contracts**, not
final APIs — names, error handling, and async details are illustrative.

### B.1 Provider port (the vendor-neutral seam)

The kernel speaks only this; one adapter per vendor maps it to that vendor's SDK/wire format.

```rust
// Neutral conversation types — the portable subset across vendors.
enum Role { System, User, Assistant, Tool }

enum ContentBlock {
    Text(String),
    ToolCall { id: String, name: String, input: Json },   // model → us
    ToolResult { call_id: String, output: Json, is_error: bool }, // us → model
}

struct Message { role: Role, content: Vec<ContentBlock> }

// A tool as the *model* sees it: name + description + JSON-Schema for inputs.
// (Separate from how it's *executed* — see B.2.)
struct ToolDef { name: String, description: String, input_schema: Json }

enum ToolChoice { Auto, Required, None, Named(String) }

struct StepRequest {
    model: String,                 // resolved per provider by the adapter
    system: Option<String>,
    messages: Vec<Message>,
    tools: Vec<ToolDef>,           // only the relevant subset (see tool-search)
    tool_choice: ToolChoice,
    max_tokens: u32,
}

struct Usage { input_tokens: u64, output_tokens: u64, cached_tokens: u64 }

enum StepStop { EndTurn, ToolCalls, MaxTokens, Refusal }

struct StepResponse {
    content: Vec<ContentBlock>,    // text and/or one-or-more ToolCall blocks
    stop: StepStop,
    usage: Usage,
}

trait Provider {
    // One model turn. The KERNEL owns the loop; the provider owns one step.
    async fn step(&self, req: StepRequest) -> Result<StepResponse>;

    // Optional streaming variant; deltas normalized to neutral events.
    async fn step_stream(&self, req: StepRequest) -> Result<EventStream>;

    fn capabilities(&self) -> ProviderCaps;  // native web-search? code-exec? parallel calls?
}
```

Adapter responsibilities (where vendors differ — the normalization layer earns its keep):
parallel tool calls, streaming deltas, tool-call/result id pairing, `tool_choice` shapes,
refusal/stop reasons, and mapping a neutral tool that *prefers a vendor-native server-tool* to
that vendor's hosted implementation when `ProviderCaps` says it's available.

### B.2 Tool, registry, and the execute side

A tool has two faces: what the **model** sees (`ToolDef`, B.1) and how it's **executed**.

```rust
enum ToolImpl {
    Native(NativeHandlerId),       // pre-built primitive (search, http, dataframe op…)
    Script { lang: ScriptLang, source: String },   // agent-authored (Luau/Rhai)
    VendorNative(VendorToolKind),  // executed by the provider (optional accelerator)
}

enum ScriptLang { Luau, Rhai }     // WASM added at the escalation tier

enum ToolScope { Ephemeral(RunId), User(UserId), Shared }

struct ToolSpec {
    name: String,
    description: String,
    input_schema: Json,
    impl_: ToolImpl,
    required_caps: Vec<Capability>,   // what this tool may do (see B.4)
    scope: ToolScope,
    created_by: Actor,                // run/user that authored it
    test: Option<ToolTest>,           // smoke test (input → expected/asserts)
    version: u64,
}

trait ToolRegistry {
    fn register(&self, spec: ToolSpec) -> Result<ToolId>;     // after validate+test+gate
    fn get(&self, id: ToolId) -> Option<ToolSpec>;
    fn search(&self, query: &str, scope: &[ToolScope], k: usize) -> Vec<ToolDef>; // tool-search
    fn revoke(&self, id: ToolId) -> Result<()>;
    fn list(&self, scope: &[ToolScope]) -> Vec<ToolSpec>;
}

// The kernel asks the executor to run a resolved tool call within a grant context.
trait ToolExecutor {
    async fn execute(
        &self,
        tool: &ToolSpec,
        input: Json,
        grant: &GrantContext,     // which capabilities are live for this run (B.4)
    ) -> Result<Json>;
}
```

### B.3 The `author_tool` meta-tool (what the model calls to self-extend)

Exposed to the model as an ordinary `ToolDef`; its handler runs the
validate → test → scope → gate → register pipeline.

```jsonc
{
  "name": "author_tool",
  "description": "Create a reusable tool. The code runs sandboxed; it may only call host \
functions you request via `required_capabilities`, which may need approval.",
  "input_schema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["name", "description", "input_schema", "language", "code",
                 "required_capabilities", "scope", "test"],
    "properties": {
      "name":        { "type": "string", "pattern": "^[a-z][a-z0-9_]{2,63}$" },
      "description": { "type": "string", "maxLength": 1024 },
      "input_schema":{ "type": "object" },          // JSON-Schema the tool accepts
      "language":    { "type": "string", "enum": ["luau", "rhai"] },
      "code":        { "type": "string", "maxLength": 65536 },
      "required_capabilities": {                     // names from the capability catalog (B.4)
        "type": "array",
        "items": { "type": "string" },
        "maxItems": 16
      },
      "scope":       { "type": "string", "enum": ["ephemeral", "user", "shared"] },
      "test": {                                      // mandatory smoke test (the conformance gate)
        "type": "object",
        "required": ["input", "assert"],
        "properties": {
          "input":  { "type": "object" },
          "assert": { "type": "string", "description": "expression over the result; must hold" }
        }
      }
    }
  }
}
```

Handler pipeline (host-side, not model-controllable):
1. Validate `name`/`input_schema`/`code` parse; reject on syntax error (return errors to the model to retry).
2. Run `test` in the sandbox with a **dry-run grant** (only the requested caps); reject on failure.
3. Resolve `required_capabilities`: any **beyond the run's current grant tier** → queue a
   human approval; the tool registers but stays *pending* until approved.
4. Register at `scope`; **append** its `ToolDef` to the live tool set (don't rebuild — cache).
5. Append an audit record (author, code hash, caps, scope).

### B.4 Capability model & broker

Capabilities are the *entire* security boundary. Deny by default; the broker is the only path
to any effect.

```rust
enum Capability {
    HttpGet  { host_allowlist: Vec<HostPattern> },
    HttpPost { host_allowlist: Vec<HostPattern> },
    ReadFile { path_prefix: PathBuf },
    WriteFile{ path_prefix: PathBuf },
    CallTool { name_allowlist: Vec<String> },   // compose other registered tools
    Clock, Random,                              // even ambient-looking effects are explicit
    // …extend deliberately; each variant is a thing a tool may do.
}

struct GrantContext {
    run: RunId,
    granted: Vec<Capability>,    // what's live for this execution
    tier: PolicyTier,            // Safe | Balanced | Permissive (default-allow set)
}

trait CapabilityBroker {
    // Every effect goes through here; checks against GrantContext, then audits.
    async fn http_get(&self, g: &GrantContext, url: &Url) -> Result<HttpResp>;
    async fn call_tool(&self, g: &GrantContext, name: &str, input: Json) -> Result<Json>;
    fn read_file(&self, g: &GrantContext, path: &Path) -> Result<Vec<u8>>;
    // … one brokered method per Capability variant.
    // Each: (1) check capability present + arg within allowlist, (2) execute, (3) audit-log.
}
```

### B.5 Luau capability-API contract (guest ⇄ host)

Granted capabilities decide **which host functions are injected into the script's
environment** — the script literally cannot name a function it wasn't granted.

```lua
-- The host builds a fresh, restricted _ENV per execution from GrantContext.granted.
-- Only granted host functions appear; the rest of the global table is empty.
-- Example environment for a tool granted HttpGet + CallTool:

local env = {
  -- stdlib subset deemed safe (no os/io/debug/package/require/load):
  string = safe_string, table = safe_table, math = math, tostring = tostring,
  -- input the tool was called with:
  input = <decoded input json>,
  -- brokered host functions (each call → CapabilityBroker, checked + audited):
  http_get  = function(url) return host.http_get(url) end,        -- present only if HttpGet granted
  call_tool = function(name, args) return host.call_tool(name, args) end, -- only if CallTool granted
  -- NOT present: http_post, read_file, write_file, os.* … (ungranted → absent)
}

-- The tool body (agent-authored) returns its result:
-- e.g.  return { title = http_get(input.url).title }
```

Host setup per call (`mlua`): fresh `Lua`/state or fresh env table; **text-only** chunk load
(`load(code, name, "t", env)` — reject bytecode); set a **custom allocator** with a byte cap;
install an **instruction-count hook** for timeout/abort; populate `env` strictly from
`granted`. Prefer **Luau** for its untrusted-script hardening; never enable the LuaJIT FFI.

### B.6 Rhai capability-API contract (guest ⇄ host)

Same principle, expressed through Rhai's registered-function model: the host builds a fresh
`Engine` (or `Scope`) per call and **registers only the functions the grant allows**.

```rust
let mut engine = Engine::new_raw();          // empty — no stdlib ambient effects
register_safe_stdlib(&mut engine);           // strings, math, arrays — no I/O

// Register brokered fns conditionally from GrantContext.granted:
if grant.has(Capability::HttpGet(..)) {
    let b = broker.clone(); let g = grant.clone();
    engine.register_fn("http_get", move |url: &str| b.http_get(&g, url));
}
if grant.has(Capability::CallTool(..)) {
    let b = broker.clone(); let g = grant.clone();
    engine.register_fn("call_tool", move |name: &str, args: Map| b.call_tool(&g, name, args));
}
// Ungranted capabilities → their functions are simply never registered (calling them errors).

// Resource limits (Rhai's levers — the memory axis is the weak one, lean on these):
engine.set_max_operations(MAX_OPS);
engine.set_max_call_levels(MAX_DEPTH);
engine.set_max_string_size(MAX_STR);
engine.set_max_array_size(MAX_ARR);
engine.on_progress(|ops| if deadline_exceeded() { Some("timeout".into()) } else { None });

let result = engine.eval_with_scope::<Dynamic>(&mut scope_with_input, &spec.source)?;
```

### B.7 Run/event log (append-only)

The session/run history that frontends render and that makes self-extension auditable.

```rust
enum RunEvent {
    UserMessage { text: String },
    ModelStep   { content: Vec<ContentBlock>, usage: Usage, stop: StepStop },
    ToolCall    { call_id: String, name: String, input: Json },
    ToolResult  { call_id: String, output: Json, is_error: bool },
    ToolAuthored{ tool_id: ToolId, code_hash: String, caps: Vec<Capability>, scope: ToolScope },
    CapabilityExercised { tool_id: ToolId, capability: String, arg_summary: String },
    GrantRequested { capability: String }, GrantDecided { capability: String, allowed: bool },
}

struct RunEventRecord { run: RunId, seq: u64, at: Timestamp, event: RunEvent }
// Append-only; the projection of these is both the chat transcript and the audit trail.
```