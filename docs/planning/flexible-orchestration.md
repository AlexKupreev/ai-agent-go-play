# Flexible, explainable orchestration and Telegram control plane

> **Role: target architecture and detailed acceptance criteria.** Current cross-project priority
> and dependency order live in [`roadmap.md`](roadmap.md); this document supplies the design for its
> R0-R7 orchestration and frontend work.

An implementation plan for making the agent's loop, sub-agent behavior, standing guidance, and
Telegram frontend configurable **without turning safety rules into prompt suggestions**. This
aggregates the open flexibility/informativeness findings from
[`review-2026-08.md`](review-2026-08.md), the current deliberate-loop design in
[`../adr/chat-planner.md`](../adr/chat-planner.md), and the Telegram/session behavior described in
[`../usage.md`](../usage.md).

**Status: proposal, not built.** This document is deliberately outside the embedded reference-doc
set. It describes target behavior and an ordered implementation that another agent can execute in
independent, shippable slices.

The proposal has one central distinction:

- **Guidance** says what the user wants, what context matters, and how answers should feel.
- **Workflow policy** decides which execution phases run, which roles/models they use, how much
  delegation is permitted, and what budgets apply.
- **Kernel invariants** enforce security, containment, approvals, and hard resource limits. Neither
  guidance nor a workflow profile may weaken them.

---

## 0. Outcome

After this plan lands, a user can:

- choose `/profile quick`, `/profile adaptive`, or `/profile thorough` per conversation;
- control planning, critique, revision, delegation, model-role, and budget behavior without a
  rebuild;
- give global, space, or session guidance from Telegram rather than editing files over SSH;
- see *why* a planner, critic, or sub-agent was used, without exposing chain-of-thought;
- list and resume Telegram conversations after an engine or bot restart;
- change model, tier, space, profile, deliberation, and notes consistently across CLI and Telegram;
- receive long Telegram answers reliably, cancel queued/running work, and recover from errors using
  instructions included in the error itself.

An operator retains hard ceilings: a session may request less planning, fewer agents, a lower tier,
or a smaller budget, but cannot exceed serve-side security/cost ceilings.

---

## 1. Current state and the sources of illogical behavior

### 1.1 The loop is a fixed pipeline

Deliberate session turns are assembled in `cmd/serve.go` and executed by `cmd/deliberate.go`:

```
planner -> executor -> critic -> (planner -> executor)*
```

Planning and critique are process-wide startup choices (`--no-plan`, `--no-critique`,
`--max-revisions`). Every deliberate message—including conversation and simple explanation—pays
for the planner; every successful executor answer pays for the critic when critique is enabled.
The critic can trigger a second full plan/execution cycle from a subjective verdict.

The pipeline itself lives in `cmd`, even though local chat and the headless engine both use it. This
makes orchestration policy a CLI wiring concern rather than a first-class domain concept.

### 1.2 Delegation is a foreground string-returning tool

`spawn_agent(type, task)` constructs a fresh child, blocks until completion, and returns the child's
final text. The child receives a self-contained task, not the parent conversation. Agent types can
select a model, tools, prompt mode, and declare read-only parallel safety, but:

- there is no parallel batch execution yet;
- `parallel: true` validates safety but does not itself create concurrency;
- the result has no findings/evidence/artifacts/uncertainty contract;
- only depth is budgeted; there is no per-turn agent-count or token/tool budget;
- the parent receives no machine-readable record of why delegation happened or how to reconcile
  conflicting workers.

### 1.3 Guidance requires filesystem access

Standing behavior is controlled by `SYSTEM.md`, `AGENTS.md`, `PLANNER.md`, `CRITIC.md`, agent-type
files, and space notes. On the Telegram-first deployment this normally requires SSH. `SYSTEM.md`
also replaces the entire built-in executor prompt, unintentionally removing runtime constraints and
the untrusted-content rule.

### 1.4 Telegram is a thin but incomplete peer client

The current frontend maps an in-memory `chatID -> sessionID`, posts turns, and forwards event text.
Important gaps:

- the binding is lost on restart even though engine sessions persist;
- outbound messages are not split to Telegram's 4,096-character limit and send errors are mostly
  discarded;
- `/start` silently acts like `/new` and archives the current conversation;
- close/purge failures are logged while the user is still told they succeeded;
- `/purge` has no confirmation, `/tier` is missing, and `/space` cannot list/validate spaces;
- group topics, replies, command suffixes (`/help@bot`), queue state, cancellation, and progress
  rendering are not represented in the transport-neutral message shape;
- downloads and command handling run on the update-dispatch path, so slow frontend work can delay
  approval callbacks.

### 1.5 The system cannot explain its effective behavior

`status` reports resources and basic identity but not the active space, workspace, guidance
provenance, workflow profile, planner/critic mode, delegation budget, or effective ceilings. There
is no read-only effective-config endpoint, and `/reload` reports success without a diff.

---

