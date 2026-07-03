# Scheduling — design & roadmap

Recurring, unattended jobs the agent runs for you on a clock: *"store the most interesting
news every morning at 9:00."* The human touchpoints are the **initial ask** (in natural
language, in a normal conversation) and the **final delivery** (Telegram / email / stored) —
nothing in between. This is deliberately **not** the interactive/human-in-the-loop case
([`../security.md`](../security.md) approvals, the executor's `ask_user`); a scheduled run is
autonomous start-to-finish.

It composes cleanly with the rest of the system: a scheduled run is just a normal engine
run with a **trigger** in front of it and a **delivery** behind it. ~90% of the machinery
(detached execution, run lifecycle, audit, tools, tiers) already exists; this doc scopes the
~10% that's new.

---

## 0. The model in one paragraph

The agent, mid-conversation, calls a built-in tool `schedule_task(when, task, delivery)` that
persists a **schedule entry** to a JSON store. A **scheduler loop** inside `serve` wakes
periodically, finds due entries, and starts each as an ordinary background run
(`Engine.StartRun`). It owns that run's lifecycle end-to-end — start, wait for the terminal
state, then hand the result to a **deliverer** (push to the originating Telegram chat, email,
or just store it). The engine kernel is untouched; scheduling sits on top of the existing API.

---

## 1. Where this sits (what it reuses vs. what's new)

Reused, unchanged:

- **Detached execution.** `Engine.launch` (the spine of `StartRun`) already runs work in a
  goroutine under `context.WithCancel(context.Background())` — a run outlives the request that
  started it (`internal/api/engine.go`). A scheduled run is just a run with no client streaming it.
- **Run lifecycle.** `RunInfo{ID, Task, State, Result, Error, Usage, Steps}` +
  `RunStatus(id)` / `ListRuns()` already track a run's outcome. The scheduler polls these; it
  needs no new engine hook.
- **Tiers + audit + tools + memory.** A scheduled run gets the same `NewExecutor` wiring as any
  other run, so tier gating, the audit log, the tool catalog, and memory all apply as-is.
- **Telegram as a peer sink.** `Transport.Send(ctx, chatID, text, buttons)` already pushes to a
  chat (`internal/frontend/telegram/telegram.go`) — the free delivery channel.

New:

1. a **schedule store** (persisted entries),
2. the **`schedule_task`** built-in (the agent authors an entry),
3. the **scheduler loop** in `serve` (the trigger),
4. a **`Deliverer`** seam (route the result at the end),
5. a thin **management surface** (`agent schedule list/add/rm` + `/schedules` endpoints).

Relation to neighbours:

- **`spawn_agent` (§3 of [`subagents.md`](subagents.md))** is *foreground/blocking* delegation
  — the coordinator waits and threads the child's answer back into the conversation. Scheduling
  is *fire-by-clock, deliver-at-end*. Different trigger, different join.
- **`RunMeta.Origin`** (the forward-compat substrate in [`subagents.md`](subagents.md) §7):
  scheduled runs set `Origin: "scheduled"` so they're distinguishable in the audit/run list.
  If that substrate lands first, reuse it; if not, this is its first consumer.
- **The unattended posture** ([`../security.md`](../security.md)): the tier dial exists precisely
  for "the agent runs alone." Scheduled runs are the canonical alone case — see §5.

---

## 2. The flow, end to end

1. **Author.** In a conversation: *"store the most interesting news every morning at 9."* The
   model translates the NL to a schedule and calls
   `schedule_task(when="0 9 * * *", task="find and summarize the most interesting news of the day", delivery="telegram")`.
   The tool validates + persists the entry and returns its id. (Translating NL → cron is a
   natural LLM job; the tool only validates the result.)
2. **Persist.** The entry lands in `<config-dir>/schedules.json`, surviving `serve` restarts.
3. **Trigger.** The scheduler loop (a ticker in `serve`) wakes each minute, finds entries whose
   `NextRun ≤ now`, and for each computes the following `NextRun` and starts a run:
   `Engine.StartRun(entry.Task)` (with `Origin: "scheduled"`, `entry.Tier`).
4. **Execute.** The run does the job with the full tool set, bounded by `maxIterations`, audited.
5. **Deliver.** The scheduler polls `RunStatus(id)` to a terminal state, then calls
   `deliverer.Deliver(target, subject, RunInfo.Result)` — a Telegram push, an email, or a no-op
   (result already stored in the transcript / memory).

The scheduler, not the run, owns delivery — so the run machinery stays delivery-agnostic and a
delivery failure never corrupts run state.

---

## 3. Components

### 3.1 The schedule store (`internal/schedule`)

Mirror the established store pattern (`internal/session`, `internal/memory`): an interface + a
JSON `FileStore` with atomic temp-and-rename writes, loaded at `serve` start.

```go
// illustrative
type Entry struct {
    ID        string    // generated
    Cron      string    // cron expression, e.g. "0 9 * * *"
    Task      string    // the natural-language task the run executes
    Tier      string    // trust tier for the unattended run (default: conservative, §5)
    Delivery  Delivery  // where the result goes
    Enabled   bool
    OneShot   bool      // true ⇒ disable after the first fire (an "at", not a "cron")
    NextRun   time.Time // computed; the store's scheduling cursor
    LastRun   *time.Time
    LastRunID string    // for correlating with the run list / audit
    CreatedBy string    // "telegram:<chat>", "cli", etc.
}

type Delivery struct {
    Kind   string // "telegram" | "email" | "store"
    Target string // chat id / email address / "" for store-only
}

type Store interface {
    Add(e Entry) (Entry, error)
    List() []Entry
    Get(id string) (Entry, bool)
    Update(e Entry) error   // persist NextRun/LastRun/Enabled after a fire
    Delete(id string) error
}
```

Path: `schedulesPath()` → `underConfigDir("schedules.json")` (`cmd/config.go`), alongside
`tools.json` / `memory.json`. Per the share-nothing rule, schedules live under the config dir,
so two `--config-dir` agents keep separate schedules.

### 3.2 `schedule_task` — the agent authors the schedule

A trusted host built-in (not sandbox-exposed, like `remember` / `spawn_agent`), added in
`NewExecutor` when a store is wired (nil ⇒ omitted, matching every other optional dep). Its `Run`:

1. validates `when` parses as a cron expression (reject → returned to the model to retry),
2. validates `delivery`,
3. computes the first `NextRun`,
4. persists the `Entry`, emits a `schedule_created` audit event, and returns the id + the
   resolved next run time (so the model can confirm back to the user in words).

Companion read tools so the agent can manage schedules conversationally ("what have I got
scheduled?" / "cancel the news one"): `list_schedules` and `cancel_schedule(id)`. These make the
schedule set introspectable **from inside the agent** — the reason to build this in-engine rather
than lean on OS `cron` (a crontab the agent can't cleanly read, author, or reason about).

### 3.3 The scheduler loop (in `serve`)

One goroutine started by `serve` (only `serve` — the persistent process is the only sane host;
`run`/`chat` exit and would drop the loop). Shape:

```go
// illustrative
func (s *Scheduler) Run(ctx context.Context) {
    t := time.NewTicker(time.Minute)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case now := <-t.C:
            for _, e := range s.store.List() {
                if !e.Enabled || now.Before(e.NextRun) {
                    continue
                }
                s.fire(ctx, e) // advance NextRun (or disable if OneShot), then StartRun
            }
        }
    }
}
```

`fire` **first** advances `NextRun` (or disables a one-shot) and persists — so a crash mid-run
never double-fires — then `StartRun`s the task and, in its own goroutine, polls `RunStatus` to a
terminal state and delivers. Per-entry concurrency is bounded (a run in flight for an entry
skips re-firing); a global semaphore caps total concurrent scheduled runs (low-resource box).

**Missed runs** (serve was down at 9:00): v1 **skips** to the next occurrence — on load, any
`NextRun` in the past rolls forward to the next future slot. A catch-up window is a later knob.

### 3.4 Delivery (`Deliverer`)

```go
// illustrative
type Deliverer interface {
    Deliver(ctx context.Context, d Delivery, subject, body string) error
}
```

- **`TelegramDeliverer`** — pushes to `Delivery.Target` (a chat id) via the same transport the
  bot uses. `serve` constructs the Telegram transport once and shares it with both `NewBot` and
  the deliverer (or passes the deliverer a `func(chatID int64, text string) error` closure over
  `Transport.Send`). Free reuse of the existing peer-client channel.
- **`EmailDeliverer`** — SMTP; a small new integration (host/port/from creds via config +
  `AI_AGENT_SMTP_*` env). Deferred to a follow-up; the interface makes it additive.
- **`StoreDeliverer` / noop** — the result is already in the run transcript; optionally also
  `remember` a summary so a later conversation can `recall` it. The zero-config default.

A `Deliverers` map keyed by `Delivery.Kind` selects the impl; unconfigured kinds degrade to
store-only with a logged warning (fail-safe, like the Telegram allowlist).

---

## 4. Management surface (CLI + API)

Mirror the existing management endpoints (`/tools`, `/audit`, `/runs`):

- `POST /schedules` (add), `GET /schedules` (list), `DELETE /schedules/{id}` (remove),
  optional `POST /schedules/{id}` to enable/disable.
- `Client` gains `AddSchedule` / `ListSchedules` / `DeleteSchedule`.
- `agent schedule list|add|rm --addr <engine>` (`cmd/schedule.go`), so a human can inspect and
  edit the same set the agent authors — the CLI is a peer client, as everywhere else.

---

## 5. Trust & the unattended posture

Scheduled runs are the textbook **unattended** case, and the security model already has the
controls; scheduling just has to *use* them:

- **Conservative tier by default.** An entry carries its own `Tier`; default it to the config
  default but recommend `safe`/`balanced`. An escalation with no one watching **and no gate
  route** simply rejects (`Tier.AutoApproves`, [`../security.md`](../security.md)) — safe by
  construction. (A future refinement could route a scheduled run's escalation to Telegram, but v1
  keeps scheduled runs approval-free: routine auto-runs, risky rejects.)
- **Injection surface.** *"fetch data from a site"* unattended is the canonical prompt-injection
  vector. The untrusted-content wrapper on `web_fetch`/`web_search` already frames fetched text as
  data-not-instructions; the tier ceiling bounds the blast radius. Nothing new needed — but it's
  the reason the tier default matters.
- **Fully audited.** Authoring emits `schedule_created`; each run emits `run_usage` and its normal
  effect events (`Origin: "scheduled"`). So *"what did the agent quietly do overnight?"* is always
  answerable via `agent audit` / `recent_activity` — a hard requirement for autonomous jobs.

---

## 6. Decisions (settled)

- **In-engine, not OS cron.** The agent must author, introspect, and cancel schedules from a
  conversation; a crontab can't be cleanly read/reasoned-about by the agent, and a fired crontab
  job would have to re-enter the agent anyway. Build the loop in `serve`.
- **`serve`-only.** The persistent engine is the only host that's alive at fire time. `run`/`chat`
  don't offer scheduling (or forward to a running engine).
- **Scheduler owns the run lifecycle + delivery.** It polls `RunStatus`; the engine kernel and the
  run machinery stay delivery-agnostic (no `launch` hook, no coupling).
- **Advance-then-fire.** Persist the next `NextRun` before starting the run, so a crash can't
  double-fire.
- **Store under the config dir**, JSON, mirroring `memory`/`tools`/`sessions` — share-nothing.
- **Miss policy: skip** to the next occurrence in v1.

## 7. Open questions

- **Cron dependency.** A real cron parser is a solved-but-fiddly problem. Options: adopt
  `robfig/cron/v3` (consistent with already taking the telegram + yaml deps; correct DST/edge
  handling) **or** ship a minimal recurrence struct first (`Daily|Hourly|Weekly` + `HH:MM` +
  weekday) and grow to full cron later. *Leaning `robfig/cron/v3`* — hand-rolled cron is a footgun.
- **Delivery-target capture.** How does `schedule_task` know *where* to deliver? For a
  Telegram-originated conversation the originating chat id must thread into the tool's context
  (today the executor doesn't know the frontend chat). For a CLI-authored schedule, default to
  store-only or an explicit `--deliver`. This is the main new wiring question — it needs the
  frontend origin to reach the tool (a small `RunMeta`/context addition).
- **Time zone.** "9:00" in the box's local time; make it explicit and consider a per-entry TZ later.
- **Overlap / long jobs.** If a daily job still runs when the next slot arrives, skip (chosen) vs
  queue vs cancel-and-restart. v1: skip, with the in-flight guard.
- **Escalation routing for scheduled runs.** v1 rejects unattended escalations; a later option is
  to route them to Telegram (reusing the human-gate the UX cluster just unified) so a scheduled run
  can ask for a one-off approval. Deferred — keeps v1 strictly non-interactive.

## 8. Tasks (files they touch), staged

**S1 — store + tool (no trigger yet).**
- [ ] `internal/schedule`: `Entry`, `Delivery`, `Store` + `FileStore` (JSON, atomic write) + tests.
- [ ] cron parse/next helper (dep or minimal struct — §7) + `NextRun` computation.
- [ ] `internal/tools/schedule.go`: `schedule_task`, `list_schedules`, `cancel_schedule`
      (trusted, not sandbox-exposed); `NewExecutor` wires them when a store is present (nil ⇒ omitted).
- [ ] `cmd/config.go`: `schedulesPath()`; `serve` loads the store and threads it into executors.
- [ ] audit: `EventScheduleCreated = "schedule_created"`.
- *Ships:* the agent can record + list + cancel schedules; nothing fires yet (inspect the JSON).

**S2 — the scheduler loop + store-only delivery.**
- [ ] `internal/schedule/scheduler.go` (or in `internal/api`): the ticker, `fire`, advance-then-run,
      `RunStatus` polling, the concurrency semaphore, miss-skip on load.
- [ ] `serve` starts the loop in a goroutine (like the Telegram bot) when a store is wired.
- [ ] `Origin: "scheduled"` on the run (via `RunMeta`, subagents §7 substrate — build it if absent).
- [ ] Store-only `Deliverer` (result stays in the transcript; optional `remember` summary).
- *Ships:* schedules actually fire and runs execute unattended, reviewable via `agent audit`.

**S3 — delivery channels + management surface.**
- [ ] `Deliverer` interface + `TelegramDeliverer` (share the transport with the bot) + delivery-target
      capture (§7 open question).
- [ ] `/schedules` endpoints + `Client` methods + `cmd/schedule.go` (`agent schedule list|add|rm`).
- [ ] (Follow-up) `EmailDeliverer` (SMTP) + config/env.
- *Ships:* the full loop — ask in chat, it runs at 9:00, the collection lands in your Telegram.

## 9. Acceptance

- A `schedule_task` call persists an entry that survives a `serve` restart; `agent schedule list`
  and `list_schedules` show it (S1).
- A due entry fires exactly once per slot, runs unattended at its tier, and is visible in
  `agent audit` with `Origin: "scheduled"`; a crash between advance and run does not double-fire (S2).
- End-to-end: a Telegram-authored *"news at 9"* schedule delivers the run's result to that chat the
  next morning; a one-shot disables itself after firing (S3).
- Missed slots (engine down over the fire time) roll forward, not replay (S2).

## 10. Non-goals / deferred

- **Mid-run human interaction** for scheduled jobs (approvals/questions) — v1 is strictly
  non-interactive; unattended escalations reject. Routing to Telegram is a later option (§7).
- **Distributed / multi-node scheduling, exactly-once across restarts under contention** — this is a
  single-process, single-box scheduler (design §1). At-least-once-with-skip is the contract.
- **A cron *editor* UI** beyond the CLI/agent tools.
- **Email delivery** ships after Telegram (S3 follow-up), behind the same `Deliverer` seam.
