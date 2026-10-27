# Testing

Which tests to run for a change, how to run each suite, what it needs, and
how to read a failure. Build the tree first
([Build from source](../CONTRIBUTING.md#build-from-source)); the checks every
pull request runs are in
[Test your change](../CONTRIBUTING.md#test-your-change).

## Which suite to run

| You changed | Run | Needs |
| --- | --- | --- |
| Any Go code | `make test` | Go |
| Server code that touches containers, git, SSH or storage | `make test-integration` | Docker, git; root to match CI |
| Dashboard components in `web/src` | `cd web && bun run typecheck && bun run test` | Bun, Node 22+ |
| Something a person clicks through in the dashboard | `make test-e2e` | Docker, git, Bun, Node 22+, Playwright's Chromium |
| `scripts/`, the installers, `desktop/` | `make test-scripts` | sh, Node, OpenSSL 3 |
| `.github/workflows/` | `make lint-workflows` | ShellCheck |
| How an agent CLI is launched (`internal/harness`) | [Real-agent smoke tests](#real-agent-smoke-tests) | Docker |
| How a run drives, reads or wakes its agent | [Real agents against a mock model](#real-agents-against-a-mock-model) | Docker, git |
| The assistant's instructions or tools (`internal/assistant/instructions.md`, `internal/ops`) | [Assistant eval](#assistant-eval) | Claude Code signed in, spends tokens |
| Native hook adapters (the `.mjs` files in `internal/coordhooks` and `internal/agentstatus`) | `make test-native-hooks` | Node 22.13+ |
| `cmd/aether-edge`, `internal/edge` | `go test -race ./internal/edge/edgetest/` | Go |
| `images/edge/Dockerfile` | [The edge image](#the-edge-image) | Docker, curl |
| The edge store or `scripts/edge-update.sh` | `make test-edge-upgrade` | root, Docker with Compose and buildx, jq, curl |
| `images/browser/` | `make browser-image && make browser-smoke` | Docker |
| `android/` | `make android` | Docker |
| Only `docs/` or root `*.md` | `make public-audit` | sh |

## Unit tests

`make test` runs `go test -race` over every package.

```sh
make test                                   # everything
make test TEST_PKGS=./internal/store        # only these packages
make test TEST_SKIP=./internal/ptyhost      # everything but these
go test -race ./internal/scheduler -run '^TestName$' -v
```

When you write a test:

- Open a database outside `internal/store` with `storetest.Open`
  (`internal/store/storetest`). It copies a database migrated once per test
  binary; migrating a fresh one costs seconds under the race detector.
- Call `testhome.Isolate(t)` (`internal/testhome`) in any test that reads or
  writes the CLI's link config through `cli.Load`, `cli.Save` or a gateway
  handler. It sets `AETHER_CONFIG_DIR` and the platform home and config
  variables, and clears `SSH_AUTH_SOCK`. Without it the test uses your real
  config.
- Never wait for real time to pass. Give a cache or deadline an injectable
  clock and advance it by hand. Windows' clock is coarse enough that two
  reads can return the same instant.
- Give behavior that differs by platform one test per platform. CI runs the
  client packages on Windows, so a test whose subject refuses on Windows goes
  in a `//go:build !windows` file with a `_windows_test.go` counterpart that
  asserts the refusal.
- Start a bug fix with a test that reproduces it, on the real user path when
  you can. Do not prove the same behavior again at another layer.

## Integration tests

`make test-integration` runs the tests behind the `integration` build tag,
against real Docker and real git, in the packages that have them. It fails
when Docker is unreachable; it does not skip.

| Variable | Effect |
| --- | --- |
| `INTEGRATION_PKGS` | Packages to run (default: every package with integration tests) |
| `INTEGRATION_SKIP` | Packages to leave out |
| `INTEGRATION_RUN` | Passed to `go test -run` |
| `INTEGRATION_SKIP_PATTERN` | Passed to `go test -skip` |

CI runs the suite as root. Run it the same way to match:

```sh
sudo env "PATH=$PATH" "HOME=$HOME" make test-integration INTEGRATION_PKGS=./internal/scheduler
```

Without `sudo`, run containers use your uid, because the scheduler hands the
checkout to the container user and an unprivileged process can only hand
files to itself.

CI splits the suite into jobs. To repeat one that failed:

| CI job | Variables |
| --- | --- |
| `integration (server-chaos)` | `INTEGRATION_PKGS=./internal/server INTEGRATION_RUN=^TestIntegrationChaos` |
| `integration (server-mission-coordination)` | `INTEGRATION_PKGS=./internal/server INTEGRATION_RUN='^TestIntegration(Mission\|Coordination)'` |
| `integration (server-heavy)` | `INTEGRATION_PKGS=./internal/server INTEGRATION_RUN='^TestIntegration(EndToEnd\|MultiMember\|ServerUpdate)'` |
| `integration (scheduler)` | `INTEGRATION_PKGS=./internal/scheduler` |
| `integration (rest)` | `INTEGRATION_SKIP="./internal/harness ./internal/scheduler ./internal/server" INTEGRATION_SKIP_PATTERN='^TestDockerBrowser'` |
| `integration (server-rest)` | `INTEGRATION_PKGS=./internal/server INTEGRATION_SKIP_PATTERN='^TestIntegration(Chaos\|Coordination\|Mission\|EndToEnd\|MultiMember\|ServerUpdate)'` with the [browser image](#the-browser-companion) variables |

CI sets `GOFLAGS=-v`, so each test's duration is in the job log.

Some tests take an image from the environment:

| Variable | Used by | When unset |
| --- | --- | --- |
| `AETHER_BROWSER_TEST_IMAGE` | `TestDockerBrowser*` and the server's browser scenarios | `aether/browser:test`, which `make browser-image` builds ([The browser companion](#the-browser-companion)) |
| `AETHER_BROWSER_IMAGE` | The companion image a server under test starts | The build's default companion image |
| `AETHER_NESTED_DOCKER_TEST_IMAGE` | `TestDockerNestedDocker` ([inside a run](#running-the-docker-suites-inside-a-run)) | The test skips |
| `AETHER_ACP_IMAGE` | Enhanced-mode adapter tests ([Enhanced mode](#enhanced-mode)), `TestIntegrationAssistantIgnoresPlantedGatewayDocker` and `TestIntegrationAssistantIssuerMatchesTheBundledCLIs`, which install the assistant's adapters from the npm registry | The test skips, or uses busybox only |

The resource tests check real cgroup limits, PID exhaustion and OOM
containment. The capacity test starts its own `dockerd` and `containerd` in
private namespaces and never reconfigures the host's daemon. It needs root,
permission to unshare namespaces, `dockerd` and `containerd` on `PATH`, and
Docker API 1.46 or newer:

```sh
sudo env "PATH=$PATH" "HOME=$HOME" go test -race -tags=integration \
  ./internal/runtime \
  -run '^TestDocker(CapacityContainerdIsolated|ResourceLimits|PIDLimitEnforced|OOMContained)$' -v
```

Tests in `internal/runtime`, `internal/scheduler` and `internal/harness`
label their containers `aether.test=<test name>`. After an interrupted run,
`docker ps -a --filter label=aether.test` lists what was left behind.

## The E2E scenario suite

The `*_integration_test.go` files in `internal/server` start the whole
server and drive it over real SSH and git transport, with real containers
when Docker is reachable. Without Docker most scenarios fall back to an
in-process runtime. The chaos scenarios, which run `aether-server` as a child
process and kill it with `SIGKILL`, and the in-container coordination
scenario skip instead.

The agents are scripted. The `fake` harness runs the command in
`AETHER_FAKE_AGENT` (typically `sh /workspace/agent.sh {task}`, with the
script committed to the test repository).

| Test | Covers |
| --- | --- |
| `TestIntegrationEndToEnd` | One member: push, launch, attach, detach, steer, finish, pull |
| `TestIntegrationMultiMember` | Joining, roles, handoff, budget caps, an agent crash |
| `TestIntegrationChaos*` | The server killed mid-run, disk pressure, a stalled agent |
| `TestIntegrationCoordination*`, `TestIntegrationMission*` | Messages between runs, swarms |
| `TestIntegrationServerUpdate*` | `aether-server update`: preflight, rollback, downgrade guards |
| `TestIntegrationServerGateway` | The dashboard the server hosts on the tailnet |
| `TestIntegrationConnectorFlows` | A ChatGPT connector, over OAuth and MCP, launching, messaging, answering and stopping enhanced runs and a swarm |

```sh
sudo env "PATH=$PATH" "HOME=$HOME" make test-integration \
  INTEGRATION_PKGS=./internal/server INTEGRATION_RUN='^TestIntegrationEndToEnd$'
```

## Real-agent smoke tests

`internal/harness/smoke_integration_test.go` starts the vendors' real agent
CLIs to check that the command lines Aether builds are still accepted. A
vendor renaming a flag breaks every run of that agent, and nothing else
catches it.

`TestSmokeHeadlessNoLogin` runs in CI. It launches each headless template
with no credentials and passes when the run fails for want of a login, never
for an unparseable command line. CI's `smoke` job builds the standard image,
adds the agent CLIs at their current versions with `images/smoke/Dockerfile`,
and runs:

```sh
docker build -f images/standard/Dockerfile -t aether-standard:local .
docker build -f images/smoke/Dockerfile --build-arg BASE=aether-standard:local \
  -t aether-smoke:local .
AETHER_SMOKE_IMAGE_CLAUDE_NOLOGIN=aether-smoke:local \
AETHER_SMOKE_IMAGE_CODEX_NOLOGIN=aether-smoke:local \
AETHER_SMOKE_IMAGE_OPENCODE_NOLOGIN=aether-smoke:local \
  go test -tags integration -run TestSmokeHeadlessNoLogin ./internal/harness/
```

A `_NOLOGIN` image must carry no credentials. `TestSmokeClaude`,
`TestSmokeOpencode` and `TestSmokeCodexFlags` drive the agent far enough to
produce output, so they need an image that carries that agent's login, named
by `AETHER_SMOKE_IMAGE_<NAME>` (for example `AETHER_SMOKE_IMAGE_CLAUDE`). CI
has no such credentials, so they are a manual check. Each test skips when its
variable is unset. On failure a test prints the agent's output up to that
point.

`pi` and `omp` have no smoke test in that image.

### Enhanced mode

Enhanced runs drive the agent through an adapter that speaks the Agent
Client Protocol (ACP). Most tests use `internal/acphost/acpmock`, which replays
conversations recorded from the real adapters and answers prompts by their
text (`ask permission`, `ask form`, `wait`, `hit the usage limit`, `demo`,
`demo edit`):

```sh
make test-integration INTEGRATION_PKGS='./internal/scheduler ./internal/server' \
  INTEGRATION_RUN='TestIntegrationEnhancedRun'
```

`TestACPAdapterInstall` installs the pinned Claude Code and Codex adapters
into the standard image and completes the ACP handshake with each. It needs
the npm registry. With `ACP_LIVE=1` it also opens a session, which needs no
login (a logged-out Codex skips):

```sh
AETHER_ACP_IMAGE=aether-standard:local \
  go test -tags integration -run TestACPAdapterInstall -v ./internal/harness/
```

Two tests run the agents and adapters on your `PATH` with your own logins.
`TestLiveSwitch` switches a session between Standard and Enhanced both ways;
`ACP_LIVE_OMP_MODEL` picks omp's model when its default cannot answer.
`TestLiveEnhancedRunsWithoutPermissionPrompts` fails if an Enhanced session
asks for permission to run a shell command:

```sh
ACP_LIVE=1 go test -tags integration -run TestLiveSwitch -v ./internal/harness/
ACP_LIVE=1 go test -tags integration -run TestLiveEnhancedRunsWithoutPermissionPrompts -v ./internal/harness/
```

## Real agents against a mock model

`TestIntegrationRealAgents` (`internal/server/realagents_integration_test.go`)
launches real Claude Code and Codex runs, Standard, Enhanced and Background,
on real Docker, and checks that each report an agent makes, or fails to make,
leaves the run in the state the dashboard shows. A mock model in the test
process (`internal/server/mockmodel_integration_test.go`) serves streaming
Anthropic `/v1/messages` and OpenAI `/v1/responses`. Each prompt the test
sends carries a tag, `aether-script:<turn>;`, naming the script the mock
plays for that turn: one tool call per step, then a text answer. The mock
answers each report reminder Aether sends with `aether-internal report`.

One Standard and one Enhanced run per agent play these turns in order. The
launch starts `turn`, mail starts `wake`, and `run.inject` starts the rest:

| Turn | The agent | The run must show |
| --- | --- | --- |
| `turn` | runs a command and ends the turn without a report | `answered` after one reminder; Standard Codex, which Aether cannot remind, `waiting` |
| `wake` | gets mail while idle | a new turn, where Aether can wake the run |
| `waiting` | reports `waiting --for ci` | `waiting-ci` with the summary |
| `answered` | reports `answered` | `answered`, not Needs you |
| `late-report` | reports `waiting --for process` from a detached process five seconds later | the report landing after the run parked, then `waiting-tools` |
| `compaction` | claims more tokens than its context holds, compacts, then reports | the reporting rules still in the next request, then `answered` |
| `subagent` | starts a subagent that runs for eight seconds | not Needs you at any point while it runs, the subagent never reminded, then the turn ends like `turn` |
| `needs-input` | reports `needs-input` | `needs-you`, Needs you |

Each check also reads the run's `cause`, its report entries on the timeline,
and whether the report's output reached the model. A failure names the broken
link: the reporting rules missing from a request (`instruction missing`), no
report output reaching the model (`report never made`), a report missing from
the timeline or its output missing from the model's requests (`report lost`),
or a report recorded but shown as the wrong state (`misclassified`). The
first turn and every Background run also require the launch to record where
its instructions went.

The `wake` turn runs where Aether can wake the run: every Enhanced run, and a
Standard run whose agent sets `IdleWake` in its capabilities
(`internal/harness/capabilities.go`); the others skip it. A run must never
need you while its subagent works. It should show `working`, or
`waiting-agents` when Standard Claude Code ends the turn with the subagent
still running in the background. Aether gets no
signal for a Codex subagent, and Standard Claude Code's Stop hook can arrive
without the subagent among its background tasks, so those runs can read as
idle; the test logs their idle states as a known gap and fails any other.

Two Background runs per agent check that a Background run is never reminded:
one reports `success` and finishes awaiting review, the only Background state
that needs you; the other has its `needs-input` report refused and finishes
without a report.

`TestIntegrationSilentModelParksAsPossibleStall`
(`internal/server/liveness_integration_test.go`) launches an Enhanced Claude
Code run against a model that accepts every request and never answers. It
requires the run to park as a possible stall awaiting its first event, and
never to read disconnected.

Runs reach the mock the way they reach a [model
gateway](harnesses.md#per-agent-setup): `ANTHROPIC_BASE_URL` for Claude Code,
`openai_base_url` in `~/.codex/config.toml` for Codex. The test seeds the
member home with what a member's first runs leave there: onboarding, an
approved API key and a trusted checkout. The Codex config also sets
`model_auto_compact_token_limit`, so the `compaction` turn's claimed usage
makes Codex compact.

CI's `agents` job runs both as root. Run them the same way, with Docker and
git:

```sh
make agents-image   # images/agents/Dockerfile on the published standard image
sudo env "PATH=$PATH" "HOME=$HOME" make test-agents
```

`AGENTS_BASE=aether-standard:local` builds on a standard image you built. A
failure prints the broken link, the run's report entries, the agent's screen
(Standard) or ACP history (Enhanced), and the final item of each request the
mock received.

### Bumping the pinned versions

`images/agents/Dockerfile` pins the CLIs in `CLAUDE_CODE_VERSION` and
`CODEX_VERSION`; the ACP adapters follow `internal/harness/acpregistry.json`,
the versions members install. The smoke image takes each vendor's latest
release; this one is pinned so a vendor release cannot fail an unrelated pull
request. To bump, change the version, run the two commands above, and fix
what the new release broke in the same pull request.

The assistant installs the adapters at the versions in `acpregistry.json` and
`assistantPins` (`internal/scheduler/assistant_container.go`), and refreshes
its sign-ins with the endpoints and client ids next to `assistantPins`. After
moving either, run:

```sh
AETHER_ACP_IMAGE=aether-standard:local \
  go test -tags integration -run TestIntegrationAssistantIssuerMatchesTheBundledCLIs -v ./internal/scheduler/
```

## Assistant eval

`internal/assistant/eval` measures what the built-in assistant does with
`internal/assistant/instructions.md`. It runs the real Claude Code CLI (`claude -p`)
in a directory whose `CLAUDE.md` is those instructions. The agent's only tools
are the assistant's own, `ops.RegisterAssistant`, served over HTTP MCP by a fake
control surface that answers from synthetic scenario state. The fake records
every tool call. A confirmed-tier call (launch, message, merge) is recorded and
answered with a made-up success, so nothing is ever started, sent or merged;
the eval scores the parameters the agent proposed.

It spends model tokens, so it is behind the `assistanteval` build tag and no CI
job runs it. Claude Code must be on `PATH` and signed in:

```sh
AETHER_ASSISTANT_EVAL_LABEL=main \
  go test -count=1 -tags assistanteval -run TestAssistantEval -timeout 40m ./internal/assistant/eval
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `AETHER_ASSISTANT_EVAL_LABEL` | `unlabeled` | Names the output files; use the commit or branch under test |
| `AETHER_ASSISTANT_EVAL_RUNS` | `3` | Runs per scenario |
| `AETHER_ASSISTANT_EVAL_MODEL` | Claude Code's default | Passed to `claude --model`; the report names the model the harness reports |
| `AETHER_ASSISTANT_EVAL_ONLY` | all | Comma-separated scenario ids |
| `AETHER_ASSISTANT_EVAL_PARALLEL` | `4` | Concurrent agent runs |
| `AETHER_ASSISTANT_EVAL_OUT` | `$TMPDIR/assistant-eval` | Where `<label>.md` and `<label>.json` go |

The scenarios and their expected checks are in
`internal/assistant/eval/testdata/scenarios.json`: a rough voice request, a
question, a research-only request, a prohibition, hostile pasted text, a saved
preference against a one-off override, two members with different defaults, a
launch with no dashboard view, a shared account, an unavailable saved target,
merged against released, a failing PR behind a success report, an open task in
a completed swarm, and a reopened session. The checks are keyword and
tool-call tests, so they are deterministic but coarse: text matches are whole
words (so `bo` does not match `both`), an invented-scope keyword does not count
when the brief negates it ("no refactors"), and the same holds for words a brief must omit ("do not delete staging" does not copy the hostile instruction), and a question counts only when it
is a sentence addressed to the member, not a `?run=` link or inline code. A
question after a completed launch that offers to show or edit the brief is
reported in its own column, not as unnecessary. The Markdown report ends
with a manual-review list per scenario, with each launch brief and reply.

The report has a row per scenario: runs passed, unnecessary questions, brief offers, launches
that should not have happened, dropped prohibitions, invented-scope keywords,
tool calls and latency per run, and cost. Cost is the figure `claude` reports,
and "not measured" when it reports none. Results are rewritten after every
run, and each run's timeout is capped by the test's `-timeout`, so a run that
hits the limit still leaves a report.

A scenario marked `pending: <tool>` needs a tool the registry does not offer
yet, such as the saved launch choices. The eval serves an eval-only stub of the
tool from the scenario's contract so the baseline is honest about what the
agent does without it. Its rate is not product behavior. When the registry
offers the tool, the real one is served instead, and the scenario's `answers`
need an entry for each RPC method it calls; a missing one shows up as
"is not available in this synthetic scenario" in the reply.

Compare a change with `main` by running the command on both commits with
different labels. Model output varies between runs, so a few runs per scenario
shows a direction, not a proof. Scripted tests of the scorer itself,
`go test ./internal/assistant/eval`, run in `make test`.

## Native hook tests

The pi, omp and opencode adapters are JavaScript that Go never runs.
`make test-native-hooks` runs them under Node 22.13 or newer against
lifecycle fixtures shaped like each agent's SDK:

```sh
make test-native-hooks
node --experimental-vm-modules --test --test-name-pattern='omp:' \
  internal/coordhooks/native_pi_omp_lifecycle_test.mjs
```

`internal/agentstatus` also runs `status.ts` under `bun` from `go test`, and
skips those tests where `bun` is not installed.

The fixtures do not prove a real model turn. To check one, start an isolated
coordinated Standard run with the exact CLI, server and extension versions under
test, end a turn idle, send it one message with
`aether-internal send --to <run> --body <nonce>`, and confirm exactly one new
turn reads and acknowledges it. Never edit a live hook, credentials or trust
settings to get there.

## The edge suite

`internal/edge/edgetest` runs edge remote access end to end in one process,
under plain `make test`. It needs no Docker and no network. Each test starts a
real edge against a fake GitHub, real servers under each access policy, the
real client and the local gateway. A proxy in front of the edge holds the
edge's signing key and plays a compromised edge: it forges, replays, alters
and drops control messages, and tests read each server's store to see what an
attack changed.

```sh
go test -race ./internal/edge/edgetest/
go test -race ./internal/edge/edgetest/ -run '^TestClaim$' -v
```

Every request reaches the edge from 127.0.0.1, so each test moves its edge's
clock forward to refill the rate limits. Tests run in parallel; the client
calls that read `AETHER_CONFIG_DIR` take turns.

### Behind nginx

`scripts/edge-nginx-test.sh`, part of `make test-scripts`, runs a real nginx
from
[`packaging/nginx/aether-edge.conf.example`](../packaging/nginx/aether-edge.conf.example)
in front of the edge, and skips when `nginx` is not on `PATH`. It checks
sign-in, a claim, a relayed connection held silent past nginx's 60-second
read timeout, and that a request with a forged `X-Forwarded-For` is recorded
under the address nginx saw. It takes about 80 seconds:

```sh
sh scripts/edge-nginx-test.sh
```

### The edge image

`scripts/edge-image-smoke.sh` runs a built
[`images/edge/Dockerfile`](../images/edge/Dockerfile) image with fake GitHub
credentials. It checks that the image runs as a non-root user with no shell,
fails to start without configuration, passes its healthcheck, stops cleanly
on `SIGTERM` without logging the secret, and keeps its key on its volume. It
needs a container runtime, so it is not part of `make test-scripts`. The
second argument names the runtime (default `docker`):

```sh
make edge-image
sh scripts/edge-image-smoke.sh aether/edge:test
```

With podman, build with `podman build --format docker -f
images/edge/Dockerfile .`. The default OCI format drops `HEALTHCHECK` and the
smoke test fails.

Two script tests in `make test-scripts` keep the image's CI trigger and Go
version honest. When `aether-edge` starts importing another package,
`scripts/ci-classify-edge-test.sh` fails with the line to add:

```
ci-classify-edge-test: aether-edge imports internal/<package> on linux/amd64, which ci-classify-edge.sh classifies false; add internal/<package>/* to its first case pattern
```

The image builds with its own golang base image, so
`scripts/edge-go-version-test.sh` fails when that image and `go.mod`'s
`toolchain` line name different Go versions:

```
edge-go-version-test: images/edge/Dockerfile builds with golang:1.26.8, but go.mod pins go1.26.9; change the golang tag and its @sha256 digest in images/edge/Dockerfile to 1.26.9 (or go.mod's toolchain line to go1.26.8)
```

## Edge upgrade qualification

`TestEdgeUpgrade` updates real edge containers with
[`scripts/edge-update.sh`](../scripts/edge-update.sh), from the previous
release to builds of this tree. It needs root (the data directory belongs to
uid 65532), Docker with Compose and buildx, `jq`, `curl`, Go, and git history
with tags:

```sh
sudo env "PATH=$PATH" "HOME=$HOME" make test-edge-upgrade
```

It builds the edge image five times into a local registry: the previous
release (`git describe --tags HEAD^`, in a `git worktree`), this tree as
`v99.0.0-qualify.1`, and three builds of this tree with an extra migration:
one that fails, one that raises the compatibility floor and then fails, and
one that succeeds. The edge runs from `packaging/edge/compose.yaml` behind
nginx, real servers enroll and are claimed, and then it checks that:

1. `aether-edge-update` refuses an image it cannot read a version from.
2. A failing migration is rolled back without a restore and skipped next
   time.
3. A failure after a raised floor restores the pre-update backup.
4. A good migration applies, with a backup from the edge and the updater.
5. A server of the previous release still enrolls and relays.
6. `aether-edge-update --rollback` returns to the old binary.

After each step the edge must keep its key and its accounts, and each owner
must reach their server. Stub `cosign` and `docker` commands from
`internal/edge/edgetest/testdata/upgrade/` answer the release signature
checks from the digests the test built, because a production binary trusts
only the release keys built into it.

It refuses to start while a container named `aether-edge` exists, removes
everything it creates, and takes about two minutes with warm caches. CI runs
it in `edge-image.yml` on pull requests that change the edge, and in
`release.yml` before a release publishes.

## Dashboard component tests

Component tests live beside their components in `web/src/` and run in vitest
with jsdom:

```sh
cd web
bun run test                               # all of them
bun run test src/routes/files/home.test.tsx
```

jsdom has no layout, so `web/src/test/setup.ts` answers every media query
with `false` and a component renders its widest layout. `atViewport`
(`web/src/test/viewport.ts`) puts one test on one screen size. Real layout,
hit testing and focus outlines belong to the
[dashboard end-to-end suite](#the-dashboard-end-to-end-suite).

## The dashboard end-to-end suite

`web/e2e/` drives Chromium against the dashboard embedded in `dist/aether`,
through a real `aether gui` gateway and a real `aether-server` over SSH.
Playwright is the runner.

```sh
(cd web && bunx playwright install chromium)        # once, from the repo root
make test-e2e                                        # builds dist/ first
make test-e2e E2E_ARGS='--shard=1/4'                 # one of CI's four shards
make test-e2e E2E_ARGS='--project=chromium run-files.spec.ts'
cd web && bunx playwright test --project=mobile      # reuse the binaries already in dist/
```

`make test-e2e` rebuilds the dashboard and binaries because the CLI serves
the dashboard it embeds: a stale binary tests a stale dashboard.

There are two projects. `chromium` runs every spec except
`*.mobile.spec.ts` on a desktop viewport. `mobile` runs only those, as a
Pixel 7 (touch, 412x839). Mobile specs tap instead of clicking and attach a
full-page screenshot to every test. WebKit is not installed, so iOS Safari is
not covered.

Each test gets its own stack:

- One `aether-server` on its own loopback port, with a temporary data
  directory, the `busybox:1.36` image and `AETHER_FAKE_AGENT`. The first
  identity to connect becomes the admin.
- One `aether gui` per member, each with its own `HOME` and
  `AETHER_CONFIG_DIR`, so nothing touches your own keys or links.
- Real git repositories on disk.

`aether.server.restart()` kills the server with `SIGKILL` and starts it
again on the same port and data directory, for specs about what survives a
crash.

Specs that open a real run need Docker and skip without it.
`onboarding-github`, `mission-candidate-review`,
`remote-development-git/native.spec.ts` and `swarm.mobile.spec.ts` fail
without Docker instead. Some specs need more:

| Spec | Needs |
| --- | --- |
| `onboarding-github`, and `native`, `git-card` and `github` in `remote-development-git/` | The real standard image: `docker build -f images/standard/Dockerfile -t aether-standard:ci .`, or another build named by `AETHER_E2E_STANDARD_IMAGE` |
| `development-browser/*` | The browser companion image ([below](#the-browser-companion)) and `node:22.14.0-alpine3.21` |
| `remote-development-git/import.spec.ts` | Network access to `AETHER_E2E_GIT_SOURCE_URL` (default `https://github.com/3xDevOps/Aether.git`) |
| `remote-development-git/github.spec.ts` | Opt-in: `AETHER_E2E_GITHUB_PUBLISH=1`, `AETHER_E2E_GITHUB_TOKEN`, `AETHER_E2E_GITHUB_BASE_REPOSITORY` (a public `owner/name`), `AETHER_E2E_GITHUB_HEAD_REPOSITORY` (your writable fork); `AETHER_E2E_GITHUB_BASE_BRANCH` defaults to `main` |

`github.spec.ts` opens, comments on and closes a real pull request upstream.
Pass the token through the environment only, never in a file or a log.

To run as root like an installed server:

```sh
sudo env "PATH=$PATH" "HOME=$HOME" \
  "PLAYWRIGHT_BROWSERS_PATH=$HOME/.cache/ms-playwright" \
  AETHER_E2E_STANDARD_IMAGE=aether-standard:ci \
  AETHER_BROWSER_TEST_IMAGE=aether/browser:test \
  AETHER_BROWSER_IMAGE=aether/browser:test make test-e2e
```

### Reading a failure

- `web/playwright-report/` is the HTML report; open it with
  `cd web && bunx playwright show-report`. A failed test has the server's
  output attached.
- `web/test-results/` holds traces and screenshots.
- A failed test keeps its scratch directory, `/tmp/aether-e2e-*`. After a run
  as root it holds root-owned files; remove it with `sudo rm -rf`.
- In CI each shard uploads `playwright-report-<shard>` and
  `playwright-results-<shard>` for seven days, on success and failure:
  `gh run download <run-id> -n playwright-report-1`.

A new onboarding wizard step is one class in `web/e2e/pages/wizard.ts`: give
it the `aria-label` of the step's `<section>`, add its label to `stepNames`
in header order, and hang it off `OnboardingWizard`.

## The browser companion

A run's Browser tab is a Chromium in its own container, the companion,
built from `images/browser/`. It needs no display, host browser or host
browser libraries, but it does need a working kernel user-namespace and
AppArmor policy on Ubuntu. A sandbox failure must fail the check: do not
retry with `--no-sandbox` or relax the host's security policy.

```sh
docker build -f images/standard/Dockerfile -t aether-standard:ci .
make browser-image                 # aether/browser:test; BROWSER_IMAGE=<ref> for another tag
make browser-smoke                 # runs the image's own sandbox and behavior checks

sudo env "PATH=$PATH" "HOME=$HOME" \
  AETHER_BROWSER_TEST_IMAGE=aether/browser:test \
  go test -race -timeout=10m -tags=integration ./internal/runtime \
    -run '^TestDockerBrowser' -v

AETHER_BROWSER_TEST_IMAGE=aether/browser:test \
AETHER_BROWSER_IMAGE=aether/browser:test make test-integration
```

Run the runtime test as root, like the installed server. Do not make its
control directory world-writable to get a pass.

Committing selected paths from the Changes view uses native git and `gh`.
Their suite runs against a local TLS stand-in for GitHub:

```sh
go test ./internal/runrepo
```

## Android

`make android` runs the app's JUnit tests, then builds the APK and the app
bundle, all in pinned containers; `make android-debug` builds a debug APK
without the tests. Building, signing and installing are in
[Android shell](../CONTRIBUTING.md#android-shell).
`scripts/android-version-code-test.sh` and
`scripts/android-verify-signature-test.sh`, both in `make test-scripts`,
cover the version code and the signature check with the SDK tools stubbed.

## Windows

CI's `windows` job builds, vets and tests the client packages on Windows.
The `windows-install` workflow runs `scripts/install-test.ps1` under Windows
PowerShell 5.1 and PowerShell 7. You can run it on your own Windows machine
with Go installed; it restores your `PATH` and environment when it ends:

```powershell
powershell -NoProfile -File scripts/install-test.ps1
pwsh -NoProfile -File scripts/install-test.ps1
```

`scripts/install-smoke.ps1` and `scripts/update-smoke.ps1` install, update
and launch the real app under Defender. They change Defender settings, the
Start Menu and the registry, so they refuse to run unless
`GITHUB_ACTIONS=true` and `RUNNER_ENVIRONMENT=github-hosted`. When one
fails, `scripts/defender-diagnostics.ps1` prints Defender's detections in the
job log. The screenshots are uploaded as `windows-desktop-*` artifacts.

Before they run, `scripts/windows-defender.ps1 -Mode Prepare` removes the
runner image's Defender exclusions and adds one for GitHub's own agent,
`provjobd.exe*` in the runner user's Temp. On `windows-11-arm`, Defender
flags that agent as `Trojan:Win32/Gracing.I` and removes it; the job then
hangs until GitHub fails it with "The hosted runner lost communication with
the server". Nothing Aether builds or installs is excluded.

## How CI runs the suites

The `CI` workflow (`.github/workflows/ci.yml`) runs on every pull request,
every merge queue entry and every push to `main`.

| Job | Runs | Locally |
| --- | --- | --- |
| `audit` | `scripts/public-audit.sh` | `make public-audit` |
| `lint` | Formatting, vet, workflow lint, `make lint`; `make vulncheck` as advice only | `make fmt-check vet lint-workflows lint` |
| `build-and-test` | Unit tests, then `make build` | `make test build` |
| `scripts` | Script and native hook tests | `make test-scripts test-native-hooks` |
| `smoke` | Standard image checks, then the `internal/harness` integration tests on the smoke image, amd64 and arm64 | [Real-agent smoke tests](#real-agent-smoke-tests) |
| `agents` | [Real agents against a mock model](#real-agents-against-a-mock-model), amd64 | See that section |
| `browser` | Companion image build and smoke, amd64 and arm64 | [The browser companion](#the-browser-companion) |
| `integration`, `integration-server-rest` | [Integration tests](#integration-tests) in six jobs | See that table |
| `dashboard` | `bun run typecheck` and `bun run test` | Same, from `web/` |
| `dashboard-e2e` | [End-to-end suite](#the-dashboard-end-to-end-suite) in four shards | `make test-e2e E2E_ARGS='--shard=N/4'` |
| `desktop`, `desktop-windows` | `aether gui build` on Linux; the Windows desktop zip on amd64 and arm64 | [Desktop shell](../CONTRIBUTING.md#desktop-shell) |
| `windows`, `windows-install` | [Windows](#windows) | |
| `release-build`, `android`, `windows-defender` | `make release-binaries`, `make android`, a Defender scan of the Windows binaries | `make release-binaries`, `make android` |
| `edge-image` | [The edge image](#the-edge-image) and [qualification](#edge-upgrade-qualification) | |

A pull request that changes only `docs/**` or root-level `*.md` files runs
`audit` and nothing else. A Markdown file anywhere else counts as code.
`edge-image` runs only when `scripts/ci-classify-edge.sh` says a changed path
can affect the edge image, and `desktop-windows` only when
`scripts/ci-classify-desktop.sh` finds a change under `desktop/`. All three
classifiers run from the pull request's base revision, so a pull request
cannot change its own classification, and a renamed file counts by its old
path as well as its new one. A push to `main`, a merge queue entry and a
failed classification run everything. To run the edge image jobs on a branch
the classifier skips, with write access to the repository:

```sh
gh workflow run edge-image.yml --ref <branch>
gh workflow run edge-image.yml --ref main -f ref=refs/pull/<n>/head   # a pull request from a fork
```

To rerun only the failed jobs of a run: `gh run rerun <run-id> --failed`.

## Running the Docker suites inside a run

You can work on Aether from inside an Aether run (an agent's container on an
Aether server). Two things differ there.

A run sets `AETHER_*` variables (`AETHER_RUN_ID`, `AETHER_HARNESS`,
`AETHER_WORKSPACE_ID` and others) that some tests read. `internal/coordcli`
fails with them set. Unset them for the test command:

```sh
env $(env | grep -o '^AETHER_[A-Z_]*' | sed 's/^/-u /') make test
```

The Docker suites need a Docker daemon inside the run. On a server with
Sysbox ([environments.md](environments.md#docker-inside-runs)) each run and
environment terminal has its own `dockerd`; check with `docker info`. Paths
the tests bind-mount resolve because the daemon and the test share one
filesystem, which is not true of the host's socket mounted into a container.

```sh
make test-integration                    # Go and git are in the standard image
make android                             # needs only the docker CLI and network
curl -fsSL https://bun.sh/install | bash # make test-e2e also needs Bun on PATH
(cd web && bun install --frozen-lockfile && bunx playwright install --with-deps chromium)
make test-e2e
```

To check Docker inside runs on a Sysbox host, install Sysbox with
`sudo sh scripts/install-sysbox.sh`, then:

```sh
docker build -f images/standard/Dockerfile -t aether-standard:ci .
sudo env "PATH=$PATH" "HOME=$HOME" \
  AETHER_NESTED_DOCKER_TEST_IMAGE=aether-standard:ci \
  make test-integration INTEGRATION_PKGS=./internal/runtime \
    INTEGRATION_RUN='^TestDockerNestedDocker$'
sudo sh scripts/nested-docker-smoke.sh aether-standard:ci
```

`TestDockerNestedDocker` starts `dockerd` in a run container and runs a
container inside it; it skips unless `AETHER_NESTED_DOCKER_TEST_IMAGE` names
an image that has `dockerd`. `scripts/nested-docker-smoke.sh` runs the
resource limit tests against a nested daemon. It pulls `busybox` and Python
images, so it needs network access.
