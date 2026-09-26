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

- Repository formatting, vet, lint, full Go race tests, script tests, and public
  audit passed before the native-hook merge. Merged-union checks are running.
- Dashboard typecheck, 91 files / 1,381 tests, and the production dashboard plus
  CLI/server build passed. Focused merged coordinator/CLI tests and rebuilt
  binaries also passed.
- An isolated Ubuntu 24.04 server VM runs in `multi-user.target`, with no X11,
  Wayland, Xvfb, desktop session, or host Chromium. Real Docker builds of the
  standard environment and browser companion passed there.
- The actual companion smoke exercised a loopback app, DOM actions, popups,
  console, PNG/JPEG capture, alternate-buffer terminal PNG, and clean-session
  reset. All four companion behavior tests passed, including stale DOM/frame
  invalidation, static-frame replay, bounds, and held-input cleanup.
- Kernel inspection verified Chromium renderer namespace isolation,
  `NoNewPrivs`, seccomp filtering, and additional renderer filters. The browser
  retains Docker's AppArmor policy and its explicit seccomp profile; no
  privileged or unconfined fallback was used.
- Real Docker integration passed the Git engine and integration packages.
  The full integration gate is not yet complete: a negative browser fixture's
  directory permissions were corrected, and the remaining packages need their
  complete run. Do not infer a full gate from these partial results.

## Acceptance still in progress

- Genuine OMP and OpenCode model-driven web/TUI/CLI loops through registered
  Aether harnesses, including explicit retained captures after headless cleanup.
- Actual dashboard desktop/phone interaction and visual inspection, shared
  control, reconnect, and the full dashboard end-to-end suite.
- Real disposable GitHub repository/fork publishing, exact head/base rejection,
  native push failure, direct gh-created PR discovery, uncertain creation,
  member-account credential removal, and feedback to Run Room.
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
