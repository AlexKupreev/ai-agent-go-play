# API transport (Phase 4c)

How the headless engine is exposed to frontends (CLI, web, Telegram as peer clients).
Decision record for the transport fork flagged in [`plan.md`](planning/plan.md) §4c.

_Decided: 2026-06-29._

## Context

Phase 4 makes the engine headless and addressable: web/Telegram join the CLI as peer
clients, and approvals/review get a UI. The internal seams are already in place —
`agent.Observer` (event stream out) and `tools.HumanGate` (`ctx`-blocking, `RunID`-keyed
approvals **and** questions). 4c picks the wire protocol that carries those over the network.

pi (our blueprint, [`design.md` §3](design.md)) ships headless **JSON-RPC/SDK** modes. We
adopt pi's *shape* — "headless engine, frontends are peer clients" — not its exact wire
format, the same selective divergence we applied to the sandbox tier.

## Options

### A. HTTP + SSE  *(chosen)*

Plain `net/http` JSON endpoints for requests; run events streamed over **Server-Sent
Events** (a long-lived `GET` with `Content-Type: text/event-stream`).

- `POST /runs` `{task, model?, tier?}` → `{run_id}` (optional `model`/`tier` override this run;
  `tier` is clamped to no looser than the serve-configured tier, an invalid one fails the run)
- `GET /runs`, `GET /runs/{id}` → list runs / one run's status (metadata)
- `GET /runs/{id}/events` → SSE stream of run events
- `POST /runs/{id}/cancel` → kill switch (cancel a run mid-flight)
- `GET /approvals`, `POST /approvals/{id}` → list / resolve a parked approval or question
- `GET /tools`, `GET /tools/search?q=&k=` → list / search the tool catalog
- `GET /tools/{name}` → one tool's detail (adds impl source + smoke test); `DELETE /tools/{name}` →
  revoke it (404 if absent, audited as `tool_revoked`)
- `GET /audit?run=&type=&limit=` → browse the process-wide audit log (capability use, tool
  authoring/revocation, memory writes, redacted guidance updates); oldest first, `limit` keeps the
  last N matches
- `GET /guidance/global`, `GET /spaces/{id}/guidance`, `GET /sessions/{id}/guidance` → the
  target's explicit guidance document `{scope, target?, guidance, chars}`; unlike listings, these
  target-specific management endpoints intentionally return the text
- matching `PUT` endpoints with `{guidance}` → atomically replace that scope and return the updated
  document. An empty string is an idempotent clear; over 4,000 Unicode characters is 400; an
  unknown space/session is 404. Changed writes emit body-redacted `guidance_updated` audit metadata
- `POST /sessions` `{model?, tier?, space?}` → `{session_id}` (optional initial sticky model/tier/space;
  400 on a malformed tier or an unknown space);
  `GET /sessions` → list (each Info carries the session's sticky `model`/`tier`/`space` and a
  `guidance_chars` size when session guidance is present; the text is not exposed);
  `PATCH /sessions/{id}` `{model?, tier?, space?}` → updated Info (per-field: an omitted field is left
  unchanged, a present field is set — an empty string clears it back to the default; 400 on a
  malformed tier or an unknown space, 404 on an unknown session). `space` may be a space id or its
  display name and is stored canonicalized; an unknown one is rejected where it is set, with the
  available spaces named in the error, rather than failing the session's next turn;
  `DELETE /sessions/{id}` → close (archives the conversation under `sessions/archive/`,
  recoverable; reaps its scratch cache);
  `POST /sessions/{id}/turns` `{text, model?, tier?, space?}` → `{run_id}` (optional per-turn override,
  same clamp; stream the reply via `GET /runs/{run_id}/events`);
  `POST /sessions/{id}/files` (multipart: `file`, optional `source`) → `{path, name, bytes}` — store a
  user-provided file in the session's scratch dir, recorded in the artifact manifest as user-origin so
  the close-reap keeps it. The name is sanitized to a safe basename and made unique; 413 over 20 MB,
  404 on an unknown session. Served only when the host wires a `FileStore` (`agent serve` does). The
  frontend then names the returned `path` in the turn text — the bytes never go to the model, the agent
  reads the file with its own tools
