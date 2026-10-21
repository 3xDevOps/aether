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
**Total tokens used** and the count of runs waiting for you.

A run counts on the day it finished, even if it started before the 7 days.

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

- Run time: from start to finish. **Slowest 10% take over** is the 90th
  percentile.
- Agent working: time the agent spent working, including waiting for other
  agents, CI or tools.
- Waiting on you: time a run waited for an answer or an approval.
- Your response time: how long runs waited for you before working again.

A swarm integrator waiting for its workers counts as working, not as waiting
on you.

## Cost

Cost adds up the usage of runs that finished in the period. Only headless
Claude runs report cost and tokens. Every other run is listed as not reporting
tokens, and its cost is unknown, not zero, so cost and token figures are a
floor. When some runs are unmetered the page says `at least $X`.

If a run moved between accounts after a usage limit, its spend counts under
the account that paid. The run's owner decides which person's statistics it
appears in.

Cost is reported, not enforced. To cap spend, use a [budget](teams.md#budgets).
Subscription quota is under **Settings → Usage**.

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
