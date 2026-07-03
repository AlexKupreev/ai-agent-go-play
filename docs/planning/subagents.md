# Sub-agents — design & roadmap

How this engine should let one agent **delegate to other agents**. It adapts the model that
Claude Code and the pi coding agent use — **declarative agent *types* (data) + a generic spawn
tool the coordinator calls at runtime** — simplified to what this engine needs, with a clear
extension path. Build ordering lives in [`plan.md`](plan.md)'s backlog: the near-term unlock is the
**foreground `spawn_agent` tool** (§3), which lets new subagent organizations be tried from prompts +
agent-type files; **parallel read-only executors** (research / scout fan-out, §4) are **deferred** as a
later latency optimization.

Companion to [`../design.md`](../design.md) (trust model, §1) and [`plan.md`](plan.md). This is
roadmap, **not** current behavior — nothing here is built yet. It replaces an earlier
`multi-agent.md` (removed; same decisions, reorganized around the agent-type model — see git history).

---

## 0. The model in one paragraph

A **sub-agent type** is a named, tool-restricted agent definition — a system prompt + an allow-list
of tools + an optional model, expressed as *data* (built-in in code, or an `agents/<name>.md` file),
never as a bespoke Go constructor per role. The coordinator delegates by calling one built-in tool,
`spawn_agent(type, task)`, which instantiates a fresh child `*Agent` of that type, runs it to a final
answer, and returns the text. The default is **foreground and sequential** — one sub-agent runs while
the parent is paused — which is safe for *any* type. A **parallel** batch path exists for types marked
read-only (the fan-out use case, §4). A type can later carry an `Isolation` field that reroutes the
same spawn call to a separate OS process (§7) when it earns a distinct trust tier or identity — callers
don't change.

---

## 1. The distinction everything hinges on: concurrency vs identity

The recurring confusion is treating "processes vs threads" as one question. It's two:

- **Unit of concurrency = a goroutine.** Parallel runs, sessions, streams. The engine already does
  this (`Engine.launch` spawns a goroutine per run; `sessLocks` guards shared stores). Never fork a
  process just to get concurrency.
- **Unit of *agent identity* = an OS process.** A *distinct agent* — its own trust tier, its own
  `memory.json` / `tools.json` / `audit.jsonl`, its own data domain — is a separate `serve` process.
  That is the only boundary at which the design's guarantees actually hold (single-writer JSON stores;
  tier enforcement across an address-space boundary; a panic/OOM in one agent not killing the others).

**The question that classifies any sub-agent:** is it an *ephemeral worker* (born for one subtask,
returns a result, dies) or a *standing specialist* (persistent memory/tools/tier, reused across tasks)?

- Ephemeral worker ⇒ **in-engine** (a child `*Agent` in the same process; §2–§4).
- Standing specialist / distinct trust tier ⇒ **separate engine** (own process; §7).

A spawn tool serves both: in-engine is the default, and the type's `Isolation` field is the seam that
promotes a role to its own process without reshaping callers.

---

## 2. Agent types — the declarative core

An agent type is a value, resolved from a small catalog. Built-in types are registered in code so the
feature works out of the box; user types are Markdown files with YAML frontmatter (the pi/Claude-Code
layout), body = system prompt.

```go
// AgentType is a declarative sub-agent definition: a named, tool-restricted agent the
// coordinator can spawn. Values come from built-ins (code) or agents/<name>.md files.
type AgentType struct {
    Name        string   // spawn key, e.g. "researcher", "scout"
    Description string   // "when to use me" — surfaced to the spawning model
    Prompt      string   // system prompt (the file body)
    Tools       []string // allow-list of built-in tool names; empty ⇒ the safe read-only default set
    Model       string   // optional model override ("" ⇒ inherit the parent's model)
    Parallel    bool     // may run concurrently with siblings? (⇒ Tools must be read-only)
    PromptMode  string   // "replace" (body is the whole prompt) | "append" (body added to base) — see prompts.md §3
    // Reserved for later (each is just a field, not a reshape):
    //   Isolation   string  // "" ⇒ in-engine; "engine" ⇒ separate process (§7)
    //   MaxTurns    int     // per-sub-run iteration cap
    //   Memory      string  // "", "project", "local" — persistent memory scope
}
```

