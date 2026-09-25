# Remote development handoff

This document is for a fresh session that will implement remote development
on Aether. It is a session brief, not an operational user guide. Do not
advertise any of the planned browser, agent terminal, or import/PR surfaces
as shipped until their slice lands and the matching guide is updated.

Read this file, then `docs/plans/2026-09-25-remote-development.md`. The plan
is the product source of truth. This file tells you what is already on disk,
what is still missing, and how to start without throwing the work away.

Branch: `feat/remote-development` on `origin`. Check it out and continue
from there; do not start from a clean `main`.

## Job

Make Aether a complete remote development environment. A developer and an
agent must be able to implement, run, inspect, interact with, verify, review,
and publish work entirely on Aether. No local project clone or toolchain is
required. Aether's existing server and tailnet remain a prerequisite.

The loop is:

edit -> run -> observe -> interact -> correct -> verify -> review -> push -> PR

Verification is part of relevant implementation work. Agents must use the
affected interface: the built-in browser for web apps, and a rendered
terminal screen (not a log tail) for TUI/terminal work. Humans and agents
share the same terminal or page, not copies of the application.

## Paste this into the new session

```
Implement Aether remote development from handoff.md and
docs/plans/2026-09-25-remote-development.md.

Do not start over. There is substantial uncommitted WIP already in this
workspace. Inventory git status first, then compile, then continue from
the existing packages. The plan is the product contract; the uncommitted
files are an incomplete attempt at that contract.

Locked constraints:
- Reuse the existing run, checkout, run shells, ptyhost, gateway,
  controller fencing, evidence, and aether-internal skill/socket.
- Do not add a second orchestrator, jobs platform, reviewer agent,
  Git engine, terminal emulator, or public preview host.
- Development transport is independent of conflict coordination.
- Agents cannot type into the primary harness PTY or force-take a
  surface from a human.
- Screenshots and live frames are artifact handles / gateway streams,
  never base64 in the 64 KiB JSON-line socket.
- Git and gh stay native. Mirror source, checkout Origin, push remote,
  and PR head/base are separate values.
- Docs stay truthful. Do not claim unimplemented capabilities are live.

Current compile break to fix first:
internal/runtime/docker_managed_exec.go:126 uses e.attachment.done,
but execAttachment in internal/runtime/exectty.go has no done channel.

Start with slice 1 from the plan (development identity and control):
wire a real DevelopmentService, expose only implemented capabilities
through coord.status / aether-internal skill, add aether-internal
terminal/browser/control/artifact commands only for methods that
actually work, and keep mission/conflict policy independent.

Then slices 2 -> 3 -> 4, with slice 5 (import + PR) able to proceed in
parallel with browser work. Slice 6 is the verification/docs closeout.

Read AGENTS.md. Follow existing code style. Keep comments factual.
Run the tests that cover the files you touch.
```

## Source of truth

| Document | Role |
| --- | --- |
| `docs/plans/2026-09-25-remote-development.md` | Product contract, UX, APIs, security, slices, acceptance, exclusions |
| `handoff.md` (this file) | Workspace inventory and start instructions |
| `docs/terminal.md`, `docs/harnesses.md`, `docs/coordination.md`, `docs/environment-home.md`, `docs/local-gateway.md`, `docs/integration.md`, `docs/dashboard-frontend.md` | Current shipped behavior. Update only when a slice actually ships |

Orca (`stablyai/orca`) is inspiration only: native `git push` targeting,
`gh pr create` reconciliation, and screen/browser CLI. Do not copy its
client-side GitHub execution model.

## Locked decisions

Do not reopen these unless the plan is wrong against the current code.

1. Every live run can use the capabilities. Resources start on demand.
   No Chromium or empty terminals until needed.
2. One lazily started Chromium companion per run that needs a browser or
   a terminal screenshot. Playwright + matching Chromium live in an
   Aether-owned, digest-pinned image. User images do not install browsers.
3. The companion shares the run network namespace so `localhost` is the
   run's app. Private host-to-companion Unix/CDP only. No public debug
   port, even on the shared loopback.
4. Terminal observation has three different answers: output stream,
   current xterm-go screen, and on-demand screenshot. A transcript tail
   is not a screen.
5. Development leases are keyed by `(run, surface)`, not run-wide.
   The primary harness keeps today's run-control and mission-hold
   behavior. Agent surface work does not acquire or clear worker holds.
