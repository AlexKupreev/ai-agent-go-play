# Projects — concept & roadmap

A **project** is a named, persistent **sub-scope within a workspace** the agent can *recall by intent*
and *switch into* mid-conversation — the thing that lets a local chat answer *"let's discuss the
articles I shared last time"* or *"now let's write a script for health analysis"* without the user ever
naming a path. It is the innermost of the **three-scope model** (config-dir → workspace → project;
canonical in [`../environment.md`](../environment.md)): the [`workspace.md`](workspace.md) is the
sandbox the agent lives in, and a project is a named corner of it that **inherits the workspace's
common guidelines and adds its own**, keeping its own artifacts and sessions. Companion to
[`workspace.md`](workspace.md) (the container a project lives in), [`prompts.md`](prompts.md) (a
project carries the innermost prompt tier), and [`subagents.md`](subagents.md) (a scout / worktree
operates on the active project). **Status: built — P1–P5 complete** (registry read, create/promote,
mid-session switch, CLI flags, docs; see §9) — the **guideline inheritance** from the enclosing
workspace is specified but not yet wired ([`workspace.md`](workspace.md) §3). This doc resolves
the mid-session-switch and "who vouches for a workspace" open questions left in
[`workspace.md`](workspace.md) §6.

---

## 0. The model in one paragraph

The agent runs from a **home workspace** and lands in its **scratch** area — throwaway work, no
project. When work is worth keeping, the agent **promotes** it to a project: a folder
`<home>/projects/<slug>-<uid>/` with a small `.agent/project.md` marker (title + description). The
**filesystem *is* the registry** — `list_projects` is a directory listing, no separate index to keep
in sync. Later the agent **recalls** a project by matching the user's intent against those titles /
descriptions, and **switches** into it with a tier-gated `switch_project` (which re-anchors the active
working directory to the project — within the workspace — and layers its prompt tier on top). For the
CLI-launched-in-a-repo case, projects are
opt-out (`--no-project`) or redirectable (`--project`). The loop is: **scratch → promote → recall →
switch.**

---

## 1. The registry is the filesystem

Projects live in a `projects/` folder **inside the home workspace** (the user's choice — projects
nest under wherever the workspace is, so this stays coherent with the cwd model instead of adding a
second rooted store):

```
<home-workspace>/
  projects/
    articles-a3f9c1/
      .agent/project.md        # title, uid, description, timestamps
      … project artifacts …
    health-analysis-7b2e04/
      .agent/project.md
      …
  … scratch work lives at the workspace root, un-promoted …
```

Consequences of "the filesystem is the registry":

- `list_projects` = enumerate `<home>/projects/*/` and read each `.agent/project.md`. No
  `projects.json` to fall out of sync, and **no stale-path problem** — a project can't move out from
  under the index because there is no index, only the tree.
- **Trust by containment.** `projects/` sits under a workspace the operator already launched /
  authorized, so switching into one inherits that same trust origin. This is the answer to
  `workspace.md` §6's *"who vouches for a workspace that arrives from a registry?"* — **the parent
  workspace vouches, by containment.** (The §5 tier-gate still fires on the switch; see §5.)

---

## 2. Identity vs. title — `<slug>-<uid>`

The folder name is `<slug>-<uid>` (e.g. `articles-a3f9c1`), where the **UID is the stable identity**
and the slug is a human-readable convenience derived from the title at creation. The `<uid>` is a
short, filesystem-safe, collision-resistant token (6–8 chars, e.g. base32 of random bytes) —
distinguishing two projects that would otherwise share a title.

Because the dir name carries a UID, the **human title lives in metadata, not in the folder name**:

- `.agent/project.md` — YAML frontmatter (`title`, `uid`, `created`, `last_active`, `description`) +
  an optional freeform body. Reuses the frontmatter parser already a direct dep from stage E
  (`go.yaml.in/yaml/v3`, `cmd/agents.go`).
- **The title is mutable, the UID is not.** Retitle *"articles"* → *"shared reading list"* without
  moving the folder or breaking references.
- **Cross-session references are UID-keyed.** Memory and audit can write `[[project:a3f9c1]]` and
  survive a retitle — which is what makes *"the articles I shared last time"* resolvable at all
  across sessions. The stable handle is the load-bearing part.

`last_active` (or the folder mtime) orders recency for disambiguation.

---

## 3. The lifecycle — scratch → promote → recall → switch

