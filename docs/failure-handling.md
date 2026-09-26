# Failure handling and tuning


Aether is meant to survive being run on hardware that reboots, fills up and
loses connections. This file says what actually happens in each case, what
you can tune, and where the behaviour is proven. The chaos scenarios that
drive these paths are in [testing.md](testing.md).

## The tuning knobs

Five `aether-server serve` flags, all with working defaults. For duration
settings, zero means "use the default". A negative value disables a guard
where noted; for `--run-container-ttl`, negative means no retention and
immediate cleanup.

| Flag | Default | What it controls |
| --- | --- | --- |
| `--stall-threshold` | `10m` | How long a live run may go with no agent output, no file changes and nothing from its agent's own reporter before it parks at needs-attention. A run already parked because its agent said it is waiting keeps that reason. |
| `--poll-interval` | `30s` | How often that is checked, and the granularity of the return to running. |
| `--checkout-ttl` | `72h` | How long a finished run's worktree is kept before the GC reclaims it. Negative disables the GC. |
| `--run-container-ttl` | `168h` (7 days) | How long an explicitly closed TUI run retains its exact container, checkout, row, member account, and coordination surfaces. `0` uses the `168h` default; negative means no retention and immediate cleanup. |
| `--min-free-disk` | `1GiB` (`1073741824`) | Free bytes below which new runs are refused. Negative disables the floor. |

They are also `server.Config` fields (`StallThreshold`, `PollInterval`,
`CheckoutTTL`, `RunContainerTTL`, `MinFreeDiskBytes`) and pass straight through
to the scheduler.

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

### Worker launch timeouts and mirror failures

A `worker.start` response timing out (`-32004`) does **not** prove that launch
stopped. Mission launch uses a service-owned context; its durable attempt and
reserved run ID survive the request. Replay the same start command with the
**same dispatch key** to retrieve that attempt, then use `worker list` or
`worker inspect` to observe it. Do not switch keys to work around an unknown
result: the original attempt can still launch and hold concurrency.

Before creating a run, strict base capture refreshes a configured mirror.
A failed fetch never silently substitutes the previously accepted commit.
An `unknown` attempt with a mirror `last_error` is recoverable, not proof of a
dead worker: mission reconciliation can retry its original reserved run after
the cause is fixed. Replaying an already-created reserved run returns its
original pinned base without requiring another upstream fetch.

Inspect and repair the mirror from an administrator's CLI:

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
launching, running, or unknown and holds concurrency, either let reconciliation
settle it or use the normal authorized cancel operation and observe the settled
state before retrying. Cancellation and takeover authority are unchanged.
Only a launch path that explicitly supports `--cached-base <sha>` may use a
human-approved, unchanged accepted commit; mission worker recovery does not
automatically consent to a stale base.

### Integrator launch failures

`mission.create` stores the mission in `planning` and reserves its integrator
run ID before it launches that run. When the launch fails, the mission is
kept and the create error names it. Which error you get depends on whether
the scheduler wrote the run row before failing:

```
mission <mission-id> exists but its integrator run <run-id> did not launch; the server retries the launch periodically, follow it with aether swarm show <mission-id>: <cause>
mission <mission-id> exists but its integrator run <run-id> failed to start; replace it with aether swarm replace-integrator <mission-id> --agent <harness> or from the Missions page, or read it with aether swarm show <mission-id>: <cause>
```

With no run row, mission reconciliation retries the launch of the reserved
run on its periodic pass and logs each failure as `mission: recover
integrator` with the mission ID and the cause. Repeating `mission.create`
with the same contents and idempotency key also retries the launch, and
returns the same mission. The Missions page shows `The integrator run has not
started.` for that mission, and `aether swarm show <mission-id>` prints the
error as `launch error:`.
`mission.show` and `mission.list` carry the last launch error in
`integrator_launch_error` and the time it was first seen in
`integrator_launch_error_at`; both clear once the run is live (a row that
failed while provisioning keeps them) or the integrator is replaced.
`mission.replace-integrator` reports a failed launch of the new run with the
same two errors and records it the same way.

A run row that failed while provisioning is not retried, and a same-key
`mission.create` returns the mission without launching again. The Missions
page shows that the integrator run has exited. Fix the cause, then use
**Replace integrator** to launch a new integrator run.

Reconciliation only relaunches an integrator run whose row never existed.
Once the row exists, `mission.show` reports `integrator_run_launched: true`,
and deleting that run does not bring it back, not even through a same-key
`mission.create`, which then returns the mission unchanged, as it does for
a cancelled swarm. Use **Replace integrator** to start a new one, or, before
the plan is approved, cancel the swarm.

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
to run. On the next boot the scheduler reconciles every non-terminal run and
every retained closed TUI run against the runtime's actual containers:

- **An active container survived** (the server died, the container did not):
  supervision reattaches to it, the PTY session is re-adopted, the diff watch
  restarts from the tree its last snapshot wrote so the next interval
  continues the chain, and the run stays `running`. Attaches, injects and the
  eventual exit all work as if nothing happened. A kill that was accepted
  before the crash is re-issued. A run the agent had parked stays parked
  with its reason: the last report is recovered with the run, so
  reattaching - which resizes the terminal and makes a full-screen agent
  repaint - does not read as the turn resuming.
- **An active container is gone**: the partial work is committed as `wip:`, the
  run branch is published, and the run is marked `interrupted` with its
  checkout preserved. An interrupted run is not relaunchable.
- **The run never started** (it died between the row and the container): any
  container that was created is destroyed first - found by its sidecar or,
  in the narrow window before the sidecar exists, by the run ID the runtime
  persists as the container's creation key - and then the same wip-commit and
  interrupt applies.
