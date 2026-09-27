# Remote development implementation

The product contract is
[the remote-development plan](docs/plans/2026-09-25-remote-development.md).
The implementation is on `feat/remote-development`; final acceptance is in
progress. The old uncommitted-package inventory and compile failures are
obsolete. Operational commands belong in the public guides, not this file.

## Delivered surfaces

| Area | Implementation |
| --- | --- |
| Identity and control | One authenticated run socket exposes live development capabilities independently of conflict coordination. Terminal and browser leases are per surface; primary harness control and mission holds remain separate. |
| Terminals | Owned process groups, authoritative shared tabs, output cursors, structured xterm-go screens, terminal protocol responses, mode-aware input, resize, wait, explicit stop, and PNG capture. Hiding a tab detaches without stopping it. |
| Browser | On-demand non-root Chromium companion with a private Unix transport, the run's network namespace, explicit sandbox enforcement, bounded resources, DOM actions, screenshots, and shared interactive dashboard frames. No host display server or published debugging port. |
| Dashboard | Browser navigation, pages/popups, desktop/phone viewports, keyboard/pointer/touch/composition input, control takeover/release, reconnect, and the existing shared terminal dock. |
| Git and GitHub | Administrator import without a local clone; native run-account Git/gh status, selected-path commits, explicit push repository and PR head/base, partial success, exact existing-PR discovery, uncertain-create reconciliation, and selected feedback sent to Run Room. |
| Evidence | Explicit capture retention in existing evidence packets, known revision/dirty boundaries, bounded capture storage, authorized streaming, image presentation, and expired/unavailable states. No automatic public screenshot attachment. |
| Discovery | Conditional `skill terminal`, `skill browser`, and `skill git`; five shipped discovery adapters; native harness hooks from main preserved without restoring automated PTY notices. |
| Packaging | Pinned companion dependencies, immutable resolved runtime image identity, native amd64/arm64 CI, versioned release publication, and anonymous-pull release gates. |

## Verification recorded so far

- Integrated formatting, vet, lint, full Go race tests, script tests, and public
  audit passed after the capture-budget and browser-status fixes. Dashboard
  typecheck and 91 files / 1,385 tests passed.
  The production dashboard plus CLI/server build passed with Go 1.26.8.
  Run-link and startup navigation corrections passed 87 focused store tests
  and both real import scenarios. Final-head checks remain required.
- Retention retries reject changed capture selections/notes and expired packets.
  Real API checks preserved the original PNG and metadata after both changed
  retries, returned the same usable packet after transient deletion, and
  refused the expired packet while an independent packet remained readable.
  SQLite/file regressions cover database restart, exact expiry, and tombstones
  after clock rollback; integrated Go gates passed after the fix.
- An isolated Ubuntu 24.04 server VM runs in `multi-user.target`, with no X11,
  Wayland, Xvfb, desktop session, or host Chromium. Real Docker builds of the
  standard environment and browser companion passed there.
- The actual companion smoke exercised a loopback app, DOM actions, popups,
  console, PNG/JPEG capture, a 240-column alternate-buffer terminal PNG with its
  final column visible, and clean-session reset. All six companion behavior
  tests passed on the pointer-cache image, including stale DOM/frame
  invalidation, cached-title input, physical targeting after a DOM click,
  static-frame replay, bounds, and held-input cleanup. Known pointer positions
  avoid redundant moves before button down/up without changing input expiry
  or control fences. Aborting a renderer with
  Chromium virtual time paused closed its context and released the queue while
  preserving the app; the same probe failed against the previous image.
  The stalled render without caller cancellation returned `timeout` in
  31.422 seconds; only the app context remained.
  A terminal PNG before any browser open started the companion and returned
  the actual 90×28 TUI in 28.836 seconds.
- Kernel inspection verified Chromium renderer namespace isolation,
  `NoNewPrivs`, seccomp filtering, and additional renderer filters. The browser
  retains Docker's AppArmor policy and its explicit seccomp profile; no
  privileged or unconfined fallback was used.
- Genuine OMP completed the edit/run/observe/correct loop through its registered
  harness: CLI exit codes and file effects before any browser launch, valid and
  invalid sign-in, HMR, desktop/phone layouts, and alternate-screen TUI input,
  Unicode, queries, resize, and PNG capture. Its native image-read records
  match the three retained PNGs by SHA-256.
  That loop used the original companion and an explicit terminal-capture retry
  after its first timeout; it is not proof of the later companion builds.
- Genuine OpenCode completed the same CLI/web/TUI loop on the terminal-budget
  companion. All three native image-read attachments match the retained PNG
  bytes; its terminal screenshot succeeded once in 18.167 seconds. Earlier
  timeout and vendor-crash attempts are not counted as this successful run.