### Frontmatter file form (user-defined types)

```markdown
---
description: Read-only codebase scout — locates files and quotes lines, never edits.
tools: shell_ro, read_self_docs, web_search, web_fetch
model: gpt-4o-mini
parallel: true
---
You are a scout. Investigate ONE narrow question about this workspace and report what you
found with exact file:line references. Do not modify anything; do not attempt the broader task.
```

Resolution order (mirrors pi): project `agents/<name>.md` overrides a global one, which overrides a
built-in of the same name.

### The `Tools` allow-list is the load-bearing seam

It does **two** jobs at once:

1. **Capability restriction** — a sub-agent gets only the named built-ins (a subset of the parent's
   `a.byName` pool), so a scout literally cannot shell-write even if its prompt goes rogue.
2. **Concurrency-safety gate** — a type may set `Parallel: true` **only if** every tool it names is
   read-only (no writes to the shared registry / memory / audit / filesystem). This is what makes the
   fan-out in §4 safe, and it is checked when the catalog loads, not at spawn time.

Built-in types to ship first: `researcher` (`web_search`, `web_fetch`; `Parallel`) and `scout`
(read-only shell + read_self_docs; `Parallel`; operates on the workspace — see
[`workspace.md`](workspace.md)). A `general-purpose` type (inherits the parent's full tool set,
sequential only) can follow. A type's `Prompt`/`PromptMode` compose via the same seam as the main
agent — see [`prompts.md`](prompts.md) §3.

---

## 3. The spawn tool

A single trusted, host-side built-in — **not** exposed to the sandbox (same reasoning as `status`:
the capability broker does `http.MethodGet` only, and starting a sub-run is a privileged act, so it
must not be reachable from authored Lua via `call_tool`).

```
spawn_agent({ "type": "researcher", "task": "…" }) → final text of the sub-run
```

- **Foreground, blocking.** From the coordinator's view it's an ordinary tool call that returns a
  string; under the hood it builds a child agent and runs it to completion.
- **Construction:** `newAgent(parent.provider, effectiveModel, type.Prompt, subset(parent.byName,
  type.Tools), childObs)`. The child shares the concurrency-safe `provider` client and (for
  read-only tools) reuses the parent's tool instances; it gets **no** `responseFormat`.
- **Events:** the child emits through a wrapper that stamps the sub-run so the CLI/log can indent or
  label it (see `Event.Worker`/a sub-run id in §4).
- **Depth budget:** the config carries a remaining spawn depth; `spawn_agent` refuses at 0 and passes
  `depth-1` to its child. This is `Hops` from §7, applied in-engine — it makes "an agent that spawns
  agents" terminating by construction. Ship with depth 1 (a coordinator may spawn leaves; leaves may
  not spawn), widen only deliberately.

### Wiring

`ExecutorConfig` gains one optional field, `AgentCatalog *AgentCatalog` (nil ⇒ `spawn_agent` omitted,
exactly like the other optional deps gate their built-ins). No positional churn — that's why the
struct refactor landed first.

---

## 4. Use case — parallel read-only executors (deferred; latency optimization)

*Sequencing: deferred under `plan.md`'s experimentation-first ordering. The foreground spawn tool (§3)
ships first; this fan-out is a later optimization and a special case of a parallel `spawn_agents` batch
(§5). The design below stands unchanged — only its schedule moved.*


The concrete payoff: split an I/O-bound task into independent read-only sub-questions and run them
**concurrently**, then synthesize. Research fan-out is the canonical case; a repo-wide scout sweep is
the same shape.

### Two ways to drive it (pick a primary)

- **Model-driven** — a batch tool `spawn_agents([{type, task}, …])` that fans out internally, gated to
  `Parallel` types. The coordinator decides at runtime to parallelize. Most general; costs tokens
  (the model reasons about decomposition) and is less predictable.
