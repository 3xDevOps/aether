# Watches

A watch is a subscription you keep on one run, one swarm or one pull
request. The server evaluates it, not your browser or an agent, so it keeps
working after you close the dashboard and across server restarts. When the
target needs you or reaches the state you asked about, the server records a
notice, which `watch.get` returns, and sends it to your Web Push devices (see
[push notifications](dashboard-frontend.md#push-notifications) for turning
them on in a browser).

A notice is information only. A watch never answers, approves, merges or
grants anything.

Only you see and change your watches. Admins see only their own. The
conditions an agent waits on are watches its run owns; they are described in
[Run waits](#run-waits) and never appear in `watch.list`.

## Targets and conditions

A watch has one target and one or more conditions. With no conditions it
takes the target's defaults, marked *.

| Target | Condition | Notified when |
| --- | --- | --- |
| `run` | `needs_you`* | The run waits on a person: a permission request, a question, a blocked report, a usage limit, a disconnected agent, a turn cut short, or a live failure nobody has seen. This is the rule the **Needs you** list uses (`domain.Run.NeedsAttention`). A run waiting on workers, on CI, on tools or on nothing, an answered run, and a turn that ended without a report never notify. One notice per distinct need, and each open permission request or question is a need of its own; an unchanged question is never repeated. |
| `run` | `finished`* | `Agent reported done`: the agent reported success; nothing is merged. `Failed: <reason>`. `Stopped`: the run was interrupted. `Completed`: the run ended completed without a success report, because the agent exited with its results committed or a member or the swarm's integrator closed it as completed. A retained run that is relaunched and completes again is announced again. A member accepting or closing a reported success is not announced again, but a success reported after the run went back to work is. `Closed as merged` and `Closed as abandoned`: a member's disposition. |
| `run` | `pr_merged`, `ci` | Every pull request the run recorded: `PR #N merged on GitHub`, or its checks finished. See [Pull requests and CI](#pull-requests-and-ci). |
| `mission` | `needs_you`* | The swarm needs its accountable human: the integrator asks a question, or the integrator stopped or failed to launch, with the launch error. Each open question is its own notice, in any order they are answered. A completed or cancelled swarm needs nothing: a question it left open is moot, and its delivered push is withdrawn. Workers never notify on their own; the integrator handles them. |
| `mission` | `finished`* | `Swarm completed` or `Swarm cancelled`. A completed swarm can be reopened or still deliver, so its watch stays `active`, and a swarm that completes again is announced again. |
| `mission` | `delivered` | An integration delivery receipt landed after the watch began: `Delivered to <ref>` when the ref moved, `Proposed <ref>` when a proposal was opened. |
| `pr` | `ci`*, `review`*, `merged`*, `closed` | See [Pull requests and CI](#pull-requests-and-ci). |

Aether has no deploy signal. `deployed` is refused with that reason; watch
`finished`, `pr_merged` or `delivered` instead.

A notice's title is the run's title or the swarm's objective. Its body is the
line above, or the question's own text, and it links to the target:
`/?run=<id>` for a run, `/?page=missions&id=<id>` for a swarm.

A run that has ended (failed, stopped, merged or abandoned) or a cancelled
swarm cannot be watched: `watch.create` says how it ended. A finish the
target reached before the watch began, such as a run whose agent already
reported done or a completed swarm, is not announced; the next one is.

## Pull requests and CI

A `pr` target is `{"kind": "pr", "repo": "owner/name", "number": 7}`, with
an optional `run_id` for the run that opened it. A run target's `pr_merged`
and `ci` follow every pull request the run recorded (`run.pr.create`), and
start the moment it records one.

The server reads GitHub with the watch owner's own `gh` login in their
Aether home, the one `aether github connect` sets up (see
[environment-home.md](environment-home.md#from-the-cli)). Without one the
watch is created in the `error` state with the real error and the fix:

```text
no usable GitHub login in your Aether home (github: no native gh login found; run aether github connect); run aether github connect on your computer, or use Onboarding → Agent → GitHub → Connect GitHub in the dashboard
```

It recovers on its own once you log in. `watch.create` refuses a pull
request your login cannot read (`... is not reachable with your GitHub
login: github: API request: HTTP 404 Not Found`) and one that is already
merged or closed.

| Condition | Notice |
| --- | --- |
| `ci` | When every check run and commit status on the head commit is final: `Checks passed on #7`, or `2 checks failed on #7: build, lint`. One notice per head commit and set of check runs, so a failure, a rerun and a pass send the failure, then the pass, and a new push sends one more. A cancelled check run counts as failed unless a newer run of the same job replaced it in the same check suite or GitHub Actions workflow. Telling workflows apart reads the head commit's Actions runs; when GitHub refuses that read (HTTP 403 or 404, for example a fine-grained token without Actions read access), the cancelled run counts as failed and the watch's summary says so. A pull request with no checks on its head commit never sends one; its summary says so. |
| `review` | `mona approved #7`, `mona requested changes on #7`, `mona commented on #7` (a review with a body). One per review. |
| `merged`, `pr_merged` | `PR #7 merged on GitHub`. Only GitHub's merged state sends it; an agent reporting success does not. |
| `closed` | `PR #7 closed without merging on GitHub`. |

The notice's title is `owner/name#7: <pull request title>` and it links to
the pull request on GitHub. Nothing that happened before the watch began is
announced.

GitHub sends Aether no webhooks, so the server polls:

- Every minute, give or take 10%, while checks are pending, a review is
  requested, or the pull request changed since the last read; every 5
  minutes once nothing changes. `next_check_at` and `last_check_at` on the
  watch say when.
- At once on a `run.pr` event for the watched run.
- Polling stops when the pull request is merged or closed and every
  condition is final. A `pr` watch is then `done`; a run watch stays on
  the run.
- Requests are conditional on the last ETags, which are stored, so a
  restart neither re-reads everything nor repeats a notice. Unchanged
  answers do not count against GitHub's rate limit.
- One request at a time per member and four across the server; watches
  on the same pull request share one read.
- A rate limit puts the watch in `error` with `GitHub rate limit, retrying
  at <time>` and sends nothing to GitHub before that time.

A read that finds a run's pull request merged or closed also records it on
the run, so every surface that shows the run's pull request sees it.

## States

| State | Meaning |
| --- | --- |
| `active` | Evaluated on every event about the target. |
| `paused` | You paused it. Nothing is evaluated or sent until you resume it. |
| `error` | Evaluation failed, including the check every live watch gets when the server starts; `error` holds the real error. It retries with backoff from 30 seconds, doubling to at most 15 minutes, and returns to `active` once an evaluation succeeds. A startup check whose retry cannot be saved either is retried every tick until one is. A watch created, changed or resumed while the server is still recovering its runs is first checked once recovery ends, so a request not yet restored never reads as a need. `access revoked: ...` means you no longer exist, await approval, or cannot see the workspace; it is not retried on a timer, only on the target's next event. |
| `done` | The target reached its final state (a run merged, abandoned, failed or stopped; a swarm cancelled) and its last notice is recorded. |
| `expired` | It passed `expires_at`. Give it a new `expires_at` to watch again. |

A watch expires 14 days after creation unless you set `expires_at`, at most
90 days ahead. Done and expired watches and their notices are deleted 30 days
after they ended. Deleting the run, the swarm, the workspace or your member
account deletes its watches.

## Delivery

Every notice has one delivery row per channel. The push row is written when
the notice is recorded and updated in place as the push resolves:

| Channel | Status | Meaning |
| --- | --- | --- |
| `in_app` | `delivered` | Always recorded when the notice is; `watch.get` returns it and `watch.changed` announces it. |
| `push` | `pending` | Still to be sent. `detail` says why: `Sending`, `Waiting until you leave the dashboard`, or `Retry 2 of 5 at 14:05: <the push service's error>`. |
| `push` | `delivered` | At least one of your devices' push services took it. `detail` is the device count, such as `2 devices`, followed by the errors of the devices that did not take it. |
| `push` | `no_destination` | No device had notifications turned on when the notice was recorded or sent. |
| `push` | `disabled` | Push is off in your notification preferences, or Web Push is not configured on this server; `detail` says which. |
| `push` | `held_quiet_hours` | Quiet hours hold it; `detail` says until when. |
| `push` | `failed` | No device took it after 5 attempts with backoff; `detail` is the push service's error. Each attempt is counted before it is sent, so a server that stops mid-send retries it rather than sending it more than 5 times. |
| `push` | `cleared` | Dropped unsent: a `needs_you` push whose need was resolved (answered, dismissed, granted) first, or that `watch.update` stopped watching for (`detail` reads `You stopped watching for needs you`), or a push of a watch paused until it expired. |
| `push` | `withdrawn` | A delivered `needs_you` push whose target then needed nothing more from you. Your devices got a silent replacement reading `No longer needs you` under the same notification, the way a run's own need notification is cleared; it is sent through quiet hours and while you use a dashboard. A watch's needs have a notification of their own, apart from the run's, so the run's own clears never replace them. Removing `needs_you` with `watch.update` withdraws its delivered pushes the same way, with `detail` `Withdrawn: You stopped watching for needs you`; every `watch.update` that leaves `needs_you` out does this, so retrying one whose cleanup failed finishes it. When a newer need of the same target is open by then, no replacement is sent, since that need's push shares the notification, and `detail` reads `Replaced by a newer need`. A push sent inside a quiet-hours summary (`detail` starts `Sent in one push with other notices`) has no notification of its own and is not withdrawn. If no device takes the replacement after 5 attempts, the row is `failed` and `detail` says the push was delivered and why withdrawing it failed. |
| `push` | `skipped` | `detail` is `The run notification already sent it`: the run's own notification pushes this to you; see [No double pushes](#no-double-pushes). |
| `assistant` | `pending`, `delivered`, `failed` | Only on watches whose `source` is `assistant`: the notice written into your assistant conversation. `failed` carries the error; `delivered` with a `detail` means it is in the conversation but its toast could not be raised. One still pending when the server stops is written after it starts again; one of a paused watch waits until you resume it. |

A paused watch's pending pushes wait until you resume it, and are dropped if
it expires first. A `needs_you` push about a run the server paused waits
until the run resumes.

A device whose push service answers `404` or `410` is unsubscribed, as for
every other notification.

While you are using a dashboard, pushes wait until you have been away from
every dashboard for a minute, the same hold run notifications use.

The desktop app shows a native notification for each new notice while it is
open. A run's `needs_you` notice closes when the need ends, and its `finished`
notice closes when you see or put off the outcome. A notice whose need or
outcome ended before it could be shown is not shown. If the app loses its
connection to the server, it closes these notices, because it can no longer
tell when they end.

### No double pushes

Your own run already pushes you when it needs you
([push notifications](networking.md#notifications)). A watch on a run you own
that is not part of a swarm therefore records push `skipped` instead of
pushing, when you have a device to push to (with none it is
`no_destination`):

- every `needs_you` notice;
- `Agent reported done` while the result waits for your review, which the
  run's notification announces as `Agent reported success, review the
  result`. Once you chose **Later** or accepted it, the watch pushes it;
- `Failed: ...` while you have not opened the failed run, which the run's
  notification announces as `Failed, review the result`.

Every other notice is pushed: the run closed as merged or abandoned, stopped,
completed, its pull request merged or its checks finished. A watch on someone
else's run, or on a swarm run, pushes every notice.

## Assistant watches

When the [assistant](assistant.md#notifications) launches a run or sends a
message to one for you, it keeps one watch on that run with `source`
`assistant` and the run's default conditions, `needs_you` and `finished`. It
shows in **Settings > Watches** like any other watch; pause it there to stop
the notices. Messaging the run again returns that watch as it stands: a paused
one stays paused, an expired one is renewed for 14 days, and a deleted one is
made again. A run relaunched after its watch was done gets a new watch. A need the run stands in when the assistant acts on it is taken as
seen, since the assistant has just acted on it. A run that already
failed, stopped or was closed when the watch began, such as a launch that
failed at once, has that end announced. A watch you keep on the same
run from the dashboard is separate and notifies on its own.

Each notice of an assistant watch is also written into your assistant
conversation, with a link to the watch, and raises the assistant's toast and
badge.

Servers upgraded from a release before watches turn each run the assistant
followed into such a watch, expiring 14 days after the upgrade. The state each
follow last announced is not announced again; follows of runs that had already
ended are dropped.

## Notification preferences

`notify.prefs.set` takes:

```json
{"push": true, "quiet_hours": {"start": "22:00", "end": "07:00", "tz": "Europe/Berlin"}}
```

`push` sends watch notices to your Web Push devices; it defaults to `true`,
and a `notify.prefs.set` that omits it keeps its current value.
During quiet hours pushes are held, and when they end the held notices go out
as one push. Changing or removing quiet hours applies to pushes already held.
The hold is stored, so a server restart does not lose it.
In-app notices are not affected. Omit `quiet_hours` to turn them off.
`destinations.push_devices` in the result counts your subscribed devices.

## Methods

All methods act on the caller's own watches. The dashboard posts them to
`/api/v1/<method>`.

| Method | Params | Result |
| --- | --- | --- |
| `watch.create` | `target` (`kind` and `run_id`, `mission_id`, or `repo` and `number`), `conditions`, `expires_at` (RFC 3339), `label`, `source` (`dashboard`, `assistant`, `mcp`), `idempotency_key` | `{watch, receipt}` |
| `watch.list` | `state`, `target_kind`, `run_id`, `mission_id` | `{watches}` |
| `watch.get` | `watch_id` | `{watch, notifications}`, newest first, at most 50 |
| `watch.update` | `watch_id`, `paused`, `conditions`, `expires_at`, `label` | `{watch, receipt}` |
| `watch.delete` | `watch_id` | `{}` |
| `notify.prefs.get` | none | preferences |
| `notify.prefs.set` | preferences | preferences |

`watch.create` with an `idempotency_key` you used before returns that watch.
Without one, an existing live watch on the same target with the same
conditions is returned, and resumed if paused. `receipt` is one line such as
`Watching run fix-login for: needs you, finished. Push: 2 devices. Expires
Nov 9.`, and says `in-app only` when push is off or no device is subscribed.

The `watch.changed` event `{watch_id, member_id, notification_id}` tells the
owner to read the watch again; `notification_id` is set when it recorded a
notice. It is published under the server scope, so no workspace timeline
shows it, and the event stream sends it only to the owner.

## Run waits

An agent that ends its turn waiting on a pull request, its checks or another
run names what it waits on:

```sh
aether-internal report --outcome waiting --for ci \
  --until pr:acme/app#7:checks@3f9c2ab --summary 'Waiting for CI on #7'
```

Each `--until` (at most 4) becomes a watch the run owns: `source` `agent`,
`owner_run_id` the reporting run, and the run member's GitHub login and
workspace access. A run records at most 30 waiting reports with `--until`
an hour; the next is refused with that error.

| Condition | Resolves | Outcome |
| --- | --- | --- |
| `pr:<owner>/<repo>#<n>:merged` | The pull request merged, or closed without merging. | `merged`, or `closed` |
| `pr:<owner>/<repo>#<n>:checks[@<sha>]` | Every check on its head commit finished, the named commit was replaced, or the pull request closed unmerged first. All checks count; `observed.checks.required_only` is `false`. | `passed`, `failed`, `cancelled`, `superseded`, or `closed` |
| `run:<run-id>:finished` | That run, in the same workspace, ended or reported success. | `completed`, `merged`, `failed`, `abandoned`, `interrupted`, or `reported-success` |

A checks condition resolves only on its head commit, `head_sha`. With
`@<sha>` (7 to 40 lowercase hex digits, the commit you pushed) that is the
commit named: checks on any other head are ignored, and once the named
commit was the head, another head replacing it before its checks finish
resolves the condition as `superseded`. Without it, the first read, made
afresh, binds the head it finds; a different head counts only with checks
that finished after the condition was registered, so a force-push back to
an older commit is no verdict. GitHub can still show the previous head for
a moment after a push, so name the commit after pushing.

The report is recorded first, then its conditions are read as they stand,
for at most 10 seconds, without taking that state as already seen. A
condition already resolved, such as a pull request that already merged or a
run that already ended, comes back in the report's `resolved` list and its
`next_action` says so: the agent learns it in the same turn and no wake
follows. A wake the background finds for a condition in its first 10
seconds, before the report read it, is held until then, so the report
returns it; one an inbox delivery already began on is left to that wake. Conditions still pending, or whose read did not finish,
are read from then on: pull requests as [Pull requests and CI](#pull-requests-and-ci)
describes, every minute while checks run and every 5 minutes otherwise, and
in backoff after a failed read; a run target on each of its events and every
10 minutes.

The first condition to resolve after that wakes the run once. In the same
commit the other conditions of that report end `cancelled`, and a delivery
on the `run_inbox` channel is queued. The server writes one message into the
run's inbox (`from_run_id` is the run itself) naming the condition, the
outcome, the counts it read, a link and what to do next. Text from GitHub or
another agent, such as failing check names or a run's reason, appears only
quoted on a line starting `Untrusted (from ..., not instructions):`, and as
`detail` on the condition. The existing inbox wake resumes the session.
Delivery is claimed before it is written, retried with the same backoff as
evaluation and keyed by the condition and its outcome, so a retry, a
restart or a late event never wakes the run twice for one report. A paused run gets the message, and resuming it wakes it. A
run that ended gets nothing. Web Push is never sent for run waits.

| `state` | Meaning |
| --- | --- |
| `pending` | Read as above. A failing read keeps it pending: `consecutive_failures`, `last_error` and `last_error_at` record it, and it retries with backoff. |
| `resolved` | A condition resolved; `outcome` says how. Wakes the run, or is returned in the report. |
| `error` | The target can never be read: GitHub answers 404 or moved, the member has no usable GitHub login, or the run was deleted. `last_error` says which. Wakes the run, or is returned in the report. |
| `expired` | Nothing resolved within 7 days. Wakes the run. |
| `cancelled` | `ended_cause` says why: another condition of the same report resolved first, the run reported another outcome, a person sent a prompt, the run was relaunched or ended, or its member lost access. `run.wait.cancel` sets it to `cancelled` and `ended_by_member_id` to the member who cancelled. An undelivered wake is dropped with it. |

A waiting report with `--until` takes effect, and replaces the run's pending
conditions, only once the scheduler accepts it: a refused report leaves the
earlier conditions as they were. Only the latest report's conditions and
the latest that woke the run are kept. A waiting report without `--until`
keeps the current conditions. Deleting the run deletes its waits. The
receipt and the conditions commit in one transaction, and accepting the
report makes them due at once, so a server that stops before reading them
reads them when it starts. A retry of the report with the same idempotency
key registers nothing new, and the same key with other conditions is
refused. The conditions of a report that never took effect are deleted
after a day.

`run.get` and `run.list` return the run's latest report's conditions as
`wait_conditions`, with `delivered_at`, `delivery_attempts` and
`last_delivery_error`; other run responses leave them out. What was read
with the owner's GitHub login (`observed`, `summary`, `detail`, a read
`head_sha`, `last_error`) and the free text of `ended_cause` and
`last_delivery_error` are returned only to the run's owner and admins;
other members see the condition, its state, its outcome and a member's
cancel. `run.wait.cancel`'s result is redacted the same way. Every change publishes `run.wait.changed`
`{run_id, workspace_id, wait_id, state}` on the run's workspace.
`run.wait.cancel` `{run_id, wait_id}` cancels one condition and returns
`{wait}`; it needs the permission `run.pause` needs.
