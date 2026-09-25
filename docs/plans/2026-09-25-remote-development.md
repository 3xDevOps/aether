# Remote development and agent self-verification

**Status:** Proposed implementation plan. The browser and agent-facing terminal
commands described below are not shipped features. Operational guides remain
the reference for current behavior until their implementation slices land.
Session inventory and start instructions: [handoff.md](../../handoff.md).

**Goal:** A developer can implement, run, inspect, correct, and publish a change
entirely through Aether. An agent can observe and operate the application it
builds, and the developer can inspect the same running application.

A **workspace** is a repository and its shared settings. A **run** is an agent
execution with its own container, checkout, and branch. A **harness** is the
coding-agent CLI launched there. This plan extends ordinary runs; it does not
require a mission or change how mission work is assigned and accepted.

## 1. Product decisions

1. Complete the loop: edit -> run -> observe -> interact -> correct -> verify
   -> review -> push -> PR. Verification is part of relevant implementation
   work, not something the developer must remember to request afterwards.
2. Provide two interactive application surfaces: the existing run terminals
   and a new run browser. Human and agent address the same terminal or page,
   not unrelated copies of the application.
3. Observe the right thing. CLI output, a rendered TUI screen, a browser DOM,
   and browser pixels answer different questions. A successful build or an
   HTTP 200 is not evidence that an interaction or layout works.
4. Reuse the run checkout, shell PTYs, terminal emulator, gateway, controller
   fencing, evidence storage, and `aether-internal` discovery path. Add no
   separate development workspace, jobs platform, reviewer agent, or workflow
   engine. A dev server is a process in a run terminal.
5. Keep Git and `gh` as the publishing tools. Add their missing remote UI and
   PR state, not a replacement Git implementation.
6. Launch resources on demand. Every live run has access to the capabilities;
   every run does not need a running Chromium process or four empty terminals.

The end-to-end requirement is a server-hosted dashboard on a client with no
project checkout, language runtime, development server, or forwarding CLI.
Aether's existing server and tailnet setup remains a prerequisite.

## 2. Existing foundation and actual gaps

| Foundation | What to reuse | What must change |
| --- | --- | --- |
| Run shells | `internal/scheduler/run_shell.go` creates separate PTYs in the run checkout, with a four-tab limit. | Expose run-scoped lifecycle, observation, and input to agents; make server state authoritative for the shared tab list. |
| Rendered terminal state | `internal/ptyhost` maintains an xterm-go screen independently of dashboard viewers. Snapshots contain geometry, serialized VT state, and output position. | Expose keyed shell-screen reads, structured visible cells, and on-demand images. Do not return a transcript tail as a screen. |
| Dashboard terminals | Existing xterm rendering, WebSocket transport, resize ordering, and reconnect handling. | Show agent-created terminals in the same dock and share their control state. |
| Harness guidance | Five shipped harnesses can discover `aether-internal skill`; status reports current authority. | Teach verification, expose available development capabilities, and separate socket availability from conflict coordination. |
| Run lifetime | Interactive runs survive normal harness exit; headless runs are one-shot. | Tie browser and test-terminal cleanup to these existing lifetimes, including worker terminal reports. |
| GitHub | Member-account credentials, signing, checkout Origin, and source mirroring exist. | Finish remote-only import and run-linked commit/push/PR UI. Internal candidate proposals are not GitHub PRs. |
| App networking | Existing forwarding reaches a container port through the developer's machine. | A hosted browser must reach the run without a laptop tunnel or publishing the app port. |