- After both headless runs completed, the actual dashboard displayed their
  retained desktop, phone, and terminal images with capture/revision boundaries.
- The actual mobile shared-browser scenario passed HMR, reconnect, handoff,
  accented text, Japanese IME, two touches, phone layout, and sign-out.
- Actual detach, pause/resume, interactive harness exit, server restart,
  close/relaunch, whole-companion failure, deletion, and worker completion
  preserved the documented ownership and explicit-restart boundaries.
  Development input and browser interaction preserved a real human's primary
  mission PTY lease and durable hold; explicit primary release cleared the hold.
- Killing only Chromium left Node, the harness, and the app alive; the live
  browser viewer ended and status reported `unavailable` with the actual
  Chromium-disconnected reason. Explicit reset with the original control
  fence restored browsing without restarting the app.
- Revoking workspace Steer and then removing an authenticated collaborator
  ended both real SSH development streams in about two seconds. Subsequent
  input/actions were denied, controllers cleared, and app/browser work survived.
- Three companions were admitted and the fourth refused without starving
  coordination. Four streams were admitted; a fifth returned 429. With three
  private companion readers stalled, the fourth received 101 frames in
  30 seconds. Exact Node RSS measurements are observations, not a formal heap
  bound. Capture count, 128 MiB aggregate, 8 MiB image, and dimension limits
  refused excess work without retaining artifacts. A deliberately unavailable
  namespace sandbox returned its actual failure without a privileged fallback.
- Fifteen native startup variants exercised the five shipped adapters.
  Authenticated taskless OMP and OpenCode independently discovered the skill
  entry point and live capabilities. Claude, Codex, and pi authentication
  refusals are startup evidence, not authenticated model-loop acceptance.
- Native Git and gh against disposable GitHub repositories passed selected-path
  commit, explicit fork/head/base targeting, feedback to Run Room, wrong-target
  refusal, non-fast-forward push refusal, direct gh-created PR discovery, and
  member-account credential removal. The uncertain-create case discarded the
  response to one successful real POST; read-only reconciliation found that
  exact PR without another create.
- PR #247's second CI run passed native amd64/arm64 harness and companion
  smoke, Windows, release builds, and all but one server integration test.
  The remaining server failure began with an obsolete websocket assertion;
  a cold-launch check also exposed a test client that stopped answering pings.
  The corrected continuously-reading client passed the actual Docker-backed
  server gateway integration in 30.620 seconds.
  Dashboard E2E recorded 60 passes, the expected conditional live-GitHub skip,
  and two lifecycle-fixture failures: reacquiring control after closing a
  popup and reopening the run after reload. The corrected shared-terminal
  scenario passed with an actual 1394×124 PNG; final desktop/CI checks remain.
- Reconciliation now avoids rewriting identical durable companion status.
  Live container/health checks and changed-state durability are unchanged.
  The final Go quality gates passed. An actual two-viewer broker smoke sent
  60 physical pointer operations and observed `profile-delivery` in the real
  Email textbox. That profiling-overlay smoke is not full desktop acceptance.

## Acceptance still in progress

- Final unchanged-cadence desktop acceptance, including reset/reacquisition.
  One local run reached the final boundary; a subsequent run expired queued
  input with two viewers. The local VM uses QEMU TCG software emulation.
  Measured redundant journal syncs account for only 3–5% of slow intervals;
  they are not the dominant delay. The corrected full scenario must pass on
  the normal CI runner. Expiry and fencing remain unchanged.
- Production hosted HTTPS/WhoIs acceptance awaits temporary tailnet-node
  authorization. Local-gateway tests do not substitute for that boundary.
- Complete Docker integration, final merged quality gates, PR review/CI, merge,
  and a new tag plus published release. Release CI must prove public pulls of
  both companion architectures before downloadable binaries are published.

The plan's section 10 remains the acceptance checklist. Temporary credentials,
model profiles, transcripts, VM state, and test-repository state stay outside
source. Disposable repositories contain only synthetic fixtures and must be
deleted after acceptance. Do not replace missing proof with mocks, a launch
probe, a successful build, HTTP 200, or a transcript tail.

## Operational guides

See [coordination](docs/coordination.md), [harnesses](docs/harnesses.md),
[terminal](docs/terminal.md), [gateway](docs/local-gateway.md),
[dashboard](docs/dashboard-frontend.md), [quickstart](docs/quickstart.md),
[security](docs/security.md), [privacy](docs/privacy.md),
[testing](docs/testing.md), [install](docs/install.md), and
[notices](docs/notices.md).
