# Remote development implementation

The product contract is
[the remote-development plan](docs/plans/2026-09-25-remote-development.md).
The implementation is in [PR #247](https://github.com/3xDevOps/aether/pull/247).
Hosted and native-harness acceptance is recorded below. Operational commands
belong in the public guides; current CI, review and publication evidence belongs
on the PR and release.

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
  invalid sign-in, custom module replacement, desktop/phone layouts, and
  alternate-screen TUI input, Unicode, queries, resize, and PNG capture.
  Its native image-read records match the three retained PNGs by SHA-256.
  That loop used the original companion and an explicit terminal-capture retry
  after its first timeout; it is not proof of the later companion builds.
- Genuine OpenCode completed the same CLI/web/TUI loop on the terminal-budget
  companion. All three native image-read attachments match the retained PNG
  bytes; its terminal screenshot succeeded once in 18.167 seconds. Earlier
  timeout and vendor-crash attempts are not counted as this successful run.
- After both headless runs completed, the actual dashboard displayed their
  retained desktop, phone, and terminal images with capture/revision boundaries.
- Those earlier web loops used SSE notifications and cache-busted ES-module
  imports, not a framework HMR server. The automated mobile fixture also uses
  this mechanism; it covered reconnect, handoff, accented text, Japanese IME,
  two touches and sign-out. Genuine Vite interoperability was verified separately.
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
- [CI on `c43117d`](https://github.com/3xDevOps/aether/actions/runs/36281303442)
  passed every job, including all Docker integration shards, native amd64/arm64
  companion/harness checks, Windows and release builds. Dashboard E2E recorded
  62 passes, no failures or flaky cases, and one conditional live-GitHub skip.
  The unchanged-cadence desktop scenario passed reset/reacquisition. Its
  [visual report](https://github.com/3xDevOps/aether/actions/runs/36281303442/artifacts/10919117238)
  includes the inspected desktop/phone surfaces and 1394×124 terminal capture.
- After integrating main's native wake support, CI on `fdd138c` passed every
  job except one phone assertion: 61 dashboard scenarios passed; one was
  skipped. A complete 390×844 frame correctly letterboxed in Pixel 7's shorter
  available area, but the test required a width ratio above 0.9. The incidental
  ratio assertion was removed; interaction, identity, overflow and full-surface
  evidence checks remain, with unchanged input expiry, fences and cadence.
- Greptile found that rejected native OpenCode prompts left unread IDs marked
  notified. Request-owned reservations now roll back on rejection without
  erasing accepted IDs or a newer reservation. Both API generations passed
  rejection/recovery, delayed acceptance, Stop and stale-rejection regressions.
  All 79 native-hook tests and the integrated Go quality gates passed.
  Packaged receivers also passed an injected HTTP 503→202 smoke with real
  helper subprocesses: later human completion retried the unread message,
  then acceptance coalesced it. This is component, not vendor-model, evidence.
- Reconciliation avoids rewriting identical durable companion status without
  caching live health or authority. A separate two-viewer profiling smoke
  delivered 60 physical pointer operations and text observed in the app;
  the normal CI desktop scenario supplies the end-to-end proof.

## Hosted and framework-HMR acceptance

- Actual hosted HTTPS and Tailscale WhoIs worked through ordinary untagged
  devices, without a browser bearer token, local gateway or client checkout.
  Native OpenCode built the synthetic app in the run. Hosted interaction
  exercised invalid credentials with a fresh HTTP 401 and visible error,
  normalized valid sign-in, saved Unicode text and sign-out.
- The initial hosted app polled heading configuration; that was not counted as
  HMR. Native OpenCode migrated it to Vite 8.3.1 middleware with a real HMR
  WebSocket and an explicit `import.meta.hot.accept` dependency boundary.
  A bounded official `opencode run` edited only the heading module and exited 0.
  The live app changed without reload, navigation, restart or state restoration:
  authentication, distinct saved/unsaved notes, page identity/revision and the
  app process/start time remained unchanged.
- OpenCode's bundled Bun crashed twice in the longer interactive sessions,
  including after the Vite migration. Those attempts are not clean passes.
  The first explicit continuation reached readiness; the final bounded native
  session supplied the successful module edit.
- Actual hosted phone operation used a 390×844 remote viewport and a 390px
  coarse-touch client. Expand/Restore and reconnect preserved page identity and
  the unsaved draft; native touch logout succeeded. The full phone surface was
  visually inspected. This is Chromium touch emulation, not physical Safari.
  A server upgrade preserved the browser/app; managed terminals honestly
  reported unavailable rather than silently reattaching or restarting.
- Hosted native Git reviewed and committed exactly nine selected source paths,
  pushed to the explicit fork and created the exact upstream/head/base PR once.
  The accepted workspace base did not change. No client clone was involved.
  The fixture PR was closed after acceptance.
- A separately imported workspace ran genuine OMP 18.3.2 headlessly against
  that Vite app. `npm ci` and the run exited 0. Native terminal output recorded
  Vite's `hmr update /app.js` after a one-line dependency edit. Authentication,
  saved/unsaved Unicode notes, session/page/revision, app PID/start time and
  terminal incarnation stayed unchanged. Invalid login and final logout were
  verified against actual application state.
- OMP natively read both retained captures: 1280×800 desktop and 390×844 phone.
  The phone tool image matches the retained PNG byte-for-byte; the desktop
  reader produced a WebP preview, not identical PNG bytes. Both original images
  and the native desktop preview were visually inspected; the dashboard loaded
  both retained images after headless cleanup. One pre-HMR resource 404 lacked
  a URL and remains unexplained; no error-free-run claim is made.

## Merge and release gates

Require current-head CI and Greptile review; an earlier green head is not a
substitute. Local QEMU TCG input-expiry failures were not bypassed by extending
TTL, changing fences or weakening interaction cadence.

After merge, publish a new tag and a normal GitHub Release. Release CI must
prove anonymous pulls of both companion architectures before publishing
downloadable binaries. A successful local image build is not publication proof.

The plan's section 10 remains the acceptance checklist. Temporary credentials,
model profiles, transcripts, VM state, and test-repository state stay outside
source. The two disposable repositories contain only synthetic fixtures.
Delete them after acceptance with authorization for repository deletion;
repository admin rights alone do not grant a classic token's `delete_repo`
scope. Do not replace missing proof with mocks, a launch probe, a successful
build, HTTP 200, or a transcript tail.

## Operational guides

See [coordination](docs/coordination.md), [harnesses](docs/harnesses.md),
[terminal](docs/terminal.md), [gateway](docs/local-gateway.md),
[dashboard](docs/dashboard-frontend.md), [quickstart](docs/quickstart.md),
[security](docs/security.md), [privacy](docs/privacy.md),
[testing](docs/testing.md), [install](docs/install.md), and
[notices](docs/notices.md).
