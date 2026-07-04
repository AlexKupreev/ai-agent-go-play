# Resume notes — pick up here

Working scratchpad for "where we stopped." Delete or fold into `plan.md` once acted on.

_Last session: 2026-07-04._

---

## Latest (2026-07-04): Projects track — P5 (docs) — DONE → Projects track COMPLETE

Documented the Projects feature ([`projects.md`](projects.md) P5), closing the track (P1–P5 all done).
Docs-only; `go build ./...` + selfdocs/cmd tests green (`environment.md` is embedded, so
`read_self_docs` now serves the projects section).

- **`docs/environment.md`** — new "Projects — named, recallable workspaces" section (the `projects/`
  layout, trust-by-containment + tier gate on switch, the three tools as a table, the P4 flag/config
  modes as a table + precedence). Added rows to the config-reference table (`--no-project`, `--project`,
  `projects`, `projects_root`) and the files-on-disk table (`.agent/project.md` marker), and a pointer
  from "Two anchors".
- **`docs/usage.md`** — new "Projects" operational section (what the tools do, the flags, flat-repo
  mode) + a pointer from the `agent run` flags line.
- **`README.md`** — "Named projects" paragraph in the customize/experiment block.
- `workspace.md` §6 was already "resolved — see projects.md" from P3, so untouched.

**Projects track is COMPLETE.** Remaining optional follow-ups (not scheduled): auto-switch-on-create
via the `switchWorkspace` seam; bump `last_active` on switch so recall recency reflects use (currently
create-time only). **NEXT candidates across the repo:** Scheduling S1 (schedule store + `schedule_task`
tool, no trigger); Phase 6d (budget dial + context-window awareness); deferred sub-agent fan-out.
**Nothing pushed** — all commits local on `main`.

### Prior (2026-07-04): Projects track — P4 (CLI flags) — DONE

CLI/config control over the projects registry ([`projects.md`](projects.md) P4/§6). Build/vet/`go
test -race` green (full suite); flags, error paths, and config persistence live-verified via the binary.

- **One seam.** `cmd/projects.go` `resolveProjects(homeWorkDir, cfg) → (root, workDir, err)` replaces
  the hard-wired `ProjectsRoot: projects.Root(workDir)` in `run`/`chat`/`serve`. Returns the registry
  root (empty ⇒ list/create/switch_project omitted — same gate the executor already had) and the launch
  workspace.
- **Flags** (persistent, in `root.go`): `--no-project` = flat-repo mode (no registry/tools, workspace *is*
  the repo); `--project <uid|title|path>` activates a project at launch (existing dir ⇒ used as a path,
  else `projects.Resolve` against the root; ambiguous/absent reported). On activation the **workspace
  becomes the project dir** but **root stays the home registry**, so `switch_project` still reaches
  siblings; prints a `project: <path>` line.
- **Config** (`Config.Projects *bool` — unset ⇒ enabled; `Config.ProjectsRoot string`): `projects:false`
  disables by default, `projects_root` redirects the registry. Setters `config set-projects <on|off>` /
  `set-projects-root <path>`. Precedence: `--no-project` wins outright; explicit `--project` forces on
  (overrides config `false`); `--no-project`+`--project` is a conflict error.
- **Serve** threads it as `serveDeps.projectsRoot` (buildExecutor reads the field instead of recomputing).
  `eval` deliberately still untouched.
- Tests: `cmd/projects_test.go` (default root, both disable paths, config-true/root override, conflict,
  activate-by-title/-path, activate-overrides-config-false, unknown-is-error).

**NEXT (Projects):** **P5** — docs: fold "projects" into [`../environment.md`](../environment.md)
(scratch vs project, the `projects/` layout, the three tools, the P4 flags), update `workspace.md` §6 to
"resolved — see projects.md", surface `list/create/switch_project` in `usage.md`/`README.md`. Then the
optional follow-ups (auto-switch-on-create via the `switchWorkspace` seam; bump `last_active` on switch).
Other open tracks unchanged: Scheduling S1, Phase 6d (budget dial), deferred fan-out. **Nothing pushed
this session** — commits local on `main`.

### Prior (2026-07-04): Projects track — P3 (switch) — DONE

Mid-conversation project switching ([`projects.md`](projects.md) P3/§7): `switch_project(project)`
makes a named project the active workspace *without rebuilding the executor*. Build/vet/`go test -race`
green (full suite); the re-anchor is proven end-to-end with a scripted provider (a live model call
needs an API key).

- **Live re-anchor.** New mutable `tools.Workspace` anchor read by the shell at exec time
  (`NewShellIn`; `NewShell(dir, gate)` now wraps `NewShellIn(NewWorkspace(dir), gate)` — signature and
  all callers/tests unchanged). A switch calls `ws.Set(dir)`, so subsequent `cmd.Dir` moves live (§7 —
  `cd` never persists across the per-command processes).