## 2. Design principles and hard boundaries

1. **Policy is data; the loop is code.** Profiles select code-enforced behavior. Do not implement
   profiles by injecting a paragraph that asks the model to behave differently.
2. **Safe defaults remain backward compatible.** Existing sessions with no workflow fields retain
   today's deliberate behavior during rollout.
3. **Requests are clamped by operator ceilings.** The established tier rule generalizes to planning,
   delegation, paid tools, token/tool budgets, workspace containment, and concurrency.
4. **Auto decisions are visible.** Record route/review/delegation decisions and short reasons, never
   hidden reasoning or chain-of-thought.
5. **One orchestration implementation serves every frontend.** CLI, API, and Telegram select policy;
   none reimplement the pipeline.
6. **One command vocabulary serves interactive frontends.** A shared command service owns semantics;
   CLI and Telegram only parse/render.
7. **Frontend delivery is part of correctness.** A run is not usefully complete if the final answer
   could not be delivered.
8. **Persistence follows recoverability.** Session settings, user guidance, frontend bindings, and
   orchestration traces survive restart; transient typing/progress state need not.
9. **Parallelism is read-only first.** Concurrent sub-agents cannot receive state-writing tools,
   mutate shared manifests, or request approvals in v1.
10. **Errors carry recovery instructions.** A failure should say what the user can do next.

---

## 3. Target architecture

```
CLI / Telegram / future web
          |
          v
  shared command service ----------------------+
          |                                     |
          v                                     v
  session + binding stores              control-plane queries
          |                              (status/config/spaces/profiles)
          v
    Orchestrator.RunTurn
          |
          +--> resolve profile + session overrides + operator ceilings
          +--> route: bare executor | deliberate
          +--> planner (when selected)
          +--> executor
          +--> critic/revision (when selected)
          +--> sub-agent manager (bounded foreground/batch workers)
          +--> decision trace + usage/budget accounting
          |
          v
  existing agent/tool/provider/capability/sandbox layers
```

### 3.1 New `internal/orchestration` package

Move the domain logic from `cmd/deliberate.go` into a package that knows no Cobra, HTTP, Telegram,
or filesystem layout:

```
internal/orchestration/
  policy.go       profile types, validation, merge, ceiling clamp
  router.go       structured route decision for planning=auto
  turn.go         phase state machine (current deliberate loop generalized)
  trace.go        user-visible decision record
  budget.go       shared per-turn counters and refusal reasons
  result.go       TurnResult and phase summaries
```

The package receives builders/interfaces for planner, executor, critic, artifacts, and event sinks.
It may import `internal/agent`, `provider`, and `artifact`; it must not import `cmd` or `api`.

Suggested top-level shapes (names may change, semantics should not):

```go
type Orchestrator interface {
    RunTurn(context.Context, TurnRequest) (TurnResult, error)
}

type TurnRequest struct {
    RunID, SessionID string
    Text             string
    History          []Turn
    ManifestView     string
    Policy           ResolvedPolicy
    Runtime          RuntimeSnapshot
    Events           EventSink
}

type TurnResult struct {
    Answer string
    Trace  DecisionTrace
    Usage  provider.Usage
}
```

`api.TurnRunner` remains the engine seam. The cmd layer builds one orchestrator and adapts it to the
existing interface. Local deliberate chat calls the same orchestrator directly.

### 3.2 Phase state machine

Represent phases explicitly instead of nested conditionals:

```
received -> routed -> [planned] -> executing -> [reviewing -> revising]* -> completed
                                      |                 |
                                      +------ failed ---+
```

The state machine gives cancellation, tracing, budgets, queue reporting, and tests stable hook
points. It does **not** persist a live goroutine; restart recovery remains "run interrupted, session
history intact" rather than trying to resume an arbitrary model call.

### 3.3 Workflow profiles

Profiles live in config, with built-in defaults available when no config block exists:

```yaml
workflow_profiles:
  adaptive:
    planning: auto          # off | auto | always
    critique: auto          # off | auto | always
    max_revisions: 1
    clarification: when_blocked  # never | when_blocked | allowed
    progress: concise       # quiet | concise | verbose
    delegation:
      mode: auto            # off | auto
      max_agents: 3
      max_depth: 1
      parallel_readers: true
    models:
      planner: gpt-4o-mini
      executor: gpt-4.1
      critic: gpt-4o-mini
    budgets:
      model_steps: 25
      tool_calls: 30
      paid_tool_calls: 2
```

Built-in profiles:

| Profile | Planning | Critique | Delegation | Intended use |
| --- | --- | --- | --- | --- |
| `quick` | off | off | off by default | conversation, translation, simple lookup |
| `adaptive` | auto | auto | bounded | normal interactive use |
| `thorough` | always | always | bounded, parallel readers | research/high-assurance work |
| `deliberate` | always | always | current depth behavior | compatibility with today's default |

