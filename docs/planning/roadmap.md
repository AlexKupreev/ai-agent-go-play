# Project roadmap

The single ordering document for unfinished work. Detailed planning documents remain beside this
file as specifications and historical evidence; they do not independently set priority.

**Status: active as of 2026-08-21.** Before starting an item, trace the current code and move or
close the item here if reality has changed. Reference docs describe shipped behavior; this roadmap
must not be treated as a capability list.

**Current position (2026-08-21).** R0's code is shipped; its one remaining item is human-only
credential rotation. R1 is shipped: the Telegram delivery/session repairs, session-space
validation, truthful capability-failure auditing, durable audit-history access, and the GPT-5.1
default/context refresh are complete. R2 is shipped: durable guidance, effective-state and status
enrichment, plus space list/show/create parity are complete. Space removal remains explicitly
deferred pending a lifecycle decision. R3's behavior-preserving orchestration seam is next; see
[Hand-off](#hand-off--next-step).

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
- [x] Update the model context-window registry and built-in default. **Done 2026-08-21** — the
      built-in default is now `gpt-5.1`, whose documented 400k context window is registered for
      both the alias and dated snapshots. Exact `context_limits` overrides still win, and unknown
      models still report an unknown window rather than an invented percentage. GPT-5.6 Terra is
      explicitly deferred to representative evals before any later default change.

**Exit:** no known false-success path remains in Telegram/session management, operational failures
are classified truthfully, and bad state is rejected with a recovery path.

### R2 — user control and effective-state visibility

Give users a coherent way to steer the existing system before adding a smarter loop.

- [x] Add durable global/workspace, space, and session guidance with size limits, atomic writes, and
      redacted audit metadata.
- [x] Add `/guidance` global/space/session show/set/add/clear consistently to local chat, remote
      chat, Telegram, CLI, and API.
- [x] Add space list/show/create parity to the CLI/API using the metadata schema and output contract
      in [`../adr/spaces.md`](../adr/spaces.md#61-human-management-contract-shipped-2026-08-21).
      **Done 2026-08-21** — `GET /spaces`, `GET /spaces/{id}`, and `POST /spaces` expose only id,
      name, Unicode guidance size, and timestamps; creation returns 201 + `Location` and classifies
      unusable/duplicate names as 400/409. `agent space list|show|create` provides deterministic
      remote output through `--addr`. Removal remains deliberately absent.
- [x] Support `{{base}}` composition and report prompt/guidance provenance.
- [x] Extend status with active space, workspace, model/tier, prompt sources, and relevant limits;
      expose structured `GET /status[?session_id=<id>]` using the context rule in
      [`flexible-orchestration.md`](flexible-orchestration.md#72-api-additions). **Done 2026-08-21**
      — engine-only status omits session state; an explicit live session adds its canonical space,
      sticky/effective model and tier, and Unicode guidance size. Host and bounded state-disk
      snapshots are shared with the enriched in-run status tool.
- [x] Add a read-only effective-config endpoint and make reload return a meaningful diff.
- [x] Make unknown model/space and dead-engine errors say how to recover.

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
      include `gpt-5.1` versus `gpt-5.6-terra` before any model-default change, and never rewrite
      existing session behavior silently.
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
  requirement exists;
- space removal: after a focused lifecycle decision chooses archive/restore versus purge and defines
  active-session behavior, recovery, confirmations, and body-redacted audit metadata.

## Hand-off — next step

*Updated 2026-08-21 after space metadata parity shipped; this is a pointer, not a log.*

**Next implementation slice: R3's behavior-preserving orchestration seam.** Move/generalize
`cmd/deliberate.go` into `internal/orchestration` with explicit phase/result types while preserving
the current default behavior and tests. Profiles and sparse overrides follow after that seam; do
not mix adaptive routing into the move. The independent human-only credential rotation in R0 is
still outstanding.

**Completed slice: space metadata parity.** `GET /spaces`, `GET /spaces/{id}`, `POST /spaces`, and
`agent space list|show|create` now share the body-redacted metadata contract in the spaces ADR.
There is deliberately no `DELETE /spaces/{id}` or `agent space rm`; lifecycle semantics remain a
separate deferred decision.

**Completed slice: status enrichment.** `GET /status` now returns the complete secret-safe effective
configuration plus live host and bounded state-disk measurements. `?session_id=<id>` explicitly
adds the live session's sticky/effective model and tier, Unicode guidance size, and canonical active
space; bare status never guesses a session. The in-run status tool renders the same configuration
metadata alongside its run id and context-window gauge.

**Completed slice: guidance services and deterministic command/API surface.** The durable layers
and their management surface are now in place: `<workspace>/.agent/guidance.md`, active-space
guidance, and
`Session.Guidance` are independently capped at 4,000 Unicode characters and compose into executor
prompts in that order. Local/remote chat and Telegram share `/guidance
global|space|session show|set|add|clear`; the standalone `agent guidance` client and explicit
`GET`/`PUT` management endpoints cover out-of-band use. The design is specified in
[`flexible-orchestration.md`](flexible-orchestration.md#6-guidance-architecture). Current
implementation facts:

- `internal/guidance.FileStore` is the narrow global/workspace `Get`/`Set` seam. Empty writes remove
  the file, unchanged writes are idempotent, and actual changes emit only scope + previous/resulting
  Unicode size/hash. Reuse `guidance.RecordUpdate` for space/session service mutations.
- `space.Space.Guidance` persists as `guidance` in `space.json`; the agent reads/writes it through
  `space_guidance` / `update_space_guidance`. There is intentionally no legacy `notes` field,
  migration, alias tool, or planned `/notes` command.
- `session.Session.Guidance` persists through the existing atomic file store and reaches the turn
  runner through an internal-only `RunOptions.Guidance` field; `session.Info` exposes only
  `guidance_chars`. Do not add guidance text to listings or ordinary turn request JSON.
- `withGuidance` owns executor precedence: operator appends, workspace guidance, space guidance, then
  session guidance. Workspace guidance is read for every new executor, including one-shot run,
  local chat, serve, eval, and prompt inspection.
- Transport stays out of the stores: narrow workspace/space/session guidance services in the
  API layer, following the existing injected `RunStore`, `FileStore`, and `SpaceResolver` seams.
  Treat an empty write as an idempotent clear: remove the workspace guidance file, omit empty
  session guidance, or save empty space guidance. Audit changed state as scope + previous/resulting
  size/hash, never the guidance body; do not emit a duplicate update for an already-empty scope.
- `/guidance` global/space/session show/set/add/clear is consistent across local chat, remote chat,
  Telegram, and the CLI/API. Partial removal in v1 is show + set with revised text, not substring
  matching.

Also worth knowing while continuing R2:

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
