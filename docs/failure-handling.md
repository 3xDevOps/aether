# Failure handling and tuning


Aether is meant to survive being run on hardware that reboots, fills up and
loses connections. This file says what actually happens in each case, what
you can tune, and where the behaviour is proven. The chaos scenarios that
drive these paths are in [testing.md](testing.md).

## The tuning knobs

`aether-server serve` flags have automatic defaults. For duration settings,
zero means "use the default". A negative value disables a guard where noted;
for `--run-container-ttl`, negative means no retention and immediate cleanup.

| Flag | Default | What it controls |
| --- | --- | --- |
| `--stall-threshold` | `10m` | How long a live run may go with no agent output, no file changes and nothing from its agent's own reporter before it parks at needs-attention. A run already parked because its agent said it is waiting keeps that reason. |
| `--poll-interval` | `30s` | How often that is checked, and the granularity of the return to running. |
| `--checkout-ttl` | `72h` | When a finished run's worktree becomes eligible for cleanup, subject to evidence and execution ownership protection. Negative disables this GC. |
| `--run-container-ttl` | `168h` (7 days) | Grace for an explicitly closed Standard or Enhanced run, or completed swarm worker's exact container. `0` uses the default; a positive duration overrides it, and negative requests immediate release. Active/waiting runs and services are not expired. |
| `--min-free-disk` | `0` (automatic) | Per-filesystem reserve: `max(5 GiB, min(5% of capacity, 20 GiB))`, plus provisioning headroom. Positive values override the reserve in bytes; negative disables the disk guard. |
| `--run-cpus` | `0` (automatic) | New run/member container CPU ceiling: up to 8 cores, capped at the server host CPU count. Positive values override; negative/non-finite values are invalid. |
| `--run-memory` | `0` (automatic) | New run/member container memory ceiling: 8 GiB. Positive values override in bytes; negative is invalid. |
| `--run-pids` | `0` (automatic) | New run/member container task ceiling: 4096 processes/threads. Positive values override; negative is invalid. |