During migration, an absent profile resolves to `deliberate`. Change the new-session default to
`adaptive` only after evaluations show it is at least as successful and materially cheaper/faster.

### 3.4 Merge and ceiling semantics

Resolve policy in one function, with provenance:

```
built-in profile
  <- config profile definition
  <- serve defaults/ceilings
  <- session sticky profile + sparse overrides
  <- per-turn sparse overrides
  -> clamp to operator ceilings
```

Use pointer/optional fields for booleans and integers so "unset" differs from `false`/`0`.
Return `ResolvedPolicy` plus `[]PolicySource`; status and `/why` render the latter.

Examples of ceilings:

- `serve --no-plan` prevents a session from enabling planning.
- serve `max_revisions=1` clamps a requested value of 3.
- `max_agents=0` removes delegation tools.
- `paid_tool_calls=0` prevents `scrape`, regardless of prompt/profile.
- requested tier continues to clamp to the serve tier.

Do not overload trust tier with cost/orchestration decisions: these are independent axes.

---

## 4. Adaptive routing and review

### 4.1 `planning: auto`

Use a small structured router, not free-form prompt parsing. Its schema:

```go
type RouteDecision struct {
    Deliberate    bool   `json:"deliberate"`
    Review        bool   `json:"review"`
    ReasonCode    string `json:"reason_code"`
    Reason        string `json:"reason"`
    Risk          string `json:"risk"` // low | medium | high
}
```

Allowed reason codes should be an enum such as `simple_conversation`, `single_step`,
`multi_step`, `ambiguous`, `external_effect`, `paid_tool`, `high_assurance`, and
`explicit_user_choice`. The reason is a short operational explanation, not hidden reasoning.

Routing input contains the current user message, a bounded conversation summary, attachment
metadata, available capabilities, and policy—not the full tool output history. Default router model
is the planner model. If routing fails, fall back to deliberate mode; correctness beats a small cost
saving.

Explicit user choices beat the router:

- `/profile quick` or per-turn `planning=off` skips it.
- `/profile thorough` forces deliberation.
- natural-language requests such as "think carefully" may be recognized by the router but do not
  override an operator ceiling.

### 4.2 `critique: auto`

Review when at least one observable condition holds:

- the route marks medium/high risk or requests review;
- the plan contains explicit success criteria;
- a tool failed, timed out, or returned a partial-result marker;
- the executor reached an iteration/budget boundary;
- the request asks for verification, comparison, destructive action, or high assurance;
- sub-agent reports conflict or material uncertainty.

Skip critique for ordinary conversation and clean low-risk execution. This decision is code-owned
and recorded in the trace. Critic failure continues to fail open by delivering the current answer,
but the answer gets a visible "review unavailable" note when the profile requested high assurance.

### 4.3 Completion and retry policy

Replace generic "keep trying" behavior with bounded categories:

- model iterations: existing executor `max_iterations`;
- plan revisions: `max_revisions`;
- repeated identical tool failure: default 1 retry, then return an actionable refusal/result;
- paid tools: separate hard call budget;
- sub-agents: count/depth/parallelism budgets;
- total model steps/tool calls: shared turn budget.

Budget exhaustion is a normal structured outcome, not a generic error. The final response says what
was completed, what remains, and which limit stopped work.

---

## 5. Sub-agent architecture

### 5.1 Keep agent types; extend their contract compatibly

Existing `agents/*.md` files remain valid. Add optional frontmatter:

```yaml
context: task             # task | summary | full (full must be explicitly operator-enabled)
result: report            # text | report
max_iterations: 10
budget_class: reader      # reader | worker
```

- `task` is today's behavior: only the self-contained delegated task.
- `summary` adds the bounded conversation/runtime summary selected by the orchestrator.
- `full` is expensive and may expose unrelated context; disallow unless the operator profile permits
  it.
- `text` preserves today's return string.
- `report` enforces the standard schema below.

Do not accept arbitrary safety/tier escalation in an agent file. A child inherits/clamps to the
parent/operator ceilings and receives only its declared tool subset.

### 5.2 Structured report

The standard child result should be strict structured output:

```go
type SubagentReport struct {
    Summary     string        `json:"summary"`
    Findings    []Finding     `json:"findings"`
    Evidence    []EvidenceRef `json:"evidence"`
    Artifacts   []ArtifactRef `json:"artifacts"`
    Assumptions []string      `json:"assumptions"`
    Uncertainty []string      `json:"uncertainty"`
}
```

The `spawn_agent` tool returns serialized JSON for report-mode children, which keeps the ordinary
tool-result channel unchanged. The parent prompt explains that it must reconcile reports and mention
material disagreements; it must not concatenate them blindly.

### 5.3 Shared delegation budget

Replace the depth-only integer with a concurrency-safe budget shared by the coordinator and its
children:

```go
type DelegationBudget struct {
    MaxAgents, MaxDepth, MaxParallel int
    // guarded counters and cancellation
}
```

