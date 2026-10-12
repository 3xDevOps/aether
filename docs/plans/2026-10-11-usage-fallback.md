# Usage fallback to a teammate's agent account

**Status:** Implemented in PR #348 (https://github.com/3xDevOps/aether/pull/348).

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
5. **Reactive move (B)** for Enhanced runs (the agent driven over ACP, the
   Agent Client Protocol), headless runs, and Standard Claude Code runs. On
   the vendor's usage-limit signal (section 2), Aether stops the agent and
   replaces the run's container with one that mounts the next account's
   login. The login is mounted when the container is created, so it cannot
   be swapped in place. The new agent resumes its own session (ACP session
   resume; `claude -p ... --resume <session>`; Claude's TUI resume
   arguments) with the continuation prompt "Your previous turn stopped
   because <A>'s usage ran out. The run continues on <B>'s account.
   Continue the task where you stopped." The run id, worktree, branch, room
   and swarm attempt stay the same. A room note on the run says: "Continued
   on <B>'s account because <A>'s usage ran out." The next account is
   chosen as in (A), excluding the current account and every account the run
   already used. Under the launch authorisation lock the run's owner and the
   member whose home the container mounts (they differ after a handoff) are
   admitted again as for a launch, each candidate must be shared with both
   and launchable in that home, and the run records the new account; the
   lock is released before the container is replaced. A candidate qualifies
   only when the agent runs from the home's own installation there, or from
   the image when the replaced launch did too; (A) applies the same rule. A
   non-ACP run moves only with a stored agent session and resume arguments
   for its mode. The adapter reads only the current agent process's output,
   so an earlier process's limit is never read again.
6. **Empty pool.** When no candidate qualifies (no account shared, every
   one exhausted, unreadable or no longer authorised), (A) launches on the
   requested account, as without fallback, and spends what usage is left
   there; refusing at 95% would block a solo user. When (B) finds no
   account, an Enhanced or Standard run parks at `needs-attention` and a
   headless run fails, both with the reason "Usage limit reached on
   <owner>'s account; no fallback has usage left", which the dashboard's
   **Needs you** card shows. With fallback off the reason ends "fallback is
   off for this run", and a run that cannot move ends "cannot continue on
   another account: <real reason>". A move that fails after the old
   container is gone fails the run with "Usage limit reached on <A>'s
   account; continuing on <B>'s account failed: <error>"; an Enhanced
   session that cannot be restored there parks the run with "Usage limit
   reached on <A>'s account; the run is on <B>'s account and its session
   could not be restored: <error>". A candidate whose check fails is left
   out rather than failing the launch or move.
7. **Cost.** Metered cost stays with the account that spent it. A run keeps
   its account history in `run_account_segments`; a move appends a segment
   and never rewrites an earlier one. Only headless Claude runs report
   metered cost today (`result` lines read by `internal/adapter/claude.go`),
   so cost rows are kept per run and segment. A result is recorded in the
   segment that was current when it was published. Reports count a run
   once, on segment 0's account (unmetered there when only a later segment
   was measured), and each segment's usage on its own account.
8. **Crash safety.** The run's sidecar holds an `account_swap` marker,
   with an Enhanced session's pending continuation, from recording the
   account until the move finishes, and the headless usage-limit mark.
   Recovery resumes a run whose new container the sidecar names and sends
   the owed continuation, interrupts a run whose old container was removed
   before the sidecar named a new one or whose old container survives on a
   row already moved, and never creates a container itself, so a move never
   launches twice.
9. **Authorisation.** A fallback launch is authorised exactly like a direct
   launch on that account (`AuthorizeLaunch`, built on
   `fallback.ResolveLaunchAccount`), against the launcher, or the swarm's
   accountable human for workers, with the share as it stands at that moment
   and the same refusals. There is one implementation of "may this member
   launch on that account".
10. **Visibility.**
    - run header: `On <owner>'s account (fallback)`, updated live by the
      `run.account` event;
    - `aether runs`: the actual account, marked `(fallback)`;
    - `aether swarm show`: the account each attempt launched on
      (`launch_account_id`); a later move does not change it;
    - run snapshot: `requested_account_member_id` when the actual account
      differs (after a move, the first account; cleared if the run moves
      back to it);
    - a room note on every move, and an item-log notice on an Enhanced run.

## 2. Usage-limit signals