Sources: [terminal](../terminal.md), [harnesses](../harnesses.md),
[coordination](../coordination.md), [environments](../environments.md),
[GitHub connection](../environment-home.md#connect-github),
[gateway](../local-gateway.md), and [candidate integration](../integration.md).

## 3. User experience

### Web application

The developer asks an agent to implement sign-in. The agent inspects the
project's scripts and lockfile, makes the change, and starts the actual dev
command in a named run terminal. `pnpm dev` is an example, not a hard-coded
Aether convention. Required dependencies use the existing environment/setup
mechanisms; Aether does not assume pnpm or a database is already installed.

The agent opens the app in the run browser, exercises valid and invalid
sign-in, checks the resulting page and relevant errors, and corrects failures.
The developer opens **Browser** beside the agent or terminal and can take
control of that same page. Edits reach the app through its normal hot reload.
Closing the dashboard does not stop the dev server or browser.

### Terminal application

The agent starts the program under development in a separate run terminal,
not in its own harness's input terminal. It reads the current screen, sends
real keys, observes selection/focus and the resulting screen, and checks the
program's actual effects. It resizes the PTY when testing layout at another
size and captures an image when appearance matters. The developer can watch
or take control of exactly that terminal.

The terminal transcript remains useful for diagnostics. It is not a substitute
for the active screen when an application redraws lines or uses an alternate
buffer. A plain noninteractive CLI normally needs output, exit status, and
side-effect checks instead of screenshots.

### Dashboard changes

Keep the existing run view, Files, diff, and terminal dock. Add a Browser pane
with an address, navigation controls, page selector for popups, viewport size,
and explicit control ownership. Put branch publishing and PR state with the
existing changes view. Keep these usable in the server dashboard, local
gateway, desktop wrapper, and phone layout; do not make Electron required.

A terminal tab can be hidden/detached without stopping its program. **Stop
process** is a separate explicit action. Browser panes follow the same rule:
hiding the pane is not resetting the browser session.

## 4. Harness skill: conditional, observable verification

The short per-launch instruction points to `aether-internal skill`. The live
skill states the run identity, available capabilities, and this workflow:

> Verify changes using the interface affected by the task. If the work affects
> a web application, use Aether's built-in browser to inspect and exercise the
> changed behavior. For a terminal application, run it in a separate Aether
> terminal and inspect its rendered screen while interacting with it; logs
> alone do not verify a TUI. Use screenshots when visual appearance matters.
> For ordinary CLI, API, library, or documentation work, use the appropriate
> command, request, test, or document check instead; do not launch a browser
> merely to satisfy a ritual. Reproduce reported failures when possible, check
> the changed path and relevant failure cases, correct what you observe, and
> recheck. Report what you actually exercised and what remains unverified.
> Respect human control and do not publish or merge without task authority.

Apply that wording only as its capabilities ship. An unavailable browser must
produce an unavailable capability and the real reason, not instructions that
pretend a working browser exists. An image-incapable model still gets textual
page/screen observations; it must not claim it visually inspected an image.

Keep the default skill short. Proposed `skill terminal`, `skill browser`, and
`skill git` topics load version-matched details on demand. These names are a
proposed CLI design, not commands to use against today's binary. The Git topic
teaches ordinary `git` and `gh`; it does not introduce a generic Git wrapper.

### Discovery and identity

- Provision the existing authenticated per-run socket for development features
  independently of the conflict-coordination switch. Keep one socket identity
  and one explicit method allowlist. Disabling conflict coordination still
  disables its peer/radar policy; it does not disable terminal/browser access.
- Make the existing status bootstrap return identity and development
  capabilities even in that configuration. Combine them with authorized
  coordination/mission capabilities; mission assignment must not replace the
  development capability list or gain authority from it.
- Preserve the five shipped taskless discovery adapters: Claude, Codex, pi,
  omp, and OpenCode. Task-bearing interactive/headless runs receive the hint
  through their existing prompt path. Do not seed a fake task in a taskless
  interactive run, overwrite repository instructions, or edit persistent
  member configuration.
- A task-bearing custom harness can receive the prompt hint. A taskless custom
  executable has no universal system-prompt interface: its deployment-owned
  launch template must supply discovery using that CLI's documented mechanism.
  Show that requirement rather than inventing flags or claiming full coverage.
- Resolve every terminal/browser target beneath the caller's socket-bound run.
  No caller-selected run identity, human gateway token, or host SSH credential
  is required. Identity-less environment/verification containers retain general
  help but do not borrow another run's development authority.

## 5. Terminal contract

Extend scheduler-owned run shells and their existing PTY session keys. Do not
start a second shell subsystem or require the harness to install tmux/expect.
The proposed `aether-internal terminal` family has these operations:

| Operation | Contract |
| --- | --- |
| List/start | List this run's terminals; start a shell or explicit command in the checkout and return its stable handle/incarnation. Show the same handle in the dashboard. Retain the existing tab limit. |
| Output | Bounded raw/text output since a cursor, with truncation and process state explicit. A lost cursor is not silently treated as complete history. |
| Screen | Current visible rows/cells, styling, cursor, active buffer, and geometry at one captured boundary. No human attachment required. |
| Screenshot | PNG of that captured terminal state, with its dimensions and identity. No browser tab on a human device required. |
| Input | Text/paste, named keys and modifiers, and terminal mouse events where the application enables them. Respect bracketed paste and terminal modes. |
| Resize | Change the real PTY size under the terminal's controller lease; observe subsequent redraw before judging the result. Read-only viewers never resize it. |
| Wait | Bounded wait for new output/screen state, expected visible text, or process exit. A timeout returns the observed state, not success. |
| Stop | Explicitly terminate the owned command/process group, report its outcome, and release resources. Closing a transport is only detach. |

### Observation implementation

The current snapshot is replayable VT, not a PNG or a public cell-grid record.
Add a session-keyed observation API inside `internal/ptyhost`, under its existing
screen/output ordering lock. Read visible cells directly from its existing
xterm-go terminal (`Buffer`, line/cell accessors, cursor and active-buffer APIs).
Plain text must not require Chromium. Preserve style information so selection,
focus, and color-only states are not erased from structured observations.

Include session incarnation, output position, geometry, and a screen revision
that also changes on resize/reset. The existing byte sequence alone is not a
screen version: geometry can change without new output. Input is fenced by
session/control generation, not rejected merely because a spinner redrew.

For images, render the captured VT snapshot in an isolated, non-networked xterm
page using the same frontend package and bundled fonts, then capture it with
the browser companion described below. Wait for terminal parsing and fonts.
Record the snapshot revision; do not imply the PNG is a later live frame.
Support the active alternate buffer, Unicode widths, colors, and cursor.
Unsupported terminal graphics must be reported, not rendered as fake evidence.

Screen readers and screenshot renderers are read-only. Ensure exactly one
terminal-protocol responder handles device/cursor/color queries on the live
session, including when no dashboard is attached. Replayed snapshots must
never produce duplicate query replies or input. Centralize that response lane
for development terminals instead of letting every viewer answer independently.

### Process lifecycle is real work

`Runtime.ExecTTY` currently returns an attachment, not an owned-process handle
with exit/termination APIs. Extend that runtime seam for command-backed run
terminals to track execution identity, exit status, and termination. Do not
infer exit from a disconnected stream or rely on closing it to kill children.
This is a bounded extension of run-shell ownership, not a jobs database,
auto-restart policy, or general service supervisor. Ordinary harness shell
commands remain available for short commands that do not need a shared PTY.

## 6. Browser contract and deployment

**Decision:** one lazily started Chromium companion per run that needs browser
access or terminal screenshots. Package Playwright and its matching browser
in an Aether-owned, version/digest-pinned image. Do not require user images to
contain Chromium or install browser packages into a member's saved environment.

The companion shares the run's network namespace, so `http://localhost:3000`
means that run's app, including loopback-only dev servers. It has its own
filesystem/profile and no member HOME, GitHub credentials, Docker socket, or
host-network access. Network sharing is not a claim of stronger tenant or
egress isolation than the run already has.

Use a private host-to-companion Unix connection and Chromium's debugging pipe;
do not bind an unauthenticated CDP/Playwright server even to the shared run
loopback. The run must not be able to bypass human control through a debugging
port. The Go server authorizes typed operations and owns companion lifecycle.

The proposed `aether-internal browser` family supports:

- Open/navigate and list/select/close pages, including authentication popups.
- Read a bounded accessibility/DOM snapshot and capture a screenshot.
- Click, fill, select, press keys, scroll, and set viewport dimensions.
- Wait for a stated page condition; inspect bounded console errors and failed
  network requests. DOM/network silence is not an application-success check.
- Reset the run's test browser session explicitly. Reset creates a clean
  context rather than attempting an incomplete cookie-only cleanup.

Element references belong to their page/session and become invalid after
replacement/navigation or detachment. Return a stale-target error and request
another snapshot; never redirect an action to the newly selected page.

The dashboard receives Chromium screencast frames over an authenticated,
backpressured WebSocket and sends pointer/keyboard/text input through the same
control service as the CLI. Stream only while viewed, bound frame size/rate,
and discard obsolete queued frames rather than accumulate them. Use a frame's
viewport identity when interpreting input. Support touch and text composition;
a stream that only displays screenshots is not an interactive browser.

This is not an iframe proxy. App JavaScript, cookies, redirects, WebSockets,
service workers, and HMR execute in Chromium against the app's actual address.
No wildcard DNS, public preview links, rewritten application paths, VNC desktop,
or laptop tunnel is required. Local port forwarding stays available as an
optional existing tool; it is not a dependency of this workflow.

Run Chromium as a non-root user with its sandbox enabled, bounded CPU/memory,
and dedicated shared-memory capacity. Do not copy an unsafe test-image launch
with host IPC, extra host privileges, or `--no-sandbox`. Unsupported sandbox
setup reports the exact failure. Resource admission must account for companion
usage, not silently bypass the run/server limits.

Ordinary test logins and redirects are in scope. Hardware-bound authentication
or an identity provider rejecting an automated browser may require human action;
show that limitation without bypassing authentication or claiming verification.
Use test accounts and do not import the developer's everyday browser profile.

Implementation references: [Playwright containers](https://playwright.dev/docs/docker),
[context isolation](https://playwright.dev/docs/browser-contexts), and
[Docker network sharing](https://docs.docker.com/engine/network/#container-networks).
Playwright's default development image is not itself a hardened deployment.

## 7. Shared control, security, and lifetime

### Control scope

The agent's primary PTY and a test terminal are different targets. Today shell
and harness input contend for a run-wide lease. Extend `internal/control` to
key development leases by `(run, surface)`, retaining its session/generation
fencing. Migrate all run-shell dashboard/API callers to this same contract;
there must not be a second writer path bypassing it.

- The primary harness keeps its existing run-control and mission-hold behavior.
  Agent development commands cannot type into that PTY or call run steering.
- Each development terminal/browser has one controller. Agents are authenticated
  run principals, not fabricated human member sessions. They may acquire an
  unoccupied surface but cannot force-take it from a person.
- A human explicitly takes control of a surface; this fences stale agent input.
  Observation can continue. Returning control is explicit; viewing the primary
  agent terminal does not seize its browser or test terminal.
- Preserve the existing durable worker hold when a human takes writable run-shell
  control. Agent surface operations and browser takeover do not acquire or clear
  mission holds. Releasing a development surface releases only that surface;
  clearing a worker hold uses the existing authorized run-level release action.
  Update shell UI/copy for this distinction and retain pre-existing holds.
- Revalidate current authority on mutations and live streams. Human shell/browser
  access requires the existing Steer authority; do not expose authenticated app
  sessions to every transcript viewer. Run protection/revocation fences affected
  surface controllers. Agent APIs remain scoped to the active originating run.

These controls serialize Aether input; they do not stop an agent with shell
access from editing files or using credentials already present in its account
home. Do not present them as a new restricted-execution sandbox.

### Lifecycle

| Event | Required behavior |
| --- | --- |
| Human closes/reloads the dashboard | Detach only. Live terminals, app processes, and browser session continue. |
| Normal interactive harness exit | Reuse the existing persistent run lifetime. Developer can test the app and continue in the run shell. |
| Headless exit or terminal mission-worker report | Verification and evidence capture happen first. Then existing completion/worker cleanup stops the live surfaces. No new forever-running headless mode. |
| Run pause/explicit Close | Revoke input and freeze/stop companion activity together with the run; no background browser traffic from a paused run. Existing retention policy still applies. |
| Retained run relaunch | Reconcile the retained processes and browser state. Restore only what can actually be recovered and identify restarted/lost sessions. Do not silently rerun app commands. |
| Kill/Delete/retention expiry | Remove owned processes, companion, and transient browser/artifact state through existing run cleanup. |
| Server restart | Reconcile resources by recorded creation/execution identity. Reattach when possible; otherwise report unavailable/ended state and offer an explicit restart. Never claim a surviving process has a recovered PTY without proving it. |

Use ordinary interactive runs for live human review after an agent finishes.
Every live mode can verify its work; a completed one-shot worker is not a live
development environment. Browser crashes do not terminate the coding agent.

### Transport and evidence

Keep small control requests on the existing run socket. Screenshots and live
frames must not be base64 payloads in its 64 KiB JSON-line protocol. Reuse
bounded gateway streams and return artifact handles/container-readable paths
for captures. Give the run read-only access to its own capture directory outside
the checkout; do not accept arbitrary host paths. Keep status/coordination
responsive under capture load and use bounded waits instead of screenshot polling.

Extend existing evidence packets with selected terminal/browser captures and
verification notes, not a second evidence product. Capture source/run/session,
time, geometry or URL, and the Git revision/dirty-state boundary actually known.
If code changes during verification, do not attribute success to a later commit.
An observation is evidence, not an automatically assigned pass/fail verdict.

Keep transient captures bounded by bytes/count and clean them up with the run.
Explicitly retained evidence uses the existing retention/access rules. Do not
record every frame, keystroke, network body, or credential entry. Screenshots
may contain secrets: retain/share intentionally and never attach them to a
public PR automatically. Missing/truncated evidence stays explicit.

## 8. Remote repository setup and Git/PR workflow

### Import without a local clone

Add an administrator's **Import repository** route to workspace setup. Accept a
repository URL and base branch; use the existing source-mirror machinery,
public HTTPS or its existing read-only deploy-key flow. Initialize the server
bare repo directly, show/accept the first fetched base, and set checkout Origin
explicitly. Handle empty or missing source branches with the real error.

The current mirror engine can adopt an initial candidate, but newly created
workspace metadata does not by itself guarantee a bare repo exists. Fix that
initialization seam rather than requiring a hidden local Git operation or a
server restart. Do not silently adopt rewrites of an already accepted base.

Retain saved member environments, workspace setup scripts, and workspace
variables for dependencies. Databases can run as ordinary owned run processes
when the project needs them. No Docker socket mount, Compose orchestrator, or
new per-project image framework is required by this plan.

### Publish and review from the run

Add the missing actions to the changes view: inspect status/diff, select what
to commit, commit, push, create/open a PR, and refresh checks/review feedback.
A combined **Push and create PR** action may sequence them, but must expose
partial success: a pushed branch with failed PR creation remains pushed.

Use real Git and `gh` in the run's execution/account environment. Show the
GitHub identity being used, especially when the run uses a shared account.
Aether-managed actions recheck membership, Push/account-use authority, and
current branch/head before acting. Existing native credential access remains
as documented; this UI is not an enforcement gate for every possible `git` use.

Keep these values separate:

1. Source mirror and accepted base revision.
2. Run's push repository, remote, and head branch, including explicit fork targets.
3. PR repository, base branch, head repository/branch, and PR URL/number.

Never guess that the mirror source is the writable remote. Do not change a
workspace's Origin because one member selected a fork. Reject detached/wrong
branch or stale-head actions with the actual state; never silently switch
branches, commit unrelated edits, resolve conflicts, or force-push.

Discover an existing PR before offering creation. On uncertain creation, query
the exact head/base before retrying; do not duplicate external mutations.
PR state comes from GitHub, including PRs the agent creates directly with `gh`.
Refresh on actions and while the relevant view is active, not with a new global
polling service. Surface real stderr alongside actionable context.

Opening/pushing a PR is not merging it. Preserve existing human review, upstream
branch protections, and candidate-integration approval. Show checks/comments
and let the developer send selected feedback to the agent through the existing
Run Room. Do not add automatic merge or a new review/approval engine.

Orca inspiration is its [native push targeting](https://github.com/stablyai/orca/blob/main/src/main/git/remote.ts),
[PR creation/reconciliation](https://github.com/stablyai/orca/blob/main/src/main/github/client/create/create-github-pull-request.ts),
and [screen/browser CLI](https://www.onorca.dev/docs/cli/reference).
Aether keeps credentials and execution remote rather than copying Orca's
client-side GitHub execution assumption.

## 9. Implementation slices and dependency order

Each slice must ship working behavior and its guide updates. Intermediate slices
are not completion of the full remote-development requirement.

| Slice | Work and primary seams | Completion proof |
| --- | --- | --- |
| 1. Development identity and control | `internal/coord`, `coordcli`, scheduler coordination, `harness`, `control`, protocol, shell attach callers. Separate transport from feature policy; add scoped controller identity and capability composition. | An ordinary run with conflict coordination off discovers only its actual capabilities; a mission worker retains both development and authorized mission capabilities. Human primary-PTY control does not block a different test surface. |
| 2. Observable terminals | `internal/ptyhost`, scheduler run shells, runtime exec lifecycle, `sshd`, `webgate`, run terminal dock/store. Add list/start/output/screen/input/resize/wait/stop and authoritative shared tabs. | A real redraw-heavy TUI is operated without a dashboard connected; a human joins that same terminal; takeover rejects stale input; explicit stop terminates the process group. |
| 3. Browser runtime and captures | New small browser package/companion image, existing runtime/scheduler lifecycle, evidence attachments. Implement Playwright/Chromium actions, network sharing, private transport, and xterm image renderer. | Loopback-only web app, HMR, redirects/popups, page interactions and terminal screenshots work from the run CLI; no published debugging port or dashboard session is needed. |
| 4. Interactive browser UI | Shared gateway/backend streaming plus a run browser pane and typed frontend API. Complete human input, viewport/frame mapping, phone layout, control and reconnect. | Human and agent operate the same page; hiding/reopening preserves it. Slow clients do not grow frame queues. Both local and hosted gateways enforce current authority. |
| 5. Remote import and PR workflow | Existing mirror/Git engine workspace initialization, server handlers, protocol, workspace setup and changes UI. Use member-home GitHub execution; store only run/PR association needed for the UI. | Fresh workspace imports without a client clone; changed branch is committed, pushed and linked to a real test-repository PR; fork/base targeting, partial failure and existing-PR discovery are correct. |
| 6. Complete verification loop | Finish conditional skill topics, evidence presentation, lifecycle/revocation cases, and operational guidance across the completed surfaces. | Web and TUI acceptance scenarios below pass through real harnesses and the server dashboard with no local project tools. |

Slices 2 and 3 depend on the identity/control contract in slice 1; their source
work can then proceed independently with one integration owner. Slice 4 uses
slice 3's browser contract. Slice 5 is independent of browser implementation.
Terminal PNG capture lands with slice 3, not with a claim that slice 2 already
provides screenshots. Update capability discovery as each real operation lands.

Update [harnesses](../harnesses.md), [coordination](../coordination.md),
[terminal](../terminal.md), [gateway](../local-gateway.md),
[dashboard](../dashboard-frontend.md), [security](../security.md),
[privacy](../privacy.md), [quickstart](../quickstart.md), and
[testing](../testing.md) with their corresponding slices. Browser packaging also
updates [install](../install.md) and [notices](../notices.md). Leave current guides
truthful during this planning-only change.

## 10. Acceptance and release gate

Use real processes, containers, Git, and browsers. Focus permanent tests on
behavior and authority boundaries, not copies of skill wording or wiring.

- **Web:** From the hosted dashboard with no local clone/toolchain, implement
  sign-in, run the app, test valid/invalid credentials and sign-out, inspect the
  rendered result at desktop/phone widths, verify HMR, reconnect, then publish
  a PR. Check actual authenticated application state, not merely a screenshot.
- **TUI:** Start a real alternate-screen app, navigate with arrows/Tab/Enter/Esc,
  enter text, inspect highlighted selection/cursor, resize and verify redraw,
  capture the screen image, and verify the action's real result. Include Unicode
  cell widths and a terminal-querying program. Repeat without a human viewer.
- **CLI/API:** Exercise output, error exit/status, and side effects without
  unnecessarily launching browser or screenshot infrastructure.
- **Shared control:** Watch without resizing; take/release control; reject stale
  writes, wrong-surface handles, and cross-run access. Keep primary mission holds
  intact. Revoke membership/Steer and observe stream/input termination.
- **Lifecycle:** Client disconnect, normal agent exit, headless/worker completion,
  pause/Close/relaunch, browser failure, server restart, and deletion do not
  leave hidden uncontrolled work or falsely report a recovered session.
- **Git:** Real test repository and fork, wrong base/head, push rejection,
  failed/uncertain PR creation, direct agent-created PR, and credentials revoked.
  No local clone and no accidental mirror-base update are permitted.
- **Harnesses:** Verify the five shipped discovery adapters, taskless/task-bearing
  launch paths, supported headless modes, coordination off, and the documented
  custom-harness boundary. Run the full web and TUI loops with two different real
  supported harnesses; mocks alone cannot establish that agents discover/use it.
- **Resource/security:** Concurrent runs remain isolated by handle/profile;
  oversized captures, slow viewers, exhausted companion capacity, and unavailable
  sandbox/runtime fail visibly without starving coordination or enabling a
  privileged fallback. Browser state/artifacts remain outside source and images.

Run relevant Go, dashboard, and real Docker/browser suites after each integrated
slice. The full release gate includes `make fmt-check`, `make vet`, `make lint`,
`make test`, `make test-scripts`, `make public-audit`, `make test-integration`,
`make test-e2e`, and the dashboard typecheck/tests where affected. Record actual
manual/harness evidence separately from automated-suite results.

## 11. Explicit exclusions

No new mission system, mandatory reviewer agent, verification score, service
orchestration platform, public preview hosting, full remote desktop, browser
extension, or second terminal emulator. No mandatory browser use for non-browser
work. No automatic merge, forced push, or silent command restart. No promise of
arbitrary native desktop apps, hardware authentication, every terminal graphics
protocol, or cross-browser compatibility from one Chromium session.

The complete deliverable is a small set of shared, observable development tools
inside the existing run model, with honest evidence and an uninterrupted remote
path to a PR—not an additional layer of agent orchestration.
