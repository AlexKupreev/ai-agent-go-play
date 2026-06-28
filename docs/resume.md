# Resume notes — pick up here

Working scratchpad for "where we stopped." Delete or fold into `plan.md` once acted on.

_Last session: 2026-06-28._

---

## TL;DR of where we are

- **Phases 0–2 are done** (provider port, run_code + destructive-shell gate, capability
  broker + gopher-lua sandbox + audit log). See `plan.md`.
- This session was a **design/review pass**: hardened the capability broker, then planned
  Phase 3 in detail, then opened a deeper question about the security model that is **not yet
  resolved**. That open question is the thing to settle first tomorrow.

## What shipped this session (all committed on `main`)

- `eb25e19`, `428fa3a` — **`call_tool` allowlist primitive**: a *trusted* (ambient-authority)
  built-in is reachable from sandboxed authored code only when the host has `Exposed` it **and**
  the grant names it directly; a `*` grant never escalates into one. (broker `Trusted`/`Exposed`.)
- Earlier broker hardening (already pushed): per-hop HTTP **redirect re-validation**, **symlink**
  path resolution in `pathAllowed`, deterministic tool-schema `required` (+ `Tool.Required`),
  audit write-error surfacing.
- `cec1fa7` — **expanded Phase 3 plan** in `plan.md` (sub-phases 3a–3e + settled decisions).

**Git state:** `cec1fa7` is ahead of `origin/main` locally and **unpushed**. Working tree clean.

## The open question to settle FIRST tomorrow (blocks Phase 3 direction)

**"pi runs extensions with ambient authority and no sandbox — why don't we?"**

Findings & framing from the discussion:

- pi can do ambient authority because its extensions are **human-chosen up front**, it's a
  **watched** coding agent, and its blast-radius answer is **containerize the whole thing**.
- Our profile breaks that: tools are **LLM-authored at runtime** under possibly-injected
  influence, the agent **ingests untrusted web/data**, and it runs **unattended** (web/Telegram).
- **The real tension surfaced:** we already run `shell` as a trusted built-in with ambient
  authority, gated only by a *heuristic* confirm. So the broker does **not** make us hermetic —
  the agent calling `shell` directly (steered by injection) is a *bigger* hole than any
  sandboxed Lua tool, and the broker doesn't touch it. If injection is the threat, **shell is
  the thing to harden first**, not the authored sub-tier.
- The broker's real value (not "safer than shell"): **least privilege per tool**, **audit +
  revoke**, and **a gate that works unattended**. Those are worth keeping regardless.

**User's leaning:** *fix shell first* — but worried that hardening shell could cripple the
agent's self-management.

**Resolution reached on that worry:** don't fix shell by reducing capability (that *would*
cripple self-management on a trusted box). Fix the two things actually broken:

1. **Injection leverage** — mark `web_fetch`/`web_search` output as *untrusted data, not
   instructions* (wrapper + system-prompt rule). Cheap; biggest leverage-per-effort.
2. **Unattended checkpoint** — make `ConfirmFunc` async / frontend-routable and wire the
   `safe`/`balanced`/`permissive` **tier** (already in `capability`) as a user-tunable dial.
   Routine auto-runs; risky waits for approval that works remotely.
3. Keep the **audit log** as backstop.

**Honest ceiling (write this down):** no code fully stops a model being talked into something by
injected text. The real control is a **deployment dial** — full autonomy only when watched;
conservative tier when alone. Hardening shifts where the dial can safely sit; it doesn't remove
the tradeoff. This plumbing (async + tiered approval) is **the same approval mechanism Phase 3's
`author_tool` + broker need**, so it is complementary, not a detour.

## Decision pending (was mid-question when we stopped)

Scope of shell/injection hardening to do before resuming Phase 3 — options on the table:

- (a) **Untrusted-content framing only** — prompt/small-code change, no approval refactor.
- (b) **Framing + async/tiered approval** — also the plumbing Phase 3 needs.
- (c) **Just record it as "Phase 1.5" in the plan**, decide scope later.

Recommendation if undecided: **(a) now** (cheap, high value), with **(b)** folded in when Phase 3
forces the approval refactor anyway.

## Phase 3 plan (already written in `plan.md`, settled decisions)

Sub-phases **3a** ToolSpec+Registry → **3b** live broker/sandbox wiring → **3c** `author_tool`
pipeline → **3d** tool-search → **3e** lifecycle. Settled: approve-then-test; expose only
`web_search`+`web_fetch` to the sandbox; JSON catalog now / SQLite as the goal; synchronous
approval in v1 (async = Phase 4). Integration model: keep `tools.Tool` for built-ins, add a
`Registry` alongside; recompute tool defs per iteration from an append-only stable-ordered list.

## Concrete next actions (in order)

1. **Decide** the shell-hardening scope (a/b/c above).
2. If (a)/(b): implement untrusted-content framing for `web_fetch`/`web_search` + system-prompt
   rule; (b) also: async/tiered approval refactor of `ConfirmFunc` + wire `Tier`.
3. Then start **Phase 3a** (`ToolSpec` + `Registry`).
4. Housekeeping: push `cec1fa7`; optional markdownlint fix (`design.md` fenced block needs a
   language tag).