| Agent and mode | Signal Aether matches | Kind | Source read | (B) covered |
| --- | --- | --- | --- | --- |
| Claude Code, headless (`claude -p --output-format stream-json`) | Stream line `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected",...}}` with no overage in use (`isUsingOverage` false, `overageStatus` not `allowed` or `allowed_warning`), followed by a `result` line with `is_error:true` whose `result` text starts with an entry of Claude Code's exported `USAGE_LIMIT_ERROR_PREFIXES` ("You've hit your", "You've reached your", "You're out of extra usage", ...; the full list is in `internal/acphost/usagelimit.go`) | Structured | Claude Code 2.1.112 `cli.js` and SDK `sdk.d.ts` (`SDKRateLimitEvent`, `USAGE_LIMIT_ERROR_PREFIXES`); strings confirmed in 2.1.296 | Yes |
| Claude Code, Enhanced (claude-agent-acp 0.86.0) | `session/prompt` JSON-RPC error `-32603` with `data.errorKind == "rate_limit"` and a message that, after `Internal error: `, starts with a `USAGE_LIMIT_ERROR_PREFIXES` entry. `errorKind` alone also covers transient 429s, which Claude Code words "Server is temporarily limiting requests (not your usage limit)". `usage_update._meta."_claude/rateLimit"` is not used: the adapter sends it only after a model reply earlier in the same turn | Structured | Adapter `dist/acp-agent.js` 1921-1932, 5100-5112; test `src/tests/acp-agent.test.ts:9062-9116` | Yes |
| Claude Code, Standard (TUI) | `StopFailure` hook payload `error:"rate_limit"` with `last_assistant_message` starting with a `USAGE_LIMIT_ERROR_PREFIXES` entry, sent as `run.report` with `usage_limit:true` | Structured field and vendor-exported prefix | Claude Code 2.1.112 hook payload | Yes, when lifecycle reporting is on (`--conflict-coordination`, the default), since the hook is installed only then |
| Codex, headless | With the ACP adapter installed, headless Codex runs over ACP ([Background runs](../enhanced-runs.md#background-runs)) and the Enhanced row applies. Without it, `codex exec --json` emits only text: `turn.failed.error.message` starting "You’ve hit your usage limit" (U+2019); `ThreadErrorEvent` drops the structured `codex_error_info` | Text | `codex exec` event types | Over ACP, yes. `codex exec`: no, text only, and Aether does not parse its output |
| Codex, Enhanced and headless over ACP (codex-acp 2.1.1 over `codex app-server` 0.159.x) | `session/prompt` error `-32603` with `data.codexErrorInfo == "usageLimitExceeded"`; every other terminal error ends the turn normally | Structured | codex-acp `src/CodexEventHandler.ts:975-980, 1132-1151`; codex `protocol/src/error.rs:478-480` | Yes |
| opencode V2 (2.0.18), Enhanced and headless over ACP | `session/prompt` error `-32603` with `data.errorName == "provider.quota"`. Quota errors are never retried; `provider.rate-limit` is a throttle and is not matched | Structured | `packages/cli/src/acp/error.ts:78-85`, `packages/ai/src/provider-error.ts:136-201` | Signal matched, but `internal/quota` reads no opencode usage, so no candidate qualifies and the run ends with the no-fallback reason |
| opencode V1 (1.18.32), Enhanced and headless | Subscription limits from non-Zen providers are retried, possibly for hours, and end as a generic `APIError` with no structured kind | None | opencode 1.18.32 source | No |
| pi (0.87.1, 1.1.0) | Headless: text only. Over ACP (pi-acp 0.0.34) the error text is dropped and the prompt ends `end_turn`. Standard: the status extension sends no usage-limit report | Text or none | pi and pi-acp source | No |
| omp (18.8.9) | ACP emits the error as plain message text. Headless `--mode json` carries `errorId & 0x80000`, but Aether does not parse omp output. omp's shared login is the whole `~/.omp/agent`, which holds `sessions/`, so a session cannot resume on another account | Text | omp source | No |

No agent binary was run against a live limit; the signals come from vendor
source at the versions named. Each matched signal has a test fixture in
`internal/acphost/testdata` or `internal/adapter/testdata`, beside a
fixture of the near miss it must not match (a transient 429, another Codex
error, an opencode rate limit). Vendors can change these
signals; a changed signal degrades to the behaviour without fallback (the
run fails or ends its turn with the vendor's own error), never to a wrong
move.

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
already on the account, as today. A reactive move is a new launch on the
new account and is authorised as one. The signal comes from inside the
run's container (agent output, the hook payload, the ACP error), so the
agent can forge it and force a move, but only to an account the launcher may
already use that has usage left: the same reach as the launcher picking it.

## 5. What changes where

| Area | Change |
| --- | --- |
| `internal/fallback` (new) | Policy, threshold, selection, and the one share check |
| `internal/sshd` | `run.launch`, account usage, callers of the share check |
| `internal/mission` | `worker.start`, create, integrator replacement, recovery; attempt `launch_account_id`; checks use the actual account |
| `internal/store` | Run columns `requested_account_member_id` and fallback policy; mission fallback policy; attempt launch account; for (B), account segments and per-segment cost rows |
| `internal/scheduler` | (B) container replacement on a failed prompt, a headless exit or a Standard report, crash-safe through the `account_swap` sidecar marker; the no-fallback reason |
| `internal/acphost`, `internal/adapter`, `internal/agentstatus` | Signal classification, with fixtures; the `run.usage_limit` event |
| `internal/cost` | Cost recorded per segment, reports counting a run once |
| `internal/protocol`, `internal/events` | The wire fields in decisions 2, 4 and 10; `run.report` `usage_limit`; the `run.account` event |
| `cmd/aether` | `--fallback`, `--no-fallback`; account columns in listings |
| `web` | Fallback list in launch Options, header badge, Needs you card, `run.account` and `run.usage_limit` activity lines |
| `docs` | `teams.md`, `security.md`, `coordination.md`, `harnesses.md`, `failure-handling.md`, `enhanced-runs.md`, `dashboard-frontend.md`, `local-gateway.md`, `testing.md` |

## 6. Testing

- `internal/fallback`: selection unit tests (order, threshold, unreadable
  usage, revoked share, empty pool).
- `internal/mission`: a worker test that lands on the second account.
- `internal/scheduler` (`usage_fallback_test.go`): the (B) move on an
  Enhanced prompt failure, a headless exit and a Standard `StopFailure`
  report; an Enhanced run with no fallback; a restart in the middle of a
  move. Fake runtime and `acpmock`.
- `internal/acphost`, `internal/adapter`, `internal/agentstatus`:
  classifier fixtures for every matched row of section 2 and its near miss.
- `internal/cost`, `internal/store`: cost per segment.
- Integration (`make test-integration`, in-process runtime, stub quota):
  a swarm worker launches on account B because A is exhausted
  (`TestIntegrationWorkerFallsBackFromExhaustedAccount`); a headless Claude
  run moves from A to B mid-run (`TestIntegrationHeadlessFallback`).
- `web`: the Fallback list, the header badge and its `run.account` update,
  and the Needs you card.
