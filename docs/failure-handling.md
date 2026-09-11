# Failure handling and tuning


Aether is meant to survive being run on hardware that reboots, fills up and
loses connections. This file says what actually happens in each case, what
you can tune, and where the behaviour is proven. The chaos scenarios that
drive these paths are in [testing.md](testing.md).

## The tuning knobs

Four `aether-server serve` flags, all with working defaults. Zero always
means "use the default"; a negative value turns a guard off.

| Flag | Default | What it controls |
| --- | --- | --- |
| `--stall-threshold` | `10m` | How long a live run may go with no agent output, no file changes and nothing from its agent's own reporter before it parks at needs-attention. A run already parked because its agent said it is waiting keeps that reason. |
| `--poll-interval` | `30s` | How often that is checked, and the granularity of the return to running. |
| `--checkout-ttl` | `72h` | How long a finished run's worktree is kept before the GC reclaims it. Negative disables the GC. |
| `--min-free-disk` | `1GiB` (`1073741824`) | Free bytes below which new runs are refused. Negative disables the floor. |

They are also `server.Config` fields (`StallThreshold`, `PollInterval`,
`CheckoutTTL`, `MinFreeDiskBytes`) and pass straight through to the
scheduler.

### Picking a stall threshold

The threshold is the **hang detector**, and the fallback for harnesses that
cannot report their own state.

Where the agent reports (`claude`, `codex`, `opencode`, `pi` and `omp` - see
[harnesses.md](harnesses.md)), a turn that ends parks the run immediately
with a reason that says what it is waiting for, and the threshold is left
to catch the case the agent cannot report: one that hangs mid-turn, which
still parks with a `stalled:` reason. Where the agent does not report,
silence is all the server has, and the threshold is a bet about the longest
legitimate silence: an agent thinking, compiling, or waiting on a slow tool
call produces no PTY output and touches no files, and there is no way to
tell that apart from a hang.

- **Too low** and long tool calls park healthy runs, which trains people to
  ignore the badge.
- **Too high** and a wedged agent burns an afternoon before anyone notices.

10 minutes suits interactive TUI runs on a normal codebase. Raise it for
headless runs that do long builds; lower it to a minute or two for a fleet
of short scripted runs where a real stall should surface fast. The poll
interval only needs to be small relative to the threshold - a third of it is
plenty, and polling faster than that just wakes the scheduler up more often.

Parking is not terminal. A stalled run whose agent starts producing output
again returns to running on the next poll, and steering it (`aether inject`,
or typing on an attach) is usually what gets it talking. A run parked
because its agent said it was waiting is the exception: output alone does
not release it, because a TUI repainting while you type is output and is not
work. The agent's own next turn releases it, which is what typing into it
produces - across a server restart too.

Steering is not itself that output. A steer's attributed banner is the
server's own, and so is the terminal's echo of the steered line - or of
keystrokes typed on an attach - which comes back even when the agent never
reads its input. The server discounts what it wrote, so poking a hung agent
does not hide the hang for another threshold. That discount is best effort:
an unusually configured terminal, a steer over 8 KiB, or an echo that takes
more than a second to come back falls through to counting the bytes, and the
run then takes one more threshold to park again. What the agent puts on the
stream itself always counts: a full-screen agent that repaints its UI in
response is producing real output and clears its stall, which is the point.
The run parks on silence from the agent, not on silence from the stream.

### Picking a disk floor

The floor is headroom for what a *new* run is about to write: its checkout,
the container's writes, its transcript, and its share of the event log. It
is checked before the run row is created, so a refusal leaves nothing
behind. Runs already on the disk are never touched - a half-written checkout
is worse than a refused one.

Raise the floor if your workspaces are large (the checkout is a full clone)
or if the data directory shares a filesystem with something that must not be
starved. The refusal names the numbers, and the dashboard's disk gauge says
what is holding the space.

## Capture an unresponsive host

If the server is not answering even though CPU and RAM look free, capture
evidence before restarting, rebooting, pruning Docker, or killing processes.
Keep the capture outside the repository and review it before sharing: journal
lines, command lines, paths, and socket names can be sensitive.