Every spawn reserves a slot before construction and releases only the parallel slot on completion;
the total-agent count remains consumed for the turn. The budget is visible in status/trace.

Children remain unable to re-delegate by default. Nested delegation requires both profile permission
and a type that explicitly allows it; depth is still enforced even if prompts ask otherwise.

### 5.4 Parallel batch path

Add `spawn_agents(tasks[])` only after structured reports and shared budgets land:

- every selected type must have `parallel: true`;
- tool validation continues to require the static read-only set;
- v1 forbids approvals, authored tools, shared-state writes, artifact-manifest mutation, and
  `ask_user` inside parallel workers;
- preserve input order in returned results even if completion order differs;
- cancellation stops all siblings;
- events carry `worker_id` and `agent_type` so frontends can group them;
- one worker failure does not discard successful siblings; the batch result carries per-worker
  status.

Sequential general-purpose workers continue through `spawn_agent`.

### 5.5 Delegation decision trace

For each child, record:

- type/model and task summary;
- reason code (`independent_research`, `specialist`, `context_isolation`, etc.);
- tools/context mode/budget granted;
- start/end/status and usage;
- result summary plus evidence/artifact counts.

This is operational metadata, not the child's private reasoning.

---

## 6. Guidance architecture

### 6.1 Prompt layers

Compose executor guidance in this order:

```
immutable core: security + runtime constraints + factual tool/tier notes
operator base/persona: SYSTEM.md (supports {{base}})
operator append: config-dir AGENTS.md
workspace append: workspace AGENTS.md (tier-gated as today)
user global guidance: <workspace>/.agent/guidance.md
active-space notes: existing space.json notes
session guidance: persisted on Session
turn instruction: the current message/brief
```

Split the current `executorPrompt` into named blocks. Security, containment facts, and runtime
constraints always attach. `SYSTEM.md` may replace/wrap persona and tool doctrine but cannot remove
the immutable blocks. `{{base}}` substitutes the customizable built-in base; absent continues legacy
replace semantics for one compatibility cycle, with a warning in `agent prompts show`.

Planner/critic overrides retain their strict schemas. They may tune a role but cannot change its
output contract or workflow ceilings.

### 6.2 Guidance scopes

| Scope | Persistence | Intended content | Telegram surface |
| --- | --- | --- | --- |
| global/workspace | `.agent/guidance.md` | language, standing preferences, general constraints | `/guidance global ...` |
| active space | existing `space.json` notes | profile/context-specific facts and instructions | `/notes ...` |
| session | new `Session.Guidance` | temporary conversation instructions | `/guidance session ...` |
| turn | session history only | one-off request | ordinary message |

Limit global/space/session guidance independently (start at 4,000 chars each) and display the size.
Writes are atomic. Guidance is trusted user input only after frontend authorization, but still cannot
override kernel invariants.

### 6.3 Commands

Support the same operations in local/remote chat and Telegram:

```
/notes                         show active-space notes
/notes set <text>              replace
/notes add <text>              append
/guidance [global|session]     show
/guidance <scope> set <text>
/guidance <scope> add <text>
```

Natural-language guidance ("always answer this space in Polish") may be handled by the existing
`update_space_notes` tool, but explicit commands are deterministic and discoverable.

---

## 7. Control plane and persistence

### 7.1 Avoid growing `api.Engine` into every management concern

Introduce small service interfaces wired into the HTTP server:

```go
type ProfileService interface { List/Get(...) }
type SpaceService interface { List/Get/SetNotes(...) }
type EffectiveConfigService interface { Snapshot(...) }
type BindingStore interface { Get/Put/Delete/List(...) }
```

`cmd` supplies filesystem/config-aware implementations; `internal/api` owns transport and neutral
request/response types. This follows the existing `FileStore`, `RunStore`, and session-store seams.

### 7.2 API additions

```
GET   /status
GET   /config/effective
GET   /profiles
GET   /profiles/{name}
GET   /spaces
GET   /spaces/{id}
PUT   /spaces/{id}/notes
GET   /sessions/{id}/guidance
PUT   /sessions/{id}/guidance
GET   /sessions/{id}/runs
POST  /sessions/{id}/cancel
GET   /runs/{id}/orchestration
```

Extend `POST/PATCH /sessions` and `POST /sessions/{id}/turns` with sparse workflow options. Prefer a
nested `workflow` object for growth rather than adding a dozen top-level fields. Existing top-level
`model`, `tier`, and `space` remain compatible.

`GET /config/effective` includes model-role choices, tier ceiling, profile/limits, workspace, prompt
and agent-type provenance, configured secret **names**, and frontend state. It never returns API
keys, bot tokens, or secret values.

`POST /reload` should return a structured diff rather than `204`:

```json
{
  "changed": ["AGENTS.md", "profiles.adaptive"],
  "prompts": {"loaded": ["..."], "warnings": []},
  "agent_types": {"count": 4, "added": ["analyst"], "removed": []}
}
```