6. `aether-internal skill` advertises only currently available
   capabilities. Socket presence is not feature policy.
7. Headless/worker runs verify before existing cleanup. Interactive runs
   keep today's persistent lifetime. Dashboard close detaches only.
8. Remote import uses the existing mirror configure/refresh/adopt path
   and sets checkout Origin explicitly. Never silently adopt a rewritten
   accepted base.
9. Push/PR UI runs real `git`/`gh` in the run's account environment,
   rechecking membership, Push/account-use, and current branch/HEAD.
   Discover an existing PR before creating. Partial success stays
   visible: a pushed branch with a failed PR create remains pushed.

## Workspace state

This work is **not committed**. A later session must treat the tree as
in-progress implementation, not a clean plan-only checkout.

As of 2026-09-25, `git status` showed:

Modified:

- `cmd/aether-server/main.go` — dispatches `dev-exec` before the
  `aether-internal` basename path
- `internal/control/lease.go` — existing run-wide lease plus new surface
  helpers
- `internal/coord/{coord,mailbox,socket}.go` and tests — development
  transport, capability composition, `run_id` rejection
- `internal/gitengine/{engine,mirror}.go` and
  `internal/mirror/service.go` — remote import seam
- `internal/harness/harness.go` — discovery instruction already points
  at live capabilities
- `internal/ptyhost/{client,ptyhost,screen,session}.go`
- `internal/scheduler/{coauthors,coordination}.go` and tests
- `internal/server/svc_coord.go` — transport stays up when conflict
  coordination is disabled; **Development is not wired**

Untracked:

- `docs/plans/2026-09-25-remote-development.md`
- `cmd/aether-server/dev_exec.go`
- `images/browser/` — companion image and Node control/session/stream/terminal modules
- `internal/browser/` — Go manager/client
- `internal/control/surface.go` and test
- `internal/coord/development_test.go`
- `internal/devexec/`
- `internal/protocol/{development,runrepo}.go`
- `internal/ptyhost/{admission,observation,observation_test,responder}.go`
- `internal/runrepo/`
- `internal/runtime/{docker_managed_exec,managed_exec}.go`
- `internal/gitengine/mirror_import_test.go`
- `internal/mirror/import_test.go`

Not started in this tree:

- `internal/coordcli` has no `terminal`, `browser`, `control`, or
  `artifact` commands
- `web/src` has no browser pane
- no production `DevelopmentService` implementation
- `internal/server/svc_coord.go` still constructs `coord.New` without
  `Development:`
- `internal/runtime` has no `BrowserRuntime` / `CreateBrowser` /
  `InspectBrowser` implementation, though `internal/browser.Manager`
  already calls those methods
- no skill topics, no dashboard PR/import UI, no operational doc updates

## What the WIP already does

### Transport (slice 1, mostly present)

`coord.DevelopmentService` is a narrow interface: `Capabilities` and
`HandleAgent`. Socket dispatch:

- treats the listed `dev.*` methods as development methods
- works even when conflict coordination is disabled
- rejects any caller-supplied `run_id` / `RUN_ID`
- refuses oversized params (`protocol.MaxDevParamsBytes`)
- marshals results with `protocol.MarshalDevResult` (socket budget)
- does not dispatch if `cfg.Development` is nil

`coord.status` composes capabilities:

- always includes `coord.status` for a live run
- appends only methods the development authority reports **and** that
  pass `isDevelopmentMethod`
- then appends mission capabilities when coordination is enabled and
  an assignment exists
- disabled coordination still returns development methods and strips
  mission/mailbox/peers

See `internal/coord/development_test.go`. A mission stub that lists
`dev.browser.open` as a mission capability does **not** get that method
into status unless the development authority also reports it. Keep that
separation.

`internal/protocol/development.go` already defines the full typed
surface: terminal list/start/output/screen/screenshot/input/resize/wait/stop,
browser status/open/pages/navigate/snapshot/action/screenshot/viewport/wait/console/network/reset/close,
control status/acquire/release, artifact list/get/delete.

`DecodeDevAgentParams` refuses a caller-supplied run identity.
Human entry points use `DevRunParams.RunID` after their own auth.

`internal/harness/harness.go` `DiscoveryInstruction` is already:

    Use `aether-internal skill` to read this run's live identity,
    capabilities, and any assignment before acting. Use only available
    capabilities and report only what you verified.