- **Prompt reload under the §5 gate.** New `ExecutorConfig.SwitchWorkspace(workspace) →
  agent.PromptCustomization` seam; `cmd/prompts.go`'s **`switchWorkspaceFn(tier)`** implements it as the
  *same* `loadPrompts(workspace, tier)` used at build time, so the tier gate applies identically to the
  project switched into (`safe` still won't auto-load its AGENTS.md/SYSTEM.md). `Agent.switchWorkspace`
  re-anchors the `Workspace` + recomposes `systemPrompt` via a shared **`baseSystemPrompt(override,
  docsNote)`** helper (captured `docsPromptNote` keeps the recomposition identical to construction);
  the prompt is prepended fresh each request, so it takes effect next turn/step.
- **Resolve + audit.** `projects.Resolve(root, query)`: uid exact (case-insensitive) → title exact →
  title substring; `*AmbiguousError` (candidates) and `ErrNotFound` so the tool asks/guides rather than
  guessing. `switch_project` is trusted, not sandbox-exposed, wired on `ProjectsRoot != "" &&
  SwitchWorkspace != nil` after the agent exists (mutates *this* executor), in `run`/`chat`/`serve`.
  Audited as `audit.EventProjectSwitched`.
- Tests: `internal/projects/resolve_test.go`, switch cases in `internal/tools/projects_test.go`, and
  `projects_e2e_test.go` (`TestSwitchProject_ReanchorsShell` — switch then `shell pwd` lands in the new
  dir and the target's SYSTEM.md becomes the prompt; + wiring gate).
- *Deliberately not in P3:* the agent-type catalog is **not** reloaded on switch (only the
  trust-relevant prompt tier is); `create_project` still doesn't auto-switch, but `switchWorkspace` is
  now the seam to fold that into.

### Prior (2026-07-04): Projects track — P2 (create / promote) — DONE

Write side of the Projects registry ([`projects.md`](projects.md)). Build/vet/`go test -race`
green (full suite); the create write-path is fully unit-tested (the model-driven `create_project`
call itself needs an API key to exercise live).

- **`internal/projects/create.go`** — `Create(root, CreateOptions{Title, Description, FromPaths})`:
  mints `<slug>-<uid>/` (`slugify` = lowercase/hyphen-collapsed/length-bounded, `project` fallback;
  `mintUID` = 5 random bytes → lowercase unpadded base32 = 8 chars, retried on collision), creates the
  projects root on first use, seeds `.agent/project.md` via `yaml.Marshal` (round-trips back through
  `List` even with YAML-special chars), `created`/`last_active` = now. **Promotion** = same call with
  `FromPaths` (`os.Rename` under base name; errors on missing source / in-project collision — work is
  never silently dropped or clobbered).
- **`create_project` built-in** (`internal/tools/projects.go`) — trusted, **not** sandbox-exposed;
  side-effecting ⇒ **human-gated** (`gate.Approve`, `Kind:project.create`) + **audited**
  (`audit.EventProjectCreated`, uid/title/path). Wired alongside `list_projects` on
  `ExecutorConfig.ProjectsRoot`, so it rides the run/chat/serve threading already in place (no cmd change).
- Tests: `internal/projects/create_test.go`, added cases in `internal/tools/projects_test.go`, and
  `projects_e2e_test.go`'s wiring test widened (`TestProjectTools_Wiring`) to cover both built-ins.
- *Not re-anchored:* create does **not** switch the workspace — auto-switch-on-create folds into P3.

### Prior (2026-07-04): Projects track — P1 (marker + registry read) — DONE

First slice of the Projects track ([`projects.md`](projects.md)): named, recallable workspaces.
Build/vet/`go test -race` green (full suite); wiring covered by tests (the model-driven
`list_projects` call itself needs an API key to exercise live).

- **`internal/projects`** — the read side of the registry. `.agent/project.md` marker schema (YAML
  frontmatter `title`/`uid`/`created`/`last_active`/`description` + optional body), `List(root)`
  enumerating `<root>/*/.agent/project.md` (parsed via the stage-E `go.yaml.in/yaml/v3` dep), and
  `Root(workspace)` = `<workspace>/projects`. **Filesystem IS the registry** (no index). `List` is
  resilient: missing root ⇒ empty, no-marker dir = scratch (skipped), malformed marker skipped;
  most-recently-active first; fallbacks (uid ← folder `<slug>-<uid>` suffix, title ← folder name,
  last_active ← dir mtime). `splitFrontmatter` mirrors `cmd/agents.go`.
- **`list_projects` built-in** (`internal/tools/projects.go`) — read-only, trusted, **not**
  sandbox-exposed. Gated by new **`ExecutorConfig.ProjectsRoot`** (empty ⇒ omitted, like the other
  optional-dep tools), threaded from the resolved workspace in `run`/`chat`/`serve` (the home/chat/serve
  surfaces per `projects.md` §6; `eval` left untouched to keep measurements clean).
- Tests: `internal/projects/projects_test.go`, `internal/tools/projects_test.go`,
  `internal/agent/projects_e2e_test.go`.

**NEXT (Projects):** **P4** — CLI flags: `--no-project` (flat-repo mode: workspace *is* the repo, no
`projects/`, tools omitted), `--project <uid|title|path>` (activate/redirect at launch), config
`projects: false` / `projects_root`, threaded through `run`/`chat`/`serve` next to `--workspace`
(projects.md §6). Then **P5** docs (fold into `environment.md`, resolve `workspace.md` §6, surface the
three tools in `usage.md`/`README.md`). Optional small follow-ups surfaced by P3: fold auto-switch into
`create_project` (call the `switchWorkspace` seam), and bump `last_active` on switch so recall recency
reflects use (currently set at create time only). Other open tracks unchanged: Phase 6d (budget dial),
Scheduling S1, deferred fan-out. **Nothing pushed this session** — commits local on `main`.

---

## Latest (2026-07-03): UX & plumbing cluster — DONE (all three items)

The three self-contained items in `plan.md` §"UX & plumbing" all shipped this session
(build/vet/`go test -race` green; items 1–2 live-verified, item 3's serve wire live-smoked).

- **1. Verbosity setting.** `Config.Verbose` + `config set-verbose <on|off>` + `--verbose`/`--quiet`
  + `AI_AGENT_VERBOSE`, via `resolveVerbose(cmd, cfg)` (flag > env > config > default; quiet wins).
  New **`agent.GatedObserver`** wraps `CLIObserver` so `chat` (now **quiet by default**) toggles the
  trace live with `/verbose [on|off]` — no executor rebuild (the obs list is captured once). Disk
  transcript always written. `cmd/config.go`, `cmd/run.go`, `cmd/chat.go`, `internal/agent/observer.go`.
- **2. Transcript location = share-nothing.** `sessionsDir()` → **`runsDir()`**; transcript base now
  defaults to `<config-dir>/runs` (was shared `~/.local/share/ai-agent/sessions`), so separate
  `--config-dir` agents share nothing. `--sessions-dir`/env still override; `<config-dir>/sessions/`
  remains the resumable session store. Rewired `run`/`chat`/`eval`/`serve`; docs in `environment.md`
  + `usage.md`.
- **3. Unified human-in-the-loop.** *(Design chosen with the user: Option A — add `Ask` to the
  interface; full client scope incl. Telegram.)* `tools.Approver` → **`tools.HumanGate`** = `Approve`
  (yes/no) + **`Ask`** (free text); `StdinApprover`→`StdinGate`; `ApprovalQueue` gained `Ask`/`Answer`
  + a `mode` on parked items. The **executor** now has `ask_user` routed through the gate
  (`NewAskUserTool`), so a task can ask mid-run over **any** client. Wire: `POST /approvals/{id}` takes
  `{"approved":bool}` **or** `{"answer":"…"}`; events `question_requested`/`question_answered`;
  `Client.Answer`; `cmd/client.go`+`chat_remote` free-text prompt; **Telegram** relays the question and
  routes the next reply as the answer (per-chat pending state). The `ExecutorConfig.Approver` field is
  now **`Gate`** (`AuthorToolDeps.Approver`→`Gate`, `serve` field `approver`→`gate`).

**NEXT candidates:** **Phase 6d** (token budget dial + context-window awareness) is the main
remaining roadmap item; plus the deferred fan-out / cross-engine `Isolation` items and the standing
housekeeping (commit timestamps, push, markdownlint). **Nothing pushed** — all local on `main`.

## Prior (2026-07-03): stage F — experimentation loop — DONE → the A–F track is complete

- **Stage F — DONE** (this session, commits `371c58e` hot-reload + `6381309` eval). Two parts:
  - **F.1 hot-reload** (`cmd/reload.go`, edits to `cmd/chat.go`/`cmd/serve.go`/`cmd/prompts.go`,
    `Client.Reload`): `chat` grows `/reload` — rebuilds the executor from a `buildExecutor` closure
    (re-reads `loadPrompts`+`loadAgentCatalog`) and carries the conversation forward via
    `Messages`/`Restore`; a malformed file keeps the current executor so a typo doesn't end the
    session. `serve` now holds prompts+catalog behind a reloadable, lock-guarded **`promptState`**
    (in `cmd/prompts.go`): each run **snapshots** the current values (a concurrent reload can't
    mutate a running executor's prompt), and **`POST /reload`** re-reads the files atomically for
    the next run. The route is mounted on a **thin outer mux** wrapping `api.NewServer` (the api
    package stays transport-agnostic; the file-path + tier-gate logic stays in `cmd`).
    `agent reload --addr` is the remote counterpart. Tests `reload_test.go` (reload picks up edits;
    a bad file fails reload and preserves the previous config). Live-verified over `serve`: 204 on
    valid, 400+error on malformed, other routes unaffected.
  - **F.2 eval/compare harness** (`cmd/eval.go`): `agent eval <task>` runs the task under N variants
    from a YAML file (`--variants`) and/or a model sweep (`--models`, one variant per model). A
    variant overrides ambient defaults with any of model/tier/workspace/context_files/
    no_context_files; each builds a **fresh executor** (no planner, shared catalog+memory like
    `run`, its own transcript+audit) and reports output/usage/steps/duration. Per-variant errors are
    captured so the rest still report; Ctrl+C stops after the current variant. Report = a tabwriter
    table (variant, **effective** model via `executor.Model()`, steps, tokens, duration, status) +
    each full output; `formatEvalReport` is pure + unit-tested. Per-variant prompt loading reuses
    `loadPrompts`/`loadAgentCatalog` by installing the variant's knobs into the package flag vars for
    the load then restoring (variants run sequentially — no concurrent reader). Tests `eval_test.go`
    (YAML parse, merge+auto-name, empty errors, report). Live-smoked end-to-end (both variants build,
    run, capture the API error, render the comparison).
- **Stage G — docs consolidation — DONE** (this session, commit `5b089b3`). New
  **`docs/environment.md`** is the single runtime-environment reference (config-dir vs workspace,
  tier + its workspace gate, prompt customization, agent types, full config/env + files tables);
  auto-embedded via `//go:embed docs/*.md` so `read_self_docs` includes it. `usage.md` config/env +
  files sections → pointer, plus new operational sections (prompt/agent-type customization, hot-reload,
  `agent eval`); `README.md` links it + surfaces the new commands; `design.md` §1 "Two anchors" +
  `tools.md` catalog-scoping note. All doc anchors (same-file + cross-file) verified clean.
- **NEXT:** the A–G track is complete. Candidates: the **UX & plumbing** cluster (verbosity default,
  transcript-location share-nothing, unified human-in-the-loop across clients — see `plan.md`); or
  **Phase 6d** (token budget dial + context-window awareness). Also outstanding housekeeping from
  older sessions: commit timestamps, push, markdownlint. **Nothing is pushed** — all A–G commits are
  local on `main`.

## Prior (2026-07-03): stage C — workspace prompt tier — DONE → sub-agent track (D) is next

_(D and E were completed after this note — see the git log and `plan.md`; this entry is retained
for history but is superseded by the Stage F entry above.)_

## (2026-07-03): stage C — workspace prompt tier — DONE (original note)

- **Stage C — workspace prompt tier — DONE** (this session). `loadConfigDirPrompts()` →
  `loadPrompts(workspace, tier)` in `cmd/prompts.go`: loads the config-dir (global) tier, then the
  workspace (project) tier, merged **project > global** — a workspace `SYSTEM.md` wins outright,
  `AGENTS.md` bodies concatenate global→project. Rewired into `run`/`chat`/`serve` (each already had
  `workDir` + `tier` in scope). **Tier gate** (`loadWorkspaceTier`): `safe` doesn't auto-load
  workspace files unless `--workspace` is explicit; a workspace resolving to the config dir isn't
  loaded twice (`sameDir`). New persistent **`--context-file`** flag (repeatable): extra prompt
  file(s) appended last, always honored regardless of tier; a *missing named* file errors (tier files
  absent = no-op). **Single resolved workspace dir — no parent walk yet** (stop bound still open,
  `workspace.md` §6). Tests rewritten around `loadPrompts` (precedence, `SYSTEM.md` project-wins,
  safe-tier gate + explicit override, workspace==config-dir dedup, context-file append + missing
  error, `--no-context-files` gate); build/vet/test green. The prompts+workspace track (A–C) is
  **complete**.
- **NEXT: Stage D — sub-agent types** (`subagents.md` §2–§3): `AgentType` + catalog + built-in
  `researcher`/`scout`, `newSubAgent` factory, `PromptMode` (`replace`|`append`) reusing the
  `composeSystemPrompt` seam from A. Then E (foreground `spawn_agent`) → F (experimentation loop).

## Prior (2026-07-03): experimentation-track stages A + B done → C is next

- **Stage A — prompt composition core — DONE** (commit `5153696`). `composeSystemPrompt` seam +
  config-dir `SYSTEM.md`/`AGENTS.md` (alias `CLAUDE.md`) loading + `--no-context-files`. See the
  sequenced backlog in `plan.md` and `prompts.md` §5.
- **Stage B — workspace concept — DONE** (this session). `cmd/workspace.go` `resolveWorkspace()`:
  persistent `--workspace` flag (validated existing dir, absolutized) > process cwd; wired into
  `run`/`chat`/`serve`, replacing the raw `os.Getwd()` → threads into the shell tool's `workDir`.
  **Deliberately no parent walk** (the upward walk collects project *files* = stage C, and its stop
  bound is an open question, `workspace.md` §6). **`--context-file` deferred to C** (it gates
  prompt-file loading, which C builds — adding it now would be a dead flag). Tests
  `cmd/workspace_test.go`; build/vet/test green; flag + validation live-verified (missing dir / a
  file rejected before any API call).
- **NEXT: Stage C — workspace prompt tier** (deps A + B, both done). Extend prompt loading to the
  resolved workspace: project-over-global precedence (`AGENTS.md` concatenated global→project,
  `SYSTEM.md` project wins), plus the **tier gate** (`safe` doesn't auto-load workspace files;
  explicit `--context-file`/`--workspace` always honored → land the `--context-file` flag here).
  `prompts.md` §2 + `workspace.md` §5. Then the sub-agent track D→E→F.

---

## Latest session (2026-07-02): `agent chat --addr` (remote sessions) + engine aliases

- **`agent chat --addr` — DONE.** The chat REPL grew a remote mode. Without `--addr` it's the
  in-process executor as before; **with `--addr` it drives a running `agent serve` engine's persistent
  session** as a peer client — the conversation lives server-side, so it survives quitting and is
  resumable here or from another client (Telegram). Flags: `--addr` (host:port or alias), `--list`
  (show resumable sessions, exit), `--session <id>` (resume). Commands in remote mode: `/reset` = new
  session (closes old), `/end` = close, `/exit`/Ctrl-D = detach (prints the resume command). Ctrl-C
  cancels the current turn and stops the remote run. `cmd/chat_remote.go` (`runRemoteChat`,
  `attachSession`, `listRemoteSessions`, `runRemoteTurn`); reuses `printEvent` + a refactored
  `watchApprovalsScan` (shares the REPL's stdin scanner so the prompt and approval answers don't race
  two bufio readers).
- **Engine aliases — DONE.** `--addr` accepts a `host:port` **or** an alias. `agent config set-engine
  <alias> <host:port>` / `rm-engine` / `engines` manage `Config.Engines`; `resolveAddr(addr)` resolves
  a known alias, else passes the value through. Wired into **every** engine-facing command
  (`chat`/`client`/`stop`/`audit`/`tool revoke`).
- **Tests:** `TestResolveAddr` (alias vs literal passthrough), `TestAttachSession` (new / resume /
  unknown-id error, against a real httptest engine), `TestListRemoteSessions_Empty`. Build/vet/test
  green, race-clean. **Live-verified** end-to-end against a real `serve`: `config set-engine` →
  `chat --addr <alias>` (header shows the resolved address) → new session → `--list` → resume (header
  shows "resumed") → `/end` → list empty.
- Docs updated: `README.md` (ways to run + alias note), `docs/usage.md` (chat remote mode, new
  "Engine aliases" section, sessions section points at `chat --addr`, config reference + files-on-disk),
  `docs/plan.md` (Phase 4f).
- **Still deferred:** context-window trimming for long sessions. Housekeeping from earlier sessions
  (commit timestamps, markdownlint) still outstanding.

## Planned next: Phase 6 — self-awareness (staged in `plan.md`)

Discussed 2026-07-02, written up as **Phase 6** in `plan.md`. Motivation: `provider.Usage` is
captured per step but never aggregated/surfaced/fed back, and the agent can't read its own docs or
report its own status.

- **6a — DONE.** Token accounting. `agent.UsageObserver` sums input/output/cached tokens + steps
  from `EvResponse`; the API `Engine` fans one in `launch` (covers runs *and* session turns) and
  stores it on `RunInfo` (`Usage`+`Steps`) + emits a `run_usage` audit event (`SetAuditRecorder`,
  wired in `serve`). CLI/chat print a stderr line via `cmd/usage.go` `formatUsage`; `agent client` /
  `chat --addr` print it from `RunStatus`. **Tokens only, cost deferred.** Tests + live-verified
  (`GET /runs/{id}` usage, `GET /audit?type=run_usage`). No `turn_usage` event — a turn is a run.
- **6b — DONE.** Self-documentation. Embedded **reference docs + the vision doc** (not planning
  docs) via `//go:embed README.md docs/*.md self-extending-agent-design.md` in `main.go`; the
  flat glob excludes `docs/planning/`, where **`plan.md` + `resume.md` were moved** (4 links fixed).
  `internal/selfdocs` (`Docs`: List/Get/Search, `Kind` reference/vision, `vision` alias) +
  `internal/tools/selfdocs.go` `read_self_docs` (trusted, not sandbox-exposed, omitted when nil);
  `cmd.SetSelfDocs` threads it from main → every executor; `selfDocsPromptNote` appended to the
  prompt (reference = current truth, vision = not-yet). Tests + binary-grep verified the embed set.
  *Corpus decision:* include vision "to align agent+tools philosophy"; exclude planning so the
  agent doesn't mistake roadmap for current behavior.
- **6c — IN PROGRESS.** `status` tool **DONE**: identity (model/tier/run/build) + counts (#tools,
  #memory) + **host resources** (CPU+load, RAM, disk, RSS, Go heap/goroutines, uptime).
  `internal/hoststat` (best-effort `/proc` + `runtime` + `syscall.Statfs`), `internal/buildinfo`
  (`Version`, ldflags-overridable), `internal/tools/status.go`; wired unconditionally in
  `NewExecutor` (no signature change). Read-only, not sandbox-exposed. Tests + live-verified on
  this box. **`usage` tool DONE**: reports **this session** + **today**, **derived from the audit
  log** (not live accumulators) — every run/turn emits `run_usage`, so aggregates are sums over
  persisted events (restart-safe, cross-session). Enabling change: `run_usage` now tagged with the
  session id (threaded `sessionID` through `Engine.launch` + `TurnRunner.RunTurn`). `agent run`
  also writes `run_usage` centrally so **today** includes CLI runs. `internal/usage` (`Ledger`,
  `Record`), `internal/tools/usage.go`. Human surface: **`agent usage`** / `--session <id>` + a
  `today:` line after `agent run`. Tests + live-verified.
- **6c COMPLETE.** Added the last two introspection tools (`internal/tools/introspect.go`):
  **`recent_activity`** (reviews its own audit-log activity — capabilities, authoring, memory,
  usage — filterable by type/run; over an `audit.Reader` — `NewExecutor` gained an `auditReader`
  param, serve passes the process-wide log, run/chat their per-run recorder) and **`tool_catalog`**
  (lists authored tools + caps/scope so it reuses rather than re-authors; built from the registry).
  Both read-only, not sandbox-exposed. Tests + wiring e2e.
- **Heads-up for 6d:** `NewExecutor` is now 14 positional args — **refactor to an `ExecutorConfig`
  struct** before adding the budget dep, to stop the per-addition caller/test churn.
- **Next: 6d** — token budget dial (soft warn ~80% + optional hard stop) + context-window awareness
  (subsumes the deferred context-window trimming).

## Fix (2026-07-02): agent now knows the current date

Surfaced by a live run: asked the year, the agent guessed wrong (anchored to its training cutoff),
then fumbled — `run_code` can't get the clock (the Lua sandbox strips `os`), so it finally shelled
`date`. Fix: `Agent.systemMessage()` appends `Today's date is <YYYY-MM-DD (Weekday)>` to the system
prompt, rebuilt each request (**day granularity** to keep prompt-cache prefix stable). Also tightened
the `run_code` prompt line to note it's pure compute (no clock/fs/network — use shell). Applies to
executor + planner (shared loop). Test: `systemdate_test.go`.
- **6d (later):** token budget dial + context-window awareness (subsumes the deferred context-trim).

---

## Latest session (2026-07-01): interactive chat + persistent sessions (conversations)

- **Local REPL — DONE.** Executor conversation now persists across `Run` calls (`a.messages` holds the
  conversation *excluding* the system prompt, which is prepended fresh each request so prompt changes
  apply on resume; `Restore`/`Messages`/`Reset` added). `agent chat` (`cmd/chat.go`) is a multi-turn
  REPL: `/reset`, `/exit`/Ctrl-D, Ctrl-C cancels the current turn; `--plan` toggles per-turn planner
  (default off). Test `TestExecutor_RetainsConversationAcrossTurns`.
- **Persistent sessions over the API — DONE (disk-backed).** `internal/session`: `Store` + `FileStore`
  (one JSON file per session under `<config-dir>/sessions/`, atomic write, SQLite later). Design:
  **a turn is a run whose executor is seeded with the session's history** — reuses the whole
  run/hub/SSE/approval/audit machinery. Engine gained `EnableSessions`, `StartSession`, `ListSessions`,
  `CloseSession`, `PostTurn` (per-session mutex serializes turns; `launch` helper shared with
  `StartRun`); `TurnRunner` seam. Endpoints `POST /sessions`, `GET /sessions`, `DELETE /sessions/{id}`,
  `POST /sessions/{id}/turns` (→ run id, stream via `/runs/{id}/events`). `Client.StartSession/PostTurn/
  CloseSession/ListSessions`. serve builds `session.FileStore` + a `turnRunner` (shares `serveDeps.
  buildExecutor` with the run runner). Tests: `session_test.go`, `sessions_test.go` (retain+persist,
  unknown/closed → 404, disabled). Live-verified over HTTP (create → turn → delete → 404).
- **Telegram now session-based.** Bot maps chat→session; `/new` starts a fresh session, `/end`
  terminates; a normal message is a turn (`PostTurn`) with retained context. `Client` interface swapped
  `StartRun` → `StartSession`/`PostTurn`/`CloseSession`. Tests updated + `TestBot_NewAndEndCommands`.
  Transport still stubbed (`NewHTTPTransport` unbuilt) — bot logic fully tested with the fake.
- **Also this session:** `--config-dir`/`AI_AGENT_CONFIG_DIR` (isolate config/tools/memory/audit for
  independent agents) and `--sessions-dir`/`AI_AGENT_SESSIONS_DIR` (isolate per-run transcripts);
  refreshed `README.md` + new `docs/usage.md` operator guide. All build/vet/test green, race-clean.
- **Live Telegram transport — DONE (later same session).** `telegram.NewHTTPTransport` implemented with
  the `github.com/go-telegram-bot-api/telegram-bot-api/v5` SDK (long-poll `getUpdates`; `sendMessage`
  with inline keyboard; `answerCallbackQuery`), adapted behind the existing `Transport` interface. serve
  starts the bot in a **goroutine** so the Bot API handshake never delays listening; a bad/unreachable
  token logs `telegram: connect: … — running without the bot` and the engine runs normally (verified —
  a fake token got a real `Unauthorized` from api.telegram.org, so egress works). The bot is now fully
  live given a real token + allowlist.
  **Next open ideas:** `agent chat --addr` to drive a remote session (same conversation from SSH →
  Telegram); context-window trimming for long sessions.

---

## Session (2026-07-01): Phase 4e-6 — Telegram frontend as a peer client (transport stubbed)

- **4e-6 DONE (transport stubbed).** `internal/frontend/telegram`: `Bot` drives a `Client` (the slice
  of `api.Client` it needs) — a peer, no special access. A message starts a run and streams events back
  to the chat; the 4e-5 `approval_requested` event becomes an **Approve/Deny inline keyboard** whose
  callback calls `Client.Resolve`. No chat↔run map needed (callback data carries the approval id; each
  run's stream goroutine captures its chat).
- **Transport behind an interface** (`Transport`: Updates/Send/Answer; `Update`/`Message`/`Callback`/
  `Button`) so the whole frontend is testable with no live bot. `NewHTTPTransport` is the ONE
  unimplemented seam (returns `ErrTransportNotBuilt`) — the live Bot API long-poll/send is the deferred
  "add a bot later" step (needs a token + outbound network / egress). Tests `telegram_test.go` cover the
  full approval loop + auth rejection (message + callback) + callback parsing, race-clean.
- **Optional + token-activated:** `serve` starts the bot only when a token is set — config
  `telegram_token` / env `AI_AGENT_TELEGRAM_TOKEN`; allowlist config `telegram_allowed_users` / env
  `AI_AGENT_TELEGRAM_ALLOWED_USERS` (comma-sep), env wins (`resolveTelegramToken`/`resolveTelegramAllowed`
  in `cmd/config.go`). **No token ⇒ engine runs unchanged**; a token while the transport is unbuilt
  degrades gracefully (logs "live transport not built yet — running without the bot"). Allowlist is
  **fail-closed** (empty ⇒ rejects everyone; serve warns). Smoke-tested both paths.
- **Phase 4e is complete** bar the live transport. Frontend fork **resolved: Telegram.** **Next:**
  implement `telegram.NewHTTPTransport` when a bot token is available (Bot API `getUpdates` long-poll →
  `Update`s; `sendMessage`/`answerCallbackQuery` for Send/Answer) — the day it returns a working
  `Transport`, supplying a token activates the bot with no other change. Then housekeeping: commit
  timestamps, push, markdownlint (`plan.md` has pre-existing `+`-wrapped-line false positives).

---

## Session (2026-07-01): Phase 4e-5 — approval events on the stream (poll → push)

- **4e-5 DONE.** `ApprovalQueue.SetEmitter(func(runID, ev))` (wired in `serve` to `Engine.PublishToRun`).
  `Approve` emits `approval_requested` on parking; the **run's own goroutine** emits `approval_resolved`
  when it receives the decision — ordered ahead of the terminal `done` so the hub-close can't drop it
  (emitting from `Resolve` would race). `Engine.PublishToRun(runID, ev)` broadcasts into a run's hub +
  replay history (no-op on unknown/closed). New event kinds `KindApprovalRequested`/`Resolved` +
  `Event.ApprovalID`/`Approved`; a requested event reuses `Tool`=category, `Text`=title, `Input`=detail.
- **Unified the run id (important):** `Runner.Run(ctx, runID, task, obs)` now threads the engine's id;
  `serve` passes it to `logger.NewWithID` + `NewExecutor` so session dir / event stream / audit `Run` /
  approval `RunID` all share one id. Previously the executor minted its own via `logger.New`, so the
  push would have mis-routed. This also means `GET /audit?run=<engine id>` now matches a run's events.
  CLI `printEvent` renders the two new kinds. Test: `TestApprovalEmitter_PushesOntoRunStream` (real
  Engine + emitter, race-clean). Build/vet/test green. **Next: 4e-6** (Telegram frontend as a peer
  client via `api.Client`; the pushed `approval_requested` becomes an inline Approve/Deny keyboard →
  `Client.Resolve`; auth = Telegram user-id allowlist in the frontend, engine stays localhost).

---

## Session (2026-07-01): Phase 4e-4 — central audit log + browse over the API

- **4e-4 DONE.** `audit.Reader` (`Tail(n, Filter{Run,Type})`, oldest-first, n≤0 ⇒ all) on both
  `MemoryRecorder` and `JSONLRecorder` (the latter re-reads its file; `path` now tracked). `audit.Filter`
  + shared `tailMatching`; `audit.Recorders` fans one event to several sinks. `serve` shares the
  **process-wide** `~/.config/ai-agent/audit.jsonl` across runs — each run fans out
  (`audit.Recorders{sessionRec, central}`) to both the session transcript and the central log
  (`newServeRunner` gained a `central` param). `GET /audit?run=&type=&limit=` (`internal/api/audit.go`,
  empty ⇒ `[]`), `Client.Audit`, `agent audit --addr` (`cmd/audit.go`). `NewServer` gained a nil-able
  `audit.Reader` param (now `NewServer(e, approvals, catalog, rec, reader)`). Live-verified end-to-end
  (revoke → central log → `/audit` + `agent audit`). Tests: `internal/audit/audit_test.go`,
  `internal/api/audit_test.go`. Build/vet/test green. **Next: 4e-5** (approval events on the stream,
  poll → push) — give `ApprovalQueue.Approve` an observer hook so parking/resolving emits
  `approval_requested`/`approval_resolved` into the run's hub. *SQLite tipping point:*
  `JSONLRecorder.Tail` re-reads the whole file per call — the natural pressure point for the swap.

---

## Session (2026-07-01): Phase 4e-3 — tool review / revoke over the API

- **4e-3 DONE.** `DELETE /tools/{name}` → `Registry.Revoke` (404 if absent) emitting a new
  `audit.EventToolRevoked` (`tool_revoked`: name, code_hash, scope, version). `GET /tools/{name}`
  returns a `ToolDetailView` (listing fields + impl **source**/lang + smoke **test**, which the
  listing omits). `NewServer` gained a nil-able `audit.Recorder` param for management-plane effects;
  `serve` opens **one process-wide** `~/.config/ai-agent/audit.jsonl` for it (the first process-wide
  recorder — 4e-4 generalizes it to all runs + adds a browse endpoint). `Client.ToolDetail` /
  `Client.RevokeTool`; `agent tool revoke --addr` routes to a running engine (default still edits the
  local catalog directly). Tests: `TestHTTP_RevokeTool`, `TestHTTP_ToolDetail`, `TestClient_RevokeTool`.
  Note: because `serve` shares one registry between executor and endpoints and tool defs recompute per
  iteration, an API revoke drops the tool from an in-flight run at its next iteration, not just the
  next run. Build/vet/test all green. Docs: `plan.md` 4e-3 checked off, `tools.md` revoke section +
  deferred-table updated. **Next: 4e-4** (central audit log + browse over the API) — the process-wide
  recorder introduced here is the seam to build on.

---

## Latest session (2026-06-29): Phase 4d + model config

- **Phase 4d (long-term memory) — DONE.** `internal/memory` (`Store` + `MemoryStore`, JSON-file
  persistence mirroring the tool `Registry`); `remember`/`recall` built-ins
  (`internal/tools/memory.go`), trusted + **not** sandbox-exposed; `NewExecutor` gained a
  `memory.Store` param (nil ⇒ tools omitted); `serve` shares one store across runs. `remember`
  emits a `memory_write` audit event. Tests: `internal/memory/memory_test.go` +
  `internal/agent/memory_e2e_test.go`. Design + trade-offs in [`memory.md`](memory.md).
- **Model + tier config — DONE.** `Config.Model`/`Config.Tier` + `agent config set-model` /
  `set-tier`; precedence `--model`/`--tier` flag > config > built-in default
  (`resolveModel`/`resolveTier` in `cmd/config.go`; tier default `balanced`, validated by
  `capability.ParseTier`, tested). Fixed a latent bug: the setters now merge into the existing
  config instead of overwriting (old `set-key` would clobber). `saveConfig` message made generic.
- Build/vet/test all green.
- **Phase 4e planned + staged in `plan.md`** (4e-1, 4e-3…4e-6). **Multi-user model DROPPED — engine is
  single-user (one shared trust domain, design §1).** An earlier draft added a per-run `Owner` label,
  session isolation, and per-user data ownership (private memory/tools, opt-in sharing); it was removed
  to simplify — a family sharing one trusted box shares its memory + tool catalog, and a user with
  shell access could reach another's data anyway. Concurrent runs stay compute-independent
  (goroutine-per-run + fresh executor-per-run + concurrent-safe shared stores) but share data freely.
  design §1/§5, plan.md, tools.md, memory.md, api-transport.md all updated to record single-user.
- **4e-1 DONE.** Run lifecycle (no identity layer). `Engine.StartRun(task)` + run metadata (`RunInfo`)
  + `StopRun`/`ListRuns`/`RunStatus` via `lookup(id)` (`ErrUnknownRun` if absent). New endpoints
  `GET /runs`, `GET /runs/{id}`, `POST /runs/{id}/cancel`. `Client` gained `StopRun`/`RunStatus`/
  `ListRuns`; CLI `agent stop`, `signal.NotifyContext` Ctrl+C cancels (in-process for `run`, remote
  for `client`). Tests in `internal/api/runs_test.go` (`TestCancelStopsRun`, `TestUnknownRunStatusIs404`).
  *(The old owner-scoping + the old 4e-2 data-ownership phase were removed.)* **Next: 4e-3** (tool
  revoke over the API).

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

1. ~~**Decide** the shell-hardening scope (a/b/c above).~~ **DONE — chose (a).**
2. ~~Implement untrusted-content framing for `web_fetch`/`web_search` + system-prompt rule.~~
   **DONE:** `internal/tools/untrusted.go` (`wrapUntrusted` + delimiters), applied in
   `webfetch.go`/`websearch_ddg.go`, executor + planner prompt rules in `agent.go`. Build/test
   green. (b)'s async/tiered approval refactor deferred to fold into Phase 3 when it forces the
   approval refactor.
3. **All security mechanisms now documented** in [`security.md`](security.md) (threat→control map).
4. ~~start **Phase 3a** (`ToolSpec` + `Registry`).~~ **DONE:** `internal/tools/spec.go`,
   `registry.go`, `registry_test.go`; all green, not wired to the loop. Tool-system design +
   benefits/drawbacks in [`tools.md`](tools.md). `plan.md` 3a checked off.
5. ~~**Phase 3b** — wire broker/sandbox/registry into the run flow.~~ **DONE:** `cmd/run.go`
   builds per-run `audit.jsonl` + persistent catalog; `NewExecutor` wires `Broker → LuaGlue →
   Registry`, shares the glue with `run_code`, resolves via `Agent.dispatch`, sets Trusted/Exposed.
   Tests in `internal/agent/executor_dispatch_test.go`. `run_code` signature now takes the shared
   glue. All green.
6. ~~**Phase 3c** — the `author_tool` meta-tool + cross-phase test.~~ **DONE:**
   `internal/tools/authortool.go` (validate→approve→smoke-test→register→audit), tier policy
   `capability.Tier.AutoApproves`, shared script contract `tools.WrapScript`/`WrapTest`,
   `sandbox.Parse`. **Fixed:** `buildToolDefs` recomputed per iteration (was hoisted) so authored
   tools are callable same-run; logging made nil-safe. Tests: `authortool_test.go` (gates) +
   `authoring_e2e_test.go` (fake-provider end-to-end). All green.
7. ~~**Phase 3d/3e**.~~ **DONE.** 3d: `Agent.selectRegistryTools` offers all registry tools when
   catalog ≤12, else top-k by `Search(task)` ∪ ephemeral. 3e: `cmd/tool.go` (`agent tool
   list`/`revoke`) + code-hash dedup in `Registry.Register` (author_tool points the model at the
   existing tool). Tests: `executor_search_test.go`, `TestRegister_DedupsByCodeHash`,
   `TestAuthorTool_DedupsIdenticalCode`. **Phase 3 is complete.**
8. **Phase 4 started — staged 4a–4e in `plan.md`.** **4a DONE:** `Approver` seam
   (`internal/tools/approval.go`) replaces `ConfirmFunc` — `Approve(ctx, ApprovalRequest)
   (bool, error)`, `StdinApprover` (CLI) / `ApproverFunc` (tests); shell + author_tool + NewExecutor
   refactored; approval error blocks the action. This is the (b) item from the old shell-hardening
   decision. All green.
9. **4b DONE:** headless engine event sink (`internal/agent/observer.go`). `Observer.Emit(Event)`
   replaces the loop's stderr prints + concrete logger; `LoggerObserver` + `CLIObserver`, fanned via
   `Observers`; `NewExecutor`/`NewPlanner` take `Observer` + `runID`; `cmd/run.go` composes them.
   Loop is grep-clean of stdout/stderr. `TestRun_EmitsEventSequence`. All green.
10. **Phase 4c IN PROGRESS — `internal/api` transport.** Fork **settled: HTTP+SSE** (rationale +
    JSON-RPC-addable design in [`api-transport.md`](api-transport.md)). **Vertical slice DONE:**
    transport-neutral core (`engine.go` `Engine.StartRun`/`Subscribe` over a `Runner`; `hub.go`
    per-run `Hub` = `agent.Observer` with history replay; `event.go` wire `Event`) + SSE adapter
    (`http.go`: `POST /runs`, `GET /runs/{id}/events`) + `cmd/serve.go` (`agent serve`). Tests in
    `internal/api/http_test.go`. **Approval queue DONE:** `NewExecutor` takes an injectable
    `tools.Approver` (nil ⇒ `StdinApprover`); `internal/api/approval.go` `ApprovalQueue` implements it
    (park/block in `Approve`, `Resolve` single-shot, `Pending` snapshot); SSE adds `GET /approvals` +
    `POST /approvals/{id}`; `serve` shares one queue between executor and endpoints; `engine.StartRun`
    now owns the run's context (was bound to the request ctx → aborted runs mid-approval). Tests in
    `internal/api/approval_test.go`. **Tools-over-API DONE:** `internal/api/tools.go` `GET /tools` +
    `GET /tools/search?q=&k=` over `tools.Registry` (wire `ToolView`, no source); `serve` shares ONE
    persistent registry between executor and endpoints (authored tools visible across runs + API);
    `NewServer(e, approvals, catalog)` — both optionals nil-able. Tests `internal/api/tools_test.go`.
    **CLI-as-client DONE:** `internal/api/client.go` `Client` (`StartRun`/`StreamEvents`/`Pending`/
    `Resolve`); `cmd/client.go` (`agent client <task> --addr`) starts+streams a run on a running
    `serve` engine and polls `/approvals` to prompt the operator. Tests `internal/api/client_test.go`.
    **Phase 4c is COMPLETE.** All green. **NEXT: Phase 4d** (long-term memory store as a built-in
    tool, persisted + audited) then **4e** (management plane + a thin frontend; fork: Telegram vs web,
    leaning Telegram). **Recorded for 4e:** per-run kill switch (`Engine.StopRun` + `POST
    /runs/{id}/cancel` + `agent stop`/Ctrl+C) — deferred from 4c as not urgent (runs bounded by
    `maxIterations`, risky actions park for approval), matters once `serve` runs unattended.
11. **Forks to settle when reached** (in plan.md): transport (4c, leaning HTTP+SSE); first frontend
    Telegram vs web (4e, leaning Telegram); SQLite vs JSONL store.
12. Housekeeping: **change commit timestamps** (user asked — do this across the Phase 1.5–4 commits
    later); push; optional markdownlint fix (`design.md` fenced block needs a language tag).
