# Multi-agent orchestration — design & roadmap

How this engine should run **multiple agents cooperating on one task** (e.g. a planner that
fans out to research sub-agents, then synthesizes). Captures the decisions reached in design
discussion so the next session doesn't re-derive them, plus a concrete near-term build and the
forward-compatible reserve path.

Companion to [`../design.md`](../design.md) (trust model, §1) and [`plan.md`](plan.md). This is
roadmap, **not** current behavior — nothing here is built yet.

---

## 0. The core decision — concurrency vs identity

The recurring confusion is treating "processes vs threads" as one question. It's two:

- **Unit of concurrency = a goroutine.** Parallel runs, sessions, streams. The engine already
  does this (`Engine.launch` spawns a goroutine per run; `sessLocks` guards shared stores). Never
  fork a process per task.
- **Unit of *agent identity* = an OS process.** A *distinct agent* — its own trust tier, its own
  `memory.json` / `tools.json` / `audit.jsonl`, its own data domain — is a separate `serve`
  process. This is what the two-agent Fly layout already does, and it's the only boundary at which
  the design's guarantees actually hold (single-writer JSON stores; tier enforcement across an
  address-space boundary; a panic/OOM in one agent not killing the others).

**The question that decides any given case:** is this sub-agent a *standing specialist* (persistent
memory/tools/tier, reused across many tasks) or an *ephemeral worker* (born for one subtask,
returns a result, dies)?

- Ephemeral worker ⇒ **goroutine in the same engine** (it's not a distinct identity; it collapses
  into "goroutine per run", which we already have).
- Standing specialist ⇒ **separate engine** (distinct identity/trust/data ⇒ own process).

Planner + research fan-out is the **ephemeral** case → in-engine (§2). Promote a role to its own
engine only when a trigger in §4 fires.

---

## 1. Two orchestration modes

| | In-engine fan-out (§2) | Cross-engine delegation (§3) |
|---|---|---|
| Sub-agents are | goroutines in one `serve` | separate `serve` processes |
| Handoff | Go values / structs, in memory | task→string over HTTP, poll for result |
| Isolation | shared address space + stores | OS process, own config-dir, own tier |
| Fault domain | shared (a panic can take siblings down) | independent crash/restart domains |
| Audit | one log, whole tree correlated | fragmented; stitch by `request_id` |
| Machinery to build | `errgroup` + N agents + safe observer | `delegate` tool + `AwaitResult` + `RunMeta` + worker-map |
| When | ephemeral parallel workers of one task | standing specialists / distinct trust tiers |

**Default to in-engine.** Reach for cross-engine only when §4's triggers apply. The two are not
exclusive — the same task decomposition can run in-engine today and a role can be promoted to a
separate engine later without reshaping the protocol (§3 is designed for that).

---

## 2. In-engine fan-out (near-term, recommended) — planner → researchers → synthesis

Runs entirely on the existing `Agent`/`Planner`/`Executor` types. No HTTP, no new engine, no
`RunMeta`. The only genuinely new code is a researcher constructor, a fan-out helper, a
concurrency-safe observer wrapper, and one planner-schema field.

### Shape

```
planner.Run ──► Plan{ RefinedTask, Subtasks[] }        (sequential; today + decomposition)
                         │
              FanOutResearch (errgroup, bounded)
              ├─ researcher#0.Run(sub0) ─┐
              ├─ researcher#1.Run(sub1) ─┤  N fresh Agents, shared provider client
              └─ researcher#k.Run(subk) ─┘
                         │  findings[] in subtask order
              executor.Run(brief)                       (sequential synthesis)
```

### Load-bearing facts about the current code

- `Agent` holds mutable per-run state (`a.messages`, `a.task`, written in `Run`). **One `Agent`
  per goroutine — never share one across goroutines** (data race).
- `NewPlanner` is already a **web-only** agent (web_search / web_fetch / ask_user; no shell, no
  registry/glue). A research worker wants the same shape, so workers touch **none** of the
  executor's shared registry / sandbox / audit-writer — sidestepping the hard concurrency
  questions.
- `Run(ctx, string) (string, error)` is the unit to parallelize. The `provider.Provider` is the
  one shared dependency (HTTP-backed; concurrent `Step` is safe).
- `Plan` (`internal/agent/plan.go`) has no subtasks field yet — decomposition is the one addition.

### Tasks (files they touch)

