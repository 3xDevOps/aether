# Environment Page Implementation Plan (Phase 5)

> **For agentic workers:** implement this plan task-by-task, one worker per
> task with review between tasks. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** The workspace view gains an Environment panel - what is in the
remote environment, version history, rollback - and an admin can type a
change request that the workspace's registered harness turns into a
reviewed, diffed, rebuilt environment, all server-side.

**Architecture:** A scheduler orchestration runs the harness headless in
a throwaway container (the `verifyEnvironmentImage` shape plus the run
plan's credential and tool-snapshot mounts), with a host-mounted scratch
directory for the output pair, the refine prompt carrying the admin's
request, and envdef validation with one retry - the container twin of the
local `RunScan`. A successful edit is stored as a new definition version
in the existing `saved` status: the proposal IS a version, so approval is
the existing `env.build` and no new save path exists. Two new wire
methods (`env.edit` to start, `env.get` to fetch a version's Dockerfile,
manifest, and a server-generated unified diff) plus an
`environment.edit` event stream feed the dashboard panel.

**Tech stack:** Go (scheduler, protocol, sshd, events), React + Vitest
in `web/` (reusing the diff route's `parsePatch`/`FilePatch`).

**Spec:** `docs/plans/2026-08-27-env-onboarding-design.md` (Environment
page and edit agent).

## Global constraints

- No file grows past ~1000 lines; errors returned with context; docs
  updated in the same task as the behavior they describe; no em dash
  characters anywhere; TDD throughout; conventional commits with no
  co-author trailers; tick your task's plan checkboxes when done.
- Everything here is admin-guarded (`permissions.WorkspaceAdmin`), like
  the existing env methods.
- The edit container gets no aether secrets: only the member's own
  harness credential home and tool snapshot, exactly like a run.
- The panel's copy is plain language; failure states always name the
  next step (the spec's "register it with `aether agent add`" message).

## Non-goals

- No CLI surface for the edit agent (dashboard-only in v1, per spec).
- No editing of variables or setup scripts; the edit agent changes the
  Dockerfile and manifest only.
- No deletion of proposal versions; an unapproved `saved` version is
  harmless history, and retention only governs image tags.

## Design notes

Container recipe (all pieces exist): resolve the harness through
`Scheduler.command` with `domain.LaunchHeadless` and the rendered prompt
as the task; build the plan with `BuildEnvironmentPlan(ctx, nil, ws,
member, profile, EnvironmentPurposeRun, "")` so credential mounts and the
read-only tool snapshot arrive as they do for runs; take the
per-workspace environment build lock and a credential-user reservation
(`reserveCredentialUser`) so edits cannot race builds or runs; append a
per-job scratch mount created under a new server-owned scratch root the
way coordination mounts are appended and validated after the plan; run
with the workspace's effective image; capture output through
Attach/Start/Wait as `verifyEnvironmentImage` does, streaming lines as
events; enforce the ten-minute default timeout; read `Dockerfile` and
`manifest.json` from the scratch directory, validate through `envdef`,
retry once with the validation error appended.

The prompt is `envprompt.RenderRefine` with the current pair and the
admin's request as feedback, so an edit and the wizard's
"request changes" speak the same contract.

Preflight, because `checkAgentInstalled` skips custom-image workspaces:
the requested harness must be in `harness.SetupHarnesses()`, and the
member must have login state for it (a non-empty credential home under
`<homes>/<member>/<harness>` or a registered member definition);
otherwise fail with `CodeInvalidState` and the message naming
`aether agent add <harness> --workspace <ws>`.

The proposal's source carries over from the version it edited (a mirror
environment stays `mirror`); the editing harness is recorded on the new
version. The Dockerfile diff is generated server-side with
`git diff --no-index` (git is a server prerequisite) so the dashboard
reuses `parsePatch` and `FilePatch` unchanged.

## Tasks

### Task 1: scheduler - the edit-agent run

- Create: `internal/scheduler/environment_edit.go`,
  `internal/scheduler/environment_edit_test.go`,
  `internal/events/environment_edit.go`.
- Consumes: `envprompt.RenderRefine`, `envdef.ParseManifest` /
  `ValidateDockerfile`, `BuildEnvironmentPlan`, `Scheduler.command`,
  `lockEnvironmentBuild`, `reserveCredentialUser` /
  `releaseCredentialUser`, the runtime interface, the store's
  environment methods.
- Produces: `EditEnvironment(ctx, workspaceID, member, harness,
  request)` implementing the design-notes recipe and returning the
  proposed version number, plus the event type `environment.edit` with
  payload fields harness, status (`running`, `validating`, `retrying`,
  `proposed`, `failed`), line, detail, and version (set on `proposed`).
  The scratch root is a new scheduler config directory wired by the
  server under its data dir; per-job directories are created 0700 and
  always removed. The fake harness short-circuits to a canned edited
  pair (through the same validation) so the flow is demoable and
  testable without vendors.
- [x] Tests first (fake runtime and store, as `environment_build_test.go`
  does): preflight failures for a non-setup harness and for missing
  login state, each naming the agent-add command; the happy path runs
  the container with credential and tool mounts, the scratch mount, and
  the refine prompt in argv, then saves a `saved` version carrying the
  predecessor's source and the editing harness and emits `proposed`;
  validation failure retries once then emits `failed` with detail; the
  build lock serializes an edit against a build; scratch is removed in
  every outcome; the fake harness path works end to end.
- [x] Implement, run `go test ./internal/scheduler/ ./internal/events/`,
  commit.

### Task 2: wire - env.edit and env.get

- Modify: `internal/protocol/environment.go` (`MethodEnvEdit` with
  params workspace selector, harness, request, returning accepted:
  the run is asynchronous and terminal state arrives as events;
  `MethodEnvGet` with params workspace selector, version, optional
  diff-against version, returning dockerfile, manifest, source,
  harness, status, and when requested a unified diff text),
  `internal/sshd/environment.go` (both handlers, WorkspaceAdmin,
  following the `envBuild` spawn idiom for edit; the
  `EnvironmentService` seam gains `EditEnvironment`), diff generation
  helper beside the handler using `git diff --no-index` on two temp
  files, sshd and protocol tests.
- Consumes: Task 1's `EditEnvironment`.
- Produces: the wire surface Task 3 mirrors; `env.get`'s diff text is
  `diff --git`-shaped so the dashboard's `parsePatch` accepts it.
- [x] Tests first: non-admin denied; env.edit rejects an unknown
  harness before spawning; env.get round-trips dockerfile and manifest
  for a stored version, returns not-found for a missing one, and its
  diff output parses as a unified diff and reflects a known change;
  docs: `docs/local-gateway.md` untouched (control-channel only) but
  the method list in any protocol doc that enumerates env methods is
  extended (no public doc enumerates the env method list today, so no
  doc needed the extension).
- [x] Implement, run `go test ./internal/protocol/ ./internal/sshd/`,
  commit.

### Task 3: web - client surface and edit state

- Modify: `web/src/lib/types.ts` (env edit payloads, env.get result),
  `web/src/lib/api.ts` (`envEdit`, `envGet`, and the missing
  `envRollback`), `web/src/store/environment.ts` (per-workspace edit
  state: status, a capped rolling window of output lines for the
  expander, proposed version, failure detail; a matching
  `environment.edit` case in `web/src/store/sync.ts`),
  `web/src/test/fixtures.ts`, slice and sync tests.
- Consumes: Task 2's wire shapes and Task 1's event payload.
- Produces: the typed surface and store selectors Tasks 4 and 5 render
  from.
- [ ] Tests first: sync routes `environment.edit` events into the
  slice; the line window caps and keeps the newest; `proposed` and
  `failed` set their fields; the three new api functions are exercised
  through fixtures.
- [ ] Implement, run `bun run typecheck` and `bun run test` from
  `web/`, commit.

### Task 4: web - the Environment panel, read side

- Create: `web/src/routes/workspaces/environment.tsx` (an
  `EnvironmentPanel({ workspaceID, client })` following the
  self-fetching shape of `ToolsPanel` in
  `web/src/routes/workspaces/tools.tsx`), with colocated tests.
- Modify: `web/src/routes/workspace.tsx` (render the panel in the
  scrolling body above the run list, gated on
  `caps.hasMethod('env.status')`), `docs/dashboard-frontend.md`
  (component inventory).
- Consumes: `envStatus` and `envRollback` (Task 3), the environment
  slice's build state for the in-progress banner already shipped.
- The read side shows: the active version's manifest as a plain list
  (name, version, reason), which path made it and with which harness,
  a compact version history (version, status, source, when) with the
  failure detail on failed rows, and a rollback button with a confirm
  that names the version it returns to. Empty state (no definitions:
  the workspace uses its creation-time image) is one calm sentence.
- [ ] Tests first: renders manifest and history from a fixture status;
  rollback calls the api and refetches; empty state; the panel is
  absent without the capability.
- [ ] Implement, run `bun run typecheck` and `bun run test`, commit.

### Task 5: web - the edit flow

- Modify: `web/src/routes/workspaces/environment.tsx` (the write side),
  its tests, `docs/quickstart.md` (a short "changing the environment
  later" paragraph pointing at the workspace page),
  `docs/harnesses.md` if the setup-capable table gains a note about
  the edit agent.
- Consumes: `envEdit`, `envGet`, `envBuild` (existing), the edit state
  from Task 3, `parsePatch` and `FilePatch` from `web/src/routes/diff/`.
- The write side: a harness select over the setup-capable four
  (defaulting to the active version's harness when set) and a
  plain-language request box; submitting calls `envEdit` and renders
  the in-flight state - one status line plus the collapsed "View
  process" expander over the slice's line window. On `proposed`, fetch
  `envGet` with diff-against the active version and render the review:
  the Dockerfile diff via `FilePatch`, the updated manifest summary,
  and two buttons - approve (calls `envBuild` on the proposed version;
  the existing build banner and slice take over) and dismiss (clears
  the edit state; the version remains in history). On `failed`, show
  the detail with retry; when the failure is the missing-login
  preflight, surface the server's agent-add message verbatim.
- [ ] Tests first: submit wires harness and request; in-flight state
  streams lines into the expander; proposed fetches the diff and
  renders both views; approve triggers the build on the right version;
  dismiss clears; failure and preflight messages render.
- [ ] Implement, run `bun run typecheck` and `bun run test`, commit.

### Task 6: full check sweep

- [ ] Run `make fmt-check`, `make vet`, `make lint`, `make test`,
  `make public-audit`, and from `web/`: `bun run typecheck`,
  `bun run test`; fix anything surfaced and commit the fixes.