- **A retained closed TUI container survived**: its merged or abandoned row,
  checkout, member account, and coordination surfaces remain owned by that
  exact container. Boot reconciliation preserves them for an eligible
  relaunch.
- **A retained container is gone or expired**: boot cleanup destroys any
  remaining runtime object, removes its retention metadata, and leaves the
  row unavailable for relaunch. It never creates a replacement.

Headless runs are not recovered into a shell. When their agent exits, Aether
commits and publishes the branch, records `completed` for a clean exit or
`failed` for an error, and destroys the container immediately. A `completed`
run remains available for review and an authorized member may close it as
merged or abandoned, but neither headless status is relaunchable.

Mission-assigned integrator and worker runs keep that same persistent supervisor
even in headless mode, so a one-shot harness exit does not destroy the container
or mark the run completed. They stay until Close, Kill, a successful worker
report, or worker cancel.

Mission recovery also loads durable objectives and their bounded worker
attempts. If the initial mission inventory scan fails, `aether-server serve`
reports `server: start service mission: mission: recover durable state: <cause>`
and exits instead of deferring the failed scan to periodic recovery. The
underlying store error is preserved; fix that cause before restarting.
Saved missions and attempt reservations are not deleted.

### TUI lifecycle and relaunch

For `--mode tui`, container PID 1 supervises the harness. After any normal
harness exit, PID 1 opens a login shell; when that shell exits, another login
shell opens. The run and its container therefore remain `running` until an
explicit Close, Kill, or Delete. A harness terminated by a signal or other
non-normal error does not get a replacement shell; supervision records the
failure and cleans up the container.

Close is explicit and records one of the two outcomes:

```sh
aether close <run> --outcome merged
aether close <run> --outcome abandoned
```

Closing a live TUI run pauses its container, commits and publishes the current
checkout, records the selected outcome, and retains the exact container,
checkout, run row, member account, and coordination surfaces for
`--run-container-ttl`. Zero uses the default `168h` (7 days); negative TTL
disables retention and cleans up immediately. Kill stops and destroys a run
immediately.
Delete stops any live container and removes the checkout, transcript, and
durable run records; its timeline remains audit history. Its recorded cost
survives inside its workspace's and its member's spend totals - the numbers
[`aether cost` and `aether budget`](teams.md#budgets) report, and a workspace
budget checks - so deleting a run over budget cannot reopen the cap.

Relaunch is available only for an explicitly closed, retained TUI run whose
retention deadline has not passed:

```sh
aether relaunch <run>
```

It resumes the same run row, container, checkout, member account, and
coordination surfaces. It does not create a run, checkout, branch, or
replacement container, and it performs no new launch or disk-floor admission.
An expired, unavailable, interrupted, killed, deleted, or headless run cannot
be relaunched. The expiry sweep runs within at most one minute; boot
reconciliation also sweeps expired or unavailable retained runs, so a failed
relaunch never falls back to a new run.

### Disk pressure

Four things grow without bound, and the dashboard's gauge covers all four
(`GET /api/v1/disk`, shown in the status bar with the breakdown in its
tooltip):

| Growing | Reclaimed by |
| --- | --- |
| `checkouts/` | The TTL GC, deleting the run, or the archive sweep once `deletes_at` passes. |
| `transcripts/` | Deleting the run, or the archive sweep. |
| `aether.db` (and its WAL) | Deleting the run's dependent records, or the archive sweep; the event log remains. |
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
`failed`, or `interrupted`) from the board. Archiving itself removes
nothing - the run's checkout, transcripts, cost history, and timeline are
untouched - but the checkout TTL GC above still reclaims an archived run's
worktree once `--checkout-ttl` passes. The run can be restored at any time
with `run.archive` `{"archived":false}`. Archiving stamps `archived_at`;
the wire also carries `deletes_at`, the date the archive sweep deletes the
run. Re-archiving an already-archived run does not move either date.

Once `deletes_at` passes, the archive sweep deletes the run on the first
boot or hourly sweep at or after that time - it does not act the instant
the deadline arrives. Deletion removes the same checkout, transcripts,
evidence, and run-owned database records a manual Delete removes,
publishing the run's branch first if the checkout still held commits the
branch did not have. The published branch and the run's timeline survive,
the timeline carrying a system note that records the purge. The run's
cost stays in the workspace's and the member's spend totals, the same as
after a manual delete. A run whose retained
container is still held within `--run-container-ttl`, or whose branch
cannot be published, is skipped and retried on the next hourly sweep,
logging a warning on the server naming the run and the reason. Restore
works at any point before `deletes_at`; once the sweep has run, the row
is gone and restoring it returns `-32000` not found. The retention period
is fixed at 14 days - there is no flag to change it. The sweep compares
`archived_at` against the server's wall clock at boot and hourly: a
forward clock jump, or a boot after the server was down past several
runs' `deletes_at`, deletes every one of them in that pass.

Below `--min-free-disk`, `run.launch` is refused with `-32004` (unavailable)
and a message naming the numbers. Relaunching an eligible retained TUI run
does not perform a new launch or disk-floor admission, so it can reopen its
exact retained container and checkout below that floor. Everything else -
attaching, steering, pulling, closing, killing and deleting runs - keeps
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

A clean TUI harness exit returns to the supervisor, which opens a login shell
instead of finalizing the run. Explicit Close commits and publishes the latest
work, records merged or abandoned, and applies the retention policy. A harness
terminated by a signal or other non-normal error marks the run `failed` and
cleans up the container; it does not receive a replacement shell. Headless
clean exit still commits and publishes, records `completed`, and destroys the
container immediately. A failed run's partial work is committed as `wip:`.

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

Every row above has a covering scenario or unit test; the map lives in
[testing.md](testing.md) so the suite and the map stay in one place.