1. **Scratch.** With no active project the agent works at the home-workspace root. A trivial one-off
   (a quick script) can just live here and never become a project. This is the "default folder" from
   which the previous design discussion started.
2. **Promote.** When work is worth keeping, `create_project(title)` mints `<slug>-<uid>/`, seeds
   `.agent/project.md`, moves the relevant scratch artifacts in, and switches into it. This is the
   **explicit, auditable "it became a project" moment** — never a silent auto-switch (see
   `workspace.md` §5–§6 for why implicit mid-session context adoption is a trust hazard).
3. **Recall.** In a later session the user refers to a project by *intent* (*"the articles from last
   time"*). The agent calls `list_projects`, matches intent against `title`/`description`, and — if
   ambiguous or absent — **asks rather than guesses** (a wrong switch loads the wrong context *and*
   the wrong trust surface).
4. **Switch.** `switch_project(uid|title)` resolves to the path and re-anchors the workspace
   (tier-gated, §5), reloading the project prompt tier.

---

## 4. Tool surface

Three verbs, all built-ins on the trusted host side (like `spawn_agent` / `schedule_task`), never
exposed to a sandboxed `call_tool`:

- **`list_projects()`** → `[{uid, title, description, last_active}]`, by enumerating
  `<home>/projects/*/.agent/project.md`. This is *how the agent knows what exists* — the answer to
  "how could it know about existing projects available for switching?"
- **`create_project(title, description?)`** → mkdir `<slug>-<uid>/`, seed `.agent/project.md`, then
  switch to it. **Promotion** is this + moving scratch artifacts (a `shell` `mv`, or an optional
  `from_paths` arg); it isn't a fourth tool.
- **`switch_project(uid | title)`** → resolve (UID exact; title fuzzy → disambiguate on ambiguity)
  and call the workspace-switch mechanism (§7). This is the project-level face of the mid-session
  `set_workspace` primitive from the design discussion — one switch mechanism, named for the user's
  concept.

Optional later: `rename_project(uid, title)` / `describe_project(uid, description)` (metadata-only,
no move — the point of the UID).

---

## 5. Trust — unchanged, and cleaner here

Switching is still a **workspace-file re-load**, so it re-runs the `workspace.md` §5 tier gate on the
target: at `safe`, a project's `AGENTS.md`/`SYSTEM.md` does **not** auto-load; at `balanced` /
`permissive` it does. A project the agent just scaffolded (from a template / `create-*` / a clone)
holds effectively third-party prompt files, so the gate is exactly the guard you want on
`switch_project`.

What's *cleaner* than the general "registry from the wire" case: because projects live under an
already-authorized home workspace (§1), you're never adopting a path "nobody vouched for." Containment
supplies the vouch; the tier gate supplies the injection guard. Both, not either.

---

## 6. CLI-in-a-repo — opt-out or redirect

The `projects/` nesting is the **home / chat / serve** ergonomic. For CLI `run`/`chat` launched
*inside an arbitrary repo* you usually don't want the agent minting a `projects/` subfolder in someone
else's tree. Three modes, alongside the existing `--workspace` / `--context-file` (`workspace.md` §7):

- **default (home / chat / serve):** `projects/` active under the home workspace; the §3 loop applies.
- **`--no-project`** (or config `projects: false`): flat repo mode — the workspace *is* the repo, no
  `projects/` created, no registry. The pi-faithful *"I cd'd in, just act on this"* path.
- **`--project <uid|title|path>`** (redirect / select): activate a specific project at launch, or
  point the projects root elsewhere.

---

## 7. Mid-session switch mechanics (resolves `workspace.md` §6)

A switch is not cosmetic: `internal/tools/shell.go` sets `cmd.Dir = workDir` per command and each
command is a fresh process, so `cd` never persists across tool calls — re-anchoring the workspace is
the *only* way the shell `workDir` actually moves. `switch_project` therefore:

1. resolves the target path (under `<home>/projects/`, or an explicit `--project` path),
2. re-runs `resolveWorkspace`-equivalent + `loadPrompts(workspace, tier)` (the §5 gate), rebuilding
   the executor's prompt context the way `/reload` already does (stage F, `cmd/reload.go`),
3. updates `ExecutorConfig.WorkDir` so subsequent shell commands run in the new project,
4. **emits an audit event** — the switch is a logged, authorizable act, not an inferred one.

For `serve`, this is the concrete shape of the §2/§6 *per-run/per-session `workspace` field*: the
session's active project is switchable state, and the vouch is the user's in-conversation intent
(they named it) plus containment under the home workspace.

---

## 8. Open questions

- **`.agent/` marker vs. reusing the project's own `AGENTS.md`.** A dedicated `.agent/project.md`
  keeps registry metadata separate from prompt content (title ≠ system-prompt text); reusing
  `AGENTS.md`'s first line is cheaper but conflates the two. Leaning dedicated marker.
- **UID scheme.** Random base32 (no ordering, trivially unique) vs. time-sortable (ULID-ish, leaks
  creation order). Leaning random — recency already comes from `last_active`.
- **Nesting — projects are leaves.** Only a **workspace** hosts projects; a project does not host
  sub-projects. A project is a *sub-scope within* a workspace — it inherits the workspace's guidelines
  and adds its own; it doesn't replace the workspace or nest under another project. The three-scope
  model is deliberately three flat levels (config-dir → workspace → project), not a recursive tree, so
  recall / switch / `list_projects` stay a single enumeration of `<workspace>/projects/*/` and
  guideline inheritance is a fixed depth. (If a genuine grouping need appears, it's a separate future
  decision, not a default.)
- **Scratch persistence across sessions.** Is the home-workspace scratch area durable (survives
  between chats) or cleared? Durable is friendlier for *"last time"* recall of un-promoted work, but
  blurs the promote boundary.
- **`list_projects` scope in a big tree.** Enumerate only the home `projects/` (v1) vs. also
  discovering nested / sibling project markers. v1: home only.

---

## 9. Tasks (when built)

- [x] **P1 — Marker + registry read.** *(DONE — `internal/projects/projects.go`,
  `internal/tools/projects.go`.)* `.agent/project.md` schema (frontmatter: `title`, `uid`, `created`,
  `last_active`, `description` + optional body); `internal/projects` enumerates
  `<root>/*/.agent/project.md` and parses via the existing `go.yaml.in/yaml/v3` dep, `Root(workspace)`
  = `<workspace>/projects`. `List` is **resilient** — a missing root ⇒ empty (not error), a dir with no
  marker is scratch (skipped), a malformed marker is skipped so the listing stays usable; results sort
  most-recently-active first, with stable fallbacks (uid from the folder's `<slug>-<uid>` suffix, title
  from the folder name, `last_active` from the dir mtime). `list_projects` built-in (read-only, trusted,
  **not** sandbox-exposed) wired via `ExecutorConfig.ProjectsRoot` (empty ⇒ omitted), threaded from the
  resolved workspace in `run`/`chat`/`serve` (the home/chat/serve surfaces, §6; `eval` untouched). Tests:
  `internal/projects/projects_test.go` (parse/recency/skip/fallbacks), `internal/tools/projects_test.go`
  (empty + list), `internal/agent/projects_e2e_test.go` (offered only with a root). *(No tier gate on
  listing — it's read-only; the gate lands on `switch_project`, P3.)*
- [x] **P2 — Create / promote.** *(DONE — `internal/projects/create.go`,
  `internal/tools/projects.go`.)* `projects.Create(root, CreateOptions{Title, Description, FromPaths})`
  mints `<slug>-<uid>/` (slug ← title, lowercase/hyphen-collapsed/length-bounded, `project` fallback;
  uid ← 5 random bytes as lowercase unpadded base32 = 8 chars, retried on collision), creating the
  projects root on first use; seeds `.agent/project.md` via `yaml.Marshal` (so a title/description with
  YAML-special chars round-trips back through `List`) with `created`/`last_active` = now; **promotion**
  = the same call with `FromPaths` moved in (`os.Rename` under their base name, erroring on a missing
  source or in-project name collision so work is never silently dropped/clobbered). `create_project`
  built-in (trusted, **not** sandbox-exposed): **human-gated** (`gate.Approve`, `Kind:project.create`)
  and **audited** (`EventProjectCreated` with uid/title/path). Wired alongside `list_projects` on
  `ExecutorConfig.ProjectsRoot` (so it comes free on the run/chat/serve surfaces already threading it).
  Tests: `internal/projects/create_test.go` (round-trip, slug/uid, promotion + missing-path error, blank
  title), `internal/tools/projects_test.go` (approved-creates+audits, denied-creates-nothing,
  requires-title), `internal/agent/projects_e2e_test.go` (offered only with a root). *(Create does not
  re-anchor the workspace — auto-switch-on-create folds in with the P3 switch seam.)*
- [x] **P3 — Switch.** *(DONE — `internal/projects/resolve.go`, `internal/tools/{shell,projects}.go`,
  `internal/agent/agent.go`, `cmd/prompts.go`.)* `switch_project(project)` makes a named project the
  active workspace mid-run. **Re-anchor without a rebuild:** the shell now reads its working directory
  from a mutable `tools.Workspace` (via `NewShellIn`; `NewShell` wraps it), so a switch re-points
  `cmd.Dir` for subsequent commands live (§7 — `cd` never persists). **Prompt reload under the §5 gate:**
  new `ExecutorConfig.SwitchWorkspace(workspace) → PromptCustomization` seam, implemented by
  `cmd/prompts.go`'s `switchWorkspaceFn(tier)` = the *same* `loadPrompts` used at build time (so `safe`
  still won't auto-load the target's AGENTS.md/SYSTEM.md); the agent's `switchWorkspace` re-anchors the
  `Workspace` and recomposes `systemPrompt` via a shared `baseSystemPrompt` helper (picked up on the next
  request, which prepends the prompt fresh). **Resolve** (`projects.Resolve`): uid exact (case-insensitive)
  → title exact → title substring, with `*AmbiguousError` (carries candidates) so the tool reports rather
  than guesses, `ErrNotFound` otherwise. **Audit:** `EventProjectSwitched` (uid/title/path). Trusted, not
  sandbox-exposed; wired on `ProjectsRoot != "" && SwitchWorkspace != nil` after the agent exists (it
  mutates *this* executor), in `run`/`chat`/`serve` (`eval` untouched). Tests: `resolve_test.go`
  (uid/exact/substring/ambiguous/not-found), switch-tool cases in `internal/tools/projects_test.go`
  (resolve+switch+audit, not-found, ambiguous, switch-error), and `projects_e2e_test.go`
  `TestSwitchProject_ReanchorsShell` (a scripted switch → `shell pwd` runs in the new dir + the target's
  SYSTEM.md becomes the prompt) + wiring gate. *(Not reloaded on switch: the agent-type catalog stays the
  session's — only the trust-relevant prompt tier reloads; auto-switch-on-create can now call this seam.)*
- [x] **P4 — CLI flags.** *(DONE — `cmd/projects.go`, edits to `cmd/{root,config,run,chat,serve}.go`.)*
  `resolveProjects(homeWorkDir, cfg) → (root, workDir)` is the single seam all three surfaces call in
  place of the hard-wired `projects.Root(workDir)`: it returns the registry root (empty ⇒ the
  list/create/switch_project tools are omitted) and the workspace the agent acts on at launch.
  **`--no-project`** (persistent flag) = flat-repo mode: no registry, no tools, the workspace *is* the
  repo. **`--project <uid|title|path>`** activates a project at launch — a value naming an existing dir
  is used as a path, else resolved against the registry via `projects.Resolve` (ambiguous/absent
  reported, not guessed); the workspace becomes that project's dir while the root stays the *home*
  registry (so `switch_project` still reaches its siblings). Config **`projects: false`** (a `*bool`, so
  unset ⇒ enabled) disables by default; **`projects_root`** points the registry elsewhere than
  `<workspace>/projects`; both have setters (`config set-projects`, `set-projects-root`). Precedence:
  `--no-project` wins; then an explicit `--project` forces on (overriding config `false`);
  `--no-project` + `--project` is a conflict error. Tests: `cmd/projects_test.go` (default root, both
  disables, config-true/root override, conflict, activate-by-title/-path, activate-overrides-config-false,
  unknown-is-error). Build/vet/`go test -race` green; flags + error paths + config persistence
  live-verified via the binary. *(Serve threads it as `serveDeps.projectsRoot`; `eval` still untouched.)*
- [x] **P5 — Docs.** *(DONE.)* Folded "projects" into [`../environment.md`](../environment.md) (the
  runtime-env home): a new "Projects — named, recallable workspaces" section (the `projects/` layout,
  trust-by-containment + the tier gate on switch, the three tools, the P4 flag/config modes), plus rows
  in the config-reference and files-on-disk tables and a pointer from "Two anchors". `usage.md` gains a
  "Projects" operational section (what the tools do, the flags, flat-repo mode) and a pointer from the
  `agent run` flags. `README.md` gains a "Named projects" paragraph in the customize/experiment block.
  `workspace.md` §6 was **already** "resolved — see projects.md" (P3), so no change needed there.
