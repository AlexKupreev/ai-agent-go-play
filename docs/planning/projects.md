# Projects — concept & roadmap

A **project** is a named, persistent workspace the agent can *recall by intent* and *switch into*
mid-conversation — the thing that lets a local chat answer *"let's discuss the articles I shared last
time"* or *"now let's write a script for health analysis"* without the user ever naming a path. It is
the conversational counterpart to [`workspace.md`](workspace.md)'s cwd model: a workspace answers
*what the agent is acting on*, and a project is a workspace that has been **given a name and a home so
it can be found again**. Companion to [`workspace.md`](workspace.md) (the anchor a project *is*),
[`prompts.md`](prompts.md) (a project carries the project prompt tier), and
[`subagents.md`](subagents.md) (a scout / worktree operates on the active project). **Status:
design only — nothing built.** This doc resolves the mid-session-switch and "who vouches for a
workspace" open questions left in [`workspace.md`](workspace.md) §6.

---

## 0. The model in one paragraph

The agent runs from a **home workspace** and lands in its **scratch** area — throwaway work, no
project. When work is worth keeping, the agent **promotes** it to a project: a folder
`<home>/projects/<slug>-<uid>/` with a small `.agent/project.md` marker (title + description). The
**filesystem *is* the registry** — `list_projects` is a directory listing, no separate index to keep
in sync. Later the agent **recalls** a project by matching the user's intent against those titles /
descriptions, and **switches** into it with a tier-gated `switch_project` (which re-anchors the
workspace and reloads the project prompt tier). For the CLI-launched-in-a-repo case, projects are
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
- **Nesting.** A project *is* a workspace, so the mechanism is uniform and a project could hold its
  own `projects/`. Default UX is **one level** (projects are leaves; the agent doesn't create nesting
  unless asked) — uniform mechanism, simple surface.
- **Scratch persistence across sessions.** Is the home-workspace scratch area durable (survives
  between chats) or cleared? Durable is friendlier for *"last time"* recall of un-promoted work, but
  blurs the promote boundary.
- **`list_projects` scope in a big tree.** Enumerate only the home `projects/` (v1) vs. also
  discovering nested / sibling project markers. v1: home only.

---

## 9. Tasks (when built)

- [ ] **P1 — Marker + registry read.** `.agent/project.md` schema (frontmatter: `title`, `uid`,
  `created`, `last_active`, `description`); a `projects` package that enumerates
  `<home>/projects/*/.agent/project.md` and parses via the existing YAML dep. `list_projects` built-in.
- [ ] **P2 — Create / promote.** `create_project(title, description?)` — slug + UID minting, mkdir,
  seed marker, switch; promotion = create + move `from_paths`. Trusted built-in, permission-gated
  (side-effecting: mkdir + registry write).
- [ ] **P3 — Switch.** `switch_project(uid|title)` reusing the stage-F reload seam
  (`buildExecutor`/`promptState`) to re-anchor `WorkDir` + reload the project prompt tier under the
  §5 gate; audit event on switch; title→path fuzzy resolve with disambiguation.
- [ ] **P4 — CLI flags.** `--no-project` (flat repo mode) + `--project <uid|title|path>` +
  config `projects: false` / `projects_root`, threaded through `run`/`chat`/`serve` next to
  `--workspace`.
- [ ] **P5 — Docs.** Fold "projects" into [`../environment.md`](../environment.md) (the runtime-env
  home): scratch vs. project, the `projects/` layout, the three tools, the flags. Update
  `workspace.md` §6 to "resolved — see projects.md". Surface `list/create/switch_project` in
  `usage.md`/`README.md`.
