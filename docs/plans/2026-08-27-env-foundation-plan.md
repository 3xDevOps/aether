# Environment Foundation Implementation Plan (Phase 1)

> **For agentic workers:** implement this plan task-by-task, one worker per
> task with review between tasks. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** The server can store a workspace environment definition
(Dockerfile + manifest), build it into a verified local image, swap the
workspace onto it, roll back, and clean up old tags - drivable end to end
from the CLI.

**Architecture:** A new `EnvironmentDefinition` domain object versioned per
workspace in the SQLite store; a `BuildImage` verb on the runtime seam
implemented against the existing docker daemon; a scheduler orchestration
that builds, verifies the image against the manifest's check commands in a
throwaway container, swaps the workspace image, and prunes old tags; three
admin-guarded control-channel methods plus `aether env` CLI commands.

**Tech stack:** Go, docker client API (already a dependency via
`internal/runtime/docker.go`), SQLite store, existing event feed.

**Spec:** `docs/plans/2026-08-27-env-onboarding-design.md`

## Global constraints

- Per the user's standing rules and repo style: no file grows past ~1000
  lines; errors returned with context, never logged and swallowed; docs
  updated in the same task as the behavior they describe; no speculative
  configuration.
- Every build-triggering or definition-mutating call is admin-guarded.
- The build context is the Dockerfile alone; COPY and ADD are rejected at
  validation time, before anything reaches the daemon.
- Locally built tags are named `aether/ws-<workspace-id>:<version>` and are
  never pulled from a registry.
- TDD throughout: each task writes the failing test first, then the
  minimal implementation, runs the package tests, and commits.

## Non-goals (later phases)

- No dashboard wizard step, no local inventory agent, no gateway verb, no
  standard image, no edit agent, no prompt templates. Phase 1 has no agent
  anywhere; a failed verification simply marks the version failed - repair
  rounds arrive with the agent flows.
- No changes to `workspace.settings`; the new methods are the only
  sanctioned way the workspace image changes after creation.
- No pi/amp harness profiles (phase 3).

## Design notes

Definition lifecycle: `saved -> building -> verifying -> active | failed`.
Exactly one version per workspace is `active`; the workspace's
`Environment.CustomImage` always equals the active version's tag. Rollback
re-activates a previous version; if retention already removed its tag the
server rebuilds it from the stored Dockerfile first, so rollback never
depends on a registry. Retention keeps the tags of the active version and
the most recent previously-active version; other tags are removed after a
successful swap.

Verification runs one throwaway container from the freshly built tag,
executing a generated shell script that runs each manifest item's check
command between unique output markers; the server parses the captured
output and requires each item's declared version to appear in its command's
output. This reuses the runtime's existing Create/Start/Wait/Attach surface
rather than adding an exec verb.

Build serialization: one build at a time per workspace, following the
per-member+harness lock pattern in `internal/scheduler/agent_setup.go`.

## Tasks

Each task: failing test first, minimal implementation, package tests green,
commit with a conventional message.

### Task 1: domain - environment definition types

- Create: `internal/domain/environment.go`,
  `internal/domain/environment_test.go`.
- Produces (later tasks consume these names): `EnvironmentDefinition`
  (workspace ID, version number, Dockerfile text, manifest, source,
  harness, status, failure detail, timestamps), `ManifestItem` (name,
  version, reason, Dockerfile line span, check command),
  `EnvironmentSource` enum (`mirror`, `repo`, `standard`, `manual`),
  `EnvironmentStatus` enum (`saved`, `building`, `verifying`, `active`,
  `failed`), an `ImageTag()` method producing
  `aether/ws-<workspace-id>:<version>`, and validation mirroring the
  domain package's existing `Valid()` idiom.
- [ ] Tests: valid definition round-trips JSON; empty Dockerfile, empty
  manifest, item without a check command, unknown source, and unknown
  status are each rejected; `ImageTag()` output is stable and contains no
  shell-hostile characters for a representative workspace ID.
- [ ] Implement, run `go test ./internal/domain/`, commit.

### Task 2: envdef - the output-contract validator

- Create: `internal/envdef/envdef.go`, `internal/envdef/envdef_test.go`
  (new package; it will be shared with the local gateway in phase 3, so it
  must not import server-only packages).
