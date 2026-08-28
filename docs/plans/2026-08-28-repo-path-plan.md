# From-Repo Path Implementation Plan (Phase 4)

> **For agentic workers:** implement this plan task-by-task, one worker per
> task with review between tasks. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** The onboarding Environment step gains a third choice: the agent
reads the repository (and its devcontainer config, if any) and derives
the environment the project needs, rather than what the user's machine
has.

**Architecture:** A repo mode through the existing phase 3 pipeline: a
new prompt renderer, a `RepoPath` scan option that runs the agent inside
the repository while outputs land in the scratch directory, a `repo_path`
field on the scan socket's start frame, and a third wizard card that
prefills the folder from what the gateway already knows about linked
repos. Everything downstream - validation, review gate, save with source
`repo`, build, banners - is unchanged.

**Tech stack:** Go (`internal/envprompt`, `internal/localops`,
`internal/localgw`), React + Vitest in `web/`.

**Spec:** `docs/plans/2026-08-27-env-onboarding-design.md` (Local
inventory verb - the from-repo paragraph - and Onboarding flow).

## Global constraints

- No file grows past ~1000 lines; errors returned with context; docs
  updated in the same task as the behavior they describe; no em dash
  characters anywhere; TDD throughout; conventional commits with no
  co-author trailers.
- The scan must never write into the repository: outputs go to the
  scratch directory, the prompt forbids modifying repo files, and the
  engine fails the scan if the repository's git status changed during
  the run.
- Every failure path still offers "keep the standard environment".

## Non-goals

- No Environment page and no server-side edit agent (phase 5).
- No remote-only repo support: the repo must exist as a folder on the
  user's machine, same as `aether link --repo` requires.

## Design notes

Phase 3's refine loop, review gate, and build flow are mode-agnostic
already; the only genuinely new behavior is where the agent looks and
what the prompt asks. The agent runs with the repository as its working
directory so it can read manifests and lockfiles naturally, and the
prompt names an absolute output directory (the scratch dir) for the two
files. A devcontainer config, when present, is called out as the
strongest signal of intent. Refine runs for a repo-sourced pair reuse
the same repo path.

The wizard prefills the repo folder from the gateway's linked-repo
knowledge (the `link.repo`/`link.status` machinery that `aether link
--repo` feeds) and lets the user edit it; validation happens in the
engine so CLI-less surfaces get the same errors.

## Tasks

### Task 1: envprompt - the repo prompt

- Modify: `internal/envprompt/envprompt.go`, the embedded template,
  `internal/envprompt/envprompt_test.go`. Bump `Version`.
- Produces: `RenderRepo(params)` with params for the base image and the
  absolute output directory. The rendered prompt must instruct, in
  order: derive what the repository's project needs from its own files
  (manifests, lockfiles, toolchain version files, CI configs), not from
  the machine running the scan; treat `.devcontainer/devcontainer.json`
  as the strongest statement of intent when present and translate its
  features and image choice into the Dockerfile; never modify, create,
  or delete repository files; write exactly `Dockerfile` and
  `manifest.json` into the named output directory; then the same
  contract clauses the inventory prompt carries (ubuntu 24.04 single
  stage, pinned versions, no COPY or ADD, no secrets, per-item
  check_command, honest item count).
- [x] Tests first: repo prompt contains each new clause plus the shared
  contract clauses; the output directory appears verbatim; the golden
  version assertion catches the template change.
- [x] Implement, run `go test ./internal/envprompt/`, commit.

### Task 2: envscan - the repo mode

- Modify: `internal/localops/envscan.go`,
  `internal/localops/envscan_test.go`.
- Consumes: `envprompt.RenderRepo` (Task 1).
- Produces: `ScanOptions.RepoPath` (used when the mode is repo, and by
  refine runs whose original pair came from a repo scan). Before
  launching: the path must exist, be a directory, and contain a `.git`
  entry - each failure is a distinct error naming the path. The harness
  runs with the repository as its working directory; outputs are read
  from the scratch directory the prompt named. After the run, `git
  status --porcelain` in the repository must match its pre-run output or
  the scan fails stating the repository was modified. The fake harness
  gains a canned repo pair (distinct from the mirror one) so the flow is
  demoable.
- [x] Tests first: a stub harness observing its working directory
  proves it runs inside the repo and writes to the scratch dir; each
  path-validation failure; the modified-repo guard trips when the stub
  touches a repo file; refine inherits the repo path; the fake harness
  returns the canned repo pair.
- [x] Implement, run `go test ./internal/localops/`, commit.
  (Adaptation: repo-anchored refines needed the refine prompt to name
  the scratch output directory, so `RefineParams` gained an optional
  `OutputDir` and the template version rose to 3.)

### Task 3: local gateway - repo_path on the scan socket

- Modify: `internal/localgw/envscan.go` (start frame gains `repo_path`,
  required when mode is repo, forwarded to the engine; engine
  path-validation failures surface as error frames, not socket drops),
  `internal/localgw/local.go` or the existing link verb surface (the
  `env.harnesses` result gains a `repo_path` suggestion filled from the
  gateway's linked-repo knowledge when exactly one is known),
  gateway tests, `docs/local-gateway.md` (frame table and verb result).
- Consumes: Task 2's `RepoPath` option.
- [x] Tests first: a repo-mode scan streams end to end against a stub;
  a missing or invalid repo_path yields an error frame and a clean
  close; the harness verb carries the suggestion when the gateway knows
  a linked repo and omits it otherwise.
- [x] Implement, run `go test ./internal/localgw/`, commit.

### Task 4: wizard - the third card

- Modify: `web/src/routes/onboarding/environment-step.tsx`,
  `web/src/lib/types.ts` and `web/src/lib/api.ts` (start frame and verb
  result additions), `web/src/test/fixtures.ts`, onboarding tests,
  `docs/quickstart.md` (one sentence adding the from-repo option to the
  Environment step description).
- Consumes: Task 3's frame and verb shapes.
- The choice becomes three cards: mirror my machine (still recommended
  and preselected), "From the repository" (one plain sentence: the agent
  reads the project's own files and builds what it needs), keep the
  standard environment. The repo card reveals a folder input prefilled
  from the verb's suggestion; submitting starts the scan with mode repo
  and the path, and engine validation errors surface inline with the
  input kept editable. Approval saves with source `repo` and the chosen
  harness. Two targeted fixes from the phase 3 review land here since
  they live in this file: advancing past the Environment step clears
  the review state so Back returns to the choice cards, and a failure
  of the harness-listing verb shows the error with a retry instead of
  also claiming no agent was found.
- [x] Tests first: card rendering and preselection unchanged for
  mirror; repo card reveals and prefills the input; scan start carries
  mode and path; inline validation error path; approve sends source
  `repo`; Back after advancing lands on the cards; the listing-failure
  state shows the error and retry only.
- [x] Implement, run `bun run typecheck` and `bun run test` from
  `web/`, commit.

### Task 5: full check sweep

- [x] Run `make fmt-check`, `make vet`, `make lint`, `make test`,
  `make public-audit`, and from `web/`: `bun run typecheck`,
  `bun run test`; fix anything surfaced and commit the fixes.
  (All checks passed with no code changes needed. `make lint` only runs
  with `GOTOOLCHAIN=go1.25.13 GOFLAGS=-buildvcs=false` on this machine:
  the locally installed Go is an experimental 1.27 build whose export
  data golangci-lint v2.4.0 cannot read, and VCS stamping fails inside
  the sandboxed worktree. Both are environment quirks, not repo issues.)