- **Code-driven** — a fixed pipeline `RunResearchTurn`: a research planner decomposes the task into
  sub-questions → fan out over `researcher` agents → the executor synthesizes. Deterministic, cheap,
  rigid.

**Recommendation:** build the **code-driven** pipeline first (it's the smaller, testable, predictable
slice and needs no new model-facing tool), and expose the **same** fan-out helper under a
`spawn_agents` tool afterward. They share one implementation, so this is sequencing, not a fork.

### Shape (code-driven)

```
researchPlanner.Run ──► Plan{ RefinedTask, Subtasks[] }     (sequential)
                              │
                    FanOutResearch (errgroup, bounded by limit)
                    ├─ agent#0.Run(sub0) ─┐
                    ├─ agent#1.Run(sub1) ─┤  N children of a Parallel type, shared provider
                    └─ agent#k.Run(subk) ─┘
                              │  findings[] in subtask order
                    executor.Run(brief)                      (sequential synthesis)
```

### Load-bearing facts about the current code

- `Agent` holds mutable per-run state (`a.messages`, `a.task`, written in `Run`). **One `Agent` per
  goroutine — never share one across goroutines** (data race).
- A `Parallel` type names only read-only tools, so its workers touch **none** of the executor's shared
  registry / sandbox / audit-writer — sidestepping the hard concurrency questions.
- `Run(ctx, string) (string, error)` is the unit to parallelize. The `provider.Provider` is the one
  shared dependency (HTTP-backed; concurrent `Step` is safe).
- `Plan` (`internal/agent/plan.go`) has no subtasks field yet — decomposition is the one addition.

### Tasks (files they touch)

- [ ] `internal/agent/agenttype.go` — the `AgentType` value, a catalog with built-in `researcher` /
  `scout`, `agents/*.md` frontmatter loading, and a `Parallel ⇒ read-only tools` validation at load.
- [ ] `internal/agent/agent.go` — a `newSubAgent(parent, AgentType, obs)` factory (a `newAgent` with a
  tool subset selected from `a.byName`); mark which built-ins are read-only.
- [ ] `internal/agent/plan.go` — add `Subtasks []string json:"research_subtasks"` to `Plan` (+ schema
  property). Empty ⇒ no fan-out, run the executor directly. A dedicated `NewResearchPlanner` prompt asks
  for decomposition so the normal planner (run.go) is unaffected.
- [ ] `internal/agent/fanout.go` — `FanOutResearch(ctx, provider, model, obs, subtasks, limit)
  ([]string, error)`: `errgroup.WithContext` + `g.SetLimit(limit)`; one sub-agent per goroutine; write
  `findings[i]` (disjoint indices ⇒ race-free without a mutex); results in subtask order. `SetLimit`
  **is** the provider rate-limit semaphore (§6). Depends on `golang.org/x/sync` (in the module cache).
- [ ] `internal/agent/observer.go` — add `Worker int` to `Event`; a `sync.Mutex`-guarded observer
  wrapper (`workerObs`) that serializes `Emit` and stamps the worker index. The mutex is **one instance
  shared by all workers**, created in `FanOutResearch` — not one per worker.
- [ ] Orchestration `RunResearchTurn(...)`: planner → `FanOutResearch` → build a brief (`RefinedTask` +
  labelled findings) → `executor.Run(brief)`.
- [ ] `cmd/chat.go` — a `--research` flag (alongside `--plan`) routing a turn through
  `RunResearchTurn`. Off by default.
- [ ] Tests: fan-out with a fake provider (ordering, `limit` respected, first-error cancels siblings,
  `ctx` cancel stops in-flight workers); observer wrapper is race-clean under `-race`.

### Acceptance

- `go test -race ./internal/agent/...` green — the fan-out is the first concurrent path in the agent
  package, so `-race` is the gate.
- With `--research`, a task decomposes, N workers run concurrently bounded by `limit`, and the executor
  synthesizes; without it, behavior is unchanged.
