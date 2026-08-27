# Agent-built workspace environments

Design for the first-run environment flow: a member's preferred coding agent
inventories their local machine (or the repository) and produces a docker
image the server builds and attaches to the workspace, plus an agent-driven
way to edit that environment later. Dashboard first, CLI second.

## Problem

Today the server only pulls prebuilt images. Customizing the remote
environment means either handing a registry reference to `workspace init`
(admin, out-of-band build) or installing user-local tools into a `~/.local`
snapshot, which cannot cover system packages or runtime versions. New users
get a near-empty container and no guided way to make the remote environment
match the machine they already work on.

## Decisions

Settled during design review:

- The agent's output is a real docker image built by the server, not a
  setup script.
- Onboarding offers three environment paths, with "mirror my machine"
  recommended and preselected: mirror, from-repo, standard prebuilt image.
- The image is per-workspace. Whoever creates the workspace runs the flow;
  later members use the result. Personal tools still arrive via the
  existing tool snapshot.
- Mirror depth is toolchains only: language runtimes at the user's
  versions, version managers, dev CLIs, and system libraries the stack
  needs. No dotfiles, no shell theming, never credentials.
- Agent interaction is headless with a review gate. The wizard shows a
  progress line with a collapsed "view process" expander streaming raw
  agent output. Nothing builds without explicit approval.
- Environment setup flows support exactly four harnesses: claude,
  codex, pi, amp. opencode and custom remain available for running
  agents but are not offered for environment setup.
- Later environment edits run server-side and are admin-only, matching
  the existing "images are administrator-approved" line.
- Every built image is verified against its manifest before it is
  considered good.
- Onboarding does not block on the build; it continues on the neutral
  image and hot-swaps when the build succeeds.

## Architecture

### Environment definition

New per-workspace record in the server store, the single source of truth
for the workspace environment:

- Dockerfile text.
- Manifest: a list of items, each with name, version, reason, and the
  Dockerfile lines it maps to. This is what the dashboard renders as the
  human-readable environment summary, and what post-build verification
  checks.
- Provenance: which path produced it (mirror, repo, standard, manual),
  which harness wrote it, when.
- Version counter. Previous versions are retained for rollback.

### Server-side image build

The server builds with the same docker daemon it already uses to run
containers. No registry, no push.

- New runtime verb on the `Runtime` interface for building an image from a
  Dockerfile context, streaming build output.
- Built images are tagged locally per workspace and definition version
  (for example `aether/ws-<name>:<version>`). The runtime's pull step
  treats these tags as local-only and never contacts a registry for them.
- Retention: keep the current and last-good tags per workspace; older
  version tags are removed.
- Builds run with no secrets and no access to server data beyond the
  build context. A Dockerfile build is arbitrary code on the server's
  docker daemon; that is why every build-triggering call is admin-guarded.
  Document the boundary instead of pretending to sandbox it.

### Post-build verification

After a successful build, the server boots a throwaway container from the
new image and checks each manifest item's version claim. On mismatch, the
failure detail is fed back for one automatic repair round through
whichever agent flow produced the definition (the local inventory run
during onboarding, the server-side edit agent later), then surfaced to
the user. Definitions with no agent behind them (standard, manual) skip
repair and surface the mismatch directly. Only a verified image becomes the workspace
image.

### Wire protocol

Three new control-channel methods, all admin-guarded: save an environment
definition, trigger a build, and read build status. Build progress and
completion ride the existing event stream; the dashboard streams the build
log the same way the agents wizard streams shell output. Setting the
workspace image to a newly verified tag becomes the sanctioned way the
image changes after creation (today it is frozen).

### Local inventory verb

One new local-gateway verb: run an environment inventory with a chosen
harness. The gateway:

- Detects which of the four supported harnesses are installed locally
  (plain code, not an agent) and reports login state.
- Spawns the chosen harness headless in a scratch directory with a fixed,
  versioned prompt template shipped in the binary, streaming output to the
  dashboard and enforcing a timeout.
- Validates the output contract: the agent must write exactly a
  `Dockerfile` (ubuntu 24.04 base, pinned versions, no secrets, no
  host-specific paths, layers ordered for cache-friendly rebuilds) and a
  `manifest.json`. Malformed output gets one automatic retry with the
  validation error appended, then a clean failure that offers the standard
  image instead.

The from-repo path uses the same verb and contract with a prompt aimed at
the repository contents and any devcontainer config instead of the machine.

### Onboarding flow

New step in the existing dashboard wizard between Workspace and
Repository:

1. Choose a path: mirror my machine (recommended), from the repo, or the
   standard environment.
2. For agent paths: pick a harness from those detected locally.
3. Agent runs; progress line plus "view process" expander.
4. Review gate: manifest rendered as a summary list, per-item remove
   toggle (dropping an item drops its mapped Dockerfile lines), "request
   changes" re-runs the agent with the user's note, approve ships the
   definition to the server.
5. Build starts in the background; the wizard continues to Repository and
   First run on the neutral image and the workspace hot-swaps to the built
   image once verified. Every step offers "skip, use the standard
   environment" so onboarding never dead-ends.

### Environment page and edit agent

The workspace view gains an Environment section: the manifest as a
readable list, current version, build history, rollback. An admin submits
a change request in plain language; the server launches the workspace's
registered harness in a container (reusing the existing launch machinery)
with the current definition and the request, under the same output
contract. The dashboard shows a Dockerfile diff plus the updated summary;
approval triggers build, verification, and swap.

### Standard image

CI builds and publishes `images/standard/Dockerfile` alongside the
existing bootstrap image: fnm with node LTS, go, python with uv, rust,
build-essential, git, ripgrep, jq. Versioned with releases. It is the
zero-wait option and the universal fallback.

### Harness registry

Add pi and amp profiles to the shipped registry (argv templates, env
passthrough, credential paths, install script), following the existing
profile shape. Environment setup surfaces filter to claude, codex, pi,
amp.

### CLI

Second-class by design: `aether env show`, `aether env rebuild`,
`aether env rollback`. Agent-driven flows are dashboard-only in v1.

## Phasing

Five independently shippable slices, docs updated per slice:

1. Environment definition storage, server build verb, wire methods,
   post-build verification, image swap, retention.
2. Standard image in CI plus the wizard step offering it.
3. Local inventory verb, prompt template, review gate, background build
   with hot-swap: the mirror path end to end.
4. The from-repo path.
5. Environment page and the server-side edit agent.

## Testing

- Unit coverage for the output-contract validator, manifest parsing and
  line mapping, tag retention, and harness detection.
- Integration (Docker + real git): build a small definition end to end,
  verify against its manifest, swap the workspace image, roll back.
- Dashboard: wizard step and environment page tests under the existing
  Vitest setup; a fake harness drives the review-gate states.
- The `fake` harness gains a canned inventory mode so the full flow runs
  in demos and integration tests without vendor CLIs.

## Docs to update

`quickstart.md` (the flow changes shape), `bootstrap.md` (the image
escape hatch becomes a built-in), `harnesses.md` (pi, amp, setup-capable
set), `install.md` (image storage and retention), `dashboard-frontend.md`
(new routes and slices), `local-gateway.md` (new verb).

## Deferred

Tracked for later, not in v1:

- Devcontainer export of the definition so an aether environment is
  portable to other tools. Priority: sooner.
- Drift check: re-run the inventory and diff against the manifest
  ("your laptop moved to go 1.24, remote is on 1.23"). Priority:
  long-term.