### 7.3 Session schema

Add optional fields with `omitempty` so existing JSON files load unchanged:

```go
type Session struct {
    // existing fields
    Profile  string                   `json:"profile,omitempty"`
    Workflow WorkflowOverrides        `json:"workflow,omitempty"`
    Guidance string                   `json:"guidance,omitempty"`
}
```

Session listings should include the effective profile and current running/queued state, but not full
guidance text.

### 7.4 Frontend binding store

Persist bindings under the config dir (JSON v1, interface ready for SQLite):

```go
type BindingKey struct {
    Frontend string // telegram
    Account  string // Telegram bot id/instance
    ChatID   int64
    ThreadID int64  // 0 outside forum topics
    UserID   int64  // used only by per-user group mode
}

type Binding struct {
    Key       BindingKey
    SessionID string
    UpdatedAt time.Time
}
```

Telegram binding modes:

- private chats: per chat (default);
- groups: disabled by default until explicitly configured;
- group shared: per chat/thread;
- group user: per chat/thread/user.

On startup, validate that a bound session still exists. A missing session drops the stale binding
and creates a new one on the next message. `/end` removes the binding only after archive succeeds;
`/purge` removes it only after purge succeeds.

---

## 8. Telegram architecture and UX

### 8.1 Richer neutral transport types

Carry Telegram concepts required for correct behavior:

```go
type Message struct {
    ChatID, UserID, ThreadID int64
    MessageID                int
    Text                     string
    Command                  *Command
    File                     *File
}

type SendRequest struct {
    ChatID, ThreadID int64
    ReplyTo          int
    Text             string
    Buttons          []Button
}
```

The live transport normalizes `/command@botname`, preserves captions/attachments, and ignores a
command addressed to another bot. Keep SDK types out of bot logic.

### 8.2 Reliable renderer

All outgoing text flows through one renderer:

- split by Unicode rune count to at most 4,096 characters, preferring paragraph/newline boundaries;
- attach inline buttons to the final relevant chunk;
- avoid unsupported parse mode initially; if Markdown is enabled later, escape and split after
  entity calculation;
- return delivery errors and record them against the run;
- retry only transient/rate-limit errors with bounded backoff and respect Telegram retry hints;
- if final delivery fails, log a durable delivery failure retrievable via `/status` or session resume.

No call site should discard `Send` errors with `_ =`.

### 8.3 Command service

Create a frontend-neutral command registry/service; Telegram and the two chat REPLs adapt it:

```
/help
/status
/why [last|run-id]
/new
/end
/purge                    confirmation button required
/sessions
/resume <id>
/cancel
/model [id|-]
/tier [safe|balanced|permissive|-]
/space [list|name|-]
/profile [list|name]
/deliberate [on|off|auto]
/notes ...
/guidance ...
/usage
/reload
```

`/start` performs onboarding/help and reports whether a session is active. It must never archive or
replace one. `/stop` should either alias `/cancel` or be removed; it must not ambiguously mean
"archive conversation."

Destructive `/purge` uses a short-lived confirmation callback containing a server-side nonce, not a
guessable raw session id. Expired or already-used callbacks fail safely.

### 8.4 Busy, queue, and cancellation behavior

The engine already serializes turns by session lock but accepts them without exposing queue state.
Make this explicit:

- default Telegram behavior: queue at most one additional turn per session;
- acknowledge immediately: `queued (#1)` or `working...`;
- reject beyond the limit with `/cancel` guidance;
- `/cancel` cancels the active run and optionally clears queued turns;
- while `ask_user` is pending, ordinary text answers it; commands remain commands; attachments get
  the existing "answer first" guidance;
- a new run cannot steal a pending answer from another run/session.

Expose queue position in the `PostTurn` response or a session-run endpoint. Do not infer it in the
Telegram frontend.

### 8.5 Progress without noise

Map the resolved profile's progress mode to Telegram behavior:

- `quiet`: typing indicator, final answer/errors/approvals only;
- `concise`: one editable progress message showing planning/executing/reviewing and sub-agent count;
- `verbose`: separate brief and worker summaries, still no raw tool chatter by default.

Prefer editing one progress message to emitting many transient messages. Preserve the full event
trace on disk/API.

---

## 9. Informativeness and decision traces

### 9.1 `DecisionTrace`

Persist a compact trace with final `RunInfo` and emit phase events while live:

```go
type DecisionTrace struct {
    Profile          string
    PolicySources    []string
    Route            RouteDecision
    PlannerUsed      bool
    CriticUsed       bool
    Revisions        int
    ReviewReason     string
    Delegations      []DelegationTrace
    Budget           BudgetSnapshot
    Delivery         DeliveryStatus
}
```

This records decisions and outcomes only. Do not store model hidden reasoning. Existing transcripts
retain detailed tool/event data.

### 9.2 `/why`

Example rendering:

```
Profile: adaptive (session setting)
Route: deliberate — multi-step request with external data
Planner: used
Critic: skipped — clean low-risk execution met explicit criteria
Delegation: 1 researcher (read-only, 2 sources)
Budget: 7/25 model steps, 9/30 tool calls, 0/2 paid calls
```

### 9.3 Effective status

Extend the model-facing `status` tool and remote `/status` with:

- workspace and active space id/name;
- profile and effective planning/critique/revision settings;
- role models and tier ceiling/request;
- guidance/prompt sources and sizes;
- agent-type sources/count;
- current phase, queue state, delegation/budget use;
- Telegram binding/delivery health when requested from that frontend.

Keep host/resource/disk/context information already reported.

---

## 10. Safety, spend, and audit corrections

These are prerequisites for offering greater autonomy:

1. **Prompt invariants:** force-attach security and runtime-constraint blocks; test them under a
   `SYSTEM.md` override.
2. **Space validation:** inject a space resolver/validator at the session API boundary. Unknown
   space returns 400 plus available spaces; never persist a broken sticky value.
3. **Paid-tool budget:** count `scrape` against per-run and per-day limits. A budget refusal is
   actionable and auditable.
4. **Audit semantics:** failed ScrapingAnt/network/HTTP calls are `capability_exercised` with
   `ok:false` and status/error class—not `capability_denied`. Reserve denied for policy refusal.
5. **Central activity reader:** local `run`/`chat` read the process-wide audit ledger for
   `recent_activity`, matching `serve`.
6. **Local audit mode:** `agent audit` reads the local log when `--addr` is not explicitly given.
7. **Public bind guard:** refuse a non-loopback `serve --addr` unless an explicit
   `--unsafe-public-no-auth` flag is supplied, or add real API authentication before supporting
   public binding.
8. **Credential hygiene:** rotate any credentials recorded as exposed in the August review; keep
   deployment-specific Fly settings in ignored local TOML files; do not run unattended at
   `permissive` without explicit operator acceptance.

Audit additions:

- `workflow_selected` (only when non-default/changed);
- `route_decided`;
- `subagent_started` / `subagent_finished`;
- `budget_refused`;
- `guidance_updated` (scope and size/hash, never full text if it may contain sensitive material);
- `frontend_delivery_failed`.

Avoid flooding the audit log with every normal phase transition; the run trace owns those.

---

## 11. Implementation phases

Each phase must build, vet, test, update current-behavior docs, and leave the application usable.

### Phase A — correctness and safety prerequisites

**Goal:** remove behavior that would undermine the flexible control plane.

- [ ] Telegram rune-safe chunking and delivery-error propagation.
- [ ] Change `/start` to onboarding; remove/redefine `/stop`.
- [ ] Make close/purge mutate bindings and report success only after API success.
- [ ] Add confirmation callback for `/purge`.
- [ ] Validate session space at `POST/PATCH`; list recovery choices on failure.
- [ ] Split/force-attach executor security and runtime prompt blocks.
- [ ] Correct config-dir/workspace wording in `cmd/root.go`, `cmd/config.go` comments,
      `docs/environment.md`'s configuration table, and the superseded workspace ADR sections.
- [ ] Fix scrape audit event semantics.
- [ ] Add public-bind guard.

**Primary files:** `internal/frontend/telegram/{telegram.go,transport_http.go,*_test.go}`,
`internal/api/{engine.go,sessions.go}`, `cmd/{serve.go,root.go,config.go}`, `internal/agent/agent.go`,
`internal/tools/scrape.go`, relevant reference docs.

**Acceptance:** a 12k-character answer arrives in ordered chunks; simulated send/close/purge
failures reach the user and preserve binding state; `/start` does not change session; invalid space
cannot be stored; custom `SYSTEM.md` retains immutable security/runtime text.

### Phase B — guidance control plane

**Goal:** let a Telegram-only user manage standing instructions safely.

- [ ] Add workspace guidance store and session guidance field.
- [ ] Expose profile/space/guidance service interfaces and API clients.
- [ ] Implement `/notes` and `/guidance` in local/remote chat and Telegram.
- [ ] Add `{{base}}` prompt composition and prompt-provenance reporting.
- [ ] Add guidance length limits, atomic writes, and audit metadata.

**Acceptance:** an allowlisted Telegram user can set global/space/session guidance, see it, restart
the engine, and observe it in the next turn; no guidance can remove immutable prompt blocks.

### Phase C — orchestration package and profiles, behavior-preserving

**Goal:** create the architecture seam without changing default output behavior.

- [ ] Move/generalize `cmd/deliberate.go` into `internal/orchestration`.
- [ ] Add profile types, config parsing, validation, merge/clamp, and provenance.
- [ ] Add nested workflow options to session/API/client.
- [ ] Implement built-in profiles; absent profile resolves to `deliberate`.
- [ ] Map existing serve flags to hard ceilings and deprecation-compatible behavior.
- [ ] Add `/profile` and `/deliberate` command-service operations.