They are also `server.Config` fields (`StallThreshold`, `PollInterval`,
`CheckoutTTL`, `RunContainerTTL`, `MinFreeDiskBytes`, `RunCPULimit`,
`RunMemoryBytes`, `RunPidsLimit`) and pass through to the scheduler.
Resource limits apply at container creation, not retroactively; browser
companions keep separate caps. See [resource limits and admission](environments.md#resource-limits-and-launch-admission).

### Picking a stall threshold

The threshold is the **hang detector**, and the fallback for agents that
cannot report their own state.

Where the agent reports (`claude`, `codex`, `opencode`, `pi` and `omp` - see
[harnesses.md](harnesses.md)), a turn that ends can park the run at `needs-attention`
immediately. This does not mean the agent needs an answer: an open request
is a separate indication of an unresolved structured request. The threshold
still catches an agent that hangs mid-turn with a `stalled:` reason. Where
the agent does not report, silence is all the server has, and the threshold
is a bet about the longest legitimate silence: an agent thinking, compiling,
or waiting on a slow tool call produces no PTY output and touches no files,
and there is no way to tell that apart from a hang.

- **Too low** and long tool calls park healthy runs, which trains people to
  ignore **Needs you**.
- **Too high** and a wedged agent burns an afternoon before anyone notices.

10 minutes suits interactive TUI runs on a normal codebase. Raise it for
headless runs that do long builds; lower it to a minute or two for a fleet
of short scripted runs where a real stall should surface fast. The poll
interval only needs to be small relative to the threshold - a third of it is
plenty, and polling faster than that just wakes the scheduler up more often.

Parking is not terminal. A stalled run whose agent starts producing output
again returns to running on the next poll, and messaging it (`aether message`,
or typing on an attach) is usually what gets it talking. A run parked
because its agent said it was waiting is the exception: output alone does
not release it, because a TUI repainting while you type is output and is not
work. The agent's own next turn releases it, which is what typing into it
produces - across a server restart too.

A message is not itself that output. A message's attributed banner is the
server's own, and so is the terminal's echo of the message - or of
keystrokes typed on an attach - which comes back even when the agent never
reads its input. The server discounts what it wrote, so poking a hung agent
does not hide the hang for another threshold. That discount is best effort:
an unusually configured terminal, a message over 8 KiB, or an echo that takes
more than a second to come back falls through to counting the bytes, and the
run then takes one more threshold to park again. What the agent puts on the
stream itself always counts: a full-screen agent that repaints its UI in
response is producing real output and clears its stall, which is the point.
The run parks on silence from the agent, not on silence from the stream.

### Picking a disk floor

The floor leaves headroom for new work: its checkout, container writes,
transcript and event log. Admission checks the Aether data filesystem and
actual Docker storage filesystems before provisioning. It also requires
available host memory of at least `max(2 GiB, 10% of host RAM)`. The new
provisioning and each outstanding provisioning add 1 GiB disk and 512 MiB
memory to those requirements, preventing simultaneous launches from spending
the same measured headroom. Reservations end on success, error or cancellation.

Admission covers new runs and new member-environment, browser and agent
updater containers, with a five-second probe deadline. It does not re-admit
existing-environment execs, idempotent lookups of already-created runs, or
reopening live or paused retained containers. Reopening resumes existing
compute, not stopped processes; an unpause failure is returned without a
restart or replacement container. Starting new compute still requires admission.
It does not evict or pause existing runs under pressure. Checks precede
mutation where possible; a failed provisioning after admission still uses
the ordinary launch-failure lifecycle.

Raise `--min-free-disk` for large checkouts or shared filesystems. Negative
disables the disk guard, **not** host-memory admission or the requirement for
a truthful Docker capacity probe. Container maxima are not memory reservations:
admission uses actual available memory, so idle agents can coexist.

Refusals distinguish insufficient disk headroom, insufficient host memory
headroom and unavailable capacity. Read the named filesystem, available bytes,
reserve and outstanding-provisioning count in a pressure error. A capacity
error can mean a failed/timed-out probe or an unsupported, remote or
unverifiable Docker layout; fix that cause rather than trusting local `df`
output for a different daemon. The probe must include containerd content and
snapshot storage when used, not merely Docker's data directory.
Settings > **Server** shows the storage breakdown and any unknown measurement.
Free capacity or adjust the configured ceiling/reserve, then retry the
original authorized operation; changing a swarm dispatch key can duplicate
work whose launch outcome is still unknown.

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

### Worker launch timeouts and mirror failures

A `worker.start` response timing out (`-32004`) does **not** prove that launch
stopped. Swarm launch uses a service-owned context; its durable attempt and
reserved run ID survive the request. Replay the same start command with the
**same dispatch key** to retrieve that attempt, then use `worker list` or
`worker inspect` to observe it. Do not switch keys to work around an unknown
result: the original attempt can still launch.

Before creating a run, strict base capture refreshes a configured mirror.
A failed fetch never silently substitutes the previously accepted commit.
An `unknown` attempt with a mirror `last_error` is recoverable, not proof of a
dead worker: swarm reconciliation can retry its original reserved run after
the cause is fixed. Replaying an already-created reserved run returns its
original pinned base without requiring another upstream fetch.

Any admitted member can inspect mirror status. Repairing or refreshing a
mirror requires an administrator:

```sh
aether workspace mirror status --workspace <workspace>
# Fix the reported cause on the server or at the upstream, then:
aether workspace mirror refresh --workspace <workspace>
aether workspace mirror status --workspace <workspace>
```

The diagnostic distinguishes DNS/connectivity failures from authentication,
TLS/CA configuration, invalid Git URL rewrites, local permissions, and disk
space failures. In older versions, Git's generic `unable to access` wrapper
was classified as `offline` even for local or TLS failures; that old label
alone cannot establish a network outage. Operator-facing errors expose only
allowlisted diagnostics; raw Git output can contain secrets and remains in the
internal error cause, not in the public attempt or mirror status.

Check DNS and outbound access **from the server**, not just the worker or your
laptop. For TLS failures, repair the server Git trust configuration rather than
disabling certificate verification. For deploy-key failures, verify source
access, key-file permissions, and the pinned host key. Review repository-local
Git URL rewrites if the configured source looks correct but fetching fails.
Refresh again only after addressing the cause. A rewritten or diverged upstream
requires review and explicit candidate adoption; do not disable mirror
protection merely to force a launch.

After repair, inspect the original attempt before any retry. If it is still
launching, running, or unknown, either let reconciliation
settle it or use the normal authorized cancel operation and observe the settled
state before retrying. Cancellation and takeover authority are unchanged.
Only a launch path that explicitly supports `--cached-base <sha>` may use a
human-approved, unchanged accepted commit; swarm worker recovery does not
automatically consent to a stale base.

### Integrator launch failures

`mission.create` stores the swarm in `planning` and reserves its integrator
run ID before it launches that run. When the launch fails, the swarm is
kept and the create error names it. Which error you get depends on whether
the scheduler wrote the run row before failing:

```
swarm <swarm-id> exists but its integrator run <run-id> did not launch; the server retries the launch periodically, follow it with aether swarm show <swarm-id>: <cause>
swarm <swarm-id> exists but its integrator run <run-id> failed to start; replace it with aether swarm replace-integrator <swarm-id> --agent <agent> or from its swarm page, or read it with aether swarm show <swarm-id>: <cause>
```

With no run row, swarm reconciliation retries the launch of the reserved
run on its periodic pass and logs each failure as `mission: recover
integrator` with the mission ID and the cause. Repeating `mission.create`
with the same contents and idempotency key also retries the launch, and
returns the same swarm. The swarm page shows `The integrator run has not
started.` for that swarm, and `aether swarm show <swarm-id>` prints the
error as `launch error:`.
`mission.show` and `mission.list` carry the last launch error in
`integrator_launch_error` and the time it was first seen in
`integrator_launch_error_at`; both clear once the run is live (a row that
failed while provisioning keeps them) or the integrator is replaced.
`mission.replace-integrator` reports a failed launch of the new run with the
same two errors and records it the same way.

A run row that failed while provisioning is not retried, and a same-key
`mission.create` returns the swarm without launching again. The Swarms
page shows that the integrator run has exited. Fix the cause, then use
**Replace integrator** to launch a new integrator run.

Reconciliation only relaunches an integrator run whose row never existed.
Once the row exists, `mission.show` reports `integrator_run_launched: true`,
and deleting that run does not bring it back, not even through a same-key
`mission.create`, which then returns the swarm unchanged, as it does for
a cancelled swarm. Use **Replace integrator** to start a new one, or cancel
the swarm.

Upgrading to the server version that added `integrator_run_launched` marks
every existing swarm's integrator as launched, so the upgrade relaunches
nothing, including an integrator run deleted before the upgrade. An older
swarm whose integrator never launched therefore stays unlaunched; use
**Replace integrator** to start it.

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

The kernel caps inotify instances and watches per user, and a root server
shares that budget with every root process inside its run containers. Each
live run's watcher holds one instance. When the kernel refuses the instance
or the checkout's root watch, the run still launches and its watcher polls:
it takes a snapshot every maximum interval (60 seconds) for the rest of that
watch, so diff snapshots, branch updates, and the file-change activity that
stall detection reads all arrive up to that interval late. When only a
subdirectory's watch is refused, the rest of the checkout stays watched and
the same poll covers that subtree. The server logs the kernel's error for
each affected run or directory:

```
gitengine: cannot watch checkout for file changes; polling for diff snapshots instead run=<run-id> interval=1m0s error="gitengine: start watcher: couldn't initialize inotify: too many open files"
```

`no space left on device` means the per-user watch cap is spent.
`too many open files` means either the per-user instance cap is spent or the
server process is out of file descriptors. Compare the server's descriptor
count with its limit first:

```sh
pid=$(systemctl show aether-server -p MainPID --value)
sudo sh -c "ls /proc/$pid/fd | wc -l; grep 'open files' /proc/$pid/limits"
```

If the count is at the limit, raise `LimitNOFILE` in the unit. Otherwise read
the inotify caps and raise the one that is spent:

```sh
cat /proc/sys/fs/inotify/max_user_instances /proc/sys/fs/inotify/max_user_watches
printf 'fs.inotify.max_user_instances = 1024\nfs.inotify.max_user_watches = 1048576\n' |
  sudo tee /etc/sysctl.d/90-aether-inotify.conf
sudo sysctl --system
```

Runs launched after the change get a kernel watcher again; a run that is
already polling keeps polling until it is reopened.

### Environment exit

When the main shell of a member's environment exits, Aether stops its
terminal PTY sessions, destroys the exited container, and removes the
matching durable terminal row before a replacement is created. If container
destruction or durable-row handling fails during that sequence, the
supervision entry stays marked for cleanup; the next terminal ensure retries
cleanup before it can replace the terminal. A retry only removes a durable
row that still names that same container, so a newer terminal cannot be
deleted by an older cleanup.

### Database startup contention

`aether-server serve` applies pending SQLite schema migrations when it opens
the state database. Each schema change and its version record commit together.
If another opener has already committed the required version, startup resumes
from the latest committed version even when a competing writer holds the lock.
Uncommitted or rolled-back version records do not count as completed work.

If a required migration still cannot obtain the write lock within the bounded
wait, startup returns the original SQLite error, for example:

```text
store: begin migration 1: database is locked (5) (SQLITE_BUSY)
```

Investigate the competing writer before retrying startup. Do not delete the
database or edit `schema_migrations` to bypass the failure.

### Server reboot, or a hard kill

State is SQLite and git, both durable, so nothing on the shutdown path needs
to run. On the next boot the scheduler reconciles every non-terminal run and
every retained TUI or completed swarm run against the runtime's actual
containers:

- **An active container survived** (the server died, the container did not):
  supervision reattaches to its primary terminal, the diff watch restarts
  from the tree its last snapshot wrote, and the run remains supervised.
  A Standard agent continues only if its own process survived. Enhanced
  agents instead follow the [ACP replacement/restore path](#enhanced-runs);
  additional Docker exec terminals cannot recover their old streams.
  A kill that was accepted
  before the crash is re-issued. A run the agent had parked stays parked
  with its reason: the last execution report is recovered with the run, so
  reattaching - which resizes the terminal and makes a full-screen agent
  repaint - does not read as the turn resuming. Its correlated pending-input
  set is recovered separately; restart neither clears unanswered requests
  nor replays old input-update deltas. An interactive run's reported outcome
  remains available for review and follow-up; recovery does not close it.
  Background runs still finish from their reported outcome once the turn
  ends, or at the existing two-minute fallback when no reporter is available.
- **An active container is gone**: the partial work is committed as `wip:`, the
  run branch is published, and the run is marked `interrupted` with its
  checkout preserved. An interrupted run cannot be reopened.
- **The run never started** (it died between the row and the container): any
  container is found by its sidecar or, in the narrow window before that
  exists, by the runtime's durable creation key. Recovery preserves partial
  work and required evidence before destroying the container and marking the
  run interrupted. Uncertain lookup or cleanup keeps its retry owner.
- **A retained container survived**: its terminal row, checkout, member
  account, and coordination surfaces remain owned by that exact container.
  Boot reconciliation clamps older deadlines to durable completion time
  plus the current `--run-container-ttl`, never extending an earlier deadline
  or changing an active/reopened run. It preserves paused swarm workers and
  already-exited swarm containers until that deadline. Ordinary Standard and
  Enhanced runs explicitly closed by a user can be reopened; completion
  never revives an assigned worker.
- **A retained container is gone or expired**: boot cleanup preserves required
  evidence, destroys any remaining runtime object, then releases retention
  ownership. It never creates a replacement. A capture, runtime or persistence
  failure keeps ownership and a bounded cleanup cause for automatic retries;
  the deadline can pass while safe cleanup is still pending.

Ordinary headless runs are not recovered into a shell. When their agent exits, Aether
commits and publishes the branch, records `completed` for a clean exit or
`failed` for an error - or the outcome the agent reported, whatever the exit
code - and destroys the container immediately. A failed exit's reason is
`agent exited <code>: <line>`, where `<line>` is the last non-empty line the
agent printed, cut at 200 characters; the full output stays in the run's
terminal transcript. A report that reaches the
server after the exit still sets the run's status; the commit the exit
already published keeps the exit's `aether:` or `wip:` prefix, and the
status and report are the record. A `completed`
run remains available for review and an authorized member may close it as
merged or abandoned, but neither headless status can be reopened.

Swarm-assigned integrator and worker runs keep that same persistent supervisor
even in headless mode, so a one-shot agent exit does not destroy the container
or mark the run completed. They stay until Close, Kill, a terminal worker
report, or worker cancel. Accepted success/failure reports pause and retain the
exact worker container for `--run-container-ttl` (default seven days), and an actual
swarm container exit retains that exited container for the same duration.
Attempts release execution capacity only after evidence, runtime quiescence,
and durable retention have settled. Failed capture, runtime, or persistence
steps keep ownership and capacity until reconciliation succeeds. Explicit
worker cancellation, Kill, Delete, and negative-TTL cleanup remain destructive.
Use the run's terminal, **Changes**, **Captures** and its swarm page while
it is retained; no new live work is admitted to a completed worker.

Swarm recovery also loads durable objectives and their bounded worker
attempts. If the initial swarm inventory scan fails, `aether-server serve`
reports `server: start service mission: mission: recover durable state: <cause>`
and exits instead of deferring the failed scan to periodic recovery. The
underlying store error is preserved; fix that cause before restarting.
Saved swarms and attempt reservations are not deleted.

### TUI lifecycle and reopening

For `--mode standard` (`tui`), the run supervisor launches the agent. When
that child exits and the supervisor remains alive, it opens a login shell;
when the shell exits, another opens. This includes a signalled agent child:
the supervisor does not restart the agent or replay its tools. The container
can therefore stay `running` without a live agent until an explicit Close,
Kill or Delete, or a container-level exit.

An interactive agent reports its task's outcome without closing its session:

```sh
aether-internal report --outcome success --summary 'Implemented and tested the change.'
```

At turn end, with no permission or question request open, the run becomes
ready for review with the reported outcome. Its container and agent session
remain live: send another message or type into the native terminal without
reopening. A new working turn clears the idle outcome, and a later task can
report again under a new idempotency key. Reporting alone starts no expiry
timer and does not publish a result commit.

Background runs still commit, publish and finish automatically after a
success/failure report. Swarm workers still stop and retain their exact
containers after reporting. See [Report an outcome](coordination.md#report-an-outcome).

Close is explicit and records one of the two outcomes:

```sh
aether close <run> --outcome merged
aether close <run> --outcome abandoned
```

Closing a live Standard or Enhanced run pauses its container, commits and
publishes the current checkout, records the selected outcome, and retains
the exact container, checkout, run row, member account, and coordination
surfaces during `--run-container-ttl`. Zero uses the default **`168h`
(7 days)**; a positive duration overrides it and negative TTL requests immediate cleanup. Pausing still holds
RAM; it is not memory reclamation. Kill stops and destroys a run immediately,
subject to the same required evidence and confirmed-cleanup safeguards.
Delete stops any live container and removes the checkout, transcript, and
durable run records; its timeline remains audit history. Its recorded cost
survives inside its workspace's and its member's spend totals - the numbers
[`aether cost` and `aether budget`](teams.md#budgets) report, and a workspace
budget checks - so deleting a run over budget cannot reopen the cap.

The dashboard's **Free container…** in a finished run's actions removes its
retained container and browser without hiding the run or deleting its
history. **Free retained containers…** (admins), in the board's **More
finished-run actions** menu, Settings > **Server** and the palette, applies it
to eligible finished runs in the selected workspace, including archived
runs. A run whose container was freed cannot be reopened; the existing checkout and history retention rules still apply.
These actions use the same evidence-preserving cleanup as Kill: an evidence
or runtime error leaves cleanup incomplete and is shown in the dashboard.
Unfinished finalization and interrupted-finish recovery return a release error
instead of reporting that resources were freed.
**Archive closed runs…** only hides runs and starts the archive deletion
timer; it does not release container memory.

The **seven-day compute grace** is separate from the **72-hour checkout
eligibility** (`--checkout-ttl`) and retained history/evidence policies.
Expiring compute does not delete published Git result branches or captured
evidence. Required capture or failed destruction protects the checkout until
cleanup is confirmed. Run details show the exact retention deadline and
whether cleanup remains pending, including a safe operation-level failure
that survives restart and clears when its retry succeeds. Detailed runtime
errors remain in server logs, not in public diagnostics.

**Reopen** is available for a retained Standard or Enhanced run after Close,
while its retention deadline has not passed. From the CLI:

```sh
aether reopen <run>
```

It resumes the same run row, container, checkout, member account, and
coordination surfaces, and frees the run's terminal report so the agent can
report again. It does not create a run, checkout, branch, or replacement
container, restarts no stopped process, replays no tool, and performs no new
launch or disk-floor admission.
An expired, unavailable, interrupted, killed, deleted, or headless run cannot
be reopened. The expiry sweep normally runs within at most one minute; boot
reconciliation also handles expired or unavailable retained runs, so a failed
reopen never falls back to a new run. Failures retain ownership for retry
rather than promising that resources were already freed. When retention ends, a closed run's
reason becomes `retained container expired` or `retained container
unavailable`; a run its agent finished keeps `completed` or `failed` and its
reason drops `; retained container`.

### Enhanced runs

An enhanced run ([enhanced-runs.md](enhanced-runs.md)) has the tui container
and lifecycle above; the agent's ACP server is a separate exec in that same
container, replaced at every reattach. The shell-survival cases below assume
the container and shell are still healthy, not that an OOM spared them.

| Failure | What happens |
| --- | --- |
| The ACP server fails to start, or its session cannot open (not logged in) | The run stays up on its login shell and parks at `needs-attention` with `enhanced session failed: <error>`; the item log gets the same notice. Messages are refused with `scheduler: the enhanced session is not running: <error>`. Pausing and resuming the run (**Retry Enhanced** in the Session view) starts a fresh session. |
| The ACP server exits on its own | A turn in flight ends with **Turn interrupted**, pending requests are cancelled, and the run parks with `enhanced session ended: <exit code>; stderr: <tail>`. |
| Server restart or hard kill | Recovery stops the previous ACP server (Docker cannot reattach an exec's stdio) and attempts to restore the stored session in a fresh one: `session/resume`, else `session/load` without re-logging history. A turn cut off by the restart is logged as **Turn interrupted**. |
| The session cannot be restored | A new session starts, and the log says why the old one could not be restored. |
| The container is paused at a reattach | The ACP server starts when the run is resumed. |

A background run over ACP ([enhanced-runs.md](enhanced-runs.md#background-runs))
differs: an ACP server that fails to start or exits, a cancelled turn, or any
stop reason other than `end_turn` ends the container with exit code 1, so the
run records `failed` with `agent exited 1: <cause>`, for example
`agent exited 1: the agent's turn ended: cancelled`. The item log's notice
carries the full error. A server restart mid-turn resumes the session and asks the
agent to continue; a turn that had already ended finishes the run.

### Resource exhaustion and agent survival

Run CPU, memory and PID limits contain the container's workload; they do not
split the native agent from its tools. CPU saturation throttles that workload,
the task limit can prevent new processes/threads, and a memory limit can
trigger a container-cgroup OOM kill. No extra swap allowance is added to the
configured memory ceiling. The OOM victim might be a tool, the agent or a
process needed by the whole container. Host-wide exhaustion is still possible
if running workloads grow after admission; limits and launch headroom are not
a promise of continuous host or agent uptime.

A surviving supervisor may offer a shell after the agent dies; a surviving
shell does not mean the conversation is still running. An Enhanced adapter
failure is surfaced with its real error and can be retried explicitly.
Container loss follows the interrupted/failed lifecycle above. Aether does
not generally restart an OOM-killed native agent automatically, and an exit
code alone does not establish that OOM was the cause. Inspect Docker state,
kernel logs and the run's error before drawing that conclusion.

Completed writes in the host-mounted checkout and member home are distinct
from in-memory context, in-flight writes and the disposable container layer.
Retained files and the ACP display log do not reconstruct a dead agent's
private session. Restore depends on that agent's own stored session and login;
when restore fails, Enhanced mode reports a new session instead.

Before retrying a command, inspect its files and process state: losing a
connection does not prove that a side effect failed. Interactive Enhanced
recovery marks interrupted work and undelivered queued messages; it is not
tool replay. Background ACP recovery can prompt the agent to continue, as
documented above, so there is no universal exactly-once/no-replay guarantee
for native agent actions. The [execution-boundary decision](harness-integration.md#execution-boundary-keep-native-tools-with-the-agent)
states the evidence required before claiming stronger isolation.

### Disk pressure

History and durable workspace data can grow without bound. Settings >
**Server** reports storage ownership and the separate lifetime of each kind.

| Growing | Reclaimed by |
| --- | --- |
| `checkouts/` worktrees | Checkout TTL GC or explicit deletion. The `.diffsnap/` sidecars remain until explicit run deletion. |
| `transcripts/` | Explicit run or terminal-session deletion, including run removal through workspace/swarm deletion. |
| `aether.db` (and its WAL) | Explicit deletion of dependent records; the event log remains. |
| `repos/` | Nothing - every push, run branch and reflog entry stays. |

The GC sweeps on boot and hourly. It only reclaims worktrees of runs that
reached a terminal state longer than `--checkout-ttl` ago, and never a path
an active run still names. **The branch is the artifact**: publishing
happens before the checkout is reclaimable, so reclaiming a worktree never
loses work. An authorized member can use Delete at every run status. For a
live run it first stops the container, waits for supervision to publish the
final branch, then removes the checkout and durable run records; its timeline
stays as audit history.

`run.archive` hides a run in a final disposition (`merged`, `abandoned`,
`failed`, or `interrupted`) from the board. It stamps `archived_at` without
scheduling deletion. Re-archiving is a no-op, and `{"archived":false}`
restores it. Archived swarms and their runs likewise persist until explicit
Delete. Checkout TTL cleanup still reclaims eligible worktrees, but it leaves
terminal recordings, Enhanced item logs and recorded diff history intact.

The former automatic history limits and 14-day archive deletion policy no
longer apply, including to already-archived records. Data already deleted by
an older server cannot be recovered by this change; existing history gaps
remain visible rather than being presented as complete recordings.

Below `--min-free-disk`, `run.launch` is refused with `-32004` (unavailable)
and a message naming the numbers. Reopening an eligible retained TUI run
does not perform a new launch or disk-floor admission, so it can reopen its
exact retained container and checkout below that floor. Everything else -
attaching, messaging, pulling, closing, killing and deleting runs - keeps
working, which is what you need to actually clear space.

If the filesystem cannot be read at all, the floor allows the run: the guard
exists to stop a disk from filling, not to stop the server.

### Candidate assembly, verification, and delivery

Candidate work has its own durable lifecycle and is independent of the source
run's checkout and evidence row. Preparation creates the candidate aggregate
before allocating resources, then records each completed input copy as it
retains the exact evidence Git revision and, when available, a bounded
transcript artifact. Packet snapshots are bounded to 1 MiB total per
candidate. A source packet may expire or be deleted after that ownership
transfer; the candidate validates its own refs, transcript checksums, and
metadata instead of trusting the original packet or run row.

Candidate inputs are applied in order in a server-owned isolated checkout.
When a cherry-pick conflicts, the journal and checkout remain in the
`conflicted` state for explicit file resolutions. The candidate does not
freeze until every retained input has applied; after freezing, the candidate
revision and inputs are immutable. A required source that is unavailable or
truncated refuses preparation. If an owned ref, transcript, or checksum is
missing later, the candidate becomes `unavailable` and verification and
delivery are blocked; Aether does not silently rebuild it from an expired
source.

Verification is asynchronous and finite. The server persists the runtime
creation key before creating a container, runs the exact requested argv
against a disposable copy of the frozen revision, bounds retained output to
64 KiB per verification (at most 2 MiB across 32 verification records) while
continuing to drain it, and checks the tree after all child processes stop. A
timeout, cancellation, runtime failure, or source change is never a pass.
A restart reconciles persisted creation keys, destroys any
discovered verification container, removes its disposable checkout, and marks
an interrupted attempt as an error; it never reruns the command or invents an
exit code. Cleanup failures leave the verification record and recoverable
resources for a later retry.

Delivery claims its request durably before touching Git. A local workspace
target uses an atomic expected-old compare-and-swap. A mirrored target cannot
be updated directly: a proposal transaction creates the public
`refs/heads/aether/proposal-<request-id>` ref and a private receipt while
leaving the upstream-owned mirror base unchanged. A database failure after a
successful Git transaction is reconciled from that exact private receipt, not
by guessing from the target's current value; retrying therefore does not
duplicate a delivery. The public proposal remains available for the human's
normal fetch/push review route.

Candidate lifetime is 30 days. Verification validity is 24 hours, and a
delivery request cannot outlive its candidate or its selected verifications.
Expiry and deletion first fence new actions and persist the transition, then
destroy runtime/checkouts and remove candidate-private refs and transcript
copies. Public proposal refs are transport artifacts and are not removed by
candidate-private cleanup. Tombstone metadata is retained for at most 30 days
so retries and cleanup can be reconciled without keeping source artifacts.
If the transition or preservation step fails, Aether keeps recoverable
resources and retries cleanup rather than deleting evidence silently. See
[teams.md](teams.md#candidate-integration) for the operator-facing flow and
[integration.md](integration.md) for the method-level contract.

## Launch freshness and mirror failures

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

`needs-attention` - **Needs you** for the run's owner on the board - describes
execution, not a request from the agent. It means one of two things, and the reason on the run says
which.

**The agent reported idle or turn completion.** An agent that reports
its own state parks the run when it has no active work. The `waiting` wire
state means idle; generic turn completion, interruption, silence, and prose
do not create an input request. An agent that ran
`aether-internal report --outcome blocked --summary '<summary>'` during the
turn parks at its end with `blocked: <summary>` instead of `agent idle`,
until it resumes. The execution report is recovered through a server
restart.

How such a run comes back depends on how much its agent can say. Where the
agent reports both ends of a turn (`claude`, `pi`, `omp`), the run returns
to `running` with `agent resumed` when the agent starts its next turn -
which is what messaging it produces - and not on terminal output alone, since
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

Output here is anything drawn in the terminal, because an agent that
cannot say when a turn starts leaves nothing else to go on. The echo of
your own typing counts: type a long prompt into a parked `codex` run and it
can read as `running` before you send it, and read as `stalled:` rather
than `agent idle` if you then walk away.

**The run stalled.** No agent output, no file changes and nothing from the
agent's reporter past `--stall-threshold` parks a live run at
`needs-attention` with a reason that leads with `stalled:`. This is the hang
detector: it catches an agent that said it was working and then wedged, and
it is the only signal at all for an agent that cannot report. Genuine agent
output or a file change returns such a run to `running`; server-written
message echoes do not.

Either way the run remains supervised in its run container: while unpaused,
members with the `steer` permission can attach, send messages, and
open or reconnect a writable run-container shell to investigate it.

A TUI agent exit returns to a surviving supervisor, which opens a login shell
instead of restarting the agent. Explicit Close commits and publishes the
latest work, records merged or abandoned, and applies the retention policy.
If the container itself exits, supervision follows its actual exit outcome;
agent-child death alone is not proof of container death. An ordinary headless
run's clean exit still commits and publishes, records `completed`, and destroys
the container immediately. Swarm-assigned runs instead retain the exact stopped
container for the configured TTL. A failed run's partial work is committed
as `wip:`.

### Pending structured input

An open request is independent of the wire status. A run may keep
working in one session while another has an unanswered question, permission
request, form, or extension dialog. Closing a request does not claim execution
resumed; only a positive execution signal does that. Codex's legacy notify
reports turn completion only, not pending input. Supported native reporters
and their limits are listed in [harnesses.md](harnesses.md).

The server keeps a durable set keyed by session, request kind, and request
ID, separate from the last execution report. A matching close removes only
that request. A known terminated session can clear its own requests; an
authoritative adapter snapshot can replace the set, including an empty set.
Duplicate opens and closes are idempotent. A new turn or an unrelated
completion cannot resolve somebody else's request. Request metadata contains
identifiers and kinds, not prompt bodies, answers, paths, or transcripts.

`run.report` may omit execution state only when `input_updates` is nonempty;
an input-only update preserves execution. The server accepts at most 128
updates per report and 128 requests per replacement snapshot. Identifiers
must be valid, nonblank, control-free UTF-8 of at most 256 bytes. Unsupported
operations or kinds and invalid or oversized metadata return `InvalidParams`
before the scheduler is called. Reports keep the existing bounded reason
sanitization and separate lifecycle request budget. A persistence failure
returns `Internal` with the underlying cause rather than acknowledging an
input change that was not saved.
If saving succeeds but `run.input` publication fails, the report still returns
an error. The sidecar retains the publication obligation, including a last-close
empty set. A subsequent report or live-run recovery publishes the current set,
not stale deltas; successful duplicate reports remain no-write/no-event. A crash
after publication but before its acknowledgement is saved can replay the same
replacement snapshot.

The pending set survives server restart only for the same still-live run
lifetime, with no observed exit. Disconnecting, pausing, going idle, or
finishing a turn does not clear it. A durable terminal run transition clears
the set, including swarm completion that retains a paused container for
inspection. Recovery treats the run row as authoritative, so stale sidecar
data cannot resurrect requests from a terminated lifetime. Reopening resets
both execution and input before the new lifetime starts.

A clean TUI agent exit into the supervisor's login shell is not a container
exit. Pending requests clear there only when the native reporter sends a
correlated close, session clear, or replacement snapshot; the scheduler
cannot infer that the CLI exited while the container remains live. Resolving
the last request clears the indicator through a `run.input` event without
changing execution status. Use the existing terminal, approval, or room
surface to answer; the indicator is not a separate answer channel.

### Agent update fails, or the vendor is unreachable

Before a launch, the server may update the shipped agent installed in the
member home ([harnesses.md](harnesses.md#updates-before-launch)). No update
problem stops the launch. A launch waits at most 25 seconds for the update,
then starts the agent on whatever is installed at that moment. A nonzero
exit, a runtime error, or an updater stopped after 10 minutes is recorded on
the timeline of the run that started the update:

```text
could not update codex from codex-cli 0.155.1: the updater exited 1: <updater output>
```

The server log has the same failure as `scheduler: harness update failed`.
The next launch from that home tries again after 15 minutes.
`--agent-update=false` turns updates off.

An interrupted `codex` or `pi` exchange leaves either complete version
launchable. If an older updater left the installed package missing and a
surviving `~/.local/lib/.<agent>-update.*/previous` copy, the next attempt
restores that copy before contacting npm.

### Edge status during sign-in or logout

Device-key and token reads share the credential store's cross-process
`edge.lock` with writers. A status request waits for a concurrent credential
write instead of opening a partially written key or racing token-file
replacement on Windows. A process exit releases the lock automatically;
genuine file-permission and malformed-credential errors are still reported.
Status responses never include the device token or private device code.

Logout does not hold this lock while contacting the edge. After revocation,
it removes only the token it revoked, preserving a newer sign-in completed
by another client while the request was in flight. If revocation cannot
reach the edge, the token is kept so logout can be retried.

### SSH drop mid-attach

The PTY session belongs to the server, not to the connection, so a dropped
attach changes nothing about the run. Reattaching streams the complete recorded
transcript before live output, including transcript segments preserved across a
server restart. Input a member typed that the transport never delivered is
dropped whole. What reaches the agent is always an exact prefix of what the
connection delivered - never reordered, never duplicated - and a dead
connection's straggler bytes can never land after the attach unwound, so they
cannot interleave with the reattach's input.

### SSH port-forward disconnect

For `aether forward`, a client half-close is not a full disconnect. The
forwarder half-closes the backend and lets the reverse direction drain, so a
request can finish after the client has sent EOF. A full SSH channel or
connection teardown cancels the forward and closes both sides, releasing the
backend instead of leaving a stuck dial behind.

## Where each row is proven

The covering scenarios and unit-test map live in [testing.md](testing.md).
The native execution-boundary conditions linked above are acceptance criteria,
not a claim that live per-harness OOM/session-isolation proofs have passed.
