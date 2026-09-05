# Member Environment Image Implementation Plan

**Goal:** Anything a member installs in their environment terminal (apt,
brew, language toolchains, `gh`) is available to every agent run and
workspace shell of that member, after one explicit "Save environment" action.

**Architecture:** The environment terminal container is the source of truth
for a member's system state. Save runs `docker commit` on that container and
records the resulting image on the member. Runs, workspace shells, and the
terminal itself launch from the member's saved image, falling back to the
standard image when nothing is saved. Per-workspace Dockerfile environments,
their build/verify/rollback/edit pipeline, the mirror/repo scans, and image
selection at workspace creation are removed. Workspace environment
variables and the setup script stay.

**Non-goals:**

- No automatic or background saves. Save is explicit: a button in the
  terminal dock and `aether env save`.
- No save history. One saved image per member plus "Reset to standard".
  A bad save is fixed by repairing the terminal and saving again, or by
  resetting.
- No layering of a member image over a workspace image. Docker cannot
  merge two independent layer stacks; the member image is the only image.
- No admin-curated shared environments in this change. Teams that want a
  common baseline pin a different `--standard-image`.
- Run lifecycle stays as it is: one fresh container per run, exec'd shells
  in that container, same supervision and recovery. Only the image changes.

## Background

Today (`internal/scheduler/environment.go:50-61`) the environment terminal
starts from `cfg.StandardImage` and each run starts from the workspace's
effective image. Both mount the member home read-write at `$HOME`, so
`~/.local/bin` is shared but nothing outside the home is. `sudo apt install
gh` in the terminal writes the terminal container's own filesystem layer,
which no run ever sees and which is lost when the terminal container is
recreated (`docs/environment-home.md:16-18`). The inventory of the
workspace-image machinery this plan removes is about 7-9k lines across Go,
dashboard, and tests (details under "Removal").

## Design

### Data

`domain.Member` gains one field, `Image string`: the member's saved image
ref, empty when none. Stored as a nullable `image` column on `members`
(new migration, additive). The runtime image tag is
`aether/member-<member-id>:<unix-seconds>`; the tag is opaque to everything
except the runtime layer.

`domain.WorkspaceEnvironment` loses `CustomImage`, `NeutralImage`,
`EffectiveImage`, and the legacy `neutral_image` JSON decoder. It keeps
`Variables` and `SetupPolicy`. `Valid` keeps only the variable-name check.
The `workspaces.image` and `env` legacy columns are dropped in the same
migration; the JSON `environment` column keeps carrying variables and setup
policy. Existing custom-image workspaces silently move to member/standard
images; the migration is one-way and documented as a breaking change.

### Image resolution

`BuildEnvironmentPlan` resolves the image the same way for both purposes:
`member.Image` if set, else `cfg.StandardImage`. The `ws == nil` special
case for terminals goes away; the workspace is only consulted for
variables and setup script on runs. `cfg.NeutralImage` and the
`--neutral-image` server flag are removed. The `checkAgentPresent` neutral
image branch (`internal/scheduler/launch.go:165-183`) is removed with it;
the run fails on the agent's own "command not found" instead, which is the
real error.

If the saved image ref no longer exists in the Docker daemon (pruned by an
operator), plan building fails with an error naming the missing tag and the
`aether env reset` command. It does not silently fall back: a member who
saved expects their tools.

### Save

New scheduler method `SaveEnvironment(ctx, member) (image string, err)`:

1. Take the member's terminal lock (`terminalLock`). Require a running
   terminal container; otherwise return an error telling the member to open
   the terminal first.
2. Call the new `Runtime.Commit(ctx, containerID, tag)` which wraps
   `ContainerCommit` with `Pause: true` (Docker default; the shell freezes
   for the duration of the commit, typically seconds). The commit is
   configured with an empty `Cmd`/`Entrypoint` change so the committed image
   does not inherit `/bin/bash -l` as its command; runs pass their own argv.
3. Persist `member.Image = tag` via a new `Store.UpdateMemberImage`.
4. Remove the previous member image tag, if any, with the existing
   `RemoveImage`. Failure to remove is logged and not returned: the new
   image is already active.
5. Return the tag. The terminal container keeps running unchanged.

Runs already in flight keep their container. Runs launched after the save
use the new image. The terminal container itself keeps the layer it was
committed from, so it is already "on" the saved state.

### Reset

New scheduler method `ResetEnvironment(ctx, member)`: stop and destroy the
terminal container (reuse `StopTerminal`), clear `member.Image`, remove the
old tag. The next terminal open starts from the standard image. This is the
only undo.

### Terminal start

`ensureTerminalLocked` already passes the member to `BuildEnvironmentPlan`,
so the terminal starts from the saved image automatically. `TerminalStatus`
keeps reporting `Image`, which now shows the saved tag or the standard ref.
The existing sh fallback when the image has no bash stays.

### Runtime seam

