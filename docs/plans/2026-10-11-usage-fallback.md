# Usage fallback to a teammate's agent account

**Status:** In progress on branch `aether/run-feature-fall-back-to-a-teammate-fj61pjfaxy`.

**Goal:** A long run or swarm does not die when the launching account's
vendor subscription (Claude's five-hour or weekly limit, a ChatGPT plan
limit) runs out while a teammate who shared their account with the launcher
has usage left.

A **run** is one agent execution with its own container, checkout and
branch. A **swarm** is a mission: an **integrator** run that starts
**worker** runs for its tasks; each worker launch is an **attempt**. An
**agent account** is a member's vendor login for an agent CLI, the
subscription a run spends. A **share** is a directional grant from an
account's owner that lets one other member launch runs on that account
(`aether account share <member-id>`; see [teams](../teams.md#agent-accounts)
and [security](../security.md#account-sharing)). The **fallback pool** is
the set of accounts a launch may move to. **Proactive fallback (A)** picks
another account at launch when the requested one is exhausted. **Reactive
fallback (B)** moves a live run to another account when the vendor reports
the limit mid-run. An **account segment** is the stretch of a run spent on
one account.

## 1. Decisions

1. **Pool.** The launcher's own account plus every account currently shared
   with the launcher. Sharing is the consent; there is no new grant, flag or
   opt-in. A revoked share leaves the pool at once, because each candidate
   is authorised again at the moment it is chosen.
2. **Order.** Automatic: most remaining usage first, read through
   `internal/quota` from the windows the agent draws on: Claude `five_hour`
   and `seven_day`, Codex `primary` and `secondary`. Claude's per-model
   windows are ignored because Aether does not know the run's model.
   Remaining is 100 minus the highest used percent among those windows that
   have not reset; ties go to the lower member id. The launcher can instead
   give an explicit list, tried in order (reordering or excluding accounts),
   or turn fallback off:
   - dashboard: launch dialog **Options > Fallback**;
   - CLI: `--fallback <member-id,...>` or `--no-fallback` on `aether run`
     and `aether swarm create`;
   - wire: `fallback_account_member_ids` and `no_fallback` on `run.launch`
     and `mission.create`. Setting both is refused.

   The policy is stored on the run and on the mission, so later fallbacks
   follow it.
3. **Threshold for (A).** `fallback.ExhaustedPercent = 95`. Quota reads are
   cached for 60 s, and one agent turn on a large model can spend the last
   5% of a five-hour window within minutes; a worker started there dies
   almost at once. A requested account whose usage cannot be read
   (unsupported agent, API-key login, signed out, read error) is used as
   requested: only a successful read showing exhaustion triggers (A). A
   candidate needs a successful read below the threshold.
4. **Where (A) runs.** Every launch: `run.launch`, swarm create (the
   integrator), every `worker.start`, integrator replacement, and the
   recovery relaunch of a swarm run. A swarm's execution choices (account,
   agent, mode) stay as declared, and worker matching uses the declared
   choice. The substitution happens at launch, and the attempt records the
   account it launched on in `launch_account_id`. A replayed `worker.start`
   with the same dispatch key replays the attempt and never selects again.
5. **Reactive switch (B)** for Enhanced runs (the agent driven over ACP, the
   Agent Client Protocol) and headless runs. On the vendor's usage-limit
   signal (section 2), Aether stops the agent and replaces the run's
   container with one that mounts the next account's login. The login is
   mounted when the container is created, so it cannot be swapped in place.
   The new agent resumes its own session with its resume arguments and
   receives a continuation prompt. The run id, worktree, branch, room and
   swarm attempt stay the same. A room note on the run says: "Continued on
   <owner>'s account because <previous owner>'s usage ran out."
6. **Empty pool.** When no candidate qualifies (no account shared, every
   one exhausted, unreadable or no longer authorised), (A) launches on the
   requested account, as without fallback, and spends what usage is left
   there; refusing at 95% would block a solo user. The run's own vendor
   limit is the reactive half's to handle (B): there the run stops with the
   real reason and raises a **Needs you** card: "Usage limit reached on
   <owner>'s account; no fallback has usage left".
7. **Cost.** Metered cost stays with the account that spent it. A run keeps
   its account history as segments; a switch appends a segment and never
   rewrites an earlier one. Only headless Claude runs report metered cost
   today (`result` lines read by `internal/adapter/claude.go`), so cost rows
   are kept per run and segment.
8. **Authorisation.** A fallback launch is authorised exactly like a direct
   launch on that account (`AuthorizeLaunch`, built on
   `fallback.ResolveLaunchAccount`), against the launcher, or the swarm's
   accountable human for workers, with the share as it stands at that moment
   and the same refusals. There is one implementation of "may this member
   launch on that account".
9. **Visibility.**
   - run header: `On <owner>'s account (fallback)`;
   - `aether runs`: the actual account, marked `(fallback)`;
   - `aether swarm show`: each worker's actual account;
   - run snapshot: `requested_account_member_id` when the actual account
     differs;
   - a room note on every reactive switch.

## 2. Usage-limit signals

| Agent and mode | Signal Aether matches | Kind | Source read | (B) covered |
| --- | --- | --- | --- | --- |
| Claude Code, headless (`claude -p --output-format stream-json`) | Stream line `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected",...}}` with no overage in use (`overageStatus` not `allowed` or `allowed_warning`), followed by a `result` line with `is_error:true` and `api_error_status:429` whose text starts with an entry of Claude Code's exported `USAGE_LIMIT_ERROR_PREFIXES` ("You've hit your", "You've reached your", "You're out of extra usage") | Structured | Claude Code 2.1.112 `cli.js` and SDK `sdk.d.ts` (`SDKRateLimitEvent`, `USAGE_LIMIT_ERROR_PREFIXES`); strings confirmed in 2.1.296 | Yes |
| Claude Code, Enhanced (claude-agent-acp 0.86.0) | `session/prompt` JSON-RPC error `-32603` with `data.errorKind == "rate_limit"` and a message that, after `Internal error: `, starts with a `USAGE_LIMIT_ERROR_PREFIXES` entry. `errorKind` alone also covers transient 429s, which Claude Code words "Server is temporarily limiting requests (not your usage limit)". `usage_update._meta."_claude/rateLimit"` is not used: the adapter sends it only after a model reply earlier in the same turn | Structured | Adapter `dist/acp-agent.js` 1921-1932, 5100-5112; test `src/tests/acp-agent.test.ts:9062-9116` | Yes |
| Claude Code, TUI | `StopFailure` hook payload `error:"rate_limit"` with `last_assistant_message` starting with a usage-limit prefix | Structured field and vendor-exported prefix | Claude Code hook payload | TUI: decided in part (B) review; this row is updated with the outcome. |
| Codex, headless | With the ACP adapter installed, headless Codex runs over ACP ([Background runs](../enhanced-runs.md#background-runs)) and the Enhanced row applies. Without it, `codex exec --json` emits only text: `turn.failed.error.message` starting "You’ve hit your usage limit" (U+2019); `ThreadErrorEvent` drops the structured `codex_error_info` | Text | `codex exec` event types | No: text only, and Aether does not parse `codex exec` output |
| Codex, Enhanced (codex-acp 2.1.1 over `codex app-server` 0.159.x) | `session/prompt` error `-32603` with `data.codexErrorInfo == "usageLimitExceeded"`; every other terminal error ends the turn normally | Structured | codex-acp `src/CodexEventHandler.ts:975-980, 1132-1151`; codex `protocol/src/error.rs:478-480` | Yes |
| opencode V2 (2.0.18), Enhanced | `session/prompt` error `-32603` with `data.errorName == "provider.quota"`. Quota errors are never retried; `provider.rate-limit` is a throttle and is not matched | Structured | `packages/cli/src/acp/error.ts:78-85`, `packages/ai/src/provider-error.ts:136-201` | Yes |
| opencode V1 (1.18.32), Enhanced and headless | Subscription limits from non-Zen providers are retried, possibly for hours, and end as a generic `APIError` with no structured kind | None | opencode 1.18.32 source | No |
| pi (0.87.1, 1.1.0) | Headless: text only. Over ACP (pi-acp 0.0.34) the error text is dropped and the prompt ends `end_turn` | Text or none | pi and pi-acp source | No |
| omp (18.8.9) | ACP emits the error as plain message text. Headless `--mode json` carries `errorId & 0x80000`, but Aether does not parse omp output. omp's shared login is the whole `~/.omp/agent`, which holds `sessions/`, so a session cannot resume on another account | Text | omp source | No |

No agent binary was run against a live limit; the signals come from vendor
source at the versions named. Each matched signal gets a test fixture in
`internal/acphost` or `internal/adapter` testdata. Vendors can change these
signals; a changed signal degrades to today's behaviour (the run fails or
ends its turn with the vendor's own error), never to a wrong switch.

## 3. Quota read race

`internal/quota` reads the owner's login file read-only and never refreshes
or rewrites it, so reading it while a live container's agent refreshes its
token cannot corrupt the login. The worst case is reading a just-expired
token, which yields an unauthenticated or error row and is treated as usage
unknown (decision 3).

## 4. What a fallback can and cannot do

A fallback never widens who may launch on an account. It selects only among
accounts the launcher could already pick by hand, checks each with the same
share check at that moment, and records the choice. It cannot use an
account whose share was revoked. An admin gets no implicit access. A
member-defined agent still runs only on its member's own account. A swarm
worker is authorised against the swarm's accountable human.

What changes: a teammate's login can now be mounted into a run the launcher
started on another account, without the launcher picking it for that run.
The owner consented to that by sharing. Revocation still does not stop a run
already on the account, as today. A reactive switch is a new launch on the
new account and is authorised as one.

## 5. What changes where

| Area | Change |
| --- | --- |
| `internal/fallback` (new) | Policy, threshold, selection, and the one share check |
| `internal/sshd` | `run.launch`, account usage, callers of the share check |
| `internal/mission` | `worker.start`, create, integrator replacement, recovery; attempt `launch_account_id`; checks use the actual account |
| `internal/store` | Run columns `requested_account_member_id` and fallback policy; mission fallback policy; attempt launch account; for (B), account segments and per-segment cost rows |
| `internal/scheduler` | (B) container swap at run finalisation or prompt failure, crash-safe through a sidecar marker; the Needs you reason |
| `internal/acphost`, `internal/adapter` | Signal classification, with fixtures |
| `internal/protocol` | The wire fields in decisions 2, 4 and 9 |
| `cmd/aether` | `--fallback`, `--no-fallback`; account columns in listings |
| `web` | Fallback list in launch Options, header badge, Needs you card |
| `docs` | `teams.md`, `security.md`, `coordination.md`, `harnesses.md`, `dashboard-frontend.md`, `local-gateway.md`, `testing.md` |

## 6. Testing

- `internal/fallback`: selection unit tests (order, threshold, unreadable
  usage, revoked share, empty pool).
- `internal/mission`: a worker test that lands on the second account.
- `internal/scheduler`: the (B) swap on an Enhanced prompt failure and on a
  headless exit, with the fake runtime and `acpmock`.
- `internal/acphost`, `internal/adapter`: classifier fixtures for every
  covered row of section 2.
- Integration (Docker, `make test-integration`): quota stubbed so account A
  is exhausted; a swarm worker lands on account B.
- `web`: the Fallback list, the header badge and the Needs you card.