- `POST /reload` → re-read from disk the prompt files (`SYSTEM`/`AGENTS`/`PLANNER`/`CRITIC`) +
  `agents/*.md` **and** the `config.json` defaults (default model + tier ceiling), so an operator
  can retune the engine without a restart (400 on a malformed file/config or bad tier, keeping the
  current state — no partial reload); the next run picks up the change. Flag/env precedence is
  re-applied, so an engine launched with an explicit `--model`/`--tier` keeps that choice — only a
  config-sourced default moves. Per-session/per-turn overrides still clamp to the (possibly new)
  ceiling. The prompt *tier gate* (which workspace-tier prompt files loaded) stays at the startup tier.

A deliberate session turn also emits a `brief` event on the run's stream (the clean, rendered
plan the executor was seeded with, plus any critique-loop notes), published out-of-band like the
approval events so a frontend can render the deliberation distinctly from the raw planner output.

Parked approvals are also **pushed onto the run's event stream** (`approval_requested` /
`approval_resolved` events carrying `approval_id`), so a streaming frontend need not poll `/approvals`;
`POST /approvals/{id}` still resolves. This relies on the engine's run id being threaded through the
runner into the executor, so the escalation's `RunID` routes back to the right stream.

**Per-request overrides (`RunOptions`).** `POST /runs` and `POST /sessions/{id}/turns` accept an
optional `model` and `tier` alongside the task/text. The engine carries these through the
`Runner`/`TurnRunner` seam untouched (an `api.RunOptions` param); the **cmd layer** resolves them —
model falls back to the serve default, and an explicit tier is **clamped** to no looser than the
serve-configured tier (`capability.ClampTier`), so the `serve --tier` flag is a hard ceiling a client
cannot exceed. An invalid tier fails the run. This keeps the trust policy in cmd and the engine core
free of it. The struct is grown by adding fields (e.g. per-role models later), never by widening the
run/turn signatures again.

**Per-session sticky model/tier.** A session can carry a *sticky* model/tier so every turn
inherits it without re-sending: set it at `POST /sessions` or change it live with
`PATCH /sessions/{id}`. `PostTurn` merges the effective options **turn override > session-stored >
serve default** (the still-empty fields resolve downstream), so a per-turn `model`/`tier` still
wins for that one turn. The stored tier is a *request*: it is validated syntactically at the
transport boundary (bad ⇒ 400) but clamped to the serve ceiling **per turn** by the cmd layer,
exactly like a per-request override — so a stored `permissive` on a `--tier balanced` engine runs
at `balanced`. `agent chat --addr`'s `/model` and `/tier` commands (and `--model`/`--tier` at
launch) drive this through the `PATCH`.

**No owner scoping — single-user engine (design §1).** Requests carry no identity; runs and
approvals are visible to any caller of the localhost engine. Because there is no request-level
identity at all, `agent serve` binds loopback-only and refuses a public `--addr` without an
explicit `--unsafe-public` (`security.md` §7) — the transport's "trusted caller" assumption is
enforced at the socket, not merely documented. An earlier Phase 4e draft added an
`X-Agent-Owner` header + owner-scoped runs/approvals; it was **removed** — the trusted users share
one engine and one data domain. Network-facing auth (who may reach the engine) lives in the
frontend, not in a per-request owner label.

**Pros:** stdlib only (no deps); `curl`-able; trivial to debug; single binary; browser-native
via `EventSource`; maps directly onto `Observer.Emit` → one SSE frame per event, and the
approval queue onto plain REST. **Cons:** two one-directional channels — no true
server-initiated push; a client learns of a pending approval by watching the event stream or
polling `GET /approvals`.