- Produces: `ParseManifest` (bytes to validated `[]domain.ManifestItem`)
  and `ValidateDockerfile` (Dockerfile text plus manifest, returning every
  violation joined, per the repo's error style).
- Dockerfile rules from the spec: single build stage based on
  `ubuntu:24.04`; COPY and ADD forbidden; every manifest item's line span
  must fall inside the file; no obviously credential-shaped content
  (reject lines that set well-known secret env names - reuse the deny
  vocabulary in `internal/profile/scan.go` rather than inventing a new
  list).
- [ ] Tests: a well-formed pair passes; each rule above has a rejecting
  case; manifest JSON that is malformed, missing fields, or maps to
  out-of-range lines is rejected with the offending item named.
- [ ] Implement, run `go test ./internal/envdef/`, commit.

### Task 3: store - versioned definitions per workspace

- Modify: `internal/store/migrate.go` (append the next migration),
  `internal/store/store.go` (interface + not-found semantics),
  `internal/store/sqlite.go`.
- Test: `internal/store/store_test.go`.
- New table keyed `(workspace_id, version)` holding the definition as a
  JSON blob plus status and timestamps, version assigned by the store as
  max+1 per workspace; deleting a workspace cascades. Interface additions:
  save (returns the assigned version), get by version, get active, list
  versions (newest first), and a status transition that also demotes the
  previously active version when a new one activates - activation must be
  atomic in one transaction so two versions are never active.
- [ ] Tests: save assigns 1 then 2; get for a missing version returns the
  store's not-found error; activation demotes the prior active row in the
  same transaction; list orders newest first; workspace delete cascades.
- [ ] Implement, run `go test ./internal/store/`, commit.

### Task 4: runtime - image build and removal

- Modify: `internal/runtime/runtime.go` (interface),
  `internal/runtime/docker.go` (implementation, keeping the file under the
  size limit - split a `docker_build.go` if needed).
- Produces: `BuildImage(ctx, dockerfile string, tag string, progress
  io.Writer) error` and `RemoveImage(ctx, tag string) error` on the
  `Runtime` interface. The docker implementation sends a tar context
  containing only the Dockerfile, streams daemon progress lines to
  `progress`, and surfaces build failure with the daemon's error detail.
  The pull path (`pull()` in `docker.go`) learns that tags under the
  `aether/` repository are local-only: never pulled, and a missing local
  tag fails Create with an error naming `aether env rebuild`.
- [ ] Unit tests: the local-only tag predicate; error text for a missing
  local tag. Real build/remove behavior is covered by the integration
  suite (Task 8) per the repo's prefer-end-to-end rule.
- [ ] Implement, run `go test ./internal/runtime/`, commit.

### Task 5: scheduler - build, verify, swap, retain

- Create: `internal/scheduler/environment_build.go`,
  `internal/scheduler/environment_build_test.go`.
- Consumes: store methods from Task 3, runtime verbs from Task 4,
  `envdef` validation from Task 2, the event bus the scheduler already
  publishes to, and the lock pattern from `agent_setup.go`.
- Produces: `BuildEnvironment(ctx, workspaceID, version)` - the one entry
  point later tasks and phases call. It serializes per workspace,
  transitions status through the lifecycle, streams build output to the
  event feed (`environment.build` event subsystem, following existing
  event naming), runs verification, and on success atomically activates
  the version, sets the workspace's `Environment.CustomImage` to the tag,
  clears `NeutralImage`, and prunes tags beyond active + previous.
  Verification: boot a throwaway container from the new tag running the
  marker-delimited check script described in the design notes; any
  mismatch marks the version `failed` with per-item detail and leaves the
  workspace image untouched. Also produces `RollbackEnvironment(ctx,
  workspaceID)` implementing the rebuild-if-purged rule.
- [ ] Tests against the fake runtime (extend
  `internal/runtime`'s existing test double or the scheduler's fakes as
  the codebase already does): happy path reaches `active` and swaps the
  image; a build error marks `failed` and preserves the previous image; a
  check mismatch marks `failed` naming the item; concurrent builds on one
  workspace serialize; retention removes only prunable tags; rollback
  re-activates and rebuilds when the tag is gone.
- [ ] Implement, run `go test ./internal/scheduler/`, commit.

### Task 6: wire protocol and server handlers

- Modify: `internal/protocol/protocol.go` (method constants
  `MethodEnvSave`, `MethodEnvBuild`, `MethodEnvStatus`,
  `MethodEnvRollback`), `internal/protocol/wire.go` (params/results:
  save carries workspace selector, Dockerfile, manifest, source, harness;
  build and rollback carry selector plus optional version; status returns
  the version list with statuses, failure detail, and the active marker),
  `internal/protocol/client.go` (client methods), new
  `internal/sshd/environment.go` with admin-guarded handlers via
  `registerGuarded`, following `internal/sshd/settings.go`.
- Save validates through `envdef` before storing and returns the assigned
  version; build launches Task 5's `BuildEnvironment` asynchronously and
  returns immediately (progress rides the event feed); status is a plain
  read; rollback calls `RollbackEnvironment`.
- [ ] Tests: handler-level tests in the sshd package's existing style -
  non-admin denied with `CodeDenied`; invalid definition rejected with the
  validator's detail; save/status round-trip; build on a workspace with no
  saved definition returns `CodeInvalidState`.
- [ ] Implement, run `go test ./internal/protocol/ ./internal/sshd/`,
  commit.

### Task 7: CLI - aether env

- Create: `cmd/aether/env.go` following the command shape of
  `cmd/aether/agent.go`.
- Subcommands: `aether env show [--workspace <ws>]` renders the active
  version's manifest as a readable table plus recent versions and
  statuses; `aether env rebuild` triggers a build of the active (or
  `--version`) definition and follows the event feed until the terminal
  status, exiting nonzero on failure; `aether env rollback` invokes
  rollback and reports the resulting active version. Each failure message
  names the next command to run, matching the CLI's existing
  print-the-next-step convention.
- Modify: `docs/bootstrap.md` (the "Image escape hatch" section now
  documents the built-in path and the local-only tag rule) and
  `docs/install.md` (image storage and retention) in this task, since this
  is where the behavior becomes user-visible.
- [ ] Tests: command wiring tests in the existing `cmd/aether` test style
  (argument validation, output rendering against a fake client).
- [ ] Implement, run `go test ./cmd/aether/`, commit.

### Task 8: integration - end to end

- Modify: the integration suite next to the existing docker-backed tests
  (`make test-integration`).
- [ ] One end-to-end test: save a tiny definition (ubuntu base installing
  one pinned apt package, manifest with one item whose check command
  reports its version) - build - verify - workspace image swapped to the
  tag - a run container starts from it - save a second version with a
  deliberately wrong manifest version - build fails verification, image
  unchanged - rollback returns to version 1. Assert retention leaves only
  the expected tags.
- [ ] Run `make test-integration`, commit.

### Task 9: full check sweep

- [ ] Run `make fmt-check`, `make vet`, `make lint`, `make test`,
  `make public-audit`; fix anything surfaced. Dashboard checks are
  untouched by this phase but run `bun run typecheck` and `bun run test`
  from `web/` anyway to prove it.
- [ ] Commit any fixes.