That change is local and uncommitted. Do not revert it. Do not expand
the static instruction into a catalog of methods; live `skill` output
is where capabilities belong.

### Control (slice 1, package-level)

`internal/control/surface.go` keys leases by `(run, surface)` with
`Principal` (`member` vs `run_agent`). Agents cannot force-take
(`ErrAgentTakeover`). Surfaces are `terminal` and `browser`, identified
by ID + incarnation. The primary harness is deliberately not a
development surface.

Existing run-wide lease code in `lease.go` must keep working for the
primary PTY. Do not collapse the two models.

### Owned terminals (slice 2, packages only)

`internal/runtime.ManagedExecRuntime` starts/recovers/stops a command
independently of a dashboard stream. Creation keys are single-use.
`Detach` closes the stream only. `Stop` signals the process group.

`cmd/aether-server/dev_exec.go` plus `internal/devexec` is the
in-container helper. The staged binary is invoked as
`aether-internal dev-exec run|control ...`. User images do not need
extra helpers.

`internal/ptyhost` now has:

- current-screen observation (`ObserveSession`) with cells, cursor,
  modes, alternate buffer, VT snapshot, generation/incarnation
- output observation with a position cursor
- session admission so takeover stays fenced through the physical write

This is observation machinery. Agents still have no CLI or scheduler
authority that lists, starts, or drives development terminals.

### Browser (slice 3, packages only)

`images/browser/` is a non-root Playwright 1.63 / xterm 6 companion.
Unix control, no TCP debug port. Chromium sandbox stays on.

`internal/browser` journals creation intent before create, and treats a
dead socket as unavailable rather than permission to restart a lost
cookie/page session.

These packages are not wired into the scheduler, runtime create path,
gateway, or dashboard.

### Git / import (slice 5, packages only)

`internal/protocol/runrepo.go` defines human-authenticated
`workspace.import` and `run.git.*` / `run.pr.*` methods. Agents keep
using native `git`/`gh`. `run_id` on those methods must never pass
through the run-socket dispatcher.

`internal/runrepo` runs `git`/`gh` in an already authorized live run.
It does not resolve a run ID or infer a writable remote. It rechecks
authorization before every execution, including the final mutation, and
returns real stdout/stderr on failure. Stale branch/HEAD is
`runrepo.ErrStale`.

Mirror import tests exist. There is still no administrator import route
or changes-view commit/push/PR UI.

## Compile status (verified 2026-09-25)

The tree does **not** build.

    internal/runtime/docker_managed_exec.go:126:22:
    e.attachment.done undefined (type *execAttachment has no field or method done)

`execAttachment` in `internal/runtime/exectty.go` has `cli`, `id`,
`resp`, `stdout`, and `closeOnce`. No `done` channel. That single error
blocks `internal/runtime`, and therefore scheduler, server, coord tests
that import runtime, browser, runrepo, and ptyhost.

`internal/control`, `internal/gitengine`, and `internal/mirror` compiled
in isolation.

After fixing `done` (or replacing that check with the existing close
signal), expect the next break: `internal/browser.Manager` calls
`runtime.BrowserRuntime` (`CreateBrowser`, `InspectBrowser`,
`BrowserSpec`) which does not exist on `internal/runtime` yet.

Do not "simplify away" managed exec or the companion to make the tree
build. Finish the missing seams.

## Implementation order

Follow the plan's slices. Intermediate slices are not the full product.

| Slice | Status in this tree | Next work |
| --- | --- | --- |
| 1. Development identity and control | Transport + types + surface leases + discovery wording. No production authority, no CLI | Fix compile. Implement and wire `DevelopmentService`. Add CLI only for live methods. Keep capabilities honest |
| 2. Observable terminals | Observation + managed-exec packages | Shared authoritative tab list, start/input/resize/wait/stop, dashboard dock shows agent terminals, process-group stop |
| 3. Browser runtime and captures | Image + manager + Node modules | `BrowserRuntime` on Docker, scheduler lifecycle, private socket, loopback app, terminal PNG via companion |
| 4. Interactive browser UI | Nothing | Authenticated screencast, same control service, reconnect, phone viewport |
| 5. Remote import and PR | Protocol + runrepo + mirror tests | Admin import route, changes-view commit/push/PR, existing-PR discovery, partial-success UX |
| 6. Complete verification loop | Nothing | Conditional skill topics, evidence attachments, lifecycle/revocation, truthful guide updates |