Run this in a root shell (`sudo -s`) on the server host. It writes a
permission-restricted capture; Docker probes have five-second deadlines:

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

Previous-boot logs require retained journal history. Exit the root shell when
finished. Do not paste the capture into a repository or an issue without redaction.
Do not use `SIGQUIT` as a diagnostic shortcut: it terminates a Go server.
Prefer these read-only probes and preserve the original state for diagnosis.

## What happens, per failure

### Container wait errors

An error from Docker while waiting is inconclusive: it does not prove that
the container exited. Run supervision retries the wait with a backoff (from
50 ms up to 1 s), keeps the run and container supervised, and only proceeds
when Docker reports an exit or the container is definitively missing. Server
shutdown cancels the wait without killing the container; a transport error is
never converted into a made-up exit code.

### File-change watch pressure

The checkout watcher prunes Git-ignored directory subtrees instead of adding
a kernel watch for every generated child. Tracked files and files made
visible by a negated rule remain reachable even below an ignored parent.
Changes to `.gitignore`, `.git/info/exclude`, the Git index, or directory
creation/rename schedule a coalesced refresh. Newly visible directories gain
watches; descendants of newly ignored trees lose theirs. If Git cannot answer,
Aether clears the stale prune state and temporarily walks all directories,
which is safer than silently missing changes.
The server logs watcher errors. A queue overflow discards stale watch
registrations before rescanning the checkout.

Pruning reduces watcher pressure; it is not a constant-time scan guarantee.
Git refreshes and reconciliation still do work proportional to the paths they
must inspect, and snapshot timing remains governed by the watcher's quiet,
minimum, and maximum intervals.

### Environment terminal exit

When the main shell of an environment terminal exits, Aether stops its
terminal PTY sessions, destroys the exited container, and removes the
matching durable terminal row before a replacement is created. If container
destruction or durable-row handling fails during that sequence, the
supervision entry stays marked for cleanup; the next terminal ensure retries
cleanup before it can replace the terminal. A retry only removes a durable
row that still names that same container, so a newer terminal cannot be
deleted by an older cleanup.

### Server reboot, or a hard kill

State is SQLite and git, both durable, so nothing on the shutdown path needs
to run. On the next boot the scheduler reconciles every non-terminal run
against the runtime's actual containers:

- **The container survived** (the server died, the container did not):
  supervision reattaches to it, the PTY session is re-adopted, the diff
  watch restarts from the tree its last snapshot wrote so the next interval
  continues the chain, and the run stays `running`. Attaches, injects and the
  eventual exit all work as if nothing happened. A kill that was accepted
  before the crash is re-issued. A run the agent had parked stays parked
  with its reason: the last report is recovered with the run, so
  reattaching - which resizes the terminal and makes a full-screen agent
  repaint - does not read as the turn resuming.
- **The container is gone**: the partial work is committed as `wip:`, the
  run branch is published, and the run is marked `interrupted` with its
  checkout preserved.
- **The run never started** (it died between the row and the container): any
  container that was created is destroyed first - found by its sidecar or,
  in the narrow window before the sidecar exists, by the run ID the runtime
  persists as the container's creation key - and then the same wip-commit
  and interrupt applies.

`completed` runs are not recovered: their committed result branch and status
are already durable, their run containers are gone, and they remain
`completed` across the restart. They are non-final only in the review sense:
an authorized member can still close one as merged or abandoned.

An interrupted run relaunches in one click (`aether relaunch <run>`, or the
run card). The relaunch is a new run cloned from the published branch, and
where the harness supports it the agent is asked to continue its own
conversation. A harness with no resume flag starts fresh, and a
deployment-supplied argv override never has one appended - nothing checks
the override is still that CLI. See [harnesses.md](harnesses.md).

For a harness that can name a conversation, the run's identity is pinned at
launch: the server generates one UUID per run, launches with
`claude --session-id <uuid>`, and records it on the run row. The relaunch
then runs `claude --resume <uuid>`, which names that exact conversation. It
is unaffected by every run mounting its checkout at the same container path
and sharing one credential home per member, so a reboot that interrupted
several of a member's runs still relaunches each one into its own
conversation.

