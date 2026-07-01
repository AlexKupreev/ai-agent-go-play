# API transport (Phase 4c)

How the headless engine is exposed to frontends (CLI, web, Telegram as peer clients).
Decision record for the transport fork flagged in [`plan.md`](plan.md) §4c.

_Decided: 2026-06-29._

## Context

Phase 4 makes the engine headless and addressable: web/Telegram join the CLI as peer
clients, and approvals/review get a UI. The internal seams are already in place —
`agent.Observer` (event stream out) and `tools.Approver` (`ctx`-blocking, `RunID`-keyed
approval). 4c picks the wire protocol that carries those over the network.

pi (our blueprint, [`design.md` §3](design.md)) ships headless **JSON-RPC/SDK** modes. We
adopt pi's *shape* — "headless engine, frontends are peer clients" — not its exact wire
format, the same selective divergence we applied to the sandbox tier.

## Options

### A. HTTP + SSE  *(chosen)*

Plain `net/http` JSON endpoints for requests; run events streamed over **Server-Sent
Events** (a long-lived `GET` with `Content-Type: text/event-stream`).

- `POST /runs` → `{run_id}`
- `GET /runs`, `GET /runs/{id}` → list runs / one run's status (metadata)
- `GET /runs/{id}/events` → SSE stream of run events
- `POST /runs/{id}/cancel` → kill switch (cancel a run mid-flight)
- `GET /approvals`, `POST /approvals/{id}` → list / resolve a parked approval
- `GET /tools`, `GET /tools/search?q=&k=` → list / search the tool catalog
- `GET /tools/{name}` → one tool's detail (adds impl source + smoke test); `DELETE /tools/{name}` →
  revoke it (404 if absent, audited as `tool_revoked`)
- `GET /audit?run=&type=&limit=` → browse the process-wide audit log (capability use, tool
  authoring/revocation, memory writes); oldest first, `limit` keeps the last N matches
- `POST /sessions` → `{session_id}`; `GET /sessions` → list; `DELETE /sessions/{id}` → terminate;
  `POST /sessions/{id}/turns` `{text}` → `{run_id}` (stream the reply via `GET /runs/{run_id}/events`)

Parked approvals are also **pushed onto the run's event stream** (`approval_requested` /
`approval_resolved` events carrying `approval_id`), so a streaming frontend need not poll `/approvals`;
`POST /approvals/{id}` still resolves. This relies on the engine's run id being threaded through the
runner into the executor, so the escalation's `RunID` routes back to the right stream.

**No owner scoping — single-user engine (design §1).** Requests carry no identity; runs and
approvals are visible to any caller of the localhost engine. An earlier Phase 4e draft added an
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

## Approval queue (the async `Approver`)

Risky actions (destructive shell, capability escalation beyond the tier) route through the
`tools.Approver` seam (Phase 4a). `StdinApprover` serves the CLI; the API supplies a
**queue-backed approver** so a remote frontend can decide — the case the seam was built for.

`internal/api/approval.go` — `ApprovalQueue` _implements `tools.Approver`_:

- **Park & block.** `Approve(ctx, req)` registers the request under a generated id and blocks
  until a decision arrives or `ctx` is done. A cancelled context returns _not-approved_ (per
  the `Approver` contract), so a run that is abandoned never executes the gated action.
- **Resolve from an inbound call.** `POST /approvals/{id}` with `{"approved": bool}` delivers
  the decision; `GET /approvals` lists what is parked (id/kind/title/detail/run) for a frontend
  to render. Resolving an unknown or already-resolved id is a `404` — delivery is single-shot
  (buffered channel; the entry is removed when `Approve` returns).
- **One queue, two consumers.** `agent serve` constructs a single `ApprovalQueue` and passes it
  both to the executor (via `NewExecutor`'s injectable `Approver` parameter) and to `NewServer`
  (for the endpoints), so what the engine parks is exactly what the API exposes.

This replaces the slice's stdin limitation: with the queue wired, headless runs no longer block
on a terminal — a risky action parks in the queue and waits for an API decision. As of Phase 4e-5
the queue also **pushes** the escalation onto the owning run's event stream
(`approval_requested`/`approval_resolved`, via `SetEmitter` → `Engine.PublishToRun`), so a
streaming frontend need not poll `GET /approvals` (which remains as a fallback). `POST
/approvals/{id}` still resolves.

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
- `Client.StartSession` / `PostTurn` / `CloseSession` / `ListSessions` are the peer methods;
  a conversational frontend streams a turn's reply on the returned run id.

## Frontends as peer clients (Telegram)

A frontend is just another `api.Client` consumer — no privileged path into the engine. The
optional Telegram bot (`internal/frontend/telegram`, Phase 4e-6) is the reference: a `Bot` maps each
chat to an engine **session**, turning a chat message into a *turn* (`PostTurn`) and the pushed
`approval_requested` event into an **Approve/Deny inline keyboard** wired back to `Client.Resolve`.
`/new` starts a fresh session, `/end` terminates it. Its chat backend sits behind a
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
- The planner step is interactive (`ask_user` over stdin) and does not fit a headless request;
  the API runs the executor on the task directly for now. Headless planning is a later 4c/4e item.