**Acceptance:** existing tests and golden behavior remain unchanged with no profile configured;
`quick` skips planner/critic; `thorough` matches the old deliberate pipeline; session selections
survive restart and work identically from CLI and Telegram.

### Phase D — adaptive routing, review policy, and traces

**Goal:** make normal behavior more logical and explainable.

- [ ] Add strict route schema/router and fail-safe fallback.
- [ ] Implement auto-critique triggers from observable run facts.
- [ ] Add shared turn budgets and structured exhaustion outcomes.
- [ ] Persist `DecisionTrace`; expose events/API and `/why`.
- [ ] Extend status/effective-config endpoints and reload diff.
- [ ] Add evaluation variants for profiles and per-role models.

**Acceptance:** simple conversation under `adaptive` uses no planner/critic; an ambiguous multi-step
task plans; a high-assurance or failed-tool task reviews; `/why` accurately explains each choice;
router failure falls back to deliberate behavior.

### Phase E — structured and bounded sub-agents

**Goal:** make delegation predictable before making it concurrent.

- [ ] Extend agent-type parsing with context/result/iteration/budget fields.
- [ ] Add strict `SubagentReport` and backward-compatible text mode.
- [ ] Replace depth integer with shared delegation budget.
- [ ] Add delegation traces and usage attribution.
- [ ] Teach coordinator synthesis to reconcile reports/evidence/uncertainty.
- [ ] Add `spawn_agents` for parallel-safe types only.

**Acceptance:** agent-count/depth limits cannot be bypassed; report mode returns valid schema;
parallel workers never receive write/approval tools; partial batch failure preserves successful
reports; parent reports conflicts instead of hiding them.

### Phase F — durable, full Telegram workspace

**Goal:** make Telegram an operationally complete frontend.

- [ ] Persistent bot/chat/thread/user binding store and migration from ephemeral bindings.
- [ ] Shared command service adopted by Telegram and both chat REPLs.
- [ ] `/sessions`, `/resume`, `/cancel`, `/tier`, `/space list`, `/status`, `/usage`, `/reload` diff.
- [ ] Group/topic binding modes and `@botname` command normalization.
- [ ] Explicit queue state/limit/cancellation API.
- [ ] Editable progress message/typing behavior by profile.
- [ ] Delivery status persisted in run trace.

**Acceptance:** restart resumes the correct Telegram session; group topics do not cross streams;
commands have the same semantics across frontends; a second turn is visibly queued and cancellable;
delivery failure is discoverable rather than silent.

### Phase G — budgets, local consistency, and rollout

- [ ] Per-run/per-day paid-tool budgets and token soft/hard limits.
- [ ] Central `recent_activity` reader in every frontend.
- [ ] Local `agent audit` mode.
- [ ] Compare `quick`/`adaptive`/`thorough` with representative eval tasks and repeat runs.
- [ ] Choose whether `adaptive` becomes the default for new sessions; never silently rewrite old
      session settings.
- [ ] Split the large usage doc as proposed in the August review and update embedded reference docs
      to describe only shipped behavior.

---

## 12. Test strategy

### Unit tests

- profile validation, sparse merge, provenance, and every ceiling combination;
- route and verdict strict-schema parsing/fallback;
- budget reservation under concurrent delegation;
- Unicode/paragraph Telegram chunking at 4,095/4,096/4,097 and multi-byte boundaries;
- command normalization, `/start`, confirmation nonce expiry, group binding keys;
- guidance precedence, limits, atomic persistence, and immutable prompt blocks;
- space validation and error recovery text;
- audit event classification for policy denial vs service failure.

### Integration tests

- API session profile/guidance round-trip and restart;
- persistent Telegram binding restart with fake transport + real file session store;
- deliberate/quick/adaptive phase call counts using fake providers;
- queue/cancel/ask-user interaction;
- sub-agent batch cancellation and partial failure;
- final answer delivery failure reflected in `RunInfo`/trace.

### Race tests

Run at minimum:

```bash
go test -race ./internal/orchestration ./internal/agent ./internal/api ./internal/frontend/telegram
```

### Evaluation set

Include at least:

1. casual conversation (should route quick);
2. translation/explanation (no plan/critic);
3. one-step current lookup (bare executor);
4. ambiguous task requiring clarification;
5. multi-source research (planner + parallel researchers);
6. coding/data task with artifact reuse;
7. failed tool followed by recovery/review;
8. conflicting sub-agent reports;
9. paid scrape temptation/retry loop;
10. destructive request requiring approval;
11. long Telegram response;
12. restart/resume with space/profile/guidance intact.

Measure answer success, model calls, tokens, latency, tool/paid calls, unnecessary planner/critic
rate, delivery success, and user interventions. Default-profile changes require evidence from this
set, not intuition.

---

## 13. Documentation updates by phase