`Runtime` interface gains `Commit(ctx, id, tag) error`. Docker implements
it; the scheduler test fake records the call and registers the tag so a
later `Create` with that image succeeds. `ImageExists` moves from a
Docker-only method to the interface because plan building now needs it.

### Protocol and CLI

Two control-channel methods, member-scoped like `terminal.*`
(`internal/sshd/terminal.go:17-20`):

- `env.save` -> `{ image: string }`
- `env.reset` -> `{}`

`terminal.status` gains `saved_image` (the member's saved ref, empty when
none) so the dock can show state without a second call.

CLI: `aether env save` and `aether env reset` replace `aether env
show|rebuild|rollback`. `aether terminal status` prints the saved image
line. `aether workspace init` loses `--standard`, `--image`; `aether
workspace settings` loses `--image`; `aether image init` is removed.
`server.info` loses `neutral_image` and `standard_image`.

### Dashboard

Terminal dock (`web/src/routes/board/terminal-dock.tsx:200-212`): the
header actions become, left to right, **Save environment** (primary
variant, always visible when the terminal is running), then the existing
ghost **Stop environment**. Save shows a spinner label "Saving..." while
the RPC runs, then a short inline confirmation ("Saved - new runs use this
environment") that fades. Errors reuse the existing `statusError` path.
A **Reset to standard** entry sits inside the Stop confirmation dialog as a
second destructive option, since both stop the container.

When the terminal is running but no image is saved yet, the dock shows a
one-line hint under the tabs: "Installs here reach agents after you save."
The hint goes away once `saved_image` is set.

Removed from the dashboard: Environment panel on the workspace page,
onboarding Environment step (the wizard becomes five steps), create-workspace
image choice, "still building" run banner, `environment.build` and
`environment.edit` event handling, env API methods and types.

Onboarding's Agents step already opens the terminal dock to install the
agent. It gains one sentence pointing at the Save button so the first run
sees the install.

### Docs

`docs/environments.md` is rewritten around the new model: the terminal is
the environment, Save, Reset, what the standard image contains, the
`--standard-image` flag for teams. Sections on Dockerfile definitions,
mirror/repo scans, verification, rollback, `aether env rebuild`, `aether
image init`, and `workspace init --image` are removed. `environment-home.md`
drops "System packages belong in an administrator-approved image" and
explains home vs image persistence in two sentences. `terminal.md`,
`quickstart.md`, `install.md` (flag table, data layout, image retention),
`local-gateway.md` (scan routes and verbs), `dashboard-frontend.md`
(dataflow) are updated to match. The breaking change is called out in the
release notes / changelog with the migration statement.

## Removal

Fully removed (file, approx lines incl. tests):

- `internal/domain/environment.go` (~136)
- `internal/envdef/` (~510)
- `internal/envprompt/` inventory/repo/refine renderers and template
  sections; `RenderProfile`/`ProfileParams` stay for profile scans (~400)
- `internal/store/environment.go` + test (~455)
- `internal/scheduler/environment_build.go` + test (~1060)
- `internal/scheduler/environment_edit.go` + test (~1050)
- `internal/protocol/environment.go` + test (~470)
- `internal/sshd/environment.go` + test (~680)
- `internal/server/svc_environment.go`, `environment_integration_test.go`
  (~410)
- `internal/events/environment.go`, `environment_edit.go` (~70)
- `cmd/aether/env.go` + test, rewritten to save/reset (~480 removed)
- `cmd/aether/image.go` + test (~200)
- `web/src/routes/workspaces/environment.tsx` + test (~710)
- `web/src/routes/onboarding/environment-step.tsx` (~800)
- `web/src/components/environment-choice.tsx` + test (~330)
- `web/src/store/environment.ts` + test (~250)

Partially edited:

- `internal/scheduler/environment.go`: image resolution.
- `internal/scheduler/launch.go`: drop neutral-image agent presence check.
- `internal/scheduler/scheduler.go`: drop `NeutralImage`, `EnvEditDir`.
- `internal/domain/domain.go`: `WorkspaceEnvironment`, `Member.Image`.
- `internal/store/sqlite.go`, `migrate.go`: member image column, drop
  workspace image columns and `env_definitions` table.
- `internal/protocol/protocol.go`, `wire.go`, `admin.go`: method names,
  server.info fields, workspace image DTOs.
- `internal/sshd/settings.go`, `handlers.go`: `workspace.image`,
  server.info.
- `internal/server/server.go`, `cmd/aether-server/main.go`: flags/config.
- `internal/localgw/envscan.go`, `internal/localops/envscan.go`,
  `internal/localgw/api.go`: keep profile scan, drop mirror/repo/refine.
- `cmd/aether/workspace_settings.go` and workspace init: image flags.
- `web/src/lib/api.ts`, `types.ts`, `store/sync.ts`,
  `routes/workspaces/workspace.tsx`, `routes/onboarding/index.tsx`,
  `routes/board/terminal-dock.tsx`, `test/fixtures.ts`.
- `images/bootstrap/Dockerfile` and its release workflow job: the neutral
  image has no consumer after this change; remove both.

`internal/localops/rebuild.go` and `internal/localgw/rebuild.go` are the
desktop app rebuild and stay.

## Tasks

Each task is one PR-sized commit on the feature branch, with its own tests,
and leaves the tree building and `make test` green. Order matters: the
removal tasks come first so the new code is written against the simplified
model rather than beside the old one.

### Task 1: Remove workspace image definitions (server)

Delete the packages and files under "Fully removed" for Go, drop
`CustomImage`/`NeutralImage`/`EffectiveImage` from `WorkspaceEnvironment`,
add the migration dropping `workspaces.image`, `workspaces.env`, and
`env_definitions`, remove `NeutralImage`/`EnvEditDir` config and the
`--neutral-image` flag, remove `workspace.image` and `env.*` protocol
methods and server.info image fields. `BuildEnvironmentPlan` temporarily
uses `cfg.StandardImage` for both purposes so the tree stays green.
Tests: migration test asserting the columns/table are gone and variables
plus setup script survive; scheduler tests updated to the single image.

Commit: `refactor(server)!: drop per-workspace environment images`

### Task 2: Remove workspace image flows (CLI and dashboard)

Delete `aether env show|rebuild|rollback`, `aether image init`, the
`--standard`/`--image` creation flags and `workspace settings --image`.
Delete the dashboard Environment panel, onboarding Environment step, image
choice component, environment store slice, env API methods/types/events,
and the run "still building" banner. Trim the local gateway scan surface to
the profile scan. Tests: onboarding renders five steps; workspace page has
no Environment panel; `api.test.ts` env cases removed; CLI usage tests
updated.

Commit: `refactor(cli,dashboard)!: drop workspace environment flows`

### Task 3: Member image persistence and resolution

Add `Member.Image`, the `members.image` migration, `Store.UpdateMemberImage`,
`Runtime.ImageExists` on the interface, and member-first image resolution
in `BuildEnvironmentPlan` with the missing-image error. Remove the neutral
image agent presence check. Tests: plan uses the saved image for run and
terminal; falls back to standard when empty; errors when the saved tag is
missing.

Commit: `feat(scheduler): launch runs and terminals from the member image`

### Task 4: Save and reset

Add `Runtime.Commit` (Docker + fake), `Scheduler.SaveEnvironment` and
`ResetEnvironment`, `env.save`/`env.reset` protocol methods and sshd
handlers, `saved_image` on `terminal.status`, and `aether env save|reset`.
Tests: save requires a running terminal; save commits, persists, removes
the previous tag; reset stops the terminal and clears the image; sshd
round-trip for both methods; CLI output.

Commit: `feat: save the environment terminal as the member image`

### Task 5: Dashboard save button

Save and Reset in the terminal dock as designed, `saved_image` in the
status type, the unsaved hint, and the Agents step sentence. Tests: button
calls `env.save`, shows the saving and saved states, surfaces errors; reset
appears in the stop dialog and calls `env.reset`; hint hides when
`saved_image` is set.

Commit: `feat(dashboard): add save environment to the terminal dock`

### Task 6: Docs and images

Rewrite `docs/environments.md`; update `environment-home.md`,
`terminal.md`, `quickstart.md`, `install.md`, `local-gateway.md`,
`dashboard-frontend.md`; remove `images/bootstrap` and its release job;
add the changelog entry with the migration note. `make public-audit`.

Commit: `docs: describe the member environment image`

## Verification

End-to-end on a real server with Docker, as a member:

1. `aether terminal`, run `sudo apt-get install -y gh`, `which gh` works.
2. Launch a run before saving: `which gh` inside the run fails. Expected.
3. Click Save environment (or `aether env save`). `aether terminal status`
   shows the saved image.
4. Launch a run: `which gh` succeeds. Open a workspace shell in that run:
   `which gh` succeeds.
5. `aether terminal stop`, then `aether terminal`: `which gh` still works.
6. Restart the server; repeat step 4.
7. `aether env reset`, open the terminal: `which gh` fails; runs use the
   standard image again.
8. Second member on the same server never sees the first member's image.

Plus `make fmt-check vet lint test test-scripts public-audit`, `make
test-integration`, and `bun run typecheck && bun run test` in `web/`.

## Risks

- **Image growth.** Each save is a full container commit; large installs
  make large images, and Docker keeps layers until the previous tag is
  removed. Mitigated by removing the prior tag on save and on reset.
  Operators can `docker system prune` unaffected images; a pruned member
  tag surfaces as the explicit missing-image error.
- **Pause during commit.** The terminal freezes for the commit duration.
  The button label communicates this. If it proves too long for heavy
  images, `Pause: false` is a one-line change with a small consistency
  tradeoff.
- **Committed state includes junk.** `/tmp`, apt caches, shell history in
  the container layer all get committed. Home is a bind mount and is not
  included. Acceptable for a per-member dev image; not a shipping artifact.
- **Breaking change.** Existing workspaces lose their custom image on
  upgrade. The migration is one-way. Called out in the changelog and
  install.md upgrade notes.