- [ ] `internal/agent/agent.go` — `NewResearcher(p, model, obs) *Agent`: web-only agent
  (`WebSearchDDG`, `WebFetch`), a `researcherPrompt` ("investigate this one sub-question, return a
  findings brief with source URLs; do not attempt the broader task"), no `responseFormat`.
- [ ] `internal/agent/plan.go` — add `Subtasks []string json:"research_subtasks"` to `Plan` and the
  matching property to `planResponseFormat.Schema`. Empty ⇒ no fan-out, run executor directly.
- [ ] `internal/agent/fanout.go` — `FanOutResearch(ctx, p, model, obs, subtasks, limit) ([]string, error)`:
  `errgroup.WithContext` + `g.SetLimit(limit)`; one `NewResearcher` per goroutine; write results to
  `findings[i]` (disjoint indices ⇒ race-free without a mutex). Return findings in subtask order.
  `SetLimit` **is** the provider rate-limit semaphore (see §5).
- [ ] `internal/agent/fanout.go` — `workerObs(inner, i)`: a `sync.Mutex`-guarded observer wrapper
  that serializes `Emit` and stamps a worker index (add `Worker int` to `Event`) so interleaved
  research streams can be demuxed. The mutex is **one instance shared by all workers**, created in
  `FanOutResearch` — not one per worker.
- [ ] Orchestration `RunResearchTurn(...)`: `planner.Run` → unmarshal `Plan` → `FanOutResearch` →
  build a brief (`RefinedTask` + labelled findings) → `executor.Run(brief)`.
- [ ] `cmd/chat.go` — a `--research` flag (alongside the existing `--plan`) that routes `runTurn`
  through `RunResearchTurn`. Off by default.
- [ ] Tests: fan-out with a fake provider (ordering, `limit` respected, first-error cancels
  siblings, `ctx` cancel stops in-flight workers); observer wrapper is race-clean under `-race`.

### Acceptance

- `go test -race ./internal/agent/...` green — the fan-out is the first concurrent path in the
  agent package, so `-race` is the gate.
- With `--research`, a task decomposes, N researchers run concurrently bounded by `limit`, and the
  executor synthesizes; without it, behavior is unchanged.
- Ctrl-C / run cancel stops in-flight researchers (ctx threads into every `Run`).

### Dependency note

`errgroup.SetLimit` needs `golang.org/x/sync` ≥ v0.1.0. If not vendored, either add it or hand-roll:
`sync.WaitGroup` + a buffered `chan struct{}` of size `limit` as the semaphore + a `sync.Once`/atomic
for the first error + a `context.CancelFunc` to stop siblings. Same semantics, ~15 more lines.

---

## 3. Cross-engine delegation (reserve) — the forward-compatible substrate

For when a sub-agent earns a distinct identity/trust tier (§4). One agent drives another over the
existing engine HTTP API. **Nothing here changes the kernel** (`Engine`, `Broker`, capability model);
it all sits on top of the current API.

### Why a built-in, not an authored tool

The `Broker` does `http.MethodGet` only (`internal/capability/broker.go`) and `POST /runs` is a POST —
so an authored tool **cannot** start a sub-run with the existing capability set. Widening the broker to
POST would hand every authored tool arbitrary-write, breaking deny-by-default. So `delegate` is a
**trusted host-side built-in** (like `status`), configured with a worker-map at `serve` start, not
exposed to the sandbox.

### Flow

`delegate(worker, task)` → `POST http://127.0.0.1:<worker>/runs {task}` → poll `GET /runs/{id}` until
`state != running` → return `RunInfo.Result` (which already carries the final text) or `Error`.
Blocking from the model's view; async on the wire.

### Topology decision — flat, static, budgeted (not nested/dynamic)

The safety variable is **not depth, it's dynamism**. Flat (only the coordinator has `delegate`;
workers are leaves) makes termination structural — no cycles possible, no depth counter needed, one
delegator to reason about, shallow audit tree, legible cost. Depth becomes safe **only** when the
delegation graph is *statically configured* (worker-maps fixed at boot ⇒ provably a DAG) **and**
*budgeted*. So: ship flat; the three rules that make levels safe (below) get adopted the day you go
past one level, not before.

### Forward-compat substrate to lock in from the first commit

Add metadata **additively** to `POST /runs` now, even though flat barely uses it, so promoting a leaf
to a sub-coordinator later is a config change, not a wire break:

- `internal/api/http.go` — `startRunRequest{ Task string; Meta *RunMeta }` (`Meta` optional; absent ⇒
  human-originated root). Old bodies still decode.