- Ctrl-C / run cancel stops in-flight workers (ctx threads into every `Run`).

### `errgroup` dependency

`errgroup.SetLimit` needs `golang.org/x/sync` (already in the module cache; add it as a direct require).
If ever undesirable, hand-roll: `sync.WaitGroup` + a buffered `chan struct{}` of size `limit` as the
semaphore + a `sync.Once`/atomic for the first error + a `context.CancelFunc` to stop siblings — same
semantics, ~15 more lines.

---

## 5. Orchestration: by-model vs in-code

The spawn tool and the code pipeline are **two control strategies over the same worker machinery** —
compatible, not competing.

| | In-code pipeline (§4) | Model-driven spawn tool (§3) |
|---|---|---|
| Who decides to delegate | fixed Go orchestration | the coordinator model, at runtime |
| Predictability / cost | deterministic, cheap | flexible, more tokens |
| Best for | a known decomposition (research/scout sweep) | open-ended tasks where delegation isn't known up front |
| Machinery | `RunResearchTurn` + `FanOutResearch` | `spawn_agent` / `spawn_agents` built-ins |

Chosen ordering (`plan.md`): ship the **model-driven foreground spawn tool first** (it unlocks
organization experiments from prompts, with no concurrency), and add the in-code fan-out pipeline
later as a latency optimization over the *same* worker machinery. Offering both is mild redundancy
(two doors to one room), acceptable because the model-driven path generalizes beyond research while the
pipeline stays the cheap, predictable default when a decomposition is known up front.

---

## 6. Concurrency hazards (where a naive version breaks)

Applies to every parallel path (§4 and any future `spawn_agents`). This is why the observer wrapper and
`SetLimit` are mandatory, not optional.

| Hazard | Why | Mitigation |
|---|---|---|
| Sharing one `Agent` across goroutines | `Run` mutates `a.messages` / `a.task` | one sub-agent per goroutine |
| Parallel worker with write tools | races on the single-writer registry / memory / audit | `Parallel ⇒ read-only tools`, enforced at catalog load (§2) |
| Observer races | N goroutines call `obs.Emit`; the CLI/hub sink was written sequential | `workerObs` — one shared mutex, tagged with worker index |
| Interleaved events | parallel workers' events arrive mixed on one stream | stamp `Event.Worker`; frontend demuxes |
| Usage accounting races | the Phase 6a usage observer is now written from many goroutines | same mutex-guarded wrapper covers it |
| Unbounded fan-out | 20 subtasks → 20 simultaneous model calls → 429 | `g.SetLimit(limit)` |
| Orphaned workers on cancel | Ctrl-C / run cancel must stop in-flight workers | `errgroup.WithContext`; `ctx` into every `Run` |
| Runaway recursion | a sub-agent spawns sub-agents without bound | spawn-depth budget (§3), default depth 1 |

Note the sequential spawn path (§3 default) is exempt from the first six rows: with one agent running at
a time there is no concurrent access to the shared stores, so even a *write-capable* type is safe there.
Parallelism is the only thing that forces the read-only restriction.

---

## 7. Cross-engine escalation (reserve) — the `Isolation` field

For when a sub-agent earns a distinct identity / trust tier. The same `spawn_agent(type, task)` call,
with the type's `Isolation: "engine"`, drives another `serve` process over the existing engine HTTP API
instead of spawning an in-process child. **Nothing here changes the kernel** (`Engine`, `Broker`,
capability model); it sits on top of the current API.

### Promote a role to its own engine only when one of these is true

1. **Different trust tier** — the sub-agent runs untrusted authored code / shell you want sandboxed away
   from the coordinator's authority. Tier separation only *means* something across a process boundary.
2. **Standing specialist** — its own persistent memory/tools you want isolated and reused across tasks.
   That's an identity, not a worker.
3. **Independent fault/resource domain** — a sub-run can hang or blow memory and must not take the
   coordinator and siblings down with it.

None of these is implied by "parallel read-only executors." **Not a reason:** rate limits — N goroutines
and N processes draw from the *same* provider key's pool; bound concurrency with a semaphore regardless.