### B. JSON-RPC over WebSocket  *(rejected for now)*

One bidirectional socket carrying framed method calls both ways: client calls `run.start`;
server pushes `event.*` and `approval.request`; client replies `approval.resolve`.

**Pros:** true duplex — the engine can actively push "I need approval" to a connected client,
which fits the unattended/remote-approval case. This is also pi's native mode. **Cons:** needs
a WebSocket dependency; not `curl`-able; more client machinery (reconnect, framing,
correlation IDs) for the same first milestone.

## Decision

**Build A (HTTP+SSE) first.** It clears the Phase 4c acceptance bar (start + stream a run;
park + resolve an escalation) with the least code and zero dependencies, and it is the most
debuggable surface for a single-binary, unattended/mobile-approval engine. The duplex
advantage of B only pays off at 4e (a frontend *notified* of approvals rather than watching
the stream), and even then SSE-over-the-run-stream covers the case.

## Keeping JSON-RPC addable later

The wire protocol is isolated to a thin **transport adapter**. The `internal/api` package is
split so B can be added without touching the core:

- **Transport-neutral core** — `Engine` (start a run, subscribe to its events) and a per-run
  `Hub` (implements `agent.Observer`; fans events to subscribers with history replay). Knows
  nothing about HTTP/SSE.
- **Transport adapters** — `http.go` is the SSE adapter: `net/http` handlers that call
  `Engine.StartRun` and stream `Engine`/`Hub` events as SSE frames. A future `jsonrpc.go`
  (or `ws.go`) is a *second* adapter over the **same** `Engine`/`Hub` — it subscribes to the
  same per-run event stream and calls the same `StartRun`/approval-queue methods.

So adding JSON-RPC later is a new file implementing one interface against the existing core,
not a rewrite. The event/approval **semantics** live in the core; only framing differs.

## The CLI as a client

`internal/api/client.go` (`Client`) is the peer side of the SSE adapter:
`StartRun`/`StreamEvents`/`Pending`/`Resolve`. `cmd/client.go` (`agent client <task> --addr`)
uses it to drive a run on a running `agent serve` engine — streaming events to the terminal
(same trace as the in-process `CLIObserver`) and, since SSE has no server push, **polling**
`GET /approvals` to prompt the operator and `POST` the decision. This makes the CLI one client
of the headless engine rather than a special case, the Phase 4 goal; a JSON-RPC adapter would
ship an analogous client.

## Human-gate queue (the async `HumanGate`)