- `internal/api/meta.go` — `RunMeta{ RequestID, Origin, Depth, Hops, Deadline }`. `RequestID`/`Origin`
  are **used now** (cross-engine audit correlation); `Depth`/`Hops`/`Deadline` are **reserved** —
  they travel from commit one so nesting needs no reshape. `Hops` is the re-delegation budget
  (`delegate` refuses at 0, passes `Hops-1` to the child); flat runs with `--max-hops 1`, so nesting
  is off *by policy (config), not protocol*.
- Thread `Meta` into the run: `Engine.StartRunWithMeta(task, meta)` (keep `StartRun` delegating to it
  with a zero `RunMeta` — no caller breaks), and put `meta` on the run's context (`WithMeta` /
  `MetaFromContext`) so the `delegate` built-in can read its inherited budget.
- Derivation lives in one function (`childMeta`): a human-originated run (no `RequestID`) is the tree
  root and seeds `Hops` from `--max-hops`; a delegated run inherits and decrements. Flat and nested
  run the *same* code — flat is just `maxHops == 1` with the reserved fields dormant.

### The three rules that turn nesting from dangerous to safe (adopt only when `--max-hops > 1`)

| Rule | Buys |
|---|---|
| Worker-maps fixed at boot (no dynamic discovery) | the graph is a knowable DAG; lint for cycles at config load |
| `Hops` decremented per hop, hard-fail at 0 | defends against a config mistake that creates a cycle |
| Per-**request** fan-out / cost budget (whole tree, keyed by `RequestID`) | caps the geometric cost blow-up nesting invites — a *shared counter*, which per-message `Hops` (depth-only) cannot express; deliberately **out** of `RunMeta` |

### Deliberately deferred

Receive-side depth guard (`reject if Depth > localMax`) and the fan-out/cost ledger — both only matter
with nesting; add them alongside the `--max-hops > 1` flip.

---

## 4. When to promote a role from in-engine to a separate engine

Default is in-engine (§2). Move a role to its own `serve` (§3) **only** when one of these is true:

1. **Different trust tier** — the sub-agent runs untrusted authored code / shell and you want it
   sandboxed away from the coordinator's authority. Tier separation only *means* something across a
   process boundary.
2. **Standing specialist** — the sub-agent has its own persistent memory/tools you want isolated and
   reused across many tasks. That's an identity, not a worker.
3. **Independent fault/resource domain** — a sub-run can hang or blow memory and must not take the
   coordinator and siblings down with it.

None of these is implied by "planner + ephemeral research fan-out."

**Not a reason:** rate limits. N goroutines and N processes draw from the *same* provider key's
RPM/TPM pool — separate engines don't help unless they use separate keys. Bound concurrency with a
semaphore regardless (§2's `SetLimit`).

---

## 5. Concurrency hazards (the part that actually bites)

The fan-out code is easy; these are where a naive version breaks. Applies to §2 directly and is why
the observer wrapper and `SetLimit` are mandatory, not optional.

| Hazard | Why | Mitigation |
|---|---|---|
| Sharing one `Agent` across goroutines | `Run` mutates `a.messages` / `a.task` | `NewResearcher` per goroutine |
| Observer races | N goroutines call `obs.Emit`; the CLI/hub sink was written sequential | `workerObs` — one shared mutex, tagged with worker index |
| Interleaved events | parallel workers' events arrive mixed on one stream | stamp `Event.Worker`; frontend demuxes |
| Usage accounting races | Phase 6a usage observer now written from many goroutines | same mutex-guarded wrapper covers it |
| Unbounded fan-out | 20 subtasks → 20 simultaneous model calls → 429 | `g.SetLimit(limit)` |
| Orphaned workers on cancel | Ctrl-C / run cancel must stop in-flight researchers | `errgroup.WithContext`; `ctx` into every `Run` |

---

## 6. Open questions

- **Decomposition quality** — does the planner reliably produce good, non-overlapping research
  subtasks via a schema field, or is a dedicated decomposition prompt/step better? Measure before
  committing to the `Plan.Subtasks` approach.
- **Synthesis surface** — should synthesis be the full `Executor` (shell/tools to act on findings) or
  a narrower synthesizer agent? Start with the executor; split if it over-reaches.
- **Shared research memory** — should sibling researchers see each other's partial findings
  (shared scratchpad) or stay fully independent? Independent is simpler and the default here; revisit
  if subtasks turn out to be interdependent.
- **`--max-hops` default & config surface** — flag vs config-dir entry; only relevant once §3 lands.
