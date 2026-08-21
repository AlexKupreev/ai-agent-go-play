# Project roadmap

The single ordering document for unfinished work. Detailed planning documents remain beside this
file as specifications and historical evidence; they do not independently set priority.

**Status: active as of 2026-08-21.** Before starting an item, trace the current code and move or
close the item here if reality has changed. Reference docs describe shipped behavior; this roadmap
must not be treated as a capability list.

**Current position (2026-08-21).** R0's code is shipped; its one remaining item is human-only
credential rotation. R1 has started: rune-safe chunked Telegram delivery with propagated send
errors is shipped. Next up is R1's second bullet — `/start`, `/stop`, and the close/purge
bindings. See [Hand-off](#hand-off--next-step) at the end of this file for the trace that is
already done on it.

## How the planning set fits together

| Document | Role now | Live work carried here |
| --- | --- | --- |
| **This roadmap** | Current priority and dependency order | All active work |
| [`flexible-orchestration.md`](flexible-orchestration.md) | Main target architecture and acceptance criteria | R0-R7 below |
| [`review-2026-08.md`](review-2026-08.md) | Dated audit and evidence for correctness gaps | R0-R2 and R7 |
| [`scheduling.md`](scheduling.md) | Optional feature specification | F3 |
| [`vision.md`](vision.md) | Optional, mostly independent feature specification | F2 |
| [`deletion.md`](deletion.md) | Mostly shipped file/session lifecycle design | F1 (`remove_file`) |
| [`file-upload.md`](file-upload.md) | As-built upload record | F1 and F2 |
| [`plan.md`](plan.md) | Historical implementation log for the original kernel | Only explicitly retained deferrals |
| [`review-2026-07.md`](review-2026-07.md) | Superseded review snapshot | No independent priority |
| [`resume.md`](resume.md) | Historical session hand-off log | None |
| [`../deferred/projects.md`](../deferred/projects.md) | Deliberately shelved design | None until a real cwd-per-context need appears |

ADRs remain the rationale for shipped decisions, and reference docs remain the authority on current
behavior. Do not merge future behavior into either until it ships.

## Delivery order

The ordering rule is: repair misleading or unsafe behavior first, make effective state visible
second, then make orchestration more flexible. New capabilities come after the controls they rely
on.

### R0 — operator action and security boundaries

These are urgent even though some are independent of later code.

- [x] Refuse a non-loopback HTTP bind unless an explicit unsafe-public flag is supplied or real API
      authentication exists. **Done 2026-08-21** — `agent serve` checks `--addr` before opening any
      state (`cmd/serve.go` `checkBindAddr`); a non-loopback bind needs `--unsafe-public`, which
      prints a banner naming what it exposes. A non-`localhost` hostname is refused without
      resolving it (fail-closed). Documented in `security.md` §7.
- [x] Make executor security/runtime prompt blocks immutable; a custom `SYSTEM.md` may customize
      behavior but must not remove containment or untrusted-content rules. **Done 2026-08-21** —
      the built-in base is split into named blocks (`executorRoleBlock`, `executorRuntimeBlock`,
      `executorDoctrineBlock`, `executorSecurityBlock`) and `kernelPromptBlocks` re-attaches the
      runtime + untrusted-content blocks over any `SYSTEM.md` **and** over a replace-mode
      `agents/*.md` sub-agent type (runtime only when the child can run code). A prompt that
      restates a block is detected by marker and not duplicated. Composed output is unchanged when
      no override is present.
- [x] Verify and correct config-dir/workspace wording in code comments, flag help, and reference
      docs while touching the prompt boundary. **Done 2026-08-21** — `--config-dir`/`--workspace`
      flag help and `configDir`'s doc comment now state that memory and spaces are workspace-local,
      and the `environment.md` / `usage.md` reference tables no longer claim two config-dirs share
      nothing.
- [ ] **Human-only (blocks the exit criterion).** Rotate the ScrapingAnt key and the Telegram bot
      token that sat in plaintext in the working tree (`review-2026-08.md` §1.8), and decide whether
      the unattended Fly deployment keeps `AGENT_TIER = "permissive"`. The tracked
      `deploy/fly/fly.one-agent.toml` is back in template form as of 2026-08-21; the live app name,
      region, and allowlist moved to the gitignored `deploy/fly/fly.local-one.toml`.

**Exit:** leaked credentials are invalid, public exposure is explicit, and prompt customization
cannot weaken kernel invariants. *Two of three met — rotation is outstanding and only the human can
do it.*

### R1 — correctness and failure recovery

This is the fix-first slice. Keep changes small and independently shippable.

- [x] Make Telegram delivery rune-safe and chunked; propagate send failures instead of reporting a
      successful turn whose answer was not delivered. **Done 2026-08-21** — all outgoing text goes
      through one renderer (`internal/frontend/telegram/render.go`): `splitMessage` splits by rune
      count at 4,096, preferring paragraph → line → word boundaries, `Bot.send` attaches an inline
      keyboard to the last chunk only and returns the first failure, and `Bot.notify` is the single
      place where a delivery failure is deliberately only logged. `stream` carries a run's first
      delivery failure out of the event callback, logs it against the run, and tells the chat what
      was lost. The live transport retries only flood control (429), honoring Telegram's
      `retry_after` up to 30s over 3 attempts. Documented in `usage.md` §"Long answers and delivery
      failures". Persisting delivery status in the run trace remains R6.
- [ ] Make `/start` onboarding-only; define or remove `/stop`; update close/purge bindings only after
      API success; confirm `/purge`.
- [ ] Validate a session's space on create/update and include available spaces in the error.
- [ ] Record scrape/service failures as failed capability use, not security denial; preserve paid
      call attribution on failure.
- [ ] Pass the central audit reader to every frontend and add local `agent audit` behavior consistent
      with local `agent usage`.
- [ ] Update the model context-window registry or replace the hard-coded table with provider/config
      metadata plus a conservative fallback.
- [ ] Add a hard per-run/per-day paid-tool call ceiling before expanding unattended behavior.

**Exit:** no known false-success path remains in Telegram/session management, operational failures
are classified truthfully, bad state is rejected with a recovery path, and paid calls are bounded.

### R2 — user control and effective-state visibility

Give users a coherent way to steer the existing system before adding a smarter loop.

- [ ] Add durable global/workspace, space, and session guidance with size limits, atomic writes, and
      redacted audit metadata.
- [ ] Add `/notes` and `/guidance` consistently to local chat, remote chat, and Telegram; add the
      matching space-management CLI/API surface.
- [ ] Support `{{base}}` composition and report prompt/guidance provenance.
- [ ] Extend status with active space, workspace, model/tier, prompt sources, and relevant limits.
- [ ] Add a read-only effective-config endpoint and make reload return a meaningful diff.
- [ ] Make unknown model/space and dead-engine errors say how to recover.

**Exit:** a Telegram-only user can inspect and change standing guidance, restart, and see the same
effective state; CLI/API users get the same semantics.

### R3 — behavior-preserving orchestration seam

Move policy out of the CLI without changing the default behavior.

- [ ] Move/generalize `cmd/deliberate.go` into `internal/orchestration` with phase/result types.
- [ ] Add validated workflow profiles, sparse session/run overrides, operator ceilings, merge/clamp
      rules, and provenance.
- [ ] Carry workflow options through sessions, API, and clients; preserve today's deliberate path
      when no profile is stored.
- [ ] Ship `quick` and `thorough` first and expose `/profile` and `/deliberate` through one shared
      command-service interface.

**Exit:** existing behavior and tests remain stable by default, profile settings survive restart,
and all frontends invoke one orchestration implementation.

### R4 — improve the agent flow

Make planning and review conditional, measurable, and explainable.

- [ ] Add strict-schema adaptive routing with a safe fallback.
- [ ] Trigger critique/revision from observable run facts and policy, not an unconditional fixed
      pipeline.
- [ ] Persist a user-visible decision trace and expose `/why` without chain-of-thought.
- [ ] Add per-role models and evaluation variants for profiles, planning/critique, and `repeat: N`.
- [ ] Compare simple chat, lookup, ambiguous, multi-step, tool-failure, and destructive-request
      cases for quality, calls, tokens, latency, and unnecessary planner/critic use.

**Exit:** simple turns avoid needless planner/critic calls, complex turns still receive the needed
phases, and each automatic choice is inspectable.

### R5 — structured, bounded delegation

- [ ] Define a strict `SubagentReport` with findings, evidence, artifacts, and uncertainty while
      retaining a compatible text mode.
- [ ] Replace the depth-only limit with shared per-turn agent/count/token/tool budgets and usage
      attribution.
- [ ] Teach coordinator synthesis to reconcile reports and surface conflicts.
- [ ] Add batch parallelism only for agent types proven read-only and approval-free.

**Exit:** delegation limits cannot be bypassed recursively, partial failures preserve useful work,
and concurrent workers cannot mutate shared state.

### R6 — complete the interactive flow

- [ ] Persist Telegram chat/topic/user to session bindings and migrate ephemeral bindings.
- [ ] Adopt the shared command service across Telegram and both chat REPLs (`/sessions`, `/resume`,
      `/cancel`, `/tier`, `/space`, `/status`, `/usage`, and reload diff).
- [ ] Model queue state and cancellation explicitly; normalize group/topic and `@botname` commands.
- [ ] Add low-noise progress rendering and persist final delivery status in the run trace.

**Exit:** restart resumes the right conversation, commands mean the same thing everywhere, queued
work is visible/cancellable, and delivery success is observable.

### R7 — budgets, rollout, and documentation

- [ ] Add token soft/hard budgets and a shared structured exhaustion result; retain operator ceilings
      above profile/session requests.
- [ ] Run the representative evaluation set repeatedly before considering `adaptive` as the default;
      never rewrite existing session behavior silently.
- [ ] Split the oversized usage guide only after the new surfaces settle.
- [ ] Update reference docs in the same change that ships behavior, and mark superseded ADR sections
      rather than rewriting history.

**Exit:** cost and resource ceilings are enforced and visible, a default change is evidence-backed,
and embedded self-documentation describes only shipped behavior.

## Optional capability lane

These do not determine the main architecture order. Pull them in only at the indicated dependency
point rather than allowing them to interrupt R0-R2.

### F1 — finish file lifecycle

- [ ] Add provenance-safe `Manifest.Remove` plus a confirmed/audited `remove_file` human and agent
      surface. The upload and close/purge paths are already built; see
      [`deletion.md`](deletion.md) and [`file-upload.md`](file-upload.md).

This can ship after R1. It should share containment and approval rules with the existing artifact
manifest instead of inventing another path model.

### F2 — vision tool

- [ ] Implement the one-shot `view_image` approach in [`vision.md`](vision.md), including provider
      image blocks, containment, size errors, configuration, and adapter tests.

This is roughly independent and can ship after R1. Do not add persistent in-conversation image
payloads until there is evidence the simpler tool path is insufficient.

### F3 — scheduling

- [ ] Build [`scheduling.md`](scheduling.md) S1-S3 only after R1's spend limits and R6's durable
      frontend origin/binding and delivery semantics exist.

Scheduling turns existing runs into unattended runs, so it inherits stricter requirements: bounded
spend, no silent approval escalation, idempotent firing, and observable delivery failure.

## Explicit deferrals

Keep these out of the active queue until their trigger appears:

- second provider adapter: when an actual provider swap or A/B test is planned;
- WASM sandbox: when Lua containment or memory isolation is demonstrably insufficient;
- projects: when a context genuinely needs to switch working directories, not just data;
- automatic compaction and per-hub replay caps: when observed context/memory pressure justifies
  policy beyond the current manual and process-level controls;
- email schedule delivery: after Telegram delivery is complete and a real recipient/configuration
  requirement exists.

## Hand-off — next step

*Written 2026-08-21, after Telegram chunked delivery landed. Replace this section when the next
slice starts; it is a pointer, not a log.*

**Next slice: R1's second bullet — `/start`, `/stop`, close/purge bindings, `/purge`
confirmation.** The spec is [`flexible-orchestration.md`](flexible-orchestration.md) §8.3
("Command service"), but take only the four verbs above here — the shared command service itself is
R3/R6 work and must not be pulled forward. What a trace of the current code already shows:

- `handleCommand` (`internal/frontend/telegram/telegram.go`) has `/start` in the same case as
  `/new` and `/reset`, so a new user's very first `/start` closes (archives) the session they were
  mid-conversation in. It must become onboarding + help + "a session is active", never destructive.
- `/stop` shares a case with `/end`, so it silently means "archive the conversation". The spec
  wants it to be `/cancel` or gone. `api.Client` already has `StopRun(ctx, runID)`, but the bot's
  `Client` interface does not list it and nothing maps a chat to its in-flight run — `stream` is
  the only place holding a `runID`. Adding a real `/cancel` therefore needs a chat → active-run
  binding; removing `/stop` needs none. Decide that first, and note that queue/cancellation
  modelling proper is R6.
- `closeChat`/`purgeChat` delete the chat → session mapping *before* calling the engine and only
  log an API failure to stderr, so a failed close still answers "session ended" and orphans the
  session. Reorder to call the API first and keep the binding on failure; the reply must say what
  actually happened. This is the same false-success shape the delivery fix just removed.
- `/purge` is irreversible and takes effect on the bare word. The spec wants a short-lived
  confirmation button carrying a server-side nonce, not the session id (the callback data is
  echoed back by Telegram). `handleCallback`/`parseCallback` currently accept only
  `approve:<id>` / `deny:<id>`, so this adds a third action and a small nonce store beside
  `pendingQ`.

Also worth knowing before continuing in R1:

- All outgoing text now goes through `Bot.send`/`Bot.notify` (`render.go`); `b.transport.Send` has
  exactly one caller and new code must not add another. A message that is part of a run's result
  must propagate its error the way `stream` does — `notify` is for acknowledgements only.
- The fake transport in `telegram_test.go` can now fail selected sends (`sendFail`), which is how
  the false-success paths in this next slice should be tested.
- `cmd/serve.go` validates `--addr` at the top of `RunE`; anything that adds a second listener
  should reuse `checkBindAddr` rather than re-deriving the rule.
- Prompt text is no longer one constant. Anything that appends to the executor prompt should decide
  whether it is a kernel block (`kernelPromptBlocks`) or a style block, and `environment.md`'s
  survives/does-not table must be updated in the same change.

## Rules for maintaining this roadmap

1. Add priority and dependency changes here; put implementation detail in the focused spec.
2. Do not copy completed history here. Check an item off and link the shipping change or reference
   documentation when useful.
3. A slice is not done until build, vet, relevant tests, and reference docs pass together.
4. If code and a planning document disagree, verify the code, correct the reference docs if needed,
   then update this roadmap and the focused plan in the same change.
