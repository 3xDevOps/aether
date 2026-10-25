# Insights

**Insights** in the dashboard sidebar shows what your agents did over the last
7 days: runs and swarms that finished, how many ended in a merged pull
request, time spent, tokens and cost. The server computes the numbers from its
own database, and nothing is sent anywhere else.

A **run** is one agent session in its own container. A **swarm** is a group of
runs: one integrator run splits an objective into tasks and starts worker runs
for them (see [teams.md](teams.md#swarms)).

## What the page shows

The page opens on your active workspace. With two or more workspaces, a
selector at the top switches workspace or shows **All workspaces**. It has two
blocks of tiles, each with a daily chart:
**My statistics** counts runs you own and swarms you are accountable for, and
**Team statistics** counts every member's, with each member's color in the
chart. Below them:

| Section | What it shows |
| --- | --- |
| Runs / swarms completed | How many ended, plus how many failed or were abandoned |
| Success rate | Share of finished runs and swarms that merged a pull request |
| Swarm tasks accepted | Tasks the integrators of your swarms accepted |
| Total tokens used | Tokens across all runs, and the agent that used the most |
| Finished work | Your runs that ended in the last 7 days, newest first, up to 50 |
| Last 26 weeks | A heatmap of how many runs were active each day |
| By workspace, By agent | Runs launched, finished and failed per workspace, and per agent and model |
| Time | Median and slowest run times, working time, waiting time and your response time |
| Usage | Metered cost and tokens |

Days are counted in your browser's time zone, or UTC if the browser reports
none. The sections below the tiles cover only your runs.

## Which runs count

Run counts include only top-level runs: runs that are not part of a swarm. A
swarm's runs are counted once, as the swarm, so a worker that is retried or an
integrator that is replaced never adds a run. Two numbers include swarm runs:
**Total tokens used** and the count of runs that need you, which follows the
[Needs you](failure-handling.md#needs-you) rule.

A run counts on the day it finished, even if it started before the 7 days. A
relaunched run is one run: while it is live again it counts in no finished
total, and after it closes again it counts once, on the day of its latest
finish, with that outcome.

How a run or swarm ended:

| Counted as | Run | Swarm |
| --- | --- | --- |
| Completed | `completed` or `merged` | completed |
| Failed | `failed` | never: a swarm has no failed state |
| Abandoned | `abandoned` or `interrupted` | cancelled |

A run you stop yourself is abandoned.

## Success rate

A run or swarm succeeds when it ended with a merged pull request. For a run,
the run's status is `merged` or a pull request it opened is merged. For a
swarm, any of its runs, worker or integrator, has a merged pull request.

Only pull requests opened through Aether count. A pull request an agent opens
with `gh` directly is not counted. A merge is counted only after Aether has
seen it. On load, the page checks up to 10 open pull requests from your
finished runs against GitHub, so a pull request merged after its run finished
appears within a moment. An older one can stay `open` until its run's pull
request status is read again. If GitHub cannot be read, the row shows
`Could not refresh this pull request: <error>`.

## Time

Time covers runs that finished in the period.

- Run time: from the latest start (the relaunch, for a relaunched run) to finish. **Slowest 10% take over** is the 90th
  percentile.
- Agent working: time the agent spent working, including waiting for other
  agents, CI or tools.
- Waiting on you: time a run waited for an answer or an approval.
- Your response time: how long runs waited for you before working again.

A swarm integrator waiting for its workers counts as working, not as waiting
on you.

## Cost

Cost adds up the usage of runs that finished in the period. These runs report
cost and tokens:

- Claude Code in Background and Enhanced runs.
- OpenCode and omp in Enhanced runs, and in Background runs over ACP.

Every other run, Standard runs included, is listed as not reporting tokens, and
its cost is unknown, not zero, so cost and token figures are a floor. When some
runs are unmetered the page says `at least $X`.

An Enhanced agent reports the running cost of its session. Aether charges what
each report adds, with the tokens of that turn. A restored session, a new
session or Claude Code's `/clear` neither counts spend twice nor drops it, and
spend the server had not recorded when it stopped counts with the agent's next
report.

If a run moved between accounts after a usage limit, its spend counts under
the account that paid. The run's owner decides which person's statistics it
appears in.

Cost is reported, not enforced. To cap spend, use a [budget](teams.md#budgets).
Subscription quota is under **Settings → Usage**.

## System

The **System** tab shows how busy the server is right now and which runs use
it. The Activity tab is the 7-day history above; System is live.

- **Time range.** 5m, 15m or 1h scopes the charts. The line beside it reads
  `Live · every 5s`, `Paused while this tab is hidden`, or the error of the
  last failed poll; the last data stays on screen, dimmed.
- **Tiles.** CPU busy (user, system, I/O wait and steal), memory used,
  the data filesystem, network, running containers, and pressure when the
  kernel reports it. Pressure is the share of the last 10 seconds some task
  waited for CPU, memory or I/O.
- **Resources by run.** A donut of CPU or memory use. Each of up to five
  containers using at least 2% of the host gets a slice; the rest are one
  **Other runs** slice. **Everything else** is host use outside the listed
  containers: the Aether server, Docker, host processes and containers you
  cannot see. Click a slice or its legend entry to filter the ranked list to
  it; click again or **Show all** to clear. The list ranks every container
  by use, with its limit, **near limit** from 80% of that limit, **throttled**
  when CPU is capped, and a 15-minute trend. A run's browser is its own row,
  with its own limits. A row opens its run, or your own Environment.
- **History.** CPU, memory, load average, pressure, network and disk I/O.
- **Storage.** The data filesystem, Aether data by category, Docker storage,
  container writable layers and, for admins, the largest storage owners.

The server reads host figures from `/proc` and `/sys` every 5 seconds and
keeps one hour. Container figures come from Docker's per-container stats and
are sampled only while someone has the System tab open. Admins see every
container; members see their own, and the rest counts toward **Everything
else**. The tab reads `This server does not report system stats. Update the
server to see them.` when the server is too old.

## Limits

- The page covers the last 7 days. Older runs appear only in the 26-week
  heatmap.
- Time worked, time waiting and pull requests are recorded only from the day
  your server was upgraded to a version that records them. Earlier runs are
  left out of those numbers, and the page says
  `Aether records this from <date> on` when only some runs are measured.
- Deleting a run removes it from these statistics.
- The tiles read `This server does not report insights. Update the server to
  see them.` when the server is too old.
