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
`range spans 401 days; the limit is 400`.

The [local gateway](local-gateway.md#post-apiv1method) lists the result
shape. The rest of this guide says what each number means.

## Which runs count

Run counts, the series, the heatmap, `by_workspace`, `by_agent`,
`durations`, `cost` and `recent` count only *top-level* runs: runs that are
not a swarm worker and not a swarm integrator of any generation. A swarm's
runs are counted once, under `swarms`, so a worker retry or a replaced
integrator never adds a run of its own. `needs_you` and `pull_requests`
count swarm runs as well.

## Numbers

| Field | Meaning |
| --- | --- |
| `totals.launched` | Runs created in the range. |
| `totals.completed`, `merged`, `failed`, `interrupted`, `abandoned` | Runs that finished in the range with that status. A run launched before the range and finished in it counts here. |
| `totals.killed` | Abandoned runs whose reason is `killed`: a member stopped them. Part of `abandoned`. |
| `totals.finished` | `completed` + `merged`. |
| `totals.active` | Runs launched in the range that are queued, provisioning, running or waiting for you now. |
| `previous` | The same totals for the same number of local days just before `from`. |
| `bucket`, `series` | One entry per local day, or per week starting Monday when the range is over 92 days, zero-filled. `start` is the local date the bucket starts; the first and last buckets are cut at `from` and `to`. |
| `heatmap` | The 182 local days ending on the day of the last instant before `to`, whatever `from` is. `runs` is how many runs were active that day: started (or created, if never started) before the day ended and finished (or still running) after it began. |
| `by_workspace`, `by_agent` | Runs launched, finished (`completed` or `merged`) and failed in the range, per workspace and per agent and model, most launched first. A `model` of `""` is the agent's default model. |
| `swarms` | Swarms created in the range, scoped by their accountable member: how many, how many are now completed or cancelled, `agents` (distinct worker and integrator runs they started that still exist) and `tasks_accepted`. |
| `needs_you` | Runs in scope waiting for a member right now, as on the Board, swarm runs included. Ignores the range. |
| `recent` | Up to 50 runs that finished in the range, newest first, with the last pull request the run recorded. |

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
cover a run's last session: from leaving `queued`, or from its latest
relaunch, to finishing. A run whose record starts mid-session is left out;
`state_runs_measured` is how many runs are in. Percentiles are nearest-rank, and every
duration is `0` when nothing was measured.

## Cost

`cost` sums the recorded usage of runs that finished in the range, across
every account that paid for a run that moved between accounts. A run's owner,
not the paying account, decides whether it is in scope. Only headless Claude
runs are metered; any other run is counted in `unmetered_runs`. An unmetered
run's cost is unknown, not zero, so `cost_usd` and the token counts are a
floor.

## Pull requests

`pull_requests.opened` counts pull requests opened through `run.pr.create`
in the range. `merged` counts those whose last observed state is merged and
whose GitHub merge time is in the range. The server only learns a merge
when `run.pr.status` looks the pull request up; it does not poll GitHub. A
pull request an agent opened with `gh` directly is not counted.

## Recorded from this version on

Older servers kept only each run's current status and no pull requests, so
two measurements start when the server is upgraded:

- `run_state_transitions`: one row per status or reason change of a run,
  written in the same transaction as the change.
- `run_pull_requests`: one row per pull request opened through
  `run.pr.create`, with the state the server last observed.

`coverage.state_times_since` and `coverage.pull_requests_since` are the
oldest such rows, `""` while there are none. Numbers before those times
leave out what was not recorded. Deleting a run deletes its rows.
