# Troubleshooting and failure handling

What Aether does when the host reboots, a disk fills, an agent hangs or a
connection drops, and what you do next, for whoever runs the server and for
members who find a run in an unexpected state. Terms such as environment,
Enhanced mode and swarm are defined in [Concepts](README.md#concepts).

A run is one agent working on one task in its own container on the server.
While it is live its status is `running` or `needs-attention`; when it ends it
is `completed`, `failed`, `interrupted`, `merged` or `abandoned`. The reason
shown beside the status says why it is there.

## What happens when

| When | What you see | What Aether does | What you do |
| --- | --- | --- | --- |
| The server restarts or is killed, and run containers survive | Runs keep their status. | Reattaches to every live container. An Enhanced agent's session is restored in a fresh process. A kill accepted before the crash is issued again. | Nothing. An Enhanced turn that was cut off parks with `turn interrupted`: send a message to continue. |
| The host reboots, or Docker restarts without `live-restore` | Live runs read `interrupted` with `server restarted: container exited <code>` (or `server restarted` when the container is gone). | Commits the partial work as `wip: <task>`, publishes the run branch and keeps the checkout. | Pull the branch (`aether pull <run-id>`) or start a new run. An interrupted run cannot be reopened. |
| A server update is interrupted, or the new version cannot boot | `aether server update --status` shows `failed` or `rolled_back`. | Replaces nothing until every binary is verified. Rolls back a version that cannot finish booting. | [Recover from a failed server update](#recover-from-a-failed-server-update). |
| Free disk falls toward the floor | Launch refused with `scheduler: insufficient disk headroom: ...`. | Frees space on its own first, in a fixed order. | [Recover from disk pressure](#recover-from-disk-pressure). |
| Host memory is low | Launch refused with `scheduler: insufficient host memory headroom: ...`. | Starts nothing new. Running work is left alone. | Close or kill runs. A closed run's paused container still holds its memory; **Free container…** releases it. |
| An agent goes silent | `needs-attention`, reason `stalled: no output or file changes for <duration>`, state **Waiting**. | Keeps the run supervised. It returns to `running` as soon as the agent writes output or changes a file. | Attach (`aether attach <run-id>`) or send a message. See [Picking a stall threshold](#picking-a-stall-threshold). |
| A Standard agent exits | The run stays live and its terminal shows a login shell. | Opens a shell in place of the agent. It never restarts the agent. | Start the agent again in that shell, or close the run. |
| A Background agent exits | `completed`, or `failed` with `agent exited <code>: <last line of output>`. | Commits and publishes the branch, then removes the container. | Read the full output in the run's transcript. |
| The agent hits its account's usage limit | `Usage limit reached on <owner>'s account; ...` when it cannot continue. | Moves the run to another account that has usage left. | [Usage limit mid-run](#usage-limit-mid-run). |
| An agent update fails, or the vendor is unreachable | Timeline note `could not update <agent> from <version>: the updater exited <code>: <output>`. | Waits at most 25 seconds, then launches whatever is installed. Tries again from that home after 15 minutes. | Usually nothing. `sudo aether-server config set agent-update false` turns updates off. |
| An SSH connection drops during an attach | The CLI or dashboard disconnects. | Nothing changes for the run: its terminal lives on the server. Input that never reached the server is dropped whole. | Attach again. The full transcript replays before live output. |
| An `aether forward` connection drops | The local port stops answering. | Closes both ends of the tunnel. | Run `aether forward <run-id|terminal> <port>` again. |
| A launch cannot refresh the workspace mirror | The launch fails with `gitengine: mirror <kind> for branch "<branch>": <diagnostic>`. No run is created. | Never falls back to an older base on its own. | [Launch and mirror failures](#launch-and-mirror-failures). |
| A swarm worker start times out with `-32004` | `<method>: coordination is not reachable: <cause> [aether error -32004]` | The start may still launch. | Retry with the same `--dispatch-key`; a repeat returns the original attempt. |
| A swarm's integrator does not launch | `swarm <swarm-id> exists but its integrator run <run-id> ...` | Keeps the swarm. | [Integrator launch failures](#integrator-launch-failures). |
| Your environment's main shell exits | The environment terminal closes. | Stops the terminal and keeps the stopped container. | Open the environment again. It starts the container, or rebuilds it from your saved image. |
| An Enhanced session fails | **Disconnected**, with `enhanced session failed: <error>` or `enhanced session ended: ...`. | Keeps the run up on its login shell. | [Enhanced runs](#enhanced-runs). |
| A run runs out of memory | Timeline note `Out of memory: the kernel killed a process in this run at its <n> GiB memory limit (peak <n> GiB)`. | Nothing automatic. The killed process may be a tool, the agent or the shell. | Check whether the agent is still running. Raise `--run-memory` if it keeps happening. |
| The kernel's file-watch limits are spent | Server log `gitengine: cannot watch checkout for file changes; polling for diff snapshots instead`. | Launches the run anyway and checks its files every 60 seconds. | [Raise file-watch limits](#raise-file-watch-limits). |
| Another process holds the database at startup | `store: begin migration <n>: database is locked (5) (SQLITE_BUSY)` | The server exits. | Find and stop the other writer, then start again. Never delete the database. |
| Swarm state cannot be read at startup | `server: start service mission: mission: recover durable state: <cause>` | The server exits. Saved swarms are kept. | Fix the store error it names, then start again. |
| The server is down when a schedule is due | Nothing. | Skips the missed occurrence and waits for the next one. | Launch the run by hand if it matters. |

A lost connection does not prove that a command failed. Before you retry
something, check its files and processes. After a crash Aether does not
replay an agent's tool calls, but a Background Enhanced run is asked to
continue its turn, so an action can run twice.

## Run activity

A live run carries a state that says what it is doing. The dashboard and
`aether runs` show its label.

| State | Label | Meaning |
| --- | --- | --- |
| `working` | Working | A turn or tool is running. |
| `waiting-agents` | Waiting for agents | No turn runs, but the run waits on its own subagents, swarm workers, or a `waiting --for agents` report. |
| `waiting-ci` | Waiting for CI | The agent reported `waiting --for ci`. |
| `waiting-tools` | Waiting for process | The agent waits on a background task it holds, or reported `waiting --for process`. |
| `waiting` | Waiting | The turn ended with no report (`turn ended without a report`), the run stalled, a turn was cancelled, or a wait went stale (`no update for <duration>`). |
| `needs-you` | Needs you | A person is needed: a `needs-input` or `blocked` report, an open permission request or question, a usage limit Aether cannot continue from, or an Enhanced turn that waits for a reply. |
| `finished` | Ready for review | An interactive run reported `success`. |
| `failed` | Failed | The run reported `failure`, or its turn failed. |
| `disconnected` | Disconnected | The Enhanced session failed or ended while the run is open. |

When several apply, the first in this order wins: disconnected, needs-you,
working, failed, finished, waiting-agents, waiting-ci, waiting-tools, waiting.
A waiting state with no activity for three times `--stall-threshold`
(30 minutes by default) falls back to `waiting` with `no update for
<duration>`.

An agent's `blocked` report reads `blocked: <summary>`, and its question reads
`needs input: <question>`.

### Review

A reported success puts the run up for review. The run page asks **Your agent
reports this task is finished. Mark it complete?**

- **Mark complete** closes a live run as `completed`: it commits, publishes
  the branch and keeps the container like **Close**. A swarm worker completes
  through its integrator instead.
- **Not yet** keeps the run open and takes it out of **Needs you**.

Any new turn clears the review, and a later success report asks again.

### Needs you

One rule decides whether a run needs a person. The **Needs you** list,
`aether runs --needs-you`, push notifications and the assistant's notices all
use it.

| Run | Needs you | Notifies |
| --- | --- | --- |
| `needs-you` | Yes | Once, when it enters the state. |
| `disconnected` | Yes | Once. |
| `failed`, result not yet seen by the owner | Yes, while the run is live | Once: `Failed, review the result`. |
| `failed`, result seen | No | No. |
| Review ready, live or `completed` | Yes | Once per success: `Agent reported success, review the result`, at normal urgency. |
| Review marked **Not yet** or complete | No | No. |
| `working` or any waiting state, including `turn cancelled` | No | No. |
| Any state while the run is paused | No | Checked again on resume. |
| A swarm worker | Only for its `blocked` report | Its other requests go to its integrator. |

A need is announced once: opening the run or restarting the server does not
repeat it.

## Recover from disk pressure

Every launch checks that the Aether data filesystem and Docker's storage keep
a reserve free: by default the larger of 5 GiB and 5% of the filesystem,
capped at 20 GiB, plus 1 GiB for this launch and each one already starting.
When free space
drops below the reserve plus 4 GiB, Aether frees space on its own, on every
poll and before refusing a launch, in this order, until it is back above:

1. Containers of closed runs, the soonest to expire first. The timeline notes
   `Removed this closed run's container early because disk space was low; the
   run can no longer be reopened, and its branch and history are kept`.
2. Checkouts of every finished run, whatever their age. Skipped when
   `--checkout-ttl` is negative.
3. Other members' stopped environment containers. Anything they installed and
   did not save with `aether env save` is lost; their homes are kept.
4. Caches and the standard images.
5. Leftover scratch directories.

If that is not enough, the launch is refused:

```text
scheduler: insufficient disk headroom: <filesystem> has <n> free bytes; reserve <n> bytes plus <n> bytes per provisioning (<n> already in flight)
```

Attaching, messaging, pulling, closing, killing and deleting runs keep
working below the reserve, and so does reopening a closed run whose container
is still there. To free space yourself:

1. Open **Settings → Server** to see what uses the space.
2. Delete runs you no longer need with **Delete run** or `aether delete
   <run-id>`. That removes the checkout, transcript and records; the
   published branch, the timeline and the cost totals stay.
3. Free retained containers with **Free retained containers…** (admins).
4. Retry the launch.

| What grows | What frees it |
| --- | --- |
| `checkouts/` | The checkout cleanup after `--checkout-ttl`, or deleting the run. |
| `transcripts/` | Deleting the run. |
| `aether.db` | Deleting runs or the workspace. |
| `repos/` | Nothing. Every push and run branch is kept. |

Archiving a run (**Archive closed runs…** or `aether archive <run-id>`) only
hides it and frees nothing.

`scheduler: host capacity unavailable: ...` means Aether could not measure a
filesystem or host memory, for example a Docker daemon whose storage it cannot
see or a probe that timed out. Fix what the error names; free space in `df`
on a different machine does not count. If a filesystem cannot be read at all,
the disk check lets the launch through.

## Capture an unresponsive host

If the server stops answering while CPU and memory look free, capture evidence
before you restart anything, reboot, prune Docker or kill processes. Run this
in a root shell (`sudo -s`) on the server host. It writes to a private file
and gives each Docker command five seconds:

```sh
umask 077
capture=$(mktemp /tmp/aether-capture.XXXXXX)
{
  date -Is
  systemctl show aether-server docker -p Id -p MainPID -p TasksCurrent -p TasksMax \
    -p LimitNOFILE -p LimitNPROC
  systemctl status aether-server --no-pager -n 80
  journalctl -u aether-server -u docker -b --no-pager -n 200
  journalctl -k -b --no-pager -n 200
  journalctl -u aether-server -u docker -b -1 --no-pager -n 200
  journalctl -k -b -1 --no-pager -n 200
} >"$capture" 2>&1

pid=$(systemctl show aether-server -p MainPID --value)
if [ "${pid:-0}" -gt 0 ]; then
  {
    printf '\n--- service process ---\n'
    ps -L -p "$pid" -o pid,tid,stat,nlwp,comm
    cat "/proc/$pid/limits"
    cat "/proc/$pid/status"
    printf 'fd_count='
    find "/proc/$pid/fd" -maxdepth 1 -type l | wc -l
    ls -l "/proc/$pid/fd" | sed -n '1,100p'
  } >>"$capture" 2>&1
fi

{
  printf '\n--- host pressure and capacity ---\n'
  cat /proc/pressure/cpu /proc/pressure/memory /proc/pressure/io
  cat /proc/loadavg /proc/sys/kernel/pid_max /proc/sys/kernel/threads-max
  cat /proc/sys/fs/file-nr /proc/sys/fs/inotify/max_user_watches
  cat /proc/sys/fs/inotify/max_user_instances
  ps -e -o stat= | sort | uniq -c
  printf 'host_threads='
  ps -eLf --no-headers | wc -l
  df -hT
  df -ih
  printf '\n--- bounded Docker probes ---\n'
  timeout 5s docker info
  timeout 5s docker ps --filter label=aether.managed=true \
    --format 'table {{.ID}}\t{{.State}}\t{{.Names}}' | sed -n '1,50p'
  timeout 5s docker stats --no-stream \
    --format 'table {{.Name}}\t{{.PIDs}}\t{{.CPUPerc}}\t{{.MemUsage}}'
} >>"$capture" 2>&1
printf '%s\n' "$capture"
```

The previous boot's logs need a persistent journal. Keep the capture out of
the repository and redact it before you share it: log lines, command lines,
paths and socket names can be sensitive. Do not send `SIGQUIT` to get a stack
dump; it stops the server.

## Recover from a failed server update

`aether server update` downloads and verifies both binaries before it replaces
either, so a bad tag or a failed download changes nothing. A scheduled update
that fails on the network or the release host is retried at the next idle
moment, up to three times, then recorded as `failed`.

After the swap, the new version must open and migrate the database, then bind
its SSH listener and recover the runs within 5 minutes of migrating. If it
fails to finish booting twice, or misses those 5 minutes, Aether rolls back:
the previous binaries go back into place, and if the schema already changed,
the database copy taken before the update is restored. Every change made after
that copy is lost.

1. Read the result:

   ```sh
   aether server update --status
   ```

   ```text
   update            rolled_back v0.8.0 -> v0.9.0
   error             v0.9.0 started 2 times without finishing its boot: store: apply migration 91: ...; database restored to its snapshot of 2026-10-16T09:12:03Z; changes made after it are lost
   ```

2. Fix the cause the error names.
3. Update again.

An error ending in `rollback incomplete: <error>` means a file could not be
put back, and the server exits without starting either binary. A binary that
cannot start at all cannot roll itself back either. Restore the previous
binaries by hand as described in [install.md](install.md#upgrading).

## Launch and mirror failures

A workspace with a mirror refreshes it from the upstream before every launch.
If that fails, the launch is refused and no run is created; Aether never
starts from the previously accepted commit on its own. When an accepted commit
exists, the CLI prints a one-off retry from it:

```text
retry from cached <commit> with:
  aether run --agent <agent> --mode <mode> --workspace <workspace> --account <member-id> --cached-base <commit> -- <task>
```

`--cached-base` applies to that launch only, and is refused when the base has
moved since. To repair the mirror itself (admins):

1. Read the diagnostic:

   ```sh
   aether workspace mirror status --workspace <workspace>
   ```

2. Fix the cause from the server, not from your laptop. Check DNS and outbound
   access from the server host. For TLS errors, fix the server's git
   certificate trust; never turn verification off. For a deploy key, check
   that the key is still installed upstream.
3. Refresh with `aether workspace mirror refresh --workspace <workspace>`.

An upstream that was rewritten or diverged stays held (`rewritten` or
`diverged`) until an admin reviews it and adopts the new revision with
`aether workspace mirror adopt --workspace <workspace> --generation <n>
--commit <reviewed-sha> --yes`. Published run branches stay fetchable while a
mirror is failing.

## Integrator launch failures

Creating a swarm stores it before it launches the integrator run. When that
launch fails, the swarm is kept and the error says what happens next:

```text
swarm <swarm-id> exists but its integrator run <run-id> did not launch; the server retries the launch periodically, follow it with aether swarm show <swarm-id>: <cause>
swarm <swarm-id> exists but its integrator run <run-id> failed to start; replace it with aether swarm replace-integrator <swarm-id> --agent <agent> or from its swarm page, or read it with aether swarm show <swarm-id>: <cause>
```

- "did not launch": the server retries on its own and logs each failure as
  `mission: recover integrator`. The swarm page shows `The integrator run has
  not started.` Fix the cause and wait, or follow it with `aether swarm show
  <swarm-id>`.
- "failed to start": nothing retries it. Fix the cause, then press **Replace
  integrator…** on the swarm page or run `aether swarm replace-integrator
  <swarm-id> --agent <agent>`.

Deleting an integrator run that did launch does not bring it back either:
replace it, or cancel the swarm.

## Enhanced runs

An [Enhanced run](enhanced-runs.md) talks to its agent through a separate
process in the run container. When that process fails, the container and its
login shell stay up.

| Failure | What you see | What you do |
| --- | --- | --- |
| The session cannot start, for example because the agent is not signed in | **Disconnected**, `enhanced session failed: <error>`. Messages are refused with `scheduler: the enhanced session is not running: <error>`. | Fix the cause, then press **Retry Enhanced** in the Session view. |
| The agent's process exits | The turn in flight shows **Turn interrupted**; **Disconnected**, `enhanced session ended: the agent's ACP server exited with code <n>`, with `; stderr: <tail>` when it printed any. | **Retry Enhanced**. |
| The server restarts mid-turn | **Needs you**, `turn interrupted`. Background tasks the agent was running are gone. | Send a message to continue. |
| You cancel a turn or press Stop | **Waiting**, `turn cancelled`. | Send a message when you want it to go on. |
| A Claude background task never ends | `running`, `working in background: <n> tasks`. | Press Stop. |
| The old session cannot be restored | A new session starts; the item log says why. | Repeat context the agent lost. |

A Background Enhanced run has no one to retry it: any of these failures ends
it as `failed` with `agent exited 1: <cause>`, for example `agent exited 1:
the agent's turn ended: cancelled`. A server restart mid-turn resumes its
session and asks the agent to continue.

## Usage limit mid-run

When an agent reports that its account's usage limit stopped it, Aether moves
the run to another account that has usage left
([usage fallback](teams.md#usage-fallback)): the same run, checkout and branch
continue on the new account's login, and a room note says so. When it cannot
move, interactive runs park as **Needs you**, a Background run fails (a swarm's
Background worker parks), and the reason says why:

| Reason | Meaning |
| --- | --- |
| `Usage limit reached on <owner>'s account; no fallback has usage left` | No account shared with the launcher has usage. Wait for the limit to reset, or ask a teammate to share theirs. |
| `Usage limit reached on <owner>'s account; fallback is off for this run` | The run was launched with `--no-fallback`. |
| `Usage limit reached on <owner>'s account; cannot continue on another account: <reason>` | The run cannot move: for example the agent reported no session to resume, it is a member's own agent definition, or the owner may no longer launch. Nothing was changed. |
| `Usage limit reached on <A>'s account; continuing on <B>'s account failed: <error>` | The move started and failed. The run fails. |
| `Usage limit reached on <A>'s account; the run is on <B>'s account and its session could not be restored: <error>` | The run moved, but the Enhanced session did not come back. Send a message to start fresh. |

A paused run, or one being closed or killed, is not moved. A move survives a
server restart: the next start finishes it.

## Raise file-watch limits

Aether watches each run's checkout for changes with inotify. The kernel caps
watches and instances per user, and a server running as root shares that
budget with every root process in its run containers. When the kernel refuses,
the run still launches and its file changes arrive up to 60 seconds late,
which also delays stall detection.

`no space left on device` in the log means the watch limit is spent; `too many
open files` means the instance limit, or the server's own file descriptors.

1. Compare the server's open descriptors with its limit:

   ```sh
   pid=$(systemctl show aether-server -p MainPID --value)
   sudo sh -c "ls /proc/$pid/fd | wc -l; grep 'open files' /proc/$pid/limits"
   ```

   If the count is at the limit, raise `LimitNOFILE` in the systemd unit
   instead.
2. Otherwise raise the inotify limits:

   ```sh
   printf 'fs.inotify.max_user_instances = 1024\nfs.inotify.max_user_watches = 1048576\n' |
     sudo tee /etc/sysctl.d/90-aether-inotify.conf
   sudo sysctl --system
   ```

Runs launched afterwards watch again. A run that is already polling keeps
polling until it is reopened.

## What stays after a run ends

| Action | Container | Checkout | Reopen |
| --- | --- | --- | --- |
| **Close** (`aether close <run-id> --outcome merged` or `abandoned`) | Paused and kept for `--run-container-ttl` (7 days). It still holds its memory. | Kept while the container is kept. Removed after that, but no sooner than `--checkout-ttl` (72 hours) after the run ended: at 7 days with the defaults. | Yes, until the deadline: **Reopen run** or `aether reopen <run-id>`. |
| A Background run finishes | Removed at once. | Kept for `--checkout-ttl` (72 hours) after the run ends. | No. |
| A swarm worker reports | Paused and kept for `--run-container-ttl`. | Kept as for **Close**. | No. |
| **Kill run** (`aether kill <run-id>`) | Stopped and removed. | Kept for `--checkout-ttl` after the run ends. | No. |
| **Free container…** | Removed. History stays. | Kept until `--checkout-ttl` after the run ended. | No. |
| **Delete run** (`aether delete <run-id>`) | Removed. | Removed with the transcript and records. | No. |

The run branch is published before a checkout is removed, so cleanup never
loses committed work. When disk runs low, Aether removes both sooner
([Recover from disk pressure](#recover-from-disk-pressure)). Reopening resumes
the same container and checkout; it restarts no process that had stopped. When
retention ends, a closed run's reason reads `retained container expired` or
`retained container unavailable`.

## Tuning

`aether-server serve` reads these from `/etc/aether/server.conf`. Set one by
its flag name, then restart. For durations and ceilings, `0` means the default.

```sh
sudo aether-server config set stall-threshold 20m
sudo systemctl restart aether-server
```

| Flag | Default | What it changes | When to change it |
| --- | --- | --- | --- |
| `--stall-threshold` | `10m` | How long a live run may go with no agent output, file change or working report before it parks as stalled. Waiting states fall back to `waiting` after three times this. | See below. |
| `--poll-interval` | `30s` | How often stalls, retention and disk are checked. Values above `1m` act as `1m`. | Keep it at a third of the threshold or less. |
| `--checkout-ttl` | `72h` | How long a finished run's checkout is kept after the run ends. It also stays while the run's container is kept. Negative keeps every checkout, and disk cleanup leaves them alone. | Lower on a small disk. |
| `--run-container-ttl` | `168h` | How long a closed run's container, or a finished swarm worker's, is kept for reopening. Negative removes it at once. | Lower when paused containers hold too much memory. |
| `--min-free-disk` | automatic | The free-space reserve per filesystem, in bytes. Negative turns the disk check off; the memory check stays. | See below. |
| `--run-cpus` | 8, or the host's CPU count if lower | CPU ceiling per run and environment. | Lower to share a small host. |
| `--run-memory` | 8 GiB | Memory ceiling per run and environment, in bytes. Swap adds a quarter of it. | Raise when runs hit out-of-memory kills. |
| `--run-pids` | `4096` | Process and thread ceiling per container. | Raise for builds that start many processes. |
| `--agent-update` | `true` | Update a shipped agent in the member's home before each launch. | Turn off on a host without internet access, or to hold a version. |

The CPU, memory and process ceilings apply to containers created after the
change. A host-wide out-of-memory kill takes agent processes before the server
(`OOMScoreAdjust=-500` on the server, 500 in containers).

### Picking a stall threshold

For agents that report their turns (`claude`, `codex`, `opencode`, `pi` and
`omp`), a finished turn parks the run at once, and the threshold only catches
an agent that hangs mid-turn. For other agents, silence is the only signal,
and a long build or slow tool call looks the same as a hang.

- Too low, and long tool calls park healthy runs as stalled.
- Too high, and a wedged agent wastes hours before anyone notices.

10 minutes suits interactive runs. Raise it for Background runs with long
builds. Lower it to a minute or two for many short scripted runs.

Messaging a stalled run usually gets it going. The echo of your message does
not count as agent output, so a hung agent still reads as stalled afterwards.

### Picking a disk floor

The automatic reserve suits most hosts; raise `--min-free-disk` for large
checkouts or a filesystem shared with other services. Host memory is checked
separately: a launch needs the larger of 2 GiB and 10% of RAM available, plus
512 MiB for it and each launch already starting. Memory ceilings are not
reservations, so idle runs can share a host. Refusals and the cleanup order
are in [Recover from disk pressure](#recover-from-disk-pressure).

## Limits

- `aether.db` does not sync every write to disk. A killed server loses no
  committed write, but a power loss or kernel crash can drop the last few;
  the database stays consistent.
- A Standard agent survives a server restart only if its own process
  survived. Extra terminals in a run lose their old output streams.
- Aether does not restart an agent killed for memory, and an exit code alone
  does not prove memory was the cause. A cgroup v1 host posts no
  out-of-memory note; check `docker inspect` and the kernel log there.