Two cases do not resume the pinned conversation. `claude --resume` on an ID it
cannot find prints `No conversation found with session ID: <id>` and exits 1,
which would fail the relaunch outright.

The first opens a fresh conversation instead:

- The run was interrupted before its agent ever started (a `queued` or
  `provisioning` row). The ID is stamped when the row is created, so it
  names a conversation the harness never opened.
- The relaunch changes agent accounts. A normal run relaunched directly by
  another member uses that member's account, whose home lacks the transcript.
  An account distinct from the run owner, whether selected at launch or left
  by a handoff, stays pinned; the owner can relaunch and resume it only while
  they have access.

The second is refused: relaunching one interrupted row twice while the first
relaunch is still active fails with `agent conversation already resumed by
active run <id>`. Two agents appending to one transcript is not a
recoverable state, and the checkout guard never catches it because every
relaunch gets a checkout of its own. Once the first relaunch reaches a
terminal state, relaunching the original row resumes the conversation
again.

A run whose harness cannot pin a session (`pi`, `omp`) falls back to
`--continue`, and so does a run row created before pinning existed.
`--continue` names no conversation: it resumes that member's most recent
conversation at that container path, which is not necessarily this run's own
and not necessarily one from this workspace. Treat that fallback as a
convenience, not a guarantee, and read the agent's first turn before
steering it. The fallback is sticky - a row that has no pinned ID never
acquires one, because there is no earlier conversation to name.

Relaunching a run that finished on its own does *not* resume: there is no
interrupted conversation behind it. It gets a session of its own instead.

### Disk pressure

Four things grow without bound, and the dashboard's gauge covers all four
(`GET /api/v1/disk`, shown in the status bar with the breakdown in its
tooltip):

| Growing | Reclaimed by |
| --- | --- |
| `checkouts/` | The TTL GC, or deleting the run. |
| `transcripts/` | Deleting the run. |
| `aether.db` (and its WAL) | Deleting the run's dependent records; the event log remains. |
| `repos/` | Nothing - every push, run branch and reflog entry stays. |

The GC sweeps on boot and hourly. It only reclaims worktrees of runs that
reached a terminal state longer than `--checkout-ttl` ago, and never a path
an active run still names. **The branch is the artifact**: publishing
happens before the checkout is reclaimable, so reclaiming a worktree never
loses work. An authorized member can use Delete at every run status. For a
live run it first stops the container, waits for supervision to publish the
final branch, then removes the checkout and durable run records; its timeline
stays as audit history.

Below `--min-free-disk`, `run.launch` and `run.relaunch` are refused with
`-32004` (unavailable) and a message naming the numbers. Everything else -
attaching, steering, pulling published run branches, closing, killing and
deleting runs - keeps working, which is what you need to actually clear space.

If the filesystem cannot be read at all, the floor allows the run: the guard
exists to stop a disk from filling, not to stop the server.

### Launch freshness and mirror failures

Launch freshness is server-owned. Before a run row, checkout or container
exists, the scheduler captures the workspace base. A configured workspace
mirror is refreshed from its source at that point; a local-only workspace reads
its local base directly. The client does not decide whether this base is fresh;
freshness does not require a pre-launch local operation. If that pre-run capture
fails - for example, because the source is offline, rewritten or diverged - the
launch is refused and leaves no run row.

When a strict mirror capture fails after a commit was already accepted, the
error may include that exact accepted commit. The CLI can print the explicit
retry:

```text
aether run --cached-base <40-character-commit>
```

The `--cached-base` override is request-scoped: it applies only to the launch
request where it is supplied and is never inherited by later launches.
Repeating the same override is accepted while the SHA still matches the
accepted commit and the workspace base has not moved; a mismatched SHA or
moved base is refused. It is not a general freshness bypass. A successful
retry records the cached base as the run's immutable base provenance.

Mirror state does not remove review flow. Published run branches remain
fetchable and pullable even while a mirror is failing, and a local-only
workspace keeps its normal direct base writes and repository setup flow.

