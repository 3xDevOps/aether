# Standard Image Implementation Plan (Phase 2)

> **For agentic workers:** implement this plan task-by-task, one worker per
> task with review between tasks. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** A prebuilt "standard environment" image published with each
release, offered as the recommended default when a workspace is created -
in the dashboard wizard, the workspaces view, and the CLI.

**Architecture:** `images/standard/Dockerfile` published by the release
workflow exactly like the bootstrap image; the server learns its pinned
default ref the same way it knows the neutral image and reports both refs
through `server.info`; the two duplicated workspace-creation forms in the
dashboard collapse into one shared environment-choice component that
defaults to the standard image; `aether workspace init` gains a
`--standard` flag. No new wire methods: the choice rides the existing
`workspace.add` environment field as a pinned `custom_image` ref.

**Tech stack:** Docker/GitHub Actions (release workflow), Go, React +
Vitest in `web/`.

**Spec:** `docs/plans/2026-08-27-env-onboarding-design.md` (Standard
image section, including the creation-time-choice amendment).

## Global constraints

- No file grows past ~1000 lines; errors returned with context; docs
  updated in the same task as the behavior they describe; no speculative
  configuration; no em dash characters anywhere.
- Toolchain versions in the standard image are pinned explicitly; nothing
  installs "latest".
- The workspace records the pinned ref, never a "standard" intent flag:
  `domain.WorkspaceEnvironment` is unchanged.
- TDD throughout; each task commits with a conventional message and no
  co-author trailers.

## Non-goals (later phases)

- No environment wizard step, no agent flows, no environment definitions
  with source `standard` - choosing standard is just workspace creation
  with a known-good ref.
- No changes to the neutral image or to `EffectiveImage` resolution.

## Design notes

The dashboard currently renders the same name/base-branch/custom-image
creation form twice (`web/src/routes/onboarding/steps.tsx` WorkspaceStep
and `web/src/routes/workspaces/index.tsx` AddForm), each shaping the
environment object by hand. Both move to one shared component so the
standard option exists exactly once.

Degradation: a dashboard talking to an older server whose `server.info`
lacks the image refs hides the standard card and defaults to the minimal
starter, which is today's behavior.

## Tasks

Task 1 and Task 2 are independent; Tasks 3 and 4 both depend only on
Task 2. Docs land inside the task that changes the behavior they
describe.

### Task 1: the standard image and its release publish

- Create: `images/standard/Dockerfile`.
- Modify: `.github/workflows/release.yml`.
- The Dockerfile starts `FROM ubuntu:24.04` and follows the bootstrap
  Dockerfile's conventions (noninteractive frontend, one apt layer with
  cleaned lists, `/workspace` workdir, bash CMD). Contents: the bootstrap
  apt set plus `build-essential`, `pkg-config`, `unzip`, `ripgrep`,
  `jq`, `python3`, `python3-venv`; then pinned toolchains installed
  system-wide so any container uid can run them: go from the official
  tarball into `/usr/local/go` with a `/usr/local/bin` symlink, node LTS
  via fnm with a shared `FNM_DIR` and `/usr/local/bin` shims, uv's
  standalone binary, and rust via rustup with shared `RUSTUP_HOME` and
  `CARGO_HOME` and `/usr/local/bin` symlinks. Every pinned version is a
  build arg defaulted at the top of the file so bumps are one-line diffs.
- Release workflow: mirror the bootstrap image's four steps (metadata,
  build-push, pull-verify) for `ghcr.io/3xdevops/aether-standard`, and
  add a smoke-check step that runs the published image once per tool
  (`go version`, `node --version`, `npm --version`, `python3 --version`,
  `uv --version`, `rustc --version`, `cargo --version`, `rg --version`,
  `jq --version`, `git --version`) - a failing check aborts the release,
  same rationale as the existing pull-verify comment.
- [ ] Verify locally: `docker build images/standard` succeeds and each
  smoke-check command runs in the built image (document the one-off
  command in the commit message body, not in the repo).
