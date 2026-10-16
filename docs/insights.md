# Insights

Insights are statistics about what your agents did over a date range: runs
launched and how they ended, swarms, time spent, cost and pull requests. The
server computes them from its database (`<data-dir>/aether.db`) on each
`insights.get` call; nothing is sent anywhere else.

A *run* is one agent session in its own container, started in a
*workspace* (a repository plus its environment). A *swarm* is a group of
runs: one integrator run splits the work into tasks and launches a worker
run for each, retrying a worker when it fails.

## The request

```http
POST /api/v1/insights.get
Content-Type: application/json
Authorization: Bearer <local-gateway-token>

{"workspace_id":"","scope":"mine","from":"2026-10-09T00:00:00-07:00","to":"2026-10-16T00:00:00-07:00","time_zone":"America/Los_Angeles"}
```

| Param | Meaning |
| --- | --- |
| `workspace_id` | One workspace, or `""` for every workspace. An unknown workspace is error `-32000`. |
| `scope` | `mine`: runs you own and swarms you are accountable for. `everyone`: every member's, summed; never broken down per member. Any member who can see runs can ask for either. |
| `from`, `to` | RFC3339 times; the range is `[from, to)`. `to` must be after `from` and the range at most 400 local days. |
| `time_zone` | IANA name, such as `Europe/Berlin`, that days are counted in, DST included. `""` is UTC; `Local` and unknown names are rejected. The server binary carries its own zone database, so a host without system tzdata resolves every name. |

A bad param is error `-32602` with the reason, for example
`range spans 401 days; the limit is 400`. The range, its comparison period
and the heatmap must fall between 1677 and 2262, the times the database can
store.

The [local gateway](local-gateway.md#post-apiv1method) lists the result
shape. The rest of this guide says what each number means.

## Which runs count

Run counts, the series, the heatmap, `by_workspace`, `by_agent`,
`durations`, `cost` and `recent` count only *top-level* runs: runs that are
not a swarm worker and not a swarm integrator of any generation. A swarm's
runs are counted once, under `swarms`, so a worker retry or a replaced
integrator never adds a run of its own. `needs_you` and `pull_requests`
count swarm runs as well.

Whether a run belongs to a swarm is stored on the run (`runs.swarm_run`)
when the run is created, so deleting the swarm does not turn its runs into
top-level runs. Upgrading marks existing runs from the swarm records still
in the database; the runs of a swarm deleted before the upgrade count as
top-level.

## Numbers

| Field | Meaning |
| --- | --- |
| `totals.launched` | Runs created in the range. |
| `totals.completed`, `merged`, `failed`, `interrupted`, `abandoned` | Runs that finished in the range with that status. A run launched before the range and finished in it counts here. |
| `totals.killed` | Abandoned runs whose reason is `killed`: a member stopped them. Part of `abandoned`. |
| `totals.finished` | `completed` + `merged`. |
| `totals.active` | Runs launched in the range that are queued, provisioning, running or waiting for you now. |
| `previous` | The same totals for the period just before `from`: the same number of local days when `from` and `to` are both local midnights, otherwise the same length of time, so "the last 7 days ending now" is compared with the 168 hours before it. |
| `bucket`, `series` | One entry per local day, or per week starting Monday when the range is over 92 days, zero-filled. `start` is the local date the bucket starts; the first and last buckets are cut at `from` and `to`. |
| `heatmap` | The 182 local days ending on the local day of `to` minus one nanosecond, whatever `from` is: a `to` at local midnight ends it on the day before, a `to` of now ends it today. `runs` is how many runs were active that day: started (or created, if never started) before the day ended and finished (or still running) after it began. |
| `by_workspace`, `by_agent` | Runs launched, finished (`completed` or `merged`) and failed in the range, per workspace and per agent and model, most launched first. A `model` of `""` is the agent's default model. |
| `swarms` | Swarms created in the range, scoped by their accountable member: how many, how many are now completed or cancelled, `agents` (distinct worker and integrator runs they started that still exist) and `tasks_accepted`. |
| `needs_you` | Runs in scope waiting for a member right now, as on the Board, swarm runs included. Ignores the range. |
| `recent` | Up to 50 runs that finished in the range, newest first, with the last pull request the run recorded. `title` is the run's title, or the first non-blank line of its task when it has none, cut to 120 characters, as the dashboard names runs. |

## Durations

`durations` covers runs that finished in the range.

- `median_run_seconds`, `p90_run_seconds`: from start (or creation, if the
  run never started) to finish. `runs_measured` is how many runs.
- `median_working_seconds`: a run's total time in `running`.
- `median_waiting_seconds`: a run's total time in `needs-attention`.
- `median_response_seconds`: every stretch of `needs-attention` that ended
  with the run going back to `running`, so how long a run waited for an
  answer.

The last three come from the status changes the server records. They
cover a run's last session that spent time in `running`: from leaving
`queued`, or from a relaunch, to finishing. A relaunch that failed and was
rolled back before the agent ran leaves the session before it measured.
A run whose record starts mid-session is left out; `state_runs_measured`
is how many runs are in. Percentiles are nearest-rank, and every duration
is `0` when nothing was measured.

## Cost

`cost` sums the recorded usage of runs that finished in the range, across
every account that paid for a run that moved between accounts. A run's owner,
not the paying account, decides whether it is in scope. Only headless Claude
runs are metered; any other run is counted in `unmetered_runs`. An unmetered
run's cost is unknown, not zero, so `cost_usd` and the token counts are a
floor.

## Pull requests

`pull_requests.opened` counts pull requests opened through `run.pr.create`
in the range, each repository and number once however many runs recorded
it. `merged` counts those whose last observed state is merged and
whose GitHub merge time is in the range. A pull request an agent opened
with `gh` directly is not counted.

The server learns a merge only when `run.pr.status` is called for the run;
it does not poll GitHub, and `insights.get` never calls it. While the run's
container is up, the call runs `gh` in it, as before. Once the run has
finished (completed, merged, failed or abandoned) and its container is
gone, `run.pr.status {"run_id":"<run-id>"}` answers from the recorded
pull requests instead: each one not yet seen merged, open or closed, since
a closed pull request can be reopened and merged, is read again from
`GET /repos/<owner>/<name>/pulls/<number>` with the `gh` login the
container used (`~/.config/gh/hosts.yml` in the home it mounted, which is
the launching member's even on a shared agent login), and the new state is
recorded. A recorded merge is never set back to open.
`target.repository` narrows it to one repository; `expected` is not
checked. A finished run with nothing recorded, and a queued,
provisioning, paused or stopping run, still fails with `-32002` because
it has no live environment. The dashboard's publish dialog calls
`run.pr.status` only for a live run, so a pull request merged after its run
finished stays `open` here until a client makes that call.

## Recorded from this version on

Older servers kept only each run's current status and no pull requests, so
these measurements start when the server is upgraded:

- `run_state_transitions`: one row per status or reason change of a run,
  written in the same transaction as the change.
- `run_pull_requests`: one row per pull request opened through
  `run.pr.create`, with the state the server last observed.
- `runs.swarm_run`: set when a swarm creates the run, and at upgrade from
  the swarm records that exist then.

`coverage.state_times_since` and `coverage.pull_requests_since` are the
oldest such rows, `""` while there are none. Numbers before those times
leave out what was not recorded. Deleting a run deletes its rows.
