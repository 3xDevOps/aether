# Mirror Path Implementation Plan (Phase 3)

> **For agentic workers:** implement this plan task-by-task, one worker per
> task with review between tasks. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** The onboarding wizard can launch the user's preferred coding
agent on their own machine, inventory the local toolchains, show a
reviewable summary, and turn an approved inventory into a verified
workspace image - built in the background while onboarding continues.

**Architecture:** Two new harness profiles (pi, amp) and a
setup-capable subset; a versioned embedded prompt template; a local
inventory engine beside the existing local ops that runs the chosen
harness headless in a scratch directory and validates its output through
`internal/envdef`; a local-gateway WebSocket endpoint streaming the run;
a new wizard Environment step with the review gate, driving the phase 1
`env.save`/`env.build` methods and following the `environment.build`
event feed while the wizard moves on.

**Tech stack:** Go (harness registry, local gateway, engine), React +
Vitest in `web/`, the phase 1 wire methods and event feed.

**Spec:** `docs/plans/2026-08-27-env-onboarding-design.md` (Local
inventory verb, Onboarding flow, Post-build verification sections).

## Global constraints

- No file grows past ~1000 lines; errors returned with context; docs
  updated in the same task as the behavior they describe; no speculative
  configuration; no em dash characters anywhere; TDD throughout;
  conventional commits with no co-author trailers.
- The inventory agent never receives credentials from aether and the
  prompt forbids reading credential stores; the scratch directory is
  removed after every run, success or failure.
- Environment-setup surfaces offer exactly the setup-capable harnesses:
  claude, codex, pi, amp. opencode and custom stay launchable for runs
  but are never offered for environment setup.
- The wizard must never dead-end: every failure path offers "keep the
  standard environment", which simply advances the step.

## Non-goals (later phases)

- No from-repo path (phase 4): the Environment step ships with two
  choices - mirror my machine, and keep the current (standard) image.
- No Environment page and no server-side edit agent (phase 5).
- No changes to the phase 1 build, verification, or retention logic.

## Design notes

The engine runs on the user's machine, so it lives with the existing
client-side code and is reached through the local gateway, mirroring how
`image.scaffold` and the shell WebSocket already work. One WebSocket
endpoint carries the whole scan: the dashboard opens it with the chosen
harness and mode, receives progress frames (the raw agent output for the
"view process" expander plus coarse status frames), and a final frame
carrying either the Dockerfile + manifest pair or a failure with detail.
"Request changes" and post-verification repair are the same operation: a
refine run that reopens the endpoint with the previous pair and feedback
text.

Harness invocation reuses the shipped profiles' headless argv templates
with the rendered prompt as the task, executed directly on the host (no
container) in a scratch directory, with a hard timeout. The agent is
instructed to write `Dockerfile` and `manifest.json` into its working
directory; the engine validates both through `envdef` and retries once
with the validation error appended before reporting failure.

Vendor flags for pi and amp were researched but their docs are thin;
the harness task verifies exact argv against each CLI's own help output
before committing, and the values below are the starting point.

## Tasks

### Task 1: harness - pi and amp profiles, setup-capable subset

- Modify: `internal/harness/harness.go` (registry entries),
  `internal/harness/harness_test.go`, `docs/harnesses.md` (shipped
  table, setup-capable column, install notes).
- Produces: registry profiles `pi` and `amp`, and
  `SetupHarnesses() []Profile` returning exactly claude, codex, pi, amp
  in that order - later tasks and the dashboard treat this list as the
  authority.