- [ ] Update `docs/install.md` "Images and containers" with the standard
  image's name, contents summary, and release tagging.
- [ ] Commit.

### Task 2: server default ref and server.info exposure

- Modify: `internal/server/server.go` (a `standardImageRepo` constant and
  `DefaultStandardImage` computed with the same tag-derivation helper the
  neutral default uses - generalize that helper rather than copying it),
  `cmd/aether-server/main.go` (`--standard-image` flag mirroring
  `--neutral-image`, config-file key included), `internal/protocol/wire.go`
  (`ServerInfoResult` gains `neutral_image` and `standard_image` string
  fields), `internal/sshd/handlers.go` (populate both from server config).
- Consumes: the existing neutral-image default plumbing as the pattern.
- Produces: `server.info` reporting both refs; Tasks 3 and 4 read
  `standard_image` from it.
- [ ] Tests first: server config defaulting resolves the standard ref
  like the neutral one (including the non-release version fallback); the
  server-info handler returns both refs; existing tests still pass.
- [ ] Implement, run `go test ./internal/server/ ./internal/sshd/
  ./internal/protocol/`, commit.

### Task 3: dashboard - one environment choice, standard by default

- Create: `web/src/components/environment-choice.tsx` plus a colocated
  test file.
- Modify: `web/src/routes/onboarding/steps.tsx` (WorkspaceStep),
  `web/src/routes/workspaces/index.tsx` (AddForm),
  `web/src/lib/types.ts` (`ServerInfo` gains `neutral_image?` and
  `standard_image?`), `web/src/test/fixtures.ts` (the `serverInfo`
  fixture carries both refs), existing tests in
  `web/src/routes/onboarding/onboarding.test.tsx` and
  `web/src/routes/workspaces/workspaces.test.tsx` (their exact
  `workspaceAdd` payload assertions change deliberately).
- Consumes: `standard_image` from the server store slice
  (`web/src/store/server.ts`, populated during hydration).
- The component renders three radio cards in this order: "Standard
  environment" (recommended, preselected; plain-language one-line
  description of what is inside, with the ref shown small), "Minimal
  starter" (the neutral image), and "Custom image" which reveals the
  existing text input. It emits the protocol environment object
  (`{custom_image: <standard ref>}`, `{neutral_image: true}`, or
  `{custom_image: <typed ref>}`) so both callers stay dumb. When the
  store has no `standard_image`, the standard card is not rendered and
  the starter is preselected. Copy is new-user first: no registry
  jargon on the cards themselves.
- [ ] Tests first: default submit sends the standard ref; each card
  produces its exact payload; the custom input only appears when its
  card is selected; the older-server case hides the card and defaults to
  starter; both routes render the shared component (no duplicated
  form logic remains).
- [ ] Implement, run `bun run typecheck` and `bun run test` from `web/`,
  commit.

### Task 4: CLI --standard and the quickstart

- Modify: `cmd/aether/workspace.go` (`workspace init` and `workspace add`
  gain a `--standard` boolean; mutually exclusive with `--image`, error
  message names both flags; when set, the client fetches `server.info`
  and uses its `standard_image` as the custom image ref, with a clear
  error naming a server upgrade when the field is empty),
  `cmd/aether/image_test.go` and the workspace command tests.
- Consumes: `server.info` fields from Task 2.
- [ ] Tests first: flag exclusivity error; `--standard` resolves the ref
  into the `workspace.add` environment payload; empty `standard_image`
  errors with the upgrade hint.
- [ ] Update `docs/quickstart.md` (workspace creation now leads with the
  standard environment in both dashboard and CLI forms) and the
  vocabulary note in `docs/bootstrap.md` that names the standard image
  beside the neutral one.
- [ ] Implement, run `go test ./cmd/aether/`, commit.

### Task 5: full check sweep

- [ ] Run `make fmt-check`, `make vet`, `make lint`, `make test`,
  `make public-audit`, and from `web/`: `bun run typecheck`,
  `bun run test`; fix anything surfaced and commit the fixes.