### Why a host built-in, not an authored tool

The `Broker` does `http.MethodGet` only (`internal/capability/broker.go`), and `POST /runs` is a POST —
an authored tool **cannot** start a sub-run under the existing capability set. Widening the broker to
POST would hand every authored tool arbitrary-write, breaking deny-by-default. So spawn is a trusted
host built-in, configured with a worker-map at `serve` start.

### Flow

`spawn_agent(engine-type, task)` → `POST http://127.0.0.1:<worker>/runs {task, meta}` → poll
`GET /runs/{id}` until `state != running` → return `RunInfo.Result` or `Error`. Blocking from the
model's view; async on the wire.

### Forward-compat substrate to lock in from the first commit

Add metadata **additively** to `POST /runs` now, so promoting a leaf to a sub-coordinator later is a
config change, not a wire break:

- `internal/api/http.go` — `startRunRequest{ Task string; Meta *RunMeta }` (`Meta` optional; absent ⇒
  human-originated root). Old bodies still decode.
- `internal/api/meta.go` — `RunMeta{ RequestID, Origin, Depth, Hops, Deadline }`. `RequestID`/`Origin`
  are **used now** (cross-engine audit correlation); `Depth`/`Hops`/`Deadline` are **reserved** so
  nesting needs no reshape. `Hops` is the re-delegation budget (refuse at 0, pass `Hops-1` to the child)
  — the same counter as the in-engine spawn depth (§3), just carried on the wire.
- Thread `Meta` into the run: `Engine.StartRunWithMeta(task, meta)` (keep `StartRun` delegating with a
  zero `RunMeta` — no caller breaks), and put `meta` on the run's context so the spawn built-in reads its
  inherited budget.
- Derivation lives in one function (`childMeta`): a human-originated run seeds `Hops` from `--max-hops`;
  a delegated run inherits and decrements. Flat and nested run the *same* code.

### Topology: flat, static, budgeted (not nested/dynamic) until proven needed

The safety variable is **dynamism**, not depth. Flat (only the coordinator delegates; workers are
leaves) makes termination structural. Depth becomes safe **only** when the delegation graph is
*statically configured* (worker-maps fixed at boot ⇒ provably a DAG) **and** *budgeted*. Ship flat
(`--max-hops 1`); adopt the three rules the day you go past one level:

| Rule | Buys |
|---|---|
| Worker-maps fixed at boot (no dynamic discovery) | a knowable DAG; lint for cycles at config load |
| `Hops` decremented per hop, hard-fail at 0 | defends against a config mistake that creates a cycle |
| Per-**request** fan-out / cost budget (whole tree, keyed by `RequestID`) | caps the geometric cost blow-up nesting invites — a *shared counter*, deliberately **out** of `RunMeta` |

Deferred until `--max-hops > 1`: a receive-side depth guard (`reject if Depth > localMax`) and the
fan-out/cost ledger — both only matter with nesting.

---

## 8. Open questions

- **Orchestration primary** — do we want *both* the code-driven pipeline and the model-driven spawn
  tools long-term, or does one subsume the other in practice? (§5 leans: pipeline first, spawn tools as
  the general superset.)
- **Decomposition quality** — does a planner schema field reliably produce good, non-overlapping
  subtasks, or is a dedicated decomposition step better? Measure before committing to `Plan.Subtasks`.
- **Synthesis surface** — should synthesis be the full `Executor` (tools to act on findings) or a
  narrower synthesizer agent? Start with the executor; split if it over-reaches.
- **Agent-type file location & precedence** — confirm the `agents/<name>.md` config-dir layout and the
  project-over-global-over-built-in override order.
- **Shared worker scratchpad** — should sibling workers see each other's partial findings, or stay fully
  independent? Independent is the simpler default; revisit if subtasks turn out interdependent.
- **Spawn-depth / `--max-hops` default & surface** — flag vs config-dir entry; only bites once depth > 1
  or cross-engine (§7) lands.