- Starting values, each to be verified against the vendor CLI's help
  before committing (install both via npm in a scratch prefix to check):
  pi - executable `pi`, TUI `pi {task}`, headless `pi -p {task}`, env
  passthrough `ANTHROPIC_API_KEY` and `OPENAI_API_KEY`, credential path
  and local root `.pi`, install script following the codex npm pattern
  with package `@earendil-works/pi-coding-agent` (add
  `--ignore-scripts` per the vendor's own instruction). amp - executable
  `amp`, TUI `amp {task}`, headless `amp -x {task}`, env passthrough
  `AMP_API_KEY`, credential paths `.config/amp` and `.local/share/amp`
  (the opencode precedent covers the tool-mount overlap), local root
  `.config/amp`, deny names for its token files, install script with
  package `@sourcegraph/amp`. Record any correction from the
  verification in the commit body.
- [ ] Tests first: both profiles validate under the registry's existing
  invariants; `SetupHarnesses` returns the exact four and excludes
  opencode, custom, fake.
- [ ] Implement, run `go test ./internal/harness/`, commit.

### Task 2: envprompt - the versioned inventory prompt

- Create: `internal/envprompt/envprompt.go`, an embedded template file
  beside it, `internal/envprompt/envprompt_test.go`.
- Produces: `Version` (an integer bumped on any template change),
  `RenderInventory(params)` and `RenderRefine(params)` returning the
  full prompt string. Inventory params: target base image name. Refine
  params: previous Dockerfile, previous manifest JSON, feedback text.
- The template must instruct, in this order: inventory scope is
  toolchains only (language runtimes with exact versions, version
  managers, dev CLIs, system libraries the stack needs - never dotfiles,
  shell theming, credentials, or personal files, and never read
  credential stores); translate findings to ubuntu 24.04 equivalents
  rather than copying (homebrew and darwin artifacts must become apt or
  official-installer forms); write exactly two files into the current
  directory - `Dockerfile` (single stage from the given base, pinned
  versions, no COPY or ADD, no secrets, layers ordered stable-first for
  cache-friendly rebuilds) and `manifest.json` (array of items with
  name, version, reason, dockerfile_lines span, check_command whose
  output must contain the version); keep the item count honest - only
  what the machine actually uses.
- [x] Tests first: rendered inventory prompt contains each contract
  clause (assert on distinctive phrases, not the whole string); refine
  prompt embeds the previous pair and feedback verbatim; `Version`
  changes are caught by a golden assertion so a template edit without a
  bump fails.
- [x] Implement, run `go test ./internal/envprompt/`, commit.

### Task 3: envscan - the local inventory engine

- Create: `internal/localops/envscan.go`,
  `internal/localops/envscan_test.go` (the package already holds the
  client-machine operations; split an `envscan_run.go` if the file
  nears the size limit).
- Consumes: `harness.SetupHarnesses` and `harness.Argv` (Task 1),
  `envprompt` (Task 2), `envdef.ParseManifest` and
  `envdef.ValidateDockerfile` (phase 1).
- Produces: `DetectHarnesses()` reporting, for each setup-capable
  harness, whether its executable is on PATH (plain `exec.LookPath`,
  no agent); and `RunScan(ctx, opts, progress)` where opts carry the
  harness name, mode (inventory or refine with previous pair and
  feedback), and an optional argv override used by tests and the fake
  harness; progress is a callback receiving raw output lines and coarse
  status changes. RunScan creates a scratch directory, runs the harness
  headless with the rendered prompt, enforces a configurable timeout
  (default ten minutes), validates the two output files, retries once
  with the validation error appended to the prompt, always removes the
  scratch directory, and returns the validated Dockerfile and manifest
  or a failure carrying the last output tail for diagnosis.
- The shipped `fake` harness gains a canned inventory: when the chosen
  harness is `fake`, RunScan writes a small fixed Dockerfile and
  manifest (one apt item with a real check command) instead of
  executing anything, so demos and tests exercise the full flow without
  vendor CLIs.
- [ ] Tests first: detection reflects PATH contents (temp dir with stub
  executables); a stub script that writes a valid pair succeeds; a stub
  writing an invalid pair triggers exactly one retry with the error in
  its second prompt, then failure; timeout kills the process and
  reports it; the scratch directory is gone in every outcome; the fake
  harness returns the canned pair.
- [ ] Implement, run `go test ./internal/localops/`, commit.

### Task 4: local gateway - the scan endpoint

- Modify: `internal/localgw/local.go` (new verb `env.harnesses`
  returning Task 3's detection list), new `internal/localgw/envscan.go`
  (WebSocket endpoint `/ws/envscan` following the framing conventions
  of `internal/localgw/shell.go`), gateway tests beside the existing
  ones, `docs/local-gateway.md` (verb table and WebSocket section).
- Consumes: `localops.DetectHarnesses` and `localops.RunScan` (Task 3).
- Produces, for the dashboard: the client opens `/ws/envscan` with a
  JSON start frame (harness, mode, previous pair and feedback for
  refine); receives `output` frames (raw lines), `status` frames
  (detecting, running, validating, retrying), and one terminal frame -
  `result` with the Dockerfile and manifest, or `error` with detail and
  output tail. Closing the socket cancels the scan and its process.
- [x] Tests first: verb returns the detection list; the endpoint
  streams a stub scan end to end (reuse Task 3's stub-executable
  pattern through the argv override); early close cancels; a second
  concurrent scan on the same gateway is refused with a clear error
  (one scan at a time is plenty for onboarding).
- [x] Implement, run `go test ./internal/localgw/`, commit.

### Task 5: web - api surface and the manifest editing helper

- Modify: `web/src/lib/types.ts` (EnvironmentVersion, ManifestItem,
  scan frame types), `web/src/lib/api.ts` (`envStatus`, `envSave`,
  `envBuild` calls; `envHarnesses` local verb; an `openEnvScan` helper
  wrapping the `/ws/envscan` socket the way the shell client wraps
  its socket), `web/src/test/fixtures.ts` (fakeApi gains the new
  functions).
- Create: `web/src/lib/manifest.ts` with a colocated test - a pure
  `removeManifestItem(dockerfile, items, name)` returning the new
  Dockerfile text and items with line spans shifted, used by the review
  gate's per-item remove toggle. Mirror `internal/envdef`'s span
  semantics exactly (1-indexed, inclusive).
- Produces: the typed client surface Tasks 6 and 7 consume; names above
  are the contract.
- [x] Tests first: removeManifestItem drops the item's lines and shifts
  later spans correctly across adjacent and non-adjacent items;
  removing the last remaining item is refused (the review gate offers
  the standard fallback instead of an empty Dockerfile); api additions
  are exercised through the fixture fakes.
- [x] Implement, run `bun run typecheck` and `bun run test` from
  `web/`, commit.

### Task 6: wizard - the Environment step and the scan flow

- Modify: `web/src/routes/onboarding/index.tsx` (insert `Environment`
  into the steps tuple after Workspace), `web/src/routes/onboarding/`
  (a new `environment-step.tsx` beside `steps.tsx` - do not grow
  steps.tsx past the size limit), onboarding tests,
  `docs/dashboard-frontend.md` (route inventory).
- Consumes: Task 5's client surface; `useIsAdmin` and the capability
  gates the existing steps use.
- The step renders two cards: "Mirror my machine" (recommended,
  preselected) and "Keep the standard environment" (one sentence: the
  workspace already has a ready-to-use environment; this just advances).
  Mirror flow: harness picker listing only detected setup-capable
  harnesses by friendly name (from `envHarnesses`; when none are
  detected the card explains none were found, names the four supported
  CLIs, and offers the keep card as the way on); a run screen with a
  one-line status, a collapsed "View process" expander streaming output
  frames into a scrollable monospace pane, and a cancel button; scan
  errors show the detail with "try again" and "keep the standard
  environment". A non-admin member sees the keep card only, mirroring
  the existing workspace-step gate. Copy is plain language throughout -
  no registry refs, no jargon on cards.
- [ ] Tests first (fake scan socket in fixtures): step order and
  breadcrumb; preselection; no-harness degradation; scan success hands
  the pair to the review gate (Task 7's component boundary - assert the
  callback payload); cancel and error paths land on the fallback
  offer; non-admin sees only the keep card.
- [ ] Implement, run `bun run typecheck` and `bun run test`, commit.

### Task 7: wizard - review gate, background build, banners

- Modify: `web/src/routes/onboarding/environment-step.tsx` (the review
  and build states), `web/src/routes/onboarding/steps.tsx` (First-run
  step banner), `web/src/routes/run/` (the run view banner),
  onboarding and run tests, `docs/quickstart.md` (the flow narrative
  now includes the environment step), `docs/dashboard-frontend.md` if a
  store slice is added.
- Consumes: Task 5's `envSave`/`envBuild`/`envStatus` and manifest
  helper; the events WebSocket the dashboard already holds
  (`environment.build` payloads: version, status, line, detail).
- Review gate: the manifest rendered as a readable list (name, version,
  plain-language reason), each row with a remove toggle backed by
  `removeManifestItem`; a free-text "request changes" box that reopens
  the scan in refine mode with the note; approve calls `envSave` (source
  `mirror`, the chosen harness) then `envBuild`, subscribing to events
  before the build call so no frame is missed (the CLI's follow logic is
  the reference), and immediately advances the wizard. Build state lives
  in a small store slice so later steps can read it.
- Banners: while the latest build for the active workspace is pending,
  the First-run step and the run view show "your environment is still
  building, using the starter image for now"; on success the banner
  clears; on verification failure the wizard (or, post-onboarding, the
  banner) offers "ask the agent to fix it" - a refine scan seeded with
  the failure detail, feeding back into the same review gate - plus
  "keep the standard environment".
- [ ] Tests first: per-item removal updates the summary and the payload
  sent to envSave; request-changes reopens the socket in refine mode
  with the note; approve issues save then build and advances while
  status events drive the slice; the banner appears on building and
  clears on active; verification failure surfaces both offers and the
  repair path reopens the scan with the failure detail.
- [ ] Implement, run `bun run typecheck` and `bun run test`, commit.

### Task 8: full check sweep

- [ ] Run `make fmt-check`, `make vet`, `make lint`, `make test`,
  `make public-audit`, and from `web/`: `bun run typecheck`,
  `bun run test`; fix anything surfaced and commit the fixes.