Human-in-the-loop interactions route through one `tools.HumanGate` seam: **approvals** (yes/no
— destructive shell, capability escalation beyond the tier) via `Approve`, and **questions**
(free text — the executor's `ask_user`) via `Ask`. `StdinGate` serves the CLI; the API supplies
a **queue-backed gate** so a remote frontend can decide/answer — the case the seam was built for.

`internal/api/approval.go` — `ApprovalQueue` _implements `tools.HumanGate`_:

- **Park & block.** `Approve(ctx, req)` / `Ask(ctx, q)` register the request under a generated id
  and block until a resolution arrives or `ctx` is done. A cancelled context returns _not-approved_
  (approval) or an error (question), so an abandoned run never executes the gated action.
- **Resolve from an inbound call.** `POST /approvals/{id}` delivers the resolution —
  `{"approved": bool}` for an approval, `{"answer": "…"}` for a question; `GET /approvals` lists
  what is parked (id/**mode**/kind/title/detail/run) for a frontend to render. Resolving an unknown
  or already-resolved id, or using the wrong resolution kind for the item's mode, is a `404` —
  delivery is single-shot (buffered channel; the entry is removed when the call returns).
- **One queue, two consumers.** `agent serve` constructs a single `ApprovalQueue` and passes it
  both to the executor (via `NewExecutor`'s injectable `Gate` parameter) and to `NewServer`
  (for the endpoints), so what the engine parks is exactly what the API exposes.

This replaces the slice's stdin limitation: with the queue wired, headless runs no longer block
on a terminal — a risky action or a clarifying question parks in the queue and waits for an API
resolution. As of Phase 4e-5 the queue also **pushes** the parked item onto the owning run's event
stream (`approval_requested`/`approval_resolved` and `question_requested`/`question_answered`, via
`SetEmitter` → `Engine.PublishToRun`), so a streaming frontend need not poll `GET /approvals`
(which remains as a fallback). `POST /approvals/{id}` still resolves.

## Persistent conversations (sessions)

The stateless `POST /runs` path is one-shot: a fresh executor per call. For a multi-turn
conversation that survives restarts and is resumable from any frontend, the engine adds a
**session layer on top of runs**: a session owns a persisted message history, and a *turn is
a run whose executor is seeded with that history*. So turns reuse the entire run machinery —
the same event hub/SSE stream, the same approval queue, the same audit — nothing is
duplicated.

- **State, not executors, is persisted.** The live executor is unserializable; the message
  history is plain JSON. A turn loads the history, builds a fresh executor, `Restore`s it,
  runs, and the engine persists the updated history. `internal/session` is the store (one
  JSON file per session; SQLite is the eventual home behind the `session.Store` interface).
- **Turns are serialized per session** (a per-session mutex) so history can't interleave.
- **The system prompt is not stored** — it's re-seeded from current code each turn, so prompt
  changes take effect on resume.
- A session record may carry **session guidance**, loaded after workspace and active-space guidance
  on every turn. It survives restart and is capped at 4,000 Unicode characters. Turn requests do
  not accept an ad-hoc `guidance` field; explicit `GET`/`PUT /sessions/{id}/guidance` endpoints
  manage the durable value, while `GET /sessions` exposes only its redacted `guidance_chars` size.
  This prevents the ordinary turn endpoint from becoming a hidden, non-persistent prompt-override
  channel.
- `Client.StartSession` / `PostTurn` / `CloseSession` / `ListSessions` are the conversation peer
  methods; `GetGuidance` / `SetGuidance` drive the explicit management endpoints. A conversational
  frontend streams a turn's reply on the returned run id.

## Frontends as peer clients (Telegram)

A frontend is just another `api.Client` consumer — no privileged path into the engine. The
optional Telegram bot (`internal/frontend/telegram`, Phase 4e-6) is the reference: a `Bot` maps each
chat to an engine **session**, turning a chat message into a *turn* (`PostTurn`) and the pushed
`approval_requested` event into an **Approve/Deny inline keyboard** wired back to `Client.Resolve`.
`/new` (alias `/reset`) starts a fresh session, `/end` terminates it, and `/reload` re-reads
the engine's prompt/agent-type files (⇢ `POST /reload`, allowlist-gated). Its chat backend sits behind a
`Transport` interface so the bot logic is tested with a fake and no network; the live transport
(`NewHTTPTransport`, backed by the `go-telegram-bot-api` SDK) long-polls the Bot API. The bot is
**optional and
token-activated** (config/env), **auth lives in the frontend** (a fail-closed Telegram user-id
allowlist), and the engine stays bound to `127.0.0.1` — only the bot faces the network. This is
why auth is a frontend concern (design §1): the engine trusts localhost; the bot is the gate.

## Consequences

- CLI becomes one client of this engine (peer to web/Telegram), per the Phase 4 goal.
- Events are serialized to a wire-neutral `Event` type (not `agent.Event` directly) so both
  adapters share one schema and the on-the-wire format is stable independent of internal
  fields.
- Headless planning shipped with the chat planner (`docs/adr/chat-planner.md`): **session turns**
  run the full deliberate planner → executor → critic pipeline over the engine, with the planner's
  `ask_user` routed through the shared approval queue (no stdin needed). One-shot `POST /runs`
  still runs the executor directly — the planner is a session-turn concern, not a one-shot one.