Slices 2 and 3 depend on slice 1's identity/control contract, then can
proceed independently with one integration owner. Slice 4 needs slice 3.
Slice 5 does not need the browser. Terminal screenshots belong to slice
3, not as a claim that slice 2 already has them.

## First concrete work

1. `git status` and read the uncommitted files above. Do not recreate
   packages that already exist.
2. Fix `execAttachment` / `Attachment()` so managed exec compiles
   without inventing a second attachment type.
3. Add the missing `BrowserRuntime` seam if you touch the browser
   package, or keep browser files compiling-but-unwired until slice 3.
   Do not leave `go test ./...` broken.
4. Implement a scheduler/server `DevelopmentService` that:
   - reports only methods whose handlers exist
   - binds identity from the socket
   - uses surface leases for mutations
   - never accepts a caller-chosen `run_id`
5. Wire it in `internal/server/svc_coord.go` as `Development: ...`.
6. Extend `aether-internal` the same way `task`/`integration` work:
   thin adapters, no identity flags, JSON envelopes except `skill`.
7. Teach `skill` to describe available development methods from
   `coord.status`, plus verification guidance, without listing dead
   commands.
8. Prove slice 1 with tests: coordination disabled still discovers
   real development methods; a mission worker sees both sets; a
   finished run does not; identity override is rejected; human
   primary-PTY control does not block a different test surface.

Only then start slice 2 against a real TUI.

## Constraints that bite

- The 64 KiB JSON-line run socket cannot carry screenshots or video.
  Return artifact handles and use gateway streams.
- Give the run read-only access to its own capture directory outside
  the checkout. No caller-selected host paths.
- Do not type into the agent's primary PTY from development commands.
- Do not silently restart an app command or browser session after
  retain/relaunch. Restore only what can be proved; say what was lost.
- Do not treat HTTP 200, a successful build, or DOM silence as
  application success.
- Do not attach screenshots to public PRs automatically.
- Do not mount the Docker socket, add Compose, or create a per-project
  image framework for this work.
- Custom harnesses are not a supported discovery target. The five
  shipped harnesses are.

## Peer overlap

Two other runs have spoken to this work:

- `01m37hqtnhjxv1k0eg4y350bed` said PR #235 does not touch
  `internal/harness/harness.go`. This tree **does** change that file
  (discovery instruction only). If they later edit it, coordinate.
- `01m37gjbgj77grxxqj80jyansn` said terminal-history work merged in
  PR #219. This tree has uncommitted `internal/ptyhost` observation
  and admission work on top of that area. Rebase/reconcile before
  expanding ptyhost further.

Before editing those files, check `aether-internal inbox` and send a
short overlap note if another run is in them.

## Docs and release

Update guides only with the slice that makes the behavior real:

- harnesses, coordination, terminal, gateway, dashboard, security,
  privacy, quickstart, testing
- browser packaging also updates install and notices

Keep the plan's status line accurate until the product exists.
`make public-audit` must still pass: no secrets, no private planning
in user-facing docs.

Full gate from the plan: `make fmt-check`, `make vet`, `make lint`,
`make test`, `make test-scripts`, `make public-audit`,
`make test-integration`, `make test-e2e`, plus dashboard typecheck
where affected. Per-slice, run the tests for the packages you touch
and any real Docker/browser proof the slice claims.

## Acceptance reminder

The product is done when these pass on a hosted dashboard with no
local clone:

- Web: implement sign-in, run the real app, test valid/invalid
  credentials and sign-out, desktop and phone widths, HMR, reconnect,
  then publish a PR. Check authenticated application state, not only
  a screenshot.
- TUI: real alternate-screen app, arrows/Tab/Enter/Esc, typed input,
  selection/cursor, resize/redraw, screen image, Unicode widths,
  including with no human viewer.
- Shared control, lifecycle, Git/PR, and the five shipped harness
  discovery paths as written in the plan section 10.

## Do not

- Delete or rewrite the uncommitted packages to start from a blank
  design.
- Advertise `aether-internal browser` or a dashboard Browser pane
  before they work.
- Add a mission, reviewer agent, verification score, or workflow
  engine.
- Force-push, auto-merge, or silently switch branches.
- Copy Orca's local-GitHub assumption.
- Expand this handoff into user documentation.
