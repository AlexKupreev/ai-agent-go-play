# Chat planner — deliberate multi-turn chat via a persistent planner

A design for making interactive `agent chat` **deliberate** — the agent orients, researches,
weighs real alternatives, and decides — instead of single-shotting an answer and relying on the
user to correct it every turn. The mechanism is a **persistent, conversation-aware planner in
front of a stateless executor**, running the full clarify/refine → execute cycle on every turn.

Companion to [`plan.md`](../planning/plan.md) §4f (the chat REPL + persistent sessions this builds on),
[`prompts.md`](prompts.md) (system-prompt composition) with [`../environment.md`](../environment.md)
/ [`../usage.md`](../usage.md) (the hot-reloadable `PLANNER.md` override), and
[`workspace.md`](workspace.md) (the scratch-directory home for the artifact cache §D5).
**Status: built** — `agent chat` and `agent serve` (planner + critique **on by default**;
`--no-plan` / `--no-critique` to disable). §1–§8 and the §9 critique loop are implemented for both
the local CLI and the remote engine session path (the §7 "local only" boundary was lifted: the
planner's `ask_user` routes through the engine gate). **Deferred:** the project-scoped parts (§D5–D7
— cache promotion, `switch_project` briefing, proactive proposals) pending the named-projects
feature below; the TTL/review reaper (§D5/O3); and routine turn-log summarization (§O5, a
threshold guard ships, the policy does not). This doc is retained as the **design record**.

> **Note:** the **project** scoping used below (§D5–§D7 — a project-scoped artifact cache, the
> `list/create/switch_project` verbs) depends on the named-projects feature, which is currently
> [**deferred**](../deferred/projects.md) (built then reverted to a two-scope model). Until it
> returns, treat those parts as design that assumes it; the session/scratch (workspace-scoped) paths
> stand on their own.

---

## 0. The problem

`agent run` is deliberate by construction: it runs **planner → executor**, so a clarify/refine
pass front-loads ambiguity resolution before the executor commits. `agent chat` today is a bare
executor — the same `*agent.Agent`, but fed raw user input with no planning stage — so it reads as
single-shot: think-steps → execute → summarize, then wait for the user to steer.

The existing `chat --plan` flag doesn't close the gap:

- **Context-blind.** `buildPlanner` mints a *fresh* planner each turn that never sees the
  conversation (`cmd/chat.go`: "planning is independent of the running dialogue"), so mid-chat it
  re-clarifies from scratch and asks about things already settled.
- **Invisible.** It emits one terse `[planner] <refined task>` line, so the deliberation never
  shows.

(It's also startup-only — as is this design, opt-in via `--plan`, §O2 — but that isn't the problem;
the problem is that even when on, the two defects above make the deliberation too weak to be worth
leaving on.)

The goal: make chat **deliberate by construction** — `run`'s clarify/refine, but conversational and
in context — without a second-guess-every-point feedback loop, and without polluting the executor's
context with the by-products of thinking.

---

## 1. The shape (decided)

> **A conversation-aware planner (fed a loop-owned turn log) in front of a stateless executor. Every turn runs the
> full cycle. The planner never answers — it always refines and delegates. The executor holds no
> conversation; it is seeded per turn with a self-contained brief. Working data lives on the
> filesystem, not in either agent's context; the planner tracks it via an artifact manifest.**

Chat becomes *"`agent run`, but the planner is conversational and persistent."* It reuses the
`run` mental model rather than inventing a new one, and the executor is once again the single-shot,
tool-equipped agent it already is in `run`.

Why each piece — the reasoning that produced this shape:

- **Deliberation belongs in a separate agent, not inline in the executor.** Inline deliberation
  (orient/assumptions/options as executor prose) *permanently pollutes* the executor's retained
  chat context — every future turn carries the planning cruft. A separate planner does that
  thinking in its own ephemeral context and hands over only a clean brief. **Clean executor
  context is the whole point, and it's the same coin as "the executor keeps no working memory"
  (§D2/§D3).**
- **The planner never answers** (§D1), so *every* grounded, user-facing answer comes from the
  tool-equipped executor. This preserves the safety property the two-agent split exists to give,
  and removes any "should I answer or delegate?" classification that could misfire into an
  ungrounded, tool-less answer.
- **The executor is stateless** (§D2): no two-history sync problem, because there is only one
  conversation, and the chat loop owns it as a turn log the planner reads (§D6).
- **The filesystem is the working memory** (§D3–D5): nobody holds 50k rows in context; both agents
  deal in references.

---

## 2. Pipeline (per turn)

1. **User posts a message.** (Or answers an in-flight `ask_user` — see the note below.)
2. **Planner runs, context-aware.** It receives the conversation (or a meaningful summary) **plus
   the artifact manifest** (§D4) — what data/files already exist, with their schema/shape. It may
   do bounded web research to resolve the *request* (§ open question O1), and `ask_user` to clarify
   a genuine unknown.