### Scheduled occurrences

Each due schedule occurrence is consumed once. The server records the
occurrence, re-checks the creating member's current launch permission, and
then enters the same server-owned launch path as a manual run. An occurrence
skipped by current permission or template checks, or refused by a pre-run
guard (disk floor, budget or mirror/base capture), creates no run row; launch
failures are reported as timeline failure notes when the schedule still has
template context. It is never represented as a failed run. The next future
occurrence is the next chance, rather than an immediate retry or a catch-up
storm. Once the base is captured and the row exists, ordinary provisioning
failures do produce the failed run row described below.

Missed slots while the server is down are skipped; the scheduler resumes at
the next occurrence. See [teams.md](teams.md#task-templates-and-schedules)
for schedule administration.

### Agent stall or crash

`needs-attention` - **Needs you** on the board - means one of two things,
and the reason on the run says which.

**The agent is waiting for you.** A harness that reports its own state
parks the run the moment its turn ends, or it asks for permission or an
answer, with a reason that reads `waiting for your input`,
`waiting for your permission` or `waiting for your answer`. There is no
delay: the report arrives as the agent stops. A server restart does not
change that: the report is recovered with the run, so a run that was
waiting for you is still waiting for you afterwards.

How such a run comes back depends on how much its harness can say. Where the
agent reports both ends of a turn (`claude`, `pi`, `omp`), the run returns
to `running` with `agent resumed` when the agent starts its next turn -
which is what steering it produces - and not on terminal output alone, since
a TUI repaints while the member types. Where it only reports that a turn
ended (`codex`), there is no such report to wait for, so the run returns
with `activity resumed` on agent output or a file change. That takes
activity the report did not already cover: the answer the turn wrote before
it ended, and the frames the TUI keeps painting for a few seconds after it,
belong to the turn that is over. Those few seconds are measured on the
clock, not in polls, so `--poll-interval` can be set to anything without
turning a trailing repaint into a new turn; past them, output still has to
keep arriving into a later poll before the run reads as working again.
That second poll is what sets the delay: expect the return to `running` to
land one to two `--poll-interval`s behind the agent.

Output here is anything drawn in the terminal, because a harness that
cannot say when a turn starts leaves nothing else to go on. The echo of
your own typing counts: type a long prompt into a parked `codex` run and it
can read as `running` before you send it, and read as `stalled:` rather
than `waiting for your input` if you then walk away.

**The run stalled.** No agent output, no file changes and nothing from the
agent's reporter past `--stall-threshold` parks a live run at
`needs-attention` with a reason that leads with `stalled:`. This is the hang
detector: it catches an agent that said it was working and then wedged, and
it is the only signal at all for a harness that cannot report. Genuine agent
output or a file change returns such a run to `running`; server-written
steering echoes do not.

Either way the run remains supervised in its run container: while unpaused,
members with the existing steer permission can attach, inject input, and
open or reconnect a writable run-container shell to investigate it.

A clean agent exit commits the latest work to the run branch, destroys the
container, and marks the run `completed`. A crashing agent marks the run
`failed`; either outcome has no replacement login shell. The worktree and
transcript are preserved as appropriate, and a failed run's partial work is
committed as `wip:`.

### SSH drop mid-attach

The PTY session belongs to the server, not to the connection, so a dropped
attach changes nothing about the run: reattaching resumes from the replay
ring. Input a member typed that the transport never delivered is dropped
whole. What reaches the agent is always an exact prefix of what the
connection delivered - never reordered, never duplicated - and a dead
connection's straggler bytes can never land after the attach unwound, so
they cannot interleave with the reattach's input.

### SSH port-forward disconnect

For `aether forward`, a client half-close is not a full disconnect. The
forwarder half-closes the backend and lets the reverse direction drain, so a
request can finish after the client has sent EOF. A full SSH channel or
connection teardown cancels the forward and closes both sides, releasing the
backend instead of leaving a stuck dial behind.

## Where each row is proven

Every row above has a covering scenario or unit test; the map lives in
[testing.md](testing.md) so the suite and the map stay in one place.
