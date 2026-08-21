# Project roadmap

The single ordering document for unfinished work. Detailed planning documents remain beside this
file as specifications and historical evidence; they do not independently set priority.

**Status: active as of 2026-08-21.** Before starting an item, trace the current code and move or
close the item here if reality has changed. Reference docs describe shipped behavior; this roadmap
must not be treated as a capability list.

**Current position (2026-08-21).** R0's code is shipped; its one remaining item is human-only
credential rotation. R1 is in progress: chunked Telegram delivery, the Telegram session-command
repairs (`/start`, `/stop`, close/purge bindings, `/purge` confirmation), and session-space
validation, truthful capability-failure auditing, and durable audit-history access are shipped.
Next up is refreshing or replacing the model context-window registry. See
[Hand-off](#hand-off--next-step) at the end of this file for the trace that is already done on it.

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
- [x] Make `/start` onboarding-only; define or remove `/stop`; update close/purge bindings only after
      API success; confirm `/purge`. **Done 2026-08-21** — `/start` prints the shared `helpText`
      plus whether a session is running and touches nothing (it no longer shares `/new`'s case);
      `/stop` is removed as an `/end` alias and now explains itself, since real cancellation needs
      the chat → active-run binding R6 owns. `closeChat`/`purgeChat` both go through `endChat`,
      which calls the engine first and drops the chat → session binding only on success, so a
      failed close reports the error, keeps the session, and `/new` refuses to replace it.
      `/purge` sends a `Delete permanently` / `Keep it` keyboard whose callback data carries a
      single-use 12-byte nonce (never the session id) that expires after 2 minutes; an expired,
      replayed, or superseded confirmation deletes nothing. Documented in `usage.md`
      §"Ending a conversation".
- [x] Validate a session's space on create/update and include available spaces in the error.
      **Done 2026-08-21** — `Engine.StartSession`/`UpdateSession` run the requested space through an
      injected `SpaceResolver` (`SetSpaceResolver`, wired in `cmd/serve.go` over the workspace's
      `space.Store`, like `SetRunStore`/`SetSessionCloseHook`), store the canonical id, and reject an
      unknown one with `ErrUnknownSpace` → HTTP 400. `space.Store.Resolve`'s miss now names the
      available spaces, so every entry point that already used it — the `switch_space` tool, local
      chat's `/space` and `--space` — gained the same error for free. The remote REPL and Telegram
      send the argument as typed (the engine resolves a name or an id) and report the id it stored.
      A nil resolver keeps the old store-verbatim behavior for embedders with no space store.
- [x] Record scrape/service failures as failed capability use, not security denial; preserve paid
      call attribution on failure. **Done 2026-08-21** — audit has a typed three-state capability
      outcome (`capability_exercised`, `capability_failed`, `capability_denied`) shared by the
      broker and paid scraper. Once policy allows an operation, transport, response-read,
      filesystem, called-tool, random-source, and HTTP-status failures use `capability_failed`
      with a stable `error_class` and optional numeric `status`; `capability_denied` is policy-only.
      Failed scrapes retain their run id, host, `[secret:scrapingant]`, and `[browser]` cost marker
      while never logging the full URL, raw error, or key. The API/CLI/activity readers already
      filter arbitrary event-type strings, and their documented type lists now include the new one.
- [x] Pass the central audit reader to every applicable frontend and add local `agent audit`
      behavior consistent with local `agent usage`. **Done 2026-08-21** — `run`, local `chat`, and
      `serve` now give `recent_activity` the process-wide reader; eval deliberately remains
      variant-local for reproducibility. A read-only JSONL reader lets `agent audit` default to the
      local `<config-dir>/audit.jsonl` without creating a missing file, while an explicitly supplied
      `--addr` preserves API/alias behavior. Command tests cover selection, filters, limits, and a
      missing log.
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

*Written 2026-08-21, after durable audit-history access shipped. Replace this section when the next
slice starts; it is a pointer, not a log.*

**Next slice: R1's sixth bullet — refresh the model context-window registry or replace it with
provider/config metadata plus a conservative fallback.** The current gauge is useful but its
built-in knowledge stops at the `o4-mini` generation. What a first look at the code shows:

- `internal/agent/context.go` owns a small static `contextWindows` map. `ContextWindow` checks an
  exact id, then the longest matching prefix so dated snapshots and sub-variants inherit a family
  size. Unknown ids return zero, which truthfully renders "window size unknown" rather than an
  invented percentage.
- The built-in default is `gpt-4o-mini`; it currently inherits 128k from the `gpt-4o` prefix.
  `internal/agent/context_test.go` pins exact, snapshot, longest-prefix, default-family, and unknown
  behavior. Any refresh should add current model families and prefix-collision cases using current
  official provider documentation as the source of truth.
- `config.json`'s `context_limits` remains the operator escape hatch and wins on an exact model id
  in `cmd.contextLimitFor`. It covers private, renamed, and OpenAI-compatible endpoints and should
  keep precedence over any built-in/provider metadata.
- Every executor frontend already receives the resolved limit (`run`, local `chat`, `serve`, and
  eval), while local and remote chat render the same last-input/window gauge. The provider port
  currently exposes only `Step`; adding metadata there would be a real interface expansion, so
  compare that cost with a focused table refresh before choosing the design.

Also worth knowing before continuing in R1:

- Injected seams are how the API core reaches disk-backed policy: `SetRunStore`,
  `SetSessionCloseHook`, `SetFileStore`, and now `SetSpaceResolver`, all wired in one block of
  `cmd/serve.go`. A nil seam must keep the previous behavior so tests and embedders still work.
- Space validation lives in `Engine.checkSpace`; `ErrUnknownSpace` is a classification sentinel
  matched with `errors.Is` (the message belongs to the resolver, so nothing prefixes it). A session
  error's HTTP status is decided in one place, `sessionErrStatus` (`internal/api/sessions.go`).
- All outgoing Telegram text goes through `Bot.send`/`Bot.notify` (`render.go`); `b.transport.Send`
  has exactly one caller and new code must not add another. A message that is part of a run's
  result must propagate its error the way `stream` does — `notify` is for acknowledgements only.
- Ending a chat's session goes through `Bot.endChat`: engine first, binding dropped only on
  success. Any new session-ending path must use it rather than deleting from `b.sessions` directly.
- A destructive Telegram command has a pattern to copy: `askPurge` + `confirmPurge` + a
  `purge`/`keep` action in `parseCallback`, with a single-use nonce in the callback data (Telegram
  echoes that data back, so it can never carry an id that means something on the engine).
- The fake transport in `telegram_test.go` can fail selected sends (`sendFail`), and `fakeClient`
  can fail close/purge (`closeErr`, `purgeErr`) — that is how the remaining false-success paths
  should be tested.
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