3. **Planner emits a brief for the executor** — a self-contained `{refined_task, context,
   artifact_refs, assumptions, confirmed}`. `artifact_refs` are **cache-with-fallback** pointers:
   *"the 2024 CSV may be cached at `<scratch>/sales-2024.csv`; if absent, fetch from `<source>`."*
   The planner **never** produces the user-facing answer. The brief is **surfaced to the user**
   (replacing today's terse `[planner] <task>` line) — this is what closes the *invisible* gap in
   §0: the deliberation is now legible and auditable.
4. **Executor runs the brief to completion**, stateless. It reads cached artifacts named in the
   brief (re-fetching on a miss), does the grounded work, **materializes any sizeable intermediate
   data to the scratch dir** and records it in the manifest (§D4), and produces the answer.
5. **Answer is shown to the user** and enters the conversation the planner reads next turn.

**Confirmations during execution do not re-enter the planner.** If the executor needs the user
mid-task, that's an `ask_user` tool call answered straight to the in-flight executor — no new
planner cycle. So: every *new* turn goes through the planner; a reply to the *executor's* in-flight
`ask_user` goes straight back to that executor. (A clarifying `ask_user` the *planner* raises in
step 2 is answered within the same planner run — intra-turn, not a new cycle.)

---

## 3. Decisions & rationale

### D1. The planner never answers — full cycle every turn

The planner keeps its **exact current contract** ("clarify and refine, never answer"), so `run` and
`chat` share one planner *role* (only the chat wiring feeds it conversation + manifest). No
`{reply | delegate}` output fork, no responder prompt, no lifting of the "never answer" rule.

- **Enforced in code, not prose:** "never answer" is a *structural* guarantee, not a prompt wish.
  The planner's only output channel is the strict `Plan` schema (`planResponseFormat`, `Strict:
  true` — `internal/agent/plan.go`); there is no field in which to emit a user-facing answer. This
  is also what lets the `PLANNER.md` override be retuned freely without breaking the pipeline — the
  contract lives in Go, the prompt only tunes it (see §4).
- **Safety:** every grounded answer comes from the tool-equipped executor; a half-equipped planner
  (no `shell`/`run_code`/memory) never answers from its head.
- **No classification risk:** there is no "answer vs delegate" decision to get wrong — it always
  delegates. The unstable middle (a planner answering substantive questions with half the tools) is
  designed out, not merely discouraged.
- **Cost of the alternative rejected:** letting the planner answer simple turns saves one
  round-trip on chit-chat but reopens the misclassification-into-hallucination path. Not worth it.

One prompt edge to handle (§4): the current planner is aggressively task-oriented ("preserve the
intent to DO the task; never downgrade do-X into confirm-X"). A pure conversational turn ("how are
you?", "explain X") isn't a do-something task, so the planner needs one instruction that a
conversational turn is a **valid** task — refine it into *"respond to the user's message: …"* and
delegate that. The executor already answers general knowledge directly, so it is fully equipped to
be the sole responder.

### D2. Stateless executor

The executor holds no conversation across turns (unlike today's chat executor, which retains
`a.messages`). Each turn it is the `run`-style single-shot agent, seeded with the planner's brief
and discarded.

- Removes the **two-history** problem entirely: there is one conversation, owned by the chat loop as
  a turn log (§D6) and read by the planner.
- Keeps the executor's context **lean** — the benefit that motivated the whole design.
- **Cost:** the executor forgets its own in-context working set between turns (§D3 is the fix).

### D3. The filesystem is the working memory — not context

Clean executor context ⟺ no executor working memory: the same property. The fix is to stop using
context as the scratchpad. The executor **writes sizeable intermediate data to disk** and both
agents pass **references** (path + short description), never the payload. Nobody holds 50k rows in
context.

- The three failure flavors of a naive stateless executor (reproducible-cheap, reproducible-
  expensive, non-reproducible) all collapse: cached data survives between turns regardless of how
  expensive or one-shot it was to obtain.

### D4. The planner tracks an artifact manifest

So the planner *knows* what's materialized instead of inferring it from transcript prose. When the
executor materializes an artifact it records `{path, origin, source, short schema/description,
timestamp}` **by calling a `record_artifact` tool** (schema-validated at the tool boundary, not
hand-written into prose — see §4) which appends to a small manifest (JSON in the scratch dir). The
planner reads that manifest each turn, appended to its prompt the way `EnvironmentSummary` already
is (§4).

- The **schema/description matters, not just existence**: to brief *"break down by region"* well
  the planner must know the CSV *has* a `region` column. "Meaningful summary" = a one-line
  shape/columns note per artifact.
- **Provenance drives retention:** each entry carries `origin: user | agent` (D5 uses it to decide
  what may be reaped). Agent-materialized files are recorded via `record_artifact`; **user-provided**
  files the loop registers with `origin: user` when the user attaches one, since the executor doesn't
  "materialize" those. **v1 scopes this to an explicit attach**, not prose reference-detection:
  registration is triggered by the CLI attach path (a `--file`/attach affordance the loop owns), so
  the loop has the real path in hand. Sniffing *"the file I mentioned earlier"* out of a chat message
  is exactly the transcript-parsing D4 exists to avoid, so a bare textual mention does **not**
  auto-register — the executor resolves it like any other path, and records it via `record_artifact`
  (`origin: agent`) only if it materializes a derived copy. Richer reference-detection is post-v1.
- **`record_artifact` is an auto-permitted built-in, tier-independent.** It is a host tool that only
  appends a manifest entry and writes within the active workspace's scratch dir — no capability, no
  network, no arbitrary path. So it belongs in the **PERMITTED-automatically** built-in set alongside
  `memory`/`status` (`agent.go:243`), **not** the authored/capability-gated lane (`agent.go:245`) and
  not the destructive-shell confirm path. It never prompts, at any tier — recording an artifact must
  be as frictionless as the executor writing the file it describes, or the manifest goes stale.
- **Paths are relative to the workspace root**, so an entry survives the file being moved into a
  project on promotion (D5/D7); resolved against the active workspace at read time.
- Robustness: parsing file existence out of narrative answers is fragile; a structured manifest is
  not.

### D5. Scratch cache — workspace-scoped, provenance-aware lifecycle

Artifacts are **not deleted immediately**; they live in a **scratch cache** for reuse, scoped to the
active workspace (D6) — a **session/scratch** run has a session-scoped cache; a **project** has a
project-scoped one. The manifest is that cache's index and shares its scope. Ties into the existing
workspace/projects machinery ([`workspace.md`](workspace.md), [`projects.md`](../deferred/projects.md)) for
namespacing.

- **The key safety rule:** every `artifact_ref` in a brief is **cache-with-fallback** — *"read
  `PATH` if it exists, else fetch from `SOURCE`."* So a reaped/expired file is **never** a wrong
  answer; it just costs a re-download. Reaping can therefore be lazy or a sweep without correctness
  worries.
- **Retention by scope × provenance** (resolves O3 + projects.md §8):
  - *session / scratch (any origin):* kept for a **TTL grace window after the session closes** — not
    deleted on close — then reaped. The window lets a reopened session ([`resume.md`](../planning/resume.md)) and
    near-term follow-ups reuse the files; genuine long-term keep is what **promotion** is for, so
    un-promoted work still expires rather than accumulating forever.
  - *project, user-provided:* kept **until explicit deletion** — never auto-reaped. The agent must
    not garbage-collect data the user handed it.
  - *project, agent-derived:* durable, but subject to **periodic review** — flag stale candidates and
    ask before removing, never silently. (Mechanism deferred — see §7.)
- **Promotion migrates the subset.** Switching scratch → project (`create_project` with `from_paths`,
  projects.md P2) moves the *relevant* files **and their manifest entries** into the project cache,
  not the whole scratch pile (D7). Manifest paths are workspace-root-relative (D4) so the move stays
  self-consistent.

### D6. The chat loop owns the conversation (a turn log), not the planner

"The conversation" is a **cross-agent** object — user messages + executor answers — so it lives in
neither agent's `a.messages`. The executor is stateless (D2); the planner sees only its own side
(its `ask_user` exchanges and the `Plan`s it emitted), never the executor's answers. So a component
*outside both agents* must assemble it: the **chat loop holds a turn log** (`{user_message,
executor_answer}` per turn; data artifacts are covered by the manifest, D4) and re-feeds it to a
**fresh** planner each turn. "Persistent planner" therefore means *the conversation persists and is
re-fed* — not a long-lived agent object.

- **Why not a retained planner object:** even a long-lived planner would still need the executor's
  answers injected from outside — at which point it just *is* the turn log, dragging along
  planner-internal cruft (old `Plan` JSON, clarifying Q&A) the next plan doesn't want. A
  fresh-per-turn planner also keeps the `PLANNER.md` hot-reload free (`cmd/chat.go`: `buildPlanner`),
  with no `Restore()` dance.
- **Transformable by construction:** because the turn log is a plain loop-owned structure, any
  transform — compress/summarize (O5), redact, drop stale turns — happens *on that structure* before
  it is fed, with no surgery on an agent's internal history. This seam is what makes O5 cheap.
- **Storage is a separate axis:** in-memory for a live session; serialize the turn log to the
  session store ([`resume.md`](../planning/resume.md)) only if/when resume is wanted — independent of this
  decision.

### D7. Project switching, and proactive project proposals

Only the executor holds the project verbs (`list_projects` / `create_project` / `switch_project`,
[`projects.md`](../deferred/projects.md) §4); the planner has web + `ask_user` only. Two interactions the
pipeline has to place:

- **Switching mid-conversation.** Recognizing *"let's work on the heart-health project"* is an
  *orientation* judgment, so the **planner detects the intent and briefs the switch** —
  `refined_task: "switch into the heart-health project, then …"` — and the executor calls
  `switch_project` (tier-gated + audited, projects.md §5/§7). Planner stays tool-light, executor
  stays the sole actor (D1). **Ordering constraint:** a switch re-scopes the artifact cache (D6 — the
  manifest follows the active workspace), so **after a turn that switched projects the loop
  re-resolves the scratch/manifest path to the new workspace before the next planner run**, or the
  planner reads the old project's manifest.
- **Proactive proposals (agent-initiated).** The agent can *offer* to create or switch: *"we've
  analysed a lot of heart-specific data — want to save this as a heart-health project?"* This fits
  the design:
  - **The manifest is the signal, not a vibe.** A cluster of related artifacts (`heart-*` with
    schema notes) is concrete evidence the planner already reads each turn (D4), so the detection is
    its orientation job.
  - **Planner detects, executor voices (D1 preserved).** The planner folds the offer into the brief
    (*"…note recent work is heart-health-focused; offer to promote it to a project"*); the executor
    delivers it in its answer. The planner still never addresses the user.
  - **Proposal, never auto-action.** The suggestion changes nothing by itself; on acceptance the
    *action* still runs `create_project` (human-gated) or `switch_project` (tier-gated). Proactivity
    surfaces the option without bypassing a gate — the friendly face of "promotion is explicit, never
    a silent switch" (projects.md §3).
  - **Result or preview.** *Result:* after heart-heavy work, offer to promote the existing scratch
    artifacts (`create_project` with `from_paths` = the clustered manifest entries). *Preview:* if a
    heart-health project already exists and the turn is clearly in its domain, offer to switch into it
    before doing the work.
  - **Guardrail against nagging.** Gate the offer on a real threshold (a domain cluster of N related
    artifacts, not one) and don't re-offer what was declined (a note in memory). Prompt/heuristic
    tuning — light, but load-bearing for it not to feel like a paperclip.

---

## 4. What each agent sees, and prompt changes

| | Sees | Produces |
|---|---|---|
| **Planner** | conversation (or summary) + artifact manifest + live environment (`EnvironmentSummary`) | a brief for the executor (never the answer) |
| **Executor** | the brief only (task + context + artifact refs) | the user-facing answer; materializes artifacts + records them via `record_artifact` (§4) |

**Planner prompt** (`plannerPrompt` / `PLANNER.md` override): add (a) conversational turns are valid
tasks → refine to *"respond to …"* and delegate; (b) it now receives a manifest — trust it for what
data exists and its shape; (c) reaffirm the bounded-web-research rule (O1).

**Structured output (extend, don't invent).** The planner already emits a strict, schema-enforced
`Plan` (`planResponseFormat`, `Strict: true` — `internal/agent/plan.go`); this design just widens it
from `{refined_task, assumptions, confirmed}` to add `context` + `artifact_refs`. Enforcing the
schema in code — not the prompt — is what gives D1 its teeth (the planner *cannot* answer) and keeps
the `PLANNER.md` override from being able to break the contract. Two rules so strictness doesn't box
the planner in:

- **Stay strict — every field required, "optional" = nullable.** OpenAI strict mode requires *all*
  properties listed in `required` at every object level (including `artifact_refs` items) with
  `additionalProperties: false`, so there are no genuinely optional fields. Model "may be empty" as
  a **nullable type** instead: `context` is `{"type": ["string", "null"]}`, and each `artifact_refs`
  item is `{path, source, description}` where `path` is a plain string and `source`/`description` are
  `["string", "null"]` — all three in the item's `required`. The planner emits `null` (not omission)
  for a bare first-turn pointer or a trivial "respond to …" turn. The prompt (§4) must say **emit
  `null`, never omit** — an omitted field fails strict validation. This preserves D1's teeth (no
  answer field exists, and the schema can't be relaxed by `PLANNER.md`) while keeping the planner
  free to under-specify when there's nothing to say.
- **Verify strict mode doesn't suppress mid-plan tool calls.** The planner must `ask_user` / do web
  research *before* the final `Plan`; the provider sends `ResponseFormat` and `Tools` together
  (`internal/provider/openai/openai.go`), and this already works for `run`'s planner. The chat
  planner leans on mid-plan tool use harder (manifest-aware briefing, O1), so confirm end-to-end
  that strict JSON doesn't nudge the model to satisfy the schema prematurely instead of calling a
  tool. Cheap to check; expensive to discover later.

**Executor**: no *role* change, but the manifest write is **not** a prose responsibility — a
stateless model won't reliably hand-write structured JSON, and parsing it back out of narrative is
the fragility D4 exists to avoid. Apply the same "enforce structure in code" principle as the
planner's `Plan`: the executor records an artifact by **calling a `record_artifact` tool** whose
schema is `{path, origin, source, schema/description, timestamp}`, so the entry is validated at the
tool boundary. Its other new responsibilities stay mechanical — read cached artifact refs (re-fetch on
miss), write sizeable intermediates to the scratch dir.

**Wiring** (`cmd/chat.go`): the loop keeps a **turn log** (D6); each turn it builds a fresh planner
and feeds it that log + the manifest (re-resolved to the active workspace each turn, so a mid-turn
`switch_project` is reflected next turn — D7; today `buildPlanner` is context-blind), runs
planner-always → executor-with-brief, then **appends the user message + executor answer to the log**.
The executor is rebuilt per turn (stateless), never retained.

**Brief → executor rendering.** The executor's entry point takes a *string* task, not a struct —
today `run` seeds it with the bare `plan.RefinedTask` (`cmd/run.go:168`; `cmd/chat.go:275` sets
`input = plan.RefinedTask`). The new `context` + `artifact_refs` fields therefore need a **defined
flattening**: the loop renders the `Plan` into one seed string — `refined_task`, then a `Context:`
block if non-null, then an `Artifacts:` block listing each ref as `path — description (else fetch
from source)` so the cache-with-fallback contract (§D5) is spelled out in the words the executor
reads. `assumptions`/`confirmed` render as they do in `run` today. This keeps `executor.Run(ctx,
string)` unchanged — the brief is one composed prompt, not a new executor signature — and keeps the
rendered brief the human-legible artifact §0/§2 promise (it's what's surfaced to the user). Null
fields are simply omitted from the rendering (they carry no instruction), which is why the schema
makes them nullable rather than free-form empty (§4 strict rule).

---

## 5. Cost model

Cheaper than "two calls per turn" sounds, because of D2:

- The **planner** reads the growing conversation — its input scales with dialogue length.
- The **executor** reads only the brief — small and flat, regardless of chat length.

So per turn ≈ *one* history-read (planner) + a *small* executor call — not double the tokens. The
real cost is **latency**: two sequential model calls per turn, including on trivial turns. On a
personal/family box (design §1) optimizing for deliberate correctness over snappiness, that's an
accepted trade.

---

## 6. Open questions (and resolutions)

- **O1 — Planner research depth (resolved).** The planner **plans steps; it never opens the data or
  probes the source.** A brief is an ordered plan — *"1) download the file; 2) extract March 1 2026;
  3) fetch today's prices; 4) compare and report"* — composed from the request + the manifest, not
  from inspecting the bytes. Its web use stays **request-level**: disambiguate a term, confirm an API
  name or that a source exists — never fetch-and-introspect the dataset (that would pollute its
  context, duplicate the executor's grounded work, and blur roles). *Format/parse problems are the
  executor's to surface:* if the file turns out to be a binary `.xlsx` Lua can't parse, the executor
  reports it and the next turn's plan adapts (using the manifest, D4). That is the pipeline working —
  not a failure to pre-empt — so the planner needs no peek at the data at all.
- **O2 — Activation (resolved).** Under this design a reply is *always* planned — planning every turn
  is the architecture (§1/D1), not a per-turn choice, so there is no "deliberate on this turn"
  granularity to expose. O2 is therefore only: is deliberate chat the **default**, or **opt-in**?
  *Resolved:* opt-in via **`--plan`, default OFF** for v1 — the feature is new, brief-completeness is
  prompt-tuned and load-bearing (§8), and it doubles per-turn latency (§5). **A launch flag, not a
  mid-session toggle:** deliberate chat and today's bare-executor chat own the conversation
  *differently* — a loop-owned turn log + stateless executor (D2/D6) vs. a stateful executor holding
  `a.messages` (`cmd/chat.go`) — so flipping mode mid-session is a conversation-handoff problem, not
  a flag flip, and isn't worth opening in v1. Pick the mode at launch. **Promote to default** on
  `agent eval` evidence (plan.md §F) that planned chat wins on answer quality at acceptable latency —
  the expected trajectory for a box that favors deliberate correctness (§1), but evidence-gated.
- **O3 — Expiry policy (mostly resolved; one piece deferred).** Retention is scope × provenance
  (D5): session/scratch kept for a TTL grace window past session close then reaped; project
  user-files kept until explicit deletion; project agent-files durable. Still open: the window length
  and reaper trigger (lazy on access vs sweep), and — deferred (§7) — the periodic-review reaper for
  agent-derived project files. Cache-with-fallback (§D5) keeps any choice correct.
- **O4 — Executor working-state carryover (deferred).** If re-derivation from disk proves too
  costly for tight incremental-build loops, allow the executor to retain a working context across
  turns *of the same task thread* — a scoped exception to D2. Not built in v1.
- **O5 — Bounding / transforming the turn log (v1 stance set; policy deferred).** The turn log the loop feeds the planner
  (D6) grows with the dialogue, so the cost model (§5) has the planner's input growing without bound,
  and there is no truncation/summarization policy yet (plan.md §6d defers context-window awareness).
  D6 makes the *mechanism* free — the log is a loop-owned structure, so a transform (compress,
  summarize, drop stale turns) runs on it before it is fed — but the *policy* is open. The enabling
  insight: **the manifest + the last brief are the durable continuity, so the raw transcript can be
  summarized aggressively** without losing state (state lives on disk, D3/D4, not in the transcript).
  **v1 stance (so this isn't an implementer decision on the happy path):** ship v1 feeding the
  **full turn log**, no transform — a local, single-user `agent chat --plan` session (§7) is short
  enough that unbounded growth is a non-issue in practice, and shipping the raw log first gives the
  eval (§O2, plan.md §F) a clean baseline before adding lossy summarization. **Add a guard, not a
  policy:** cap the log at a turn/character threshold that only trips in pathological sessions, and
  when it trips, fall back to *rolling summary + full manifest* (the recommended transform). So the
  bound is enforced from v1, but the summarizer is a safety valve, not a routine path — its quality
  can be tuned later without gating the feature. Full summarization policy (what to keep verbatim vs.
  compress, per-turn vs. windowed) stays post-v1.

---

## 7. Deferred / non-goals (v1)

- **No executor-state carryover** (O4): continuity is via disk + a complete brief, not executor
  memory.
- **No planner-as-responder**: the planner never answers (D1); no `{reply|delegate}` fork.
- **No new store**: manifest = small JSON in the scratch dir; migrate into SQLite only when the
  broader store does (design §9).
- **Local `agent chat` only (v1)**: the planner runs CLI-side (`ask_user` reads stdin via
  `StdinGate`, `internal/agent/agent.go`), so this pipeline covers local `agent chat --plan`, not
  the engine's remote session path (`PostTurn`) used by the API/Telegram frontends. Extending
  deliberation to remote chat is out of scope here.
- **No agent-file review reaper (v1)**: agent-derived *project* files are kept durably; the periodic
  "is this still needed?" review (flag stale + ask, never a silent delete) is a TODO, not built in v1
  (D5, O3).
- **No prose file-reference detection (v1)**: `origin: user` registration is triggered only by an
  explicit CLI attach, not by sniffing file mentions out of chat text (D4). A bare textual mention is
  resolved by the executor like any other path.
- **No routine turn-log summarization (v1)**: the loop feeds the full turn log, with a threshold
  guard that falls back to rolling-summary + manifest only in pathological sessions (O5). The
  summarization *policy* is post-v1.

## 8. Risks / notes

- **Brief completeness is now load-bearing.** Continuity depends on the planner encoding enough in
  the brief (task + context + artifact refs). Under-briefing → the executor re-derives or misses
  state. The strict `Plan` schema (§4) guarantees the brief's *shape*, not its *quality* — a
  well-formed brief can still be a thin one, so this stays prompt-tuning. It is at least observable
  (the brief is inspectable / auditable), and a stale/wrong `artifact_ref` is caught by D5's
  cache-with-fallback, not the schema.
- **Two prompts stay in sync in spirit only.** `run` and chat share the planner *role*; only chat
  feeds the manifest. Keep the shared "never answer / bounded research" rules identical; let the
  chat-specific manifest handling be additive.
- **Scratch dir hygiene.** Namespacing per session/project + a reaper keeps it from growing
  unbounded; cache-with-fallback keeps reaping safe. Consider auditing artifact writes for the same
  reason effectful paths are audited elsewhere (plan.md cross-cutting).

---

## 9. Making chat deliberate, persistent, focused — the control-loop view

**Status: proposal / discussion — not yet decided** (unlike §1–§8, which are the agreed pipeline).
This section reframes a broader question — *how do we make the agent more deliberate, persistent, and
focused?* — and places the answer relative to the pipeline above and to
[`subagents.md`](subagents.md). It exists so the next design pass starts from a structured space
rather than a list of tempting-but-competing mechanisms.

### 9.0. The three words are three different mechanisms, and they fight

They sound like one virtue; they are not, and conflating them is how a persistence feature turns into
a runaway loop:

- **Deliberate** = think *before* acting. Already built: the planner's clarify/refine front-loads the
  thinking (§0, D1). The pre-execution version.
- **Persistent** = don't give up at the first obstacle — evaluate, re-strategize, retry. The *in-loop*
  version. This is the genuinely missing piece.
- **Focused** = don't wander, don't over-scope, terminate. A *budget / scope* concern.

Persistence without focus is a loop burning tokens forever; focus without persistence is premature
give-up. So the design target is **not "add persistence" — it is "add a re-strategizing loop that
carries a termination guarantee."** Every loop level below needs both halves or it is a footgun.

### 9.1. The candidate mechanisms are one loop at four altitudes

The tempting options — *executor spawns a subagent per task; executor changes strategy inline;
executor's answer feeds a new planner cycle instead of the human; adjust autonomously vs. manually* —
are not alternatives. They are the same evaluate-and-re-strategize loop at different altitudes. Each
level has an **actor**, the thing it **evaluates**, the thing it can **change**, and a **budget**:

| Level | Actor | Evaluates | Can change | Budget | Status |
| --- | --- | --- | --- | --- | --- |
| **1. Tactical** | executor's inner think-act loop | each tool result | next action / tool | `MaxTurns` | **already exists** |
| **2. Subtask** | `spawn_agent` child ([`subagents.md`](subagents.md) §3) | its one narrow goal | tactics, in isolation | spawn depth + `MaxTurns` | designed, not built |
| **3. Strategic** | critic → planner re-plan | the whole answer | the plan itself | max-revisions | **the new piece (proposed)** |
| **Escape** | `ask_user` | "am I blocked on something only the human has?" | — | — | exists |

The insight that collapses the confusion: **level 1 is already a persistence loop.** Every ReAct step
re-evaluates and re-strategizes — a tool errors, the executor tries something else. So *"should the
executor change strategy?"* is already answered **yes, tactically.** What is missing is not executor
self-correction; it is **strategic re-evaluation of a complete answer, without a human in the middle**
(level 3) — today that failure just returns to the user (§0, "wait for the user to steer").

### 9.2. Two properties make every level work (same instinct as D1)

**P1 — the evaluator must not be the actor.** An agent grading its own work inflates — it declares
victory. This is the most common failure mode of self-correcting agents, and it is exactly the D1
separation (planner deliberates, executor grounds) applied one level up: the level-3 critique must be
a **distinct role from the executor** that produced the answer. Cheap, but it is what *detects* that
another iteration is needed — and its output is a **verdict, not a plan** (Q9a): the re-plan on a
failing verdict is a separate, normal planner call. The inline-self-critique variant (executor grades
itself) is rejected for the same reason D1 rejects a planner-that-answers.

**P2 — the loop needs an objective stop condition.** A loop against *"did I do a good job?"* never
terminates cleanly. So the planner emits a **success criterion** alongside the refined task —
*"done when: the report has a per-region 2024-vs-2025 delta table."* Concretely: **add a
`success_criteria` field to the `Plan` schema** — the same schema-widening already planned for
`context` / `artifact_refs` (§4), nullable under the strict-mode rule. This is the highest-value single
addition in this section, because it:

- gives the level-3 critic something objective to check instead of vibes;
- makes **"focused"** structural — the loop stops when the criterion is met, not when the model feels
  done;
- makes the §8 *under-briefing* risk observable — a thin brief yields a thin, visibly-inadequate
  criterion.

It makes persistence *terminating by construction* rather than hopeful.

### 9.3. Autonomy is not on-the-fly vs. manual — it is budgeted autonomy

The healthy middle: the agent re-strategizes on its own **up to a budget** (N revisions / T cost / K
turns); on non-convergence it neither loops forever nor gives up silently — it **escalates to the human
as a question** (`ask_user`), which the planner is already built to do. The routing rule:

> **Handle each failure at the lowest competent level; escalate only what that level cannot resolve.**

This makes "adjust the approach" concrete instead of magical, by mapping failure classes to levels:

| Failure | Routed to | Cost |
| --- | --- | --- |
| Transient tool error | level 1 retry | ~free |
| Wrong tactic (e.g. the `.xlsx`-won't-parse-as-CSV case, O1) | level 1 / 2 change tactic | cheap |
| Answer produced but fails `success_criteria` | **level 3, revise plan** | one extra cycle |
| Blocked on info only the human has | `ask_user` escape | a turn |
| Goal itself ill-posed | re-clarify with human | a turn |

The third row is the new one: today it returns to the human; level 3 catches it first.

### 9.4. Build order (recommendation)

1. **Tune level 1 first — nearly free.** Make the executor's turn budget explicit and prompt it to
   *persist-then-report* rather than bail. Best effort-to-effect ratio; may reduce how much level 3 is
   needed at all.
2. **Add `success_criteria` to the `Plan` schema (§4) — in the *shared* planner, for both `run` and
   `chat` (Q9c).** Small, and the enabler for everything else. Fold into the schema-widening already
   scoped for chat-planner.
3. **Build level 3 (critique loop) as explicit, budgeted, opt-in — as a shared planner mechanism, not
   chat-only (Q9c).** Cycle: executor answers → **critic** judges against `success_criteria` (Q9a) →
   **satisfied** ⇒ deliver the answer, or **not** ⇒ **planner re-plans** with the critic's `gaps` and
   re-delegates — capped at N revisions, then escalate via `ask_user`. **`run` is its prime
   beneficiary** — it is single-shot on execution with no human turn to catch a bad answer. **Promote to
   default on `agent eval` evidence** (plan.md §F), exactly as O2 gates `--plan`. It is measurable:
   "more persistent" = answer quality per turn *before* human intervention.
4. **Reach for subagents (level 2) for focus, not persistence.** Their real payoff is **context
   isolation** — the coordinator never sees the child's thrashing, and a narrow-prompt / narrow-tool
   child *cannot* wander (a focus win, orthogonal to the persistence loop). Use when a subtask genuinely
   benefits from isolation, not as the primary persistence mechanism.

### 9.5. Interaction with D2 / O4 (design them together)

Level 3 has real tension with the **stateless executor** (D2) and **executor working-state carryover**
(O4). A revise-and-retry cycle wants the executor *not* to start cold on each revision, or it re-derives
everything. Cache-with-fallback + the manifest (D3–D5) is most of the answer — state lives on disk, so a
cold retry re-*reads* rather than re-*computes*. But a tight critique loop is precisely the
incremental-build case O4 named as where D2 might need its scoped exception, so **level 3 is where O4
first bites** — the critique loop and the O4 carryover decision should be designed in one pass, not
separately.

### 9.6. Open questions (this section)

- **Q9a — Critic surface (resolved: a verdict-emitting critic, separate from the plan; re-plan is a
  normal planner call).** Correcting an earlier over-collapse — *"reuse the planner, add an `accept`
  field to `Plan`"*: **a critic's output is a verdict, not a plan.** Folding a verdict into `Plan` crams
  a tagged union into the schema (on approve, `refined_task`/`artifact_refs` are dead weight) and pays
  full planning cost just to say *"this is fine."* The loop is **three roles, only one of which answers**
  (D1 preserved — the executor is still the sole author):
  1. executor answers →
  2. **critic** judges the answer against `success_criteria` → `{satisfied: bool, gaps: [...]}`. Small,
     cheap output; can be a smaller/faster model, since judging ≠ planning.
  3. `satisfied` ⇒ **deliver the executor's answer, loop ends — no re-plan.** `¬satisfied` ⇒ the
     **planner runs again with its unchanged contract** (output = a `Plan`), now seeing the executor's
     answer + the critic's `gaps` as added context, and emits a revised brief → back to the executor.

  So `Plan` stays **exactly** as §4 defines it — **no `accept`/`verdict` field** — and D1's no-answer
  guarantee is if anything *cleaner*; the verdict lives on the critic's own small schema. The residual
  sub-question is only **whether the critic is a distinct prompt or the planner *model* invoked in a
  judge mode** — but either way the two outputs are **distinct schemas** (verdict vs. plan), which is the
  point the unified-role framing missed. **Lean distinct + cheap:** judgment is a different, cheaper
  operation paid on *every* iteration (including the common approve case), so a small dedicated critic
  keeps the loop's steady-state cost low; back it with the same model only if a second prompt proves not
  worth it. (This is [`subagents.md`](subagents.md) §8's "start simple, split if it over-reaches" — with
  the split warranted up front by the output-type mismatch.)
- **Q9b — Revision budget surface & default.** Flag, config-dir entry, or per-plan field? And N = ?
  (Start conservative — 1–2 revisions — to bound the §5 latency cost.)
- **Q9c — Criterion scope (resolved: shared, and the loop is shared too).** `success_criteria` goes in
  the **shared `Plan` schema** (`internal/agent/plan.go`), so both `run` and `chat` get it — excluding it
  from `run` would be extra work for no gain, and it sharpens the executor's target even with no critique
  loop (§4/§8 "planner role shared"). Going further: **the level-3 critique loop is a shared planner
  mechanism, not chat-only, and `run` is its prime beneficiary.** `run` is deliberate-by-construction but
  single-shot on execution — it emits an answer and stops with no human turn to catch a bad one, whereas
  in `chat` the user is right there next turn. So autonomous `run` is where executor → planner-critic →
  revise matters *most*, and because the critic is just the planner re-run (Q9a), `run` inherits the loop
  with only the revise-cap wiring — no new agent. **Consequence to note (not resolved here):** the
  `revise` path leans on the manifest (D4) to avoid re-derivation on a cold re-seed (§9.5/O4), and `run`
  has **no manifest/scratch cache today** — so a `run` critique loop either stays scoped to
  cheap-to-redo tasks or motivates giving `run` a lightweight manifest. Flagged for the joint §9.5 design
  pass.
- **Q9d — Criterion quality is now load-bearing** (like brief completeness, §8): a well-formed criterion
  can still be a bad one. Same mitigation — observable/auditable, eval-gated — but note it as a new
  prompt-tuning surface.
