# Watches

A watch is a subscription you keep on one run or one swarm. The server
evaluates it, not your browser or an agent, so it keeps working after you
close the dashboard and across server restarts. When the target needs you or
reaches the state you asked about, the server records a notice, which
`watch.get` returns, and sends it to your Web Push devices (see
[push notifications](dashboard-frontend.md#push-notifications) for turning
them on in a browser).

A notice is information only. A watch never answers, approves, merges or
grants anything.

Only you see and change your watches. Admins see only their own.

## Targets and conditions

A watch has one target and one or more conditions. With no conditions it
takes the target's defaults, marked *.

| Target | Condition | Notified when |
| --- | --- | --- |
| `run` | `needs_you`* | The run waits on a person: a permission request, a question, a blocked report, a usage limit, a disconnected agent, a turn cut short, or a live failure nobody has seen. This is the rule the **Needs you** list uses (`domain.Run.NeedsAttention`). A run waiting on workers, on CI, on tools or on nothing, an answered run, and a turn that ended without a report never notify. One notice per distinct need; an unchanged question is never repeated. |
| `run` | `finished`* | `Agent reported done`: the agent reported success; nothing is merged. `Failed: <reason>`. `Stopped`: the run was interrupted. `Completed`: the agent exited with its results committed. `Closed as merged` and `Closed as abandoned`: a member's disposition. |
| `run` | `pr_merged`, `ci` | The run's pull request merged on GitHub, or its checks finished. Not supported yet: refused until the pull request evaluator ships. |
| `mission` | `needs_you`* | The swarm needs its accountable human: the integrator asks a question, or the integrator stopped or failed to launch. Each question is its own notice. Workers never notify on their own; the integrator handles them. |
| `mission` | `finished`* | `Swarm completed` or `Swarm cancelled`. A completed swarm can be reopened or still deliver, so its watch stays `active`, and a swarm that completes again is announced again. |
| `mission` | `delivered` | An integration delivery receipt landed after the watch began: `Delivered to <ref>` when the ref moved, `Proposed <ref>` when a proposal was opened. |
| `pr` | `ci`*, `review`*, `merged`*, `closed` | Not supported yet: refused until the pull request evaluator ships. |

Aether has no deploy signal. `deployed` is refused with that reason; watch
`finished`, `pr_merged` or `delivered` instead.

A notice's title is the run's title or the swarm's objective. Its body is the
line above, or the question's own text, and it links to the target:
`/?run=<id>` for a run, `/?page=missions&id=<id>` for a swarm.

A run that has ended (failed, stopped, merged or abandoned) or a cancelled
swarm cannot be watched: `watch.create` says how it ended. A finish the
target reached before the watch began, such as a run whose agent already
reported done or a completed swarm, is not announced; the next one is.

## States

| State | Meaning |
| --- | --- |
| `active` | Evaluated on every event about the target. |
| `paused` | You paused it. Nothing is evaluated or sent until you resume it. |
| `error` | Evaluation failed; `error` holds the real error. It retries with backoff from 30 seconds, doubling to at most 15 minutes, and returns to `active` once an evaluation succeeds. `access revoked: ...` means you no longer exist, await approval, or cannot see the workspace; it is not retried on a timer, only on the target's next event. |
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
| `push` | `cleared` | Dropped unsent: a `needs_you` push whose need was resolved (answered, dismissed, granted) first, or a push of a watch paused until it expired. |

A paused watch's pending pushes wait until you resume it, and are dropped if
it expires first. A `needs_you` push about a run the server paused waits
until the run resumes.

A device whose push service answers `404` or `410` is unsubscribed, as for
every other notification.

While you are using a dashboard, pushes wait until you have been away from
every dashboard for a minute, the same hold run notifications use.

## Notification preferences

`notify.prefs.set` takes:

```json
{"push": true, "quiet_hours": {"start": "22:00", "end": "07:00", "tz": "Europe/Berlin"}}
```

`push` sends watch notices to your Web Push devices; it defaults to `true`.
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