Reference docs must change only when behavior ships:

- `README.md`: profile/guidance entry points and corrected structure if packages move.
- `docs/usage.md`: commands, Telegram behavior, profiles, guidance, queue/cancel, budgets.
- `docs/environment.md`: profile config, immutable/customizable prompt layers, actual state scopes.
- `docs/api-transport.md`: nested workflow options, control-plane endpoints, trace/delivery fields.
- `docs/security.md`: operator ceilings, guidance boundary, public bind guard, paid-tool budgets.
- `docs/tools.md`: structured/batch sub-agent behavior and budget interaction where relevant.
- `docs/adr/chat-planner.md`: retain as the historical fixed-deliberate decision, with a superseding
  pointer to the shipped orchestration architecture once Phase C/D lands.
- `docs/adr/subagents.md`: mark structured reports/shared budget/parallel batch as built as each
  lands.

Planning docs are not embedded and may continue to describe future work. Never copy this proposal
into embedded docs before its corresponding phase ships.

---

## 14. File-level work map

| Concern | Existing files | Likely additions/changes |
| --- | --- | --- |
| orchestration | `cmd/deliberate.go`, `cmd/serve.go`, `cmd/chat.go` | `internal/orchestration/*`, thin cmd adapters |
| workflow wire/persistence | `internal/api/engine.go`, `sessions.go`, `client.go`, `internal/session/session.go` | nested workflow request types, session fields, client methods |
| profiles/config | `cmd/config.go`, `cmd/serve.go` | profile parsing/validation, effective snapshot service |
| prompt/guidance | `internal/agent/agent.go`, `cmd/prompts.go`, `internal/space` | immutable prompt blocks, guidance store/service |
| sub-agents | `internal/agent/agenttype.go`, `cmd/agents.go` | report schema, delegation manager/budget, batch tool |
| events/traces | `internal/agent/observer.go`, `internal/api/event.go`, `engine.go`, run store | orchestration events, persisted `DecisionTrace` |
| Telegram | `internal/frontend/telegram/telegram.go`, `transport_http.go` | renderer, binding store adapter, richer transport types |
| shared commands | `cmd/chat.go`, `cmd/chat_remote.go`, Telegram command switch | `internal/command` or `internal/control` service + adapters |
| status/control API | `internal/tools/status.go`, `internal/api/http.go`, `cmd/serve.go` | status/config/profile/space service handlers |
| budgets/audit | `internal/usage`, `internal/tools/scrape.go`, `internal/audit` | shared counters, daily paid-call ledger, corrected events |

Before choosing `internal/command` versus `internal/control`, prototype the interface with `/model`,
`/space`, and `/status`. It should return structured results for frontend rendering, not print or
send messages itself.

---

## 15. Settled decisions and deliberately open questions

### Settled for v1

- Profiles are declarative data, not executable plugins.
- Security/runtime prompt blocks are immutable.
- Default remains current deliberate behavior during migration.
- Auto routing fails toward deliberation.
- Parallel sub-agents are read-only and approval-free.
- Telegram bindings persist in the config-dir identity scope; sessions remain the source of truth.
- `/start` is onboarding, `/purge` confirms, `/cancel` stops work.
- Decision traces contain reasons/outcomes, never chain-of-thought.
- JSON stores remain acceptable for v1 behind interfaces; SQLite migration is separate.

### Open, decide with implementation evidence

1. **Router cost:** always call a cheap router for `adaptive`, or add deterministic fast paths for
   obvious conversational turns? Start with one structured router for correctness; optimize after
   measuring.
2. **Default queue limit:** one queued Telegram turn is the lean; test whether rejection is clearer.
3. **Full-context children:** likely unnecessary and expensive. Keep disabled unless a concrete
   agent type demonstrates the need.
4. **Profile editing over Telegram:** selecting profiles is in scope; editing arbitrary profile YAML
   remotely is not v1. It has more validation/security surface than notes.
5. **Workspace switching:** add a contained workspace field only after spaces/guidance/profile work
   proves insufficient. It changes the shell and data root and deserves its own containment ADR.
6. **Vision:** the existing [`vision.md`](vision.md) tool-based proposal composes with this plan but
   is independent. Count vision side-call usage against the same turn budget when built.

---

## 16. Definition of done

This program of work is complete when:

- a user can safely control guidance and workflow from Telegram;
- the same session/profile/guidance survives restart and can be resumed from another frontend;
- simple turns avoid unnecessary planning/review while complex/high-risk turns reliably use them;
- sub-agent work is bounded, attributable, structured, and safely parallel where declared;
- `/why` and `/status` accurately explain effective behavior and provenance;
- Telegram delivery, queueing, cancellation, topics, and destructive actions are reliable;
- operator ceilings prevent profile/guidance escalation and runaway paid-tool/model spend;
- all shipped behavior is reflected in embedded reference docs, with planning claims kept outside
  the agent's current-capability knowledge.
