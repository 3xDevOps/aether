# Troubleshooting and failure handling

What Cloudfield does when the host reboots, a disk fills, an agent hangs or a
connection drops, and what you do next, for whoever runs the server and for
members who find a session in an unexpected state. Terms such as environment,
Enhanced mode and subsession are defined in [Concepts](README.md#concepts).

A session is one agent working on one task in its own sandbox on the server.
While it is live its status is `running` or `needs-attention`; when it ends it
is `completed`, `failed`, `interrupted`, `merged` or `abandoned`. The reason
shown beside the status says why it is there.

## What happens when

| When | What you see | What Cloudfield does | What you do |
| --- | --- | --- | --- |
| The server restarts or is killed, and session sandboxes survive | Sessions keep their status. | Reattaches to every live sandbox. An Enhanced agent keeps running under its keeper, and the new server adopts its conversation, turn and open requests ([Restarts and failures](enhanced-sessions.md#restarts-and-failures)). A kill accepted before the crash is issued again. | Nothing. When the keeper is gone, the conversation is restored in a fresh process and a turn that was cut off parks with `turn interrupted`: send a message to continue. |
| The host reboots, or Docker restarts without `live-restore` | Live sessions read `interrupted` with `server restarted: container exited <code>` (or `server restarted` when the container is gone). | Commits the partial work as `wip: <task>`, publishes the session branch and keeps the checkout. | Pull the branch (`cloudfield pull <session-id>`) or start a new session. An interrupted session cannot be reopened. |
| A server update is interrupted, or the new version cannot boot | `cloudfield server update --status` shows `failed` or `rolled_back`. | Replaces nothing until every binary is verified. Rolls back a version that cannot finish booting. | [Recover from a failed server update](#recover-from-a-failed-server-update). |
| Free disk falls toward the floor | Launch refused with `scheduler: insufficient disk headroom: ...`. | Frees space on its own first, in a fixed order. | [Recover from disk pressure](#recover-from-disk-pressure). |
| Host memory is low, or the server stalls on memory | Launch refused with `scheduler: insufficient host memory headroom: ...`. | For low available memory, saves idle session VMs to disk and checks once more before it refuses. Below the floor it freezes every session VM at once, so none takes more memory, then saves awake session VMs to disk and thaws the rest; it never kills one. | [Recover from memory pressure](#recover-from-memory-pressure). |
| An Enhanced Claude Code session is idle for `--run-sleep-after` (30 minutes) | The Chat view shows **Asleep since <time>**; `cloudfield sessions` shows `(asleep)`. | Stops the container with `docker stop`, freeing its memory and CPU, and keeps its disk and checkout. | Nothing. The next message wakes it ([Idle sleep](#idle-sleep)). |
| A Standard or Enhanced session in a VM is idle for `--run-sleep-after` (5 minutes) | As above. | Saves the whole VM to disk with every process in it, freeing its memory and CPU. | Nothing. The next message restores it ([Sessions in VMs](#sessions-in-vms)). |
| An agent goes silent | `needs-attention`, reason `stalled: no output or file changes for <duration>`, state **Waiting**. An Enhanced session is **Possibly stalled**: `stalled: possibly — no progress for <duration>, <phase>, transport open`. | Keeps the session supervised. It returns to `running` as soon as the agent writes output, makes progress or changes a file. | Attach (`cloudfield attach <session-id>`) or send a message. See [Picking a stall threshold](#picking-a-stall-threshold). |
| A Standard agent exits | The session stays live and its terminal shows a login shell. | Opens a shell in place of the agent. It never restarts the agent. | Start the agent again in that shell, or close the session. |
| A Background agent exits | `completed`, or `failed` with `agent exited <code>: <last line of output>`. | Commits and publishes the branch, then removes the container. | Read the full output in the session's transcript. |
| The agent hits its account's usage limit | `Usage limit reached on <owner>'s account; ...` when it cannot continue. | Moves the session to another account that has usage left. | [Usage limit mid-session](#usage-limit-mid-session). |
| The owner of the session's shared account revokes the share | `account share revoked: <owner> revoked access to their account; ...` when it cannot continue. | Moves the session to another account the launcher may use, or fails it and removes its container. | [Account sharing](security.md#account-sharing). |
| An agent update fails, or the vendor is unreachable | Timeline note `could not update <agent> from <version>: the updater exited <code>: <output>`. | Launches whatever is installed without waiting for the update. Tries again from that home after 15 minutes. | Usually nothing. `sudo cloudfield-server config set agent-update false` turns updates off. |
| An agent update an older Cloudfield interrupted left the agent's npm package missing | The launch that starts the repair may not find the agent. | Restores the copy in `~/.local/lib/.<agent>-update.*/previous` in the background, before contacting npm. | Launch again. The next launch finds the agent. |
| An SSH connection drops during an attach | The CLI or dashboard disconnects. | Nothing changes for the session: its terminal lives on the server. Input that never reached the server is dropped whole. | Attach again. The full transcript replays before live output. |
| A `cloudfield forward` connection drops | The local port stops answering. | Closes both ends of the tunnel. | Run `cloudfield forward <run-id|terminal> <port>` again. |
| A launch cannot refresh the workspace mirror | The launch fails with `gitengine: mirror <kind> for branch "<branch>": <diagnostic>`. No session is created. | Never falls back to an older base on its own. | [Launch and mirror failures](#launch-and-mirror-failures). |
| Your environment's main shell exits | The environment terminal closes. | Stops the terminal and keeps the stopped container. | Open the environment again. It starts the container, or rebuilds it from your saved image. |
| An Enhanced session fails | **Disconnected**, with `enhanced session failed: <error>` or `enhanced session ended: ...`. | Keeps the session up on its login shell. | [Enhanced sessions](#enhanced-sessions). |
| A session runs out of memory | Timeline note `Out of memory: the kernel killed a process in this session at its <n> GiB memory limit (peak <n> GiB)`. | Nothing automatic. The killed process may be a tool, the agent or the shell. | Check whether the agent is still running. Raise `--run-memory` if it keeps happening. |
| The kernel's file-watch limits are spent | Server log `gitengine: cannot watch checkout for file changes; polling for diff snapshots instead`. | Launches the session anyway and checks its files every 60 seconds. | [Raise file-watch limits](#raise-file-watch-limits). |
| Another process holds the database at startup | `store: begin migration <n>: database is locked (5) (SQLITE_BUSY)` | The server exits. | Find and stop the other writer, then start again. Never delete the database. |
| The first start after an upgrade takes minutes | Clients cannot connect and the server logs nothing yet. `sudo cloudfield-server update` prints `aether-server is migrating the database at schema <n> to <m>; waiting for it`. | Migrates the database before it serves. Migration 109 reads every stored event to build three indexes: on a 9 GB event log that took 1-2 minutes in testing, longer on a busy disk. | Wait. A restart starts the migration over, and an update rolls back a release that starts twice without finishing. Before the update, free the database's size again plus a tenth of it: the update copies the database into `server-update/`, and the migration adds indexes of about 4% of the event log's size (400 MB on 9 GB in testing), as much again in `aether.db-wal`, and sort files in `/var/tmp`. |
| `sudo cloudfield-server update` takes minutes while many sessions are open | The update waits after the new server starts. The server log shows `scheduler: recovery: reconnecting sessions sessions=<n> at_once=4`, then `scheduler: recovery: session started run=<id>` and `scheduler: recovery: session done run=<id> took=<duration>` for each session, then `scheduler: recovery: sessions done`. | Reconnects every open session before it counts as started, 4 at a time, so one slow session does not hold up the rest. An update still rolls back if a session has not finished 5 minutes after the database migration. | Wait. `sudo journalctl -u aether-server -f` shows each session; a session with `session started` and no `session done` is the one still reconnecting, and a long `took` names a slow one. |
| The server is down when a schedule is due | Nothing. | Skips the missed occurrence and waits for the next one. | Launch the session by hand if it matters. |

A lost connection does not prove that a command failed. Before you retry
something, check its files and processes. After a crash Cloudfield does not
replay an agent's tool calls, but a Background Enhanced session is asked to
continue its turn, so an action can run twice.

## Session activity

A live session carries a state that says what it is doing. The dashboard and
`cloudfield sessions` show its label.

| State | Label | Meaning |
| --- | --- | --- |
| `working` | Working | A turn or tool is running. |
| `waiting-agents` | Waiting for agents | No turn runs, but the session waits on its own subagents, subsessions (`waiting for <n> subsessions`), or a `waiting --for agents` report. |
| `waiting-ci` | Waiting for CI | The agent reported `waiting --for ci`. |
| `waiting-tools` | Waiting for process | The agent waits on a background task it holds, or reported `waiting --for process`. |
| `answered` | Answered | The agent reported `answered`: it replied or discussed, with nothing pending. Never needs you and sends no notification. |
| `waiting` | Turn ended, or Stale wait for a stale wait (`cloudfield sessions`: Waiting) | The turn ended with no report (`turn ended without a report`), the session stalled, a turn was cancelled, or a wait on agents went stale (`no update for <duration>`). Nothing is asked of you. |
| `needs-you` | Needs you | A person is needed: a `needs-input` or `blocked` report, an open permission request or question, a usage limit Cloudfield cannot continue from, an Enhanced turn that waits for a reply, or a [broken wait](#waits-cloudfield-can-check). |
| `finished` | Finished: review | An interactive session reported `success`. Asks nothing of you. |
| `failed` | Failed | The session reported `failure`, or its turn failed. |
| `disconnected` | Disconnected | The Enhanced session failed or ended while the session is open. |

When several apply, the first in this order wins: disconnected, needs-you,
working, failed, finished, waiting-agents, waiting-ci, waiting-tools,
answered, waiting. An answer never goes stale.
A wait the agent reported with no condition goes stale after three times
`--stall-threshold` (30 minutes by default) with no activity, unless the session
still holds background tasks or, as a parent, has a live subsession
or unread subsession mail. Its reason becomes `no update for <duration>: <summary>`.
A CI or process wait keeps its state; an agents wait shows `waiting`, or
`waiting-agents` while a subsession is live again. A wait goes stale once; the
next turn starts it over. A wait Cloudfield infers from subsessions or
background tasks lasts exactly as long as they do.

### Waits Cloudfield can check

A waiting report can name conditions Cloudfield checks for the agent, such as
a pull request's checks finishing, so it wakes the agent when one resolves.
While the session shows such a wait, `cloudfield sessions <session-id>` prints a `wait` line, the session record carries
`wait {kind, detail, verified_at, error}`, and the state is judged by the
evidence:

| `wait.kind` | When | What you see | Needs you |
| --- | --- | --- | --- |
| `condition` | Every condition is pending and its source was read within twice its poll interval, or one resolved and its wake is on its way. | The state, and `<condition> (checked 2m ago)`. | No. The wait never goes stale on inactivity. |
| `source_unknown` | A pending condition was not read within twice its poll interval, or its reads fail. | `Waiting for CI (status unknown)` and `<condition>: status unknown (last read 12m ago)`. | No. Cloudfield keeps retrying. |
| `system_fault` | A condition errored or expired, for example the pull request is gone, and the agent is being told. | `Waiting for CI (wait fault)` and the condition. | No. The agent decides what to do next. |
| `system_fault` | A condition resolved, errored or expired, and its wake was not delivered within 5 minutes. | `Needs you`, reason `wait broken: <condition> resolved, but the agent was not woken`, and the delivery error. | Yes. The agent will not move on its own. Send it a message. |
| `system_fault` | A pending condition's source went unread for an hour. | `Needs you`, reason `wait broken: cannot read <condition> for over 1h, so the agent will not be woken`. | Yes. |
| `system_fault` | Every read of the session's conditions failed for an hour, counted from the first failure or from the wait's report if later. A read that succeeds starts the hour over, and so does a server restart. Before that the wait is `source_unknown`. | `Needs you`, reason `wait broken: cannot read this session's wait conditions for over 1h, so the agent will not be woken`, and the read error. | Yes. |
| `unverified` | The agent named no condition, or only ones that left pending before its current turn or whose wake was delivered. Once delivered, including a condition that resolved inside the agent's own report, the session waits on its agent, not on the condition. | The state, as described above. It goes stale like a wait with no condition. | No. |

The dashboard words the same evidence as "Waiting on <condition> · checked
2m ago", "Waiting on <condition>: status unknown since 01:10" or "Wait
fault: <condition>" ([dashboard-frontend.md](dashboard-frontend.md#session-state)).
`wait` reaches every workspace member, so it never holds what a read found
or its error: those come from the owner's GitHub login and are in the
session's `wait_conditions`, which only its owner and admins read in full
([session waits](watches.md#session-waits)). Session details lists each
condition under **Waiting until** with what the server last read, when it
checks next, its failures and whether the wake reached the agent, and
**Cancel** ends a pending one
([dashboard-frontend.md](dashboard-frontend.md#details)).

A broken wait is the only one that needs you, because only then can nothing
but a person move the session. While another of its conditions is still pending
or its wake is on its way, or the session still holds background tasks or, as a
parent, has a live subsession or unread subsession
mail, those can wake it: the session keeps its waiting state, marked `(wait fault)`, and needs you
only once they end. Its reason holds no time or source status, and names
broken conditions in a fixed order, so it is announced once. A paused
session is judged again when it resumes.

Each `run.status` event for a live session carries a `cause` naming what chose
its state: `turn`, `report` (the agent's own report), `turn-end`, `stall`,
`wait-expired`, `wait-fault` (a broken wait), `workers`, `tasks-held`,
`human`, or `disconnect`. While the session shows a reported wait the event
carries `wait` too. A wait whose source was read again at least a minute
later is published as a same-status event with `reread: true`;
`cloudfield timeline` and the dashboard's activity feed leave it out unless
filtered to `run.status`, `changes.list` leaves it out, and the
dashboard's lanes and trace bars never draw it as a state change. A live
session's record carries its latest `cause` too. `cloudfield timeline <session-id>`
prints it after the state, for example
`running -> needs-attention (waiting for 2 subsessions) · waiting-agents by
workers`. The dashboard ends each state's tooltip with it, for example
`Waiting for 2 subsessions since 3 minutes ago. Mail from its agents can wake it.
Why: its subsessions are live or have unread mail for it.`

An agent's `blocked` report reads `blocked: <summary>`, and its question reads
`needs input: <question>`.

### Review

A reported success puts the session up for review. The session page asks **Your agent
reports this task is finished. Mark complete?**

- **Mark complete** closes a live session as `completed`: it commits, publishes
  the branch and keeps the container like **Close**.
- **Not yet** says the work is not finished: the session leaves review and
  reads **Answered**, "Not finished yet". Once the server
  accepts it, the session's Chat view opens with the composer focused so you
  can say what is left.
- **Review later** keeps the session open and in review without a new
  notification; the prompt folds to one line, "Finished, kept for later",
  with **Review now** to open it again.

Any new turn clears the review, and a later success report asks again.

### Before a close

Closing a session that others still depend on asks first. **Close session…**,
**Mark complete**, **Open in Standard** and `cloudfield close` read
`run.close_check` ([local-gateway.md](local-gateway.md#independent-execution-and-input-state))
and list what the close affects: protection, an open pull request, commits that
may not be merged, mail from other sessions, sessions waiting for this one to
finish, subsessions that close with it and what closing each of those affects,
and pending cleanup. With any of these closing turns into a danger button
(**Close as abandoned**, or **Close anyway** for Mark complete) and Cancel
holds the focus. A check that fails shows its error and
still lets you close. Closing as merged skips the unmerged-commits warning:
you are saying the work is merged.

`cloudfield close` prints the warnings to stderr and asks `Close anyway? [y/N]` on
a terminal; `--force` skips the question. Without a terminal it prints them
and closes, so scripts keep working. A failed check prints its error. On a
terminal it then asks too, unless the server is older than the CLI and has no
check; there the close goes ahead:

```
Before closing run-d6ay38tj8j:
  pull_request: Pull request acme/api#7 was open, not merged, when Cloudfield last checked 2026-11-03T18:20:00Z
    https://github.com/acme/api/pull/7
Close anyway? [y/N]:
```

### Needs you

One rule decides whether a session needs a person. The **Needs you** list,
`cloudfield sessions --needs-you`, `cloudfield wait` (exit 3, except that a finished
success exits 0, a failure exits 1 and a broken wait exits 5), the Insights count of sessions that need
you, `attention.list`
([local-gateway.md](local-gateway.md#methods-for-an-external-assistant)), push
notifications and the assistant's notices all use it. The session status
`needs-attention` alone does not: it means the agent is parked, which includes
waiting on agents, CI or a process and a reported success.

A reported success is never a need. While its `review` is `ready` or `later`
the session shows in **Finished: review**, its own group with its own
count below Needs you, and the label `Finished: review` in the dashboard and
`cloudfield sessions`. `attention.list` still returns it, as kind `ready` with the
reason `Finished: review the result`, after every item that asks something.

Only a top-level session can be Needs you or Finished: review. An attached
[subsession](teams.md#subsessions)'s reported success or failure is for its
parent agent only, which hears of it through its wait on the subsession: it
never counts as Needs you or Finished: review, never notifies and never moves
the family, whether the parent is working, waiting, stopped, paused or
ended. The parent carries the family: what surfaces is the parent's own
state. A subsession's requests of a person (a permission, question,
`needs-input`, `blocked`, a disconnect) still surface, listed with its
parent. A detached subsession is a session of its own and follows the rules
above. A subsession has no **Mark complete** of its own; it finishes by
reporting to its parent. A watch someone asked for on a subsession still
hears of its finish; the assistant's own follow of a session it launched or
messaged does not. The dashboard reads a subsession's success as "Finished:
reported to its parent" in a muted check, and `cloudfield sessions` as
`Finished: reported to its parent`.

| Session | Needs you | Notifies |
| --- | --- | --- |
| `needs-you` | Yes | Once, when it enters the state. |
| `disconnected` | Yes | Once. |
| `failed`, result not yet seen by the owner | Yes, while the session is live | Once: `Failed, review the result`. |
| `failed`, result seen | No | No. |
| Review ready, live or `completed` | No: Finished: review | Once per success, quietly: `Finished: review the result`, at normal urgency, never in the desktop badge. |
| Review marked **Review later**, **Not yet** or complete | No | No. |
| A broken wait (`wait-fault`) | Yes | Once: `Wait broken, send a message to continue: <condition> ...`. |
| `working` or any waiting state, including `turn cancelled` | No | No. |
| Any state while the session is paused | No | Checked again on resume. |

A need is announced once: opening the session or restarting the server does not
repeat it. When it ends, or the session is handed to another member, each
browser that showed it gets a silent `No longer needs you` (`No longer
awaiting review` for a finished session) in place of the notification, and the desktop app closes its own. A server restart forgets
which browsers showed a need, so one announced before it is not replaced.

## Recover from disk pressure

Every launch checks that the Cloudfield data filesystem and Docker's storage
keep a reserve free: by default the larger of 5 GiB and 5% of the filesystem,
capped at 20 GiB, plus 1 GiB for this launch and each one already starting.
Cloudfield frees space on its own, on every poll and before refusing a
launch. When a filesystem is 85% full it removes, at most every 5 minutes,
what nobody loses: unused standard and browser images, caches over their
budget or unused for long, and leftover scratch directories, until it is at
most 80% full. Those are the marks and the period of Kubernetes' image
garbage collection. When free space drops below the reserve plus 4 GiB it
then frees what sessions and members would lose, in this order, until it is
back above, and after them every cache, image and scratch directory nobody
uses:

1. Containers and VM disks of closed sessions, the soonest to expire first.
   They go first because a closed session's work is already on its branch,
   while everything below belongs to a session that is still open. The
   timeline notes `Removed this closed session's container early because
   disk space was low; the session can no longer be reopened, and its branch
   and history are kept`.
2. The saved memory of sleeping Enhanced session VMs whose agent restores
   its conversation, the longest asleep first
   ([Sessions in VMs](#sessions-in-vms)). The timeline notes `Deleted this
   sleeping session's saved memory because disk space was low; it starts
   again from its disk when it next wakes, and restores its conversation`.
   Open sessions whose conversation would not come back in a new process,
   Standard ones among them, keep theirs, and so do sessions the memory
   guard saved while their agent worked ([Recover from memory
   pressure](#recover-from-memory-pressure)).
3. Checkouts of every finished session, whatever their age. Skipped when
   `--checkout-ttl` is negative.
4. Other members' stopped environments. Anything they installed and
   did not save with `cloudfield env save` is lost; their homes are kept.

If that is not enough, the launch is refused:

```text
scheduler: insufficient disk headroom: <filesystem> has <n> free bytes; reserve <n> bytes plus <n> bytes per provisioning (<n> already in flight)
```

With `session-runtime vm`, a session's disks are sparse files that grow as
it writes, so a running session can take the filesystem below the reserve.
When the Cloudfield data filesystem first falls below the reserve, the server
notes each VM's disk use, and every 5 seconds after that pauses every session
whose disks have grown by 64 MiB or more since that point and stops its whole
VM, so root in it cannot write either. Measuring against the point it fell
below, not the previous check, catches a session filling the disk slowly as
well as one filling it fast. Its timeline notes `Paused: the server is low on
disk, and this session's disks were filling it. It resumes on its own once
there is space again.` A session that grew less is never paused. A VM that is
not a session, such as an environment terminal, is only logged (`a VM that is
not a session is filling the data filesystem`). Once free space is back above
the reserve plus 4 GiB, the sessions it paused resume and note `Resumed: the
server has disk space again.`; one you paused, resumed, closed or woke
yourself is yours, and the guard leaves it. A session that fills the disk
again within 10 minutes of such a resume stays paused until you resume it
(`Paused again: ...`). While a session is held, its Files view and closing it
wait for the resume. Files a session deletes give their space back to the
host. What a session writes to its home, which lives on the server rather
than on a VM disk, is not counted, as it is not for a Docker session.

Attaching, messaging, pulling, closing, killing and deleting sessions keep
working below the reserve, and so does reopening a closed session whose container
is still there. To free space yourself:

1. Open **Insights → System** to see what uses the space.
2. Delete sessions you no longer need with **Delete run** or `cloudfield delete
   <session-id>`. That removes the checkout, transcript and records; the
   published branch, the timeline and the cost totals stay.
3. Free retained sandboxes with **Free retained sandboxes…** (admins).
4. Retry the launch.

| What grows | What frees it |
| --- | --- |
| `checkouts/` | The checkout cleanup after `--checkout-ttl`, or deleting the session. |
| `transcripts/` | Deleting the session. |
| `aether.db` | Deleting runs or the workspace. |
| `repos/` | Nothing. Every push and session branch is kept. |

Archiving a session (**Archive finished sessions…** or `cloudfield archive <session-id>`) only
hides it and frees nothing. **Archive finished sessions…** also offers live
sessions idle and done for over a day; it closes each one you leave checked
before archiving it.

`scheduler: host capacity unavailable: ...` means Cloudfield could not measure a
filesystem or host memory, for example a Docker daemon whose storage it cannot
see or a probe that timed out. Fix what the error names; free space in `df`
on a different machine does not count. When Docker does not answer, the error
ends with `the server's operator can check Docker with: sudo systemctl status
docker`. If a filesystem cannot be read at all, the disk check lets the launch
through.

## Recover from memory pressure

Every launch and wake checks two things. Available memory (`MemAvailable`)
must cover the floor, the larger of 2 GiB and a tenth of the host's memory,
plus what the start takes and what starts already admitted have yet to take;
otherwise the error ends `<n> available bytes; reserve <n> bytes plus ...`.
Memory ceilings (`--run-memory`) are never counted, only what sessions hold
now.

- A launch takes 512 MiB while it runs. On a hosted test runner a 2 GiB
  session VM held 159 MiB of the host once booted; what it grows to later is
  memory the next check sees as used.
- The wake of a saved session VM takes the size of its saved memory on
  disk, which the restore loads back: 918 MiB on disk for a VM that held
  934 MiB, and 938 MiB held again after the wake. After the wake it keeps
  counting what the VM has not loaded yet, until the VM holds all of it,
  stops, or has held no more for a minute, which the server measures every
  5 seconds. While that measurement fails, nothing changes but the minute.
  A saved size that cannot be read counts as the VM's cap, its memory plus
  512 MiB.

The server's own cgroup must also not be stalling on memory: its `memory.pressure`
`some avg60` must stay below 10%, which means tasks in the server's cgroup were
blocked on memory for under 6 s of the last minute. Otherwise:

```text
scheduler: insufficient host memory headroom: the server stalled on memory for <n>% of the last 60s (its cgroup's memory.pressure, some avg60); launches are refused at 10%
```

Only the server's cgroup counts. A session that thrashes inside its own memory
limit reclaims within its own cgroup and does not refuse launches, although it
raises the host-wide pressure that **Insights → System** charts (`some avg10` of
`/proc/pressure/memory`). To read the number the gate uses, for the service:

```sh
cat /sys/fs/cgroup"$(systemctl show -p ControlGroup --value aether-server)"/memory.pressure
```

For a foreground server, take its PID from the `aether-server serve` process
(sessions run `aether-server mcp` processes that the host also sees):

```sh
cat /sys/fs/cgroup"$(cut -d: -f3 /proc/"$(pgrep -of 'aether-server serve')"/cgroup)"/memory.pressure
```

A foreground server shares its cgroup with the shell that started it, so
memory-heavy jobs in that shell count toward the server's stall. Without
cgroup v2, with the server in the root cgroup, or without `memory.pressure` for
its cgroup, the stall check is skipped and the server logs that once. A file it cannot read or
parse refuses launches with `scheduler: host capacity unavailable`, like the
other probes.

A launch or wake that memory refuses first saves idle session VMs to disk,
the longest idle first, until twice the floor plus what the start and the
starts in flight take is free, then checks once more. It waits at most 30
seconds for any one save: a save still running then goes on alone, and the
check runs at once, so the launch or wake is likely refused until that save
lands. It never saves a VM
whose agent is working. It saves none when all idle VMs together hold too
little to admit the start, or when only the server's own memory pressure
refused it.

With `--session-runtime vm`, the server stops VMs from taking the host's
last memory in two steps: a brake that freezes them at once, and a memory
guard that then frees memory by saving them to disk.

The brake reads `MemAvailable` from `/proc/meminfo` every 500 ms. Below the
floor it freezes every session's VM by writing `1` to the
`cgroup.freeze` of its unit under `/sys/fs/cgroup/aether.slice`. That takes
one write per VM, no new process and no answer from systemd, Cloud
Hypervisor or the guest, so it works on a host with no memory left. A
frozen VM runs no vCPU: its guest allocates nothing, keeps all its memory,
and answers nothing, so its terminals, messages and file views wait. Its
timeline says `Paused: the server was running out of memory. It carries on
by itself once memory is free again. If it has to be saved to disk for
that, it wakes with every process where it was.`, at most once in 5
minutes. A session the server is working on needs its guest to answer, so
the brake leaves it running until that is done: one it is launching,
waking once admission lets the wake go ahead, closing, killing, finishing,
saving because it is idle, or taking up after a restart, an Enhanced
session whose agent it restarts, and a Background session whose turn it
ends. Once
`MemAvailable` is back at the floor it thaws one VM each check, the longest
frozen first; if that takes the host below the floor again, every VM
freezes again. A woken VM still loading its saved memory keeps that memory
set aside for as long as it stays frozen. Every frozen VM so ends either
thawed or saved to disk and woken later.

A server that stops with less than twice the floor free leaves the VMs
frozen: thawed at once, they would run with nothing watching them. The stop
waits for a frozen guest only within fixed bounds. It lets go of what runs
in the VMs in steps, one after another, each step for every session at
once: kept terminals, 5 seconds to close their input; `cloudfield ssh`
commands, up to 22 seconds to stop them; Enhanced agents, 9 seconds, as
measured in a real frozen VM: 3 for the keeper to confirm a detach, 1 for
the ack of the last output the server recorded, and 5 to close the input;
Codex app-servers, 5 seconds to close their input. Frozen guests so add at
most about 41 seconds to a stop, within the `TimeoutStopSec=90` of
`aether-server.service`. The next server adopts each agent as after a kill
([enhanced-sessions.md](enhanced-sessions.md#restarts-and-failures)). The
server that starts, after a stop or a crash, never lets a frozen VM run to
take its session up. It takes the session up asleep, marked to wake, and
thaws nothing until it has taken every session up. Then the memory guard
saves those VMs to disk as they stand while the host is short, the brake
thaws them one a check once it is not, and the guard wakes each on its own
once its memory fits above twice the floor, as a session it saved; one
that was never saved needs no room, since it holds its memory already. A
VM whose
save the stop interrupted is saved, or woken, the same way. To thaw
them yourself on a host whose server stays down:

```sh
echo 0 | sudo tee /sys/fs/cgroup/aether.slice/aether-vm-*.service/cgroup.freeze
```

The floor is sized for the fastest growth measured. On a 29 GiB host, three
session VMs filling 5.5 GiB each at once took 1.4 GiB/s, 1.5 GiB/s at the
peak, and ran the host out in about 10 seconds, before the first save had
finished. That host's floor is 2.9 GiB, which such a burst takes 1.9
seconds to use, so the freeze lands with at least 2.1 GiB left. If nothing
can be saved, because no saved memory fits on the disk or every save
fails, and nothing else frees memory, the VMs stay frozen. So does a VM
the guard never saves while everything it can save is not enough: a
Background session's, a session waiting for an answer or changing mode or
account, and one with a delivery, an exec such as a port forward, or a
browser operation in flight, which a frozen guest never finishes. Kill or
close sessions, which thaws theirs, or free memory or disk.

The memory guard reads `/proc/meminfo` and
`/proc/pressure/memory` every 5 seconds. It starts when available memory
falls below the floor, or when the host stalls on memory: `full avg10` at
60% or more for 30 seconds, which means every task was blocked on memory
for most of the time. Those are systemd-oomd's defaults, where it starts
killing. The guard never kills a session. It saves awake session VMs to
disk, with every process in them, as an idle sleep does
([Sessions in VMs](#sessions-in-vms)), in this order:

1. a closed session's VM that its close could not stop, and the still
   running VM of a session recorded asleep, such as one a restart took up
   frozen; a closed session's is stopped rather than saved;
2. an open session whose agent is idle, the longest idle first;
3. when none is idle, the session holding the most host memory, even while
   its agent works. A VM's host memory is what its unit holds, guest page
   cache included ([vm-guest.md](vm-guest.md)).

While less than twice the floor is free it saves one VM each check; above
that, one more each time a stall lasts 30 seconds again, and never one
whose agent works: such a stall may come from outside the VMs. A VM it
saves while less than twice the floor is free is frozen first and saved as
it stands, without
the sync and the wait for dropped page cache an idle sleep makes
([vm-guest.md](vm-guest.md#limits-stats-and-the-clock)), which would let its guest run and
take memory. A guest the brake froze cannot say whether it is busy, so a
session saved after the brake froze it always wakes on its own, also when a
launch or wake saved it to make room. It skips a
VM whose memory is unknown or would not leave the data filesystem its
reserve plus 4 GiB once the saves still under way have written theirs,
the level below which disk cleanup deletes the saved
memory of other sleeping sessions
([Recover from disk pressure](#recover-from-disk-pressure)), and for 5
minutes one whose save failed. A save waits for Cloud Hypervisor to finish
writing the snapshot, for as long as writing the VM's memory at 20 MiB/s
takes plus 2 minutes, but the guard waits for it at most 30 seconds. A
save still running then goes on alone and ends that check. The next check
measures memory again without counting what that save will free, so it may
save another VM meanwhile: a save that hangs holds the guard up no longer.
A save that fails leaves the VM running; a VM that
died instead ends its session as any VM that
died, also when the server is shutting down: `failed`, with its work
committed from its disks. So does, at its next wake, a session recorded
asleep whose VM stopped before it was saved, unless it can start again
from its disk ([Sessions in VMs](#sessions-in-vms)). It stops once
twice the floor is free and `full avg10` is below 60%. Then a session it
saved while its agent worked, or while its guest still used the CPU, wakes
on its own, one at a time, the first saved first among those whose memory
fits above twice the floor. That wake passes the same check as any wake, so
the guard never wakes one into memory another is still loading. The guard
does not wait for it: a wake that hangs leaves the guard saving the other
VMs every check, and holds up only the next wake. A wake that
admission refuses is tried again at the next check; one that fails otherwise is not, and its timeline says
`Could not wake on its own once memory was free again: <error>`. Its turn
carries on where it was, as across a server restart; a model call the save
cut off fails as on a network outage, and the agent retries it. Every other
saved session wakes on its next use. The timeline of each saved session
says
`Saved to disk while it worked: the server was running out of memory.`,
`Saved to disk: the server was running out of memory.` for an idle session
the brake had frozen, or
`Saved to disk early: the server was running out of memory.` The server log
has `scheduler: memory guard:` lines. `--run-sleep-after 0` does not turn
the guard or the brake off.

Memory the host runs out of all the same, taken by something outside the
session VMs, such as an environment terminal's VM, which the brake does not
freeze, or faster than the brake's check, goes to the kernel's OOM killer,
which ends the process with the highest score first:

| `oom_score_adj` | Processes | Set by |
| --- | --- | --- |
| 500 | Every process in a Docker session container or browser companion | The server, when it creates the container |
| 0 | A session VM's unit: Cloud Hypervisor, which holds the guest's memory, and its file services. Also every other service on the host | systemd's default; the unit sets none |
| -500 | The server | `OOMScoreAdjust=-500` in its unit, which the server also sets on itself when it starts |

So container processes go first, one at a time, then the session VM
holding the most memory, whose whole session ends, and the server last.
`journalctl -k | grep -i 'out of memory'` names a killed VM
`cloud-hyperviso`, the kernel's 15-character cut of `cloud-hypervisor`.
Inside a VM, the guest's own OOM killer ends session processes at the
session's memory limit and never the helper
([vm-guest.md](vm-guest.md)).

To recover, close or kill sessions. A closed Standard or mode-switched
session's paused container still holds its memory; **Free sandbox…** releases it. An idle
Enhanced Claude Code session frees its memory on its own after
`--run-sleep-after` ([Idle sleep](#idle-sleep)), and so does any idle session
in a VM. A closed session VM or Enhanced session frees it as soon as it is
closed. Running work in a container is never stopped to make room.

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

`cloudfield server update` downloads and verifies both binaries before it replaces
either, so a bad tag or a failed download changes nothing. A scheduled update
that fails on the network or the release host is retried at the next idle
moment, up to three times, then recorded as `failed`.

After the swap, the new version must open and migrate the database, then bind
its SSH listener and recover the sessions within 5 minutes of migrating. If it
fails to finish booting twice, or misses those 5 minutes, Cloudfield rolls back:
the previous binaries go back into place, and if the schema already changed,
the database copy taken before the update is restored. Every change made after
that copy is lost.

1. Read the result:

   ```sh
   cloudfield server update --status
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
If that fails, the launch is refused and no session is created; Cloudfield never
starts from the previously accepted commit on its own. When an accepted commit
exists, the CLI prints a one-off retry from it:

```text
retry from cached <commit> with:
  aether session --agent <agent> --mode <mode> --workspace <workspace> --account <member-id> --cached-base <commit> -- <task>
```

`--cached-base` applies to that launch only, and is refused when the base has
moved since. To repair the mirror itself (admins):

1. Read the diagnostic:

   ```sh
   cloudfield workspace mirror status --workspace <workspace>
   ```

2. Fix the cause from the server, not from your laptop. Check DNS and outbound
   access from the server host. For TLS errors, fix the server's git
   certificate trust; never turn verification off. For a deploy key, check
   that the key is still installed upstream.
3. Refresh with `cloudfield workspace mirror refresh --workspace <workspace>`.

A new or reconfigured mirror stays `pending` until an admin adopts a revision
of its current generation, and every launch is refused until then. The
refusal ends with the adopt command, and `cloudfield workspace mirror status` and
`cloudfield workspace mirror refresh` print it with the workspace's values filled
in:

```text
sessions cannot launch until an admin adopts revision <sha>
check that it is the upstream commit you expect on <branch>, then run:
  aether workspace mirror adopt --workspace <workspace-id> --generation <n> --commit <sha> --yes
```

Compare `<sha>` with the upstream branch, for example with
`git ls-remote <source> <branch>`, before you adopt it. In the dashboard the
same step is **Repository → Advanced → Review source mirror → Adopt
candidate**.

An upstream that was rewritten or diverged stays held (`rewritten` or
`diverged`) until an admin reviews it and adopts the new revision with
`cloudfield workspace mirror adopt --workspace <workspace> --generation <n>
--commit <reviewed-sha> --yes`. Published session branches stay fetchable while a
mirror is failing.

## Enhanced sessions

An [Enhanced session](enhanced-sessions.md) talks to its agent through a separate
process in the session sandbox. When that process fails, the container and its
login shell stay up.

| Failure | What you see | What you do |
| --- | --- | --- |
| The session cannot start, for example because the agent is not signed in | **Disconnected**, `enhanced session failed: <error>`. Messages are refused with `scheduler: the enhanced session is not running: <error>`. | Fix the cause, then press **Retry Enhanced** in the Chat view. |
| The agent's process exits | The turn in flight shows **Turn interrupted**; **Disconnected**, `enhanced session ended: the agent's ACP server exited with code <n>`, with `; stderr: <tail>` when it printed any. | **Retry Enhanced**. |
| The agent drops its conversation but keeps running, as Claude's adapter does when Claude Code dies | **Prompt failed** with `Session not found`, then **Agent conversation lost** while Cloudfield restores the conversation. Lost again within 5 minutes: **Disconnected**, `enhanced session failed: the agent lost its conversation again within 5 minutes: ...`. | Send the failed message again. After a second loss, **Retry Enhanced**. |
| The agent refuses a turn with `Authentication required` | **Sign-in refused**, or **Agent signed out** when the login file cannot sign the agent in, while Cloudfield restarts the agent in its container, then `enhanced turn failed: ... Authentication required ...; <owner>'s <agent> login is signed out. Sign in, then send the message again: dashboard Agents page, or aether agent login <agent>`, or `...; Cloudfield restarted the agent to reread <owner>'s login: send the message again. Refused again? Sign in: ...` when the login file looks sound. The account owner's dashboard shows **\<agent\> is signed out** or **\<agent\>'s sign-in was refused** until the login works again. | Send the message again when the reason says so. Otherwise the account's owner signs in on the **Agents** page or with `cloudfield agent login <agent>`, then you send it again ([harnesses.md](harnesses.md#when-a-login-is-signed-out)). |
| `run.launch` answers `<owner>'s <agent> login is signed out: <problem>. Sign in, then launch again: dashboard Agents page, or aether agent login <agent>` | The login file on the account cannot sign the agent in. Nothing was created. | The account's owner signs in, then launch again. A workspace variable that signs the agent in, such as `ANTHROPIC_API_KEY`, lets the launch through ([harnesses.md](harnesses.md#use-an-api-key)). |
| Root in one of your sessions took your shared home, for example a package install that ran `systemd-tmpfiles` | **Home restored** with the owner and mode Cloudfield found, before the agent starts again. If restoring fails: **Disconnected**, `enhanced session failed: the agent cannot read its home: ...`. | Send the refused message again. Run `sudo chown "$(id -u):$(id -g)" "$HOME" && sudo chmod u+rwx "$HOME"` in the session's **Terminal** tab, which runs as the session's user, when restoring failed, or when the agent stays connected while its tools fail with `permission denied` or exit status 127, which no restore covers. After a failed restore, press **Retry Enhanced**. |
| The server restarts mid-turn | The turn keeps running in the adopted conversation; a message queued behind it is sent again unless the agent already had it. In a session VM, a name lookup made while the server is down fails: the server answers the VM's DNS. | Nothing. Run again a command that failed to resolve a name. |
| The server restarts and cannot reattach to the agent's keeper, or the session's sandbox predates keepers | **Agent restarted** with the reason, then **Needs you**, `turn interrupted`. Background tasks the agent was running are gone; commands in its `cloudfield-internal terminal` terminals keep running. | Send a message to continue. |
| You cancel a turn or press Stop | **Waiting**, `turn cancelled`. | Send a message when you want it to go on. |
| A Claude background task never ends | `running`, `working in background: <n> tasks`. | Press Stop. |
| The old session cannot be restored | A new session starts; the item log says why. | Repeat context the agent lost. |

A Background Enhanced session has no one to retry it: any of these failures ends
it as `failed` with `agent exited 1: <cause>`, for example `agent exited 1:
the agent's turn ended: cancelled`. A server restart mid-turn leaves its turn
running; when the server cannot reattach, it restores the session and asks the
agent to continue.

## Usage limit mid-session

When an agent reports that its account's usage limit stopped it, Cloudfield
moves the session to another account that has usage left
([usage fallback](teams.md#usage-fallback)): the same session, checkout and branch
continue on the new account's login, and a room note says so. When it cannot
move, interactive sessions park as **Needs you**, a Background session fails, and the reason says why:

| Reason | Meaning |
| --- | --- |
| `Usage limit reached on <owner>'s account; no other account is shared to fall back on` | No other account is shared with the launcher, or with the member whose home the session mounts, or listed in its `--fallback` order. Ask a teammate to share theirs, or wait for the limit to reset. |
| `Usage limit reached on <owner>'s account; no fallback has usage left` | Every other account was read as exhausted or was already used by this session: the launcher's own, or a shared one whose login the server confirmed in the session's sandbox. Wait for the limit to reset, or ask a teammate to share theirs. |
| `Usage limit reached on <owner>'s account; no account that may have usage left can run <agent> here` | Accounts with usage left, or whose usage could not be read, cannot launch the agent: their owner is not signed in to it, the agent is not installed for them, or the share no longer covers the session's home. |
| `Usage limit reached on <owner>'s account; Cloudfield cannot read <agent>'s usage, so its sessions do not move to another account` | Only `claude` and `codex` sessions move ([harnesses.md](harnesses.md#usage-limit-signals)). Wait for the limit to reset, or relaunch on another account. |
| `Usage limit reached on <owner>'s account; fallback is off for this session` | The session was launched with `--no-fallback`. |
| `Usage limit reached on <owner>'s account; cannot continue on another account: <reason>` | The session cannot move: for example the agent reported no session to resume, it is a member's own agent definition, or the owner may no longer launch. Nothing was changed. |
| `Usage limit reached on <A>'s account; continuing on <B>'s account failed: <error>` | The move started and failed. The session fails. `start container: runtime: <path> in the container is inode <n>, not <login> in <B's home> ...` means the container would have signed in with another login than B's; the rest of the error says whether Docker or the container runtime left B's login out. |
| `Usage limit reached on <A>'s account; the session is on <B>'s account and its session could not be restored: <error>` | The session moved, but the Enhanced session did not come back. Send a message to start fresh. |

A paused session, or one being closed or killed, is not moved. A move survives a
server restart: the next start finishes it.

## Raise file-watch limits

Cloudfield watches each session's checkout for changes with inotify. The kernel
caps watches and instances per user, and a server running as root shares that
budget with every root process in its session containers. When the kernel refuses,
the session still launches and its file changes arrive up to 60 seconds late,
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

Sessions launched afterwards watch again. A session that is already polling keeps
polling until it is reopened.

## Idle sleep

An Enhanced session of an agent whose conversation survives a container
restart falls asleep after `--run-sleep-after` (default `30m`) without
activity: Cloudfield runs `docker stop` on its container, never `docker rm`.
Memory and CPU are freed. Only Claude Code sleeps
([harnesses.md](harnesses.md#supported-agents), **Idle sleep**). Standard
sessions never sleep, because starting their container again reruns the
launch command with the original task; Background sessions are one-shot;
Enhanced sessions of other agents stay awake until a test proves their
conversation survives a restart. Sessions in VMs sleep differently: see
[Sessions in VMs](#sessions-in-vms).

Activity is any of these within the window: a turn, queued prompt, open
permission request or question; agent output or a file change in the
checkout; output or typing in the session's terminal or a shell tab; a
`cloudfield ssh` process or port forward still open; a Browser tab action such as
navigating or clicking (an open Browser tab's own polling does not count); a
member typing in the composer while another member watches the session, the
only time the dashboard reports typing. A session that waits for you after
its turn ended, or waits for CI or another session, is idle.

These keep a session awake however long it is quiet: a background task the
agent started, such as a dev server, a subagent or a monitor; a command in
the foreground of a terminal or shell tab, such as a dev server, an editor or
a REPL (a shell at its prompt does not count); a session that waits for a
process; a session you paused; a session that hit its usage limit; a session
switching modes or accounts; and a session started in Standard and switched
to Enhanced, whose container would rerun its launch command.

The session wakes when you send a message, change its model, mail
reaches it, you type in its terminal, open a shell tab, use its Browser tab,
start `cloudfield ssh` or a port forward to it, run `cloudfield resume <session-id>`,
or choose **Wake** in the Chat view. Opening the session or its Browser tab
without acting does not wake it. A port forward wakes the session, but a server
that ran detached, such as one started with `nohup`, stopped with the sleep:
the connection is refused until you start it again. The request that woke it waits while the
container starts and the agent restores its conversation, then goes through.
Two wakes at once start the container once. Waking counts against the same
memory and disk checks as a launch, and a saved VM must fit its saved memory
([Recover from memory pressure](#recover-from-memory-pressure)). A refused or failed wake returns its
error to the request that caused it, keeps the session asleep and shows the
error under **Asleep since**. A container's wake never starts a new
conversation: when the agent cannot restore its own, every wake fails with
the agent's error; launch a new session on the session's branch, which is
kept with its checkout.

A wake takes as long as starting the container and restoring the agent's
conversation: in the real-agent test, a message to a sleeping Claude Code
session was accepted about 1.6 seconds after it was sent on an idle host,
and up to 23 seconds on a loaded one.

What a sleeping session keeps: the checkout and home on disk, including
files the agent wrote but did not commit; the container's own disk layer;
the conversation, the transcript and the branch.

What it loses: every process that was still running, which only a process
Cloudfield cannot see can be, such as one started with `nohup` or `&`, or nested
Docker containers such as those of `docker compose up -d`; open network
connections; anything held only in memory; and the Claude Code session's
scheduled wakeups (`/loop`, `ScheduleWakeup`, `CronCreate`), which live in
the agent process. Shell tabs from before the sleep show as ended, and the
terminal and the Chat view say processes were stopped. No turn is lost: a
running turn, queued input or open request keeps the session awake.

The Browser tab's companion is removed when the session falls asleep; it
shares the session container's network, which the stop ends. Its open pages,
cookies and sign-ins, held only in its memory, are gone. The first Browser
use after the wake starts a new companion.

Do not run `docker container prune` or `docker system prune` on the server:
they delete the stopped containers of sleeping sessions. A sleeping session
whose container was deleted ends on its next use, as a session whose
container disappears does: its checkout and branch are kept.

A closed Enhanced session's container, whatever its agent, is stopped the
same way when the close finishes, unless the session switched modes ([Relaunching a closed
session](#relaunching-a-closed-session)). `--run-container-ttl` and
`--checkout-ttl` work as before for sleeping sessions.

To keep every session awake, run
`sudo cloudfield-server config set run-sleep-after 0`, then
`sudo systemctl restart aether-server`. Any other duration, such as `2h`,
changes the idle time the same way, for containers and VMs alike. There is
no dashboard setting.

### Sessions in VMs

With `--session-runtime vm`, a sleep saves the session's whole VM to disk,
with every process in it, and a wake restores it where it was. Nothing is
lost, so more sessions sleep, sooner:

- Every agent sleeps, and Standard sessions too. Background sessions never
  do.
- `--run-sleep-after` defaults to `5m` for session VMs. A value you set
  applies to VMs and containers alike.
- A save keeps every process, so a dev server does not keep a VM awake:
  neither a command in a terminal's foreground nor a background shell the
  agent started and does not wait for. It sleeps with the VM and answers
  again after the wake. A VM using more than half a CPU core, such as a
  build or a test run, stays awake. The other conditions above keep a VM
  awake as they keep a container, the agent's subagents and monitors among
  them, except a switch from Standard, which a restore never reruns. So
  does a wakeup the agent set that has yet to fire. Nothing from outside
  wakes a VM: a tunnel such as `cloudflared` or `ngrok`, a webhook receiver,
  or a quiet download or remote `ssh` job freezes with it and may lose its
  connection, so keep such a session in use, or set `--run-sleep-after` to
  `0`. A save that fails leaves the session running; Cloudfield tries again
  after another `--run-sleep-after`.
- A closed session's VM is stopped as soon as the close finishes, whatever
  `--run-sleep-after` is, and keeps its disks without its memory
  ([Relaunching a closed session](#relaunching-a-closed-session)). Closing
  or killing a sleeping session wakes it first, so its work is committed as
  for a running one.

A sleeping VM keeps everything: its processes, including dev servers,
nested Docker containers such as a `docker compose` stack and the Browser
tab's browser; its terminals and shell tabs, which reconnect on the wake;
the agent process and its conversation; its disks. Connections to outside
the session, such as a database client's or a port forward's, can end while
it sleeps, and the programs holding them see them fail after the wake, as
after a network outage. The VM's clock is set right on the wake.

Timers in a saved VM stop with it and count only time awake: on a test
host, a 150-second `sleep` and a Node `setTimeout` started before a save
fired 44 seconds after the wake, the time they still had left. So an
Enhanced Claude Code session stays awake while a wakeup it set with
`ScheduleWakeup` (as `/loop` does) or a one-shot `CronCreate` has yet to
fire, and while a recurring `CronCreate` job runs, until `CronDelete`.
Timers that Cloudfield cannot see, such as a Standard session's or a `sleep`
in a script, wait for the next wake. Cloudfield's own wakes still wake a
sleeping session, Standard or Enhanced: mail, `cloudfield-internal report
--until` conditions and watches reach it as any message does.

A Standard session's Chat and Terminal views, and an Enhanced session's
Chat view, show **Asleep since** with a **Wake** button while it sleeps.

A wake reads all of the saved memory before the VM runs. On a hosted test
runner, a VM with 1.3 GiB of saved memory woke in 1.9 to 8.5 seconds with
the file read from disk, and in about 1 second with it still in the host's
page cache. Cloud Hypervisor can also load the memory on demand, which ran
the first command 0.8 to 1.5 seconds after the wake began, and Cloudfield
does not use it: Cloud Hypervisor v54.1 takes a page that is already there
as loaded, so a page anything on the host had touched first kept zeros, and
VMs died as they woke
([vm-guest.md](vm-guest.md#save-and-restore)). On a test host, a
message to a sleeping Claude Code or Codex session running a dev server and
a `docker compose` stack was accepted 0.7 to 1.5 seconds after it was sent,
and a start from disk after a failed restore took 5 seconds. The Chat view
and the session's terminal say
`Cloudfield had saved this session to disk and restored it`.

When the saved state cannot be restored, because the server now runs
another Cloud Hypervisor or guest kernel, or the file is gone or damaged,
an Enhanced session of an agent that restores its conversation starts again
from its disk: processes that were running end, the conversation is
restored, and the Chat view gives the restore's error. Any other open
session, a Standard one among them, stays asleep: the wake fails with the
restore's error, and the VM keeps its disk and saved state. Wake it again,
or close it and **Relaunch** it, which starts it from its disk. A sleeping
VM with no saved memory left starts from its disk whatever its agent, as a
relaunched one does. A VM a failed save left running is taken up as it is
on the next wake.

The saved state is kept until the woken guest answers. A guest that
restarts or stops before that fails the restore at once, with the last
lines of the VM's console, such as a kernel panic, in the error. For a
guest that restarted, the server log also gets the warning `vm runtime: the
guest restarted instead of answering` with the first 100 lines its console
printed after the wake in `console_start`, which say what died before the
panic. While that guest wrote none of its disks its saved state still
matches them, and the session is handled as for any failed restore above:
one that stays asleep restores it again on the next wake. Once the guest
wrote a disk, the saved state no longer matches the disks: it is dropped,
and the session starts from its disk. Every wake that starts a session from
its disk because the restore failed leaves a note on the session's
timeline as it starts it: that its processes and terminals were stopped,
what is kept, and the first line of the restore's error. A wake that then
cannot reattach to the session says in its error that the session started
from its disk without its processes, with that first line. A guest
that keeps running but does not answer within 90 seconds holds the session
from then on: its saved state is deleted, and the wake takes the guest up
as it is, or saves it again when the agent cannot be reached. A wake also
ends as soon as the VM stops under it, with
`the session's container exited with status <n> while the session woke`,
rather than after its time runs out: 3 minutes, plus 1 second per 20 MiB
of saved memory to read it back
([vm-guest.md](vm-guest.md#save-and-restore)).

A saved VM takes about as much disk as the memory its processes use, after
the guest drops its page cache: 137 MiB for an idle test VM of 2 GiB whose
page cache held 1400 MiB. A save waits up to 40 seconds for the host to get
that cache back first ([vm-guest.md](vm-guest.md#limits-stats-and-the-clock)),
and a wake, close or kill that arrives meanwhile waits for the save. When disk runs low, Cloudfield deletes the saved
memory of the longest-sleeping sessions first
([Recover from disk pressure](#recover-from-disk-pressure)). A server
restart or update leaves sleeping VMs asleep; the next use wakes them. A
stop cuts short a wake still running and leaves its session asleep: with
its saved state, or, once the restore has resumed the guest, with its VM
running, which the next server saves again, or wakes if the memory guard
had saved it.
The agent keeps running in the VM, and the next wake reattaches to it,
unless the wake had already started the VM again from its disk: then the
next wake starts the agent and restores its conversation.

## What stays after a session ends

| Action | Container | Checkout | Reopen |
| --- | --- | --- | --- |
| **Close** (`cloudfield close <session-id> --outcome merged` or `abandoned`) | Paused for the close's commit, then stopped with only its disks kept, for `--run-container-ttl` (30 days). A Standard or mode-switched session's container on a Docker host stays paused with its memory instead, kept 7 days by default ([Relaunching a closed session](#relaunching-a-closed-session)). | Kept while the container is kept. Removed after that, but no sooner than `--checkout-ttl` (72 hours) after the session ended: at 30 days with the defaults. | Yes, until the deadline: **Relaunch** or `cloudfield reopen <session-id>` ([Relaunching a closed session](#relaunching-a-closed-session)). The deadline is cleared while the session is live; the next close sets a new one. |
| No activity for 7 days in a live session, `needs-attention` included | As **Close**, with the outcome `completed`. Its timeline notes `Closed after 7 days without activity`. Only activity counts: a question waiting for an answer, a pause or a turn that made no progress does not keep it open, while an open exec or port forward, a change of mode or account, and a session the memory guard saved while it worked do. A sleeping session's 7 days run from its sleep; an awake one's restart with the server. A sleeping session VM is woken first to commit; while admission refuses the wake, it stays open until the next hourly check. | As **Close**. | As **Close**. Once its container is gone, launch a new session on its branch. |
| A Background session finishes | Removed at once. | Kept for `--checkout-ttl` (72 hours) after the session ends. | No. |
| **Kill run** (`cloudfield kill <session-id>`) | Stopped and removed. | Kept for `--checkout-ttl` after the session ends. | No. |
| **Free sandbox…** | Removed. History stays. | Kept until `--checkout-ttl` after the session ended. | No. |
| **Delete run** (`cloudfield delete <session-id>`) | Removed. | Removed with the transcript and records. | No. |

Every row removes the session's browser companion, the Chromium container
behind its Browser tab. A relaunched session creates a new companion when its
Browser tab is next used. A companion left behind by a crash or a failed
cleanup step is removed at server start and then hourly: when its session has
ended or is unknown to the server, or when the session container it shares a
network with is gone. A companion an open session can still reach is never
removed. The sweep reads only this server's journals under
`<data-dir>/scheduler/browser/`; a companion with no journal there, such as one
another server on the same Docker daemon started, is left alone.

The session branch is published before a checkout is removed, so cleanup never
loses committed work. When disk runs low, Cloudfield removes both sooner
([Recover from disk pressure](#recover-from-disk-pressure)). Relaunching uses
the same container or VM disks and checkout
([Relaunching a closed session](#relaunching-a-closed-session)). When
retention ends, a closed session's reason reads `retained container expired` or
`retained container unavailable`; when low disk space removes the container
early it reads `retained container reclaimed for disk space`.

Cloudfield captures evidence before it removes a container or checkout, and
retries a failed capture every `--poll-interval`. If the checkout is already
gone, no retry can capture it: cleanup finishes without an evidence packet and
the reason gains `evidence unavailable: session checkout missing`.

A session VM's checkout is on the VM's disk, which only the VM reads. When
the VM stopped before the session's final commit - it died, its saved state
would not restore when a close or kill woke it, its close commit failed, or
the session ended after the VM's 10-minute linger - Cloudfield boots the VM
without the agent's command and setup script, commits the checkout as the
session's final commit, publishes the session branch, captures evidence and
stops the VM. A saved VM is restored instead, since its saved memory may
hold writes its disks do not, and a VM the disk guard holds only has its
vCPUs started. One such boot runs at a time on the server. It passes the
memory check of a wake, and counts against later ones while it runs, but
saves no idle VM and skips the disk check: disk reclaim runs these boots
to free checkouts. While it does not fit, it waits for the next try. A
boot that fails otherwise is retried 15
minutes later, doubling up to 6 hours, and the checkout stays. Only once the VM is gone,
so that no boot can make the commit, the reason gains `work could not be
captured: ...` and cleanup goes ahead: the checkout's changes since the
branch's last published commit are lost.

A checkout over the evidence storage limit cannot be captured, so the session
gets no evidence packet. Cloudfield commits the checkout's files as a `wip:`
commit, publishes the session branch, adds `evidence unavailable: evidence
storage limit exceeded` to the session's reason (shown by `cloudfield sessions
<session-id>` and the dashboard), and only then removes the checkout. If the
commit, the publish or that reason write fails, the checkout stays.

Removing a checkout first moves the session's diff history out of it into
`checkouts/<run-id>.diffsnap/`, which stays; this reads the session's diff
events from the event log. If any step fails, the checkout stays and the
server logs `scheduler: checkout gc: capture before cleanup` with the session,
the attempt, the next retry time and the error. Retries wait 15 minutes,
doubling to at most 6 hours, and continue until cleanup succeeds; nothing is
removed without its history. The wait restarts from 15 minutes when the
server restarts.

That store holds only what the session's snapshots added. It reads the fork
point from the repository of the session's workspace in `repos/` (for an
extra repository, the repository of that one's workspace), where the ref
`refs/aether/sessions/<run-id>` keeps it from `git gc` until the session is
deleted. Deleting a workspace first copies the fork point into each store that
still reads it, such as another workspace's session that used it as an extra
repository. Stores that older servers wrote with their own copy of the fork
point shrink the same way, one at a time, after the server starts. A copy
writes the store's whole history, fork point included; a shrink writes only
what the session added. Both refuse a store while the disk has 256 MiB or
less free, or less than what they may write plus 128 MiB, with
`the server has too little free disk`. A refused workspace deletion fails with
that error; a refused store waits for the next start, and the server logs
`server: share diff history with workspace repositories`.

A member's containers share one uid:gid mapping, so a launch under another
mapping is refused while one of them is live. A closed session whose cleanup
is still retrying counts only while its container exists: Cloudfield asks Docker
before refusing, and the refusal names it as `session <id> (cleanup pending)`
and includes any Docker error.

### Relaunching a closed session

**Relaunch**, or `cloudfield reopen <session-id>`, brings a closed session
back until its deadline, 30 days after the close by default
(`--run-container-ttl`). The closed session shows the deadline under its
header and beside the **Relaunch** button. After it, or sooner when disk
runs low ([Recover from disk pressure](#recover-from-disk-pressure)), the
session's container or VM disks are deleted: its work is on its branch, and
its transcript stays readable.

A session VM, or the container of a session launched and closed as Enhanced,
is stopped when its session closes. It keeps its disks and no memory; a VM
with a checkout stays up with only its helper for the 10-minute linger
first, which the memory guard leaves to end. Relaunch starts it from those
disks:

- Files, installed packages and the checkout are as the close left them. The
  environment's setup script does not run again.
- Processes that ran at the close are gone, as are the terminals and the
  Browser tab's pages.
- The agent reopens its conversation: an Enhanced session through its ACP
  server, a Standard one with the agent's resume command, such as `claude
  --resume <id>` or `codex resume <id>`.
- An agent that cannot reopen its conversation starts a new one in the same
  files: an Enhanced agent whose ACP server refuses to restore it, or a
  Standard agent that has no resume command, reported no conversation, or
  whose resume command fails within 2 to 3 seconds of starting, as it does
  when the conversation was deleted. The Chat view or the terminal says so, and the
  earlier transcript stays in the session.

A relaunch that fails returns the server's error, shown above the
**Relaunch** button, and the session stays closed with its disks. An
Enhanced agent that fails to start once its sandbox runs shows its error in
the Chat view of the relaunched session instead, as after a server restart.

On a hosted test runner, a closed VM of the `ubuntu:24.04` image with no
checkout kept 9 MiB of disk, and 300 MiB after installing `gcc`,
`libc6-dev` and `make` and building a program: its disks hold only what the
session wrote over its image. Its command ran 1.0 to 1.3 seconds after the
relaunch began; the agent's own start comes on top.

A session VM closed by an older server, which saved its memory, is restored
from that memory instead, with its processes.

Any other closed session's container, a Standard or mode-switched
session's on a Docker host, stays frozen with its processes in memory until Relaunch
thaws it, which restarts nothing: Docker starts a container only with the
command it was created with, which for a Standard session runs the task
again. Nothing frees that memory for new sessions, so with the default
`--run-container-ttl` such a container is kept 7 days. A value you set
applies to it as well. **Free sandbox…** frees one sooner.

## Tuning

`cloudfield-server serve` reads these from `/etc/aether/server.conf`. Set one by
its flag name, then restart. For durations and ceilings, `0` means the default.

```sh
sudo cloudfield-server config set stall-threshold 20m
sudo systemctl restart aether-server
```

| Flag | Default | What it changes | When to change it |
| --- | --- | --- | --- |
| `--stall-threshold` | `10m` | How long a live session may go with no agent output, file change or working report before it parks as stalled. Waiting states fall back to `waiting` after three times this. | See below. |
| `--first-event-grace` | `3m` | How long an Enhanced turn may wait for the agent's first update before it parks as stalled. At most `--stall-threshold`. | Raise for agents that think for minutes before they stream anything. |
| `--stream-quiet-grace` | `5m` | How long an Enhanced turn with no tool running may go without agent progress before it parks as stalled. At most `--stall-threshold`. | As above. |
| `--poll-interval` | `30s` | How often stalls, retention and disk are checked. Values above `1m` act as `1m`. | Keep it at a third of the threshold or less. |
| `--checkout-ttl` | `72h` | How long a finished session's checkout is kept after the session ends. It also stays while the session's container is kept. Negative keeps every checkout, and disk cleanup leaves them alone. | Lower on a small disk. |
| `--run-container-ttl` | `720h`; `168h` for a container that stays frozen in memory | How long a closed session's container or VM disks are kept for reopening ([Relaunching a closed session](#relaunching-a-closed-session)). A value you set applies to every closed session. Negative removes them at once. | Lower when paused containers hold too much memory, or closed sessions too much disk. |
| `--run-sleep-after` | `30m`; `5m` for session VMs | How long an idle Enhanced session of an agent that can restore its conversation keeps its container running, and how long any idle session VM runs before it is saved ([Idle sleep](#idle-sleep)). `0` keeps every session awake. | Lower on a host short of memory; `0` when processes in idle sessions must keep running. |
| `--min-free-disk` | automatic | The free-space reserve per filesystem, in bytes. Negative turns the disk check off; the memory check stays. | See below. |
| `--run-cpus` | 8, or the host's CPU count if lower | CPU ceiling per session and environment. | Lower to share a small host. |
| `--run-memory` | 8 GiB | Memory ceiling per session and environment, in bytes. Swap adds a quarter of it, in a container from the host's swap and in a VM from a swap file on its data disk. | Raise when sessions hit out-of-memory kills. |
| `--run-pids` | `4096` | Process and thread ceiling per container. | Raise for builds that start many processes. |
| `--agent-update` | `true` | Update a shipped agent in the member's home, in the background, when a session launches. | Turn off on a host without internet access, or to hold a version. |

The CPU, memory and process ceilings apply to containers created after the
change. A host-wide out-of-memory kill takes container processes first, then
session VMs, and the server last
([Recover from memory pressure](#recover-from-memory-pressure)). Under
CPU and disk contention the server gets a hundred shares to each container's
one
([install.md](install.md#keep-the-server-reachable-under-load)).

### Picking a stall threshold

For agents that report their turns (`claude`, `codex`, `opencode`, `pi` and
`omp`), a finished turn parks the session at once, and the threshold only catches
an agent that hangs mid-turn. For other agents, silence is the only signal,
and a long build or slow tool call looks the same as a hang.

- Too low, and long tool calls park healthy sessions as stalled.
- Too high, and a wedged agent wastes hours before anyone notices.

10 minutes suits interactive sessions. Raise it for Background sessions with long
builds. Lower it to a minute or two for many short scripted sessions.

An Enhanced session is judged on its agent's progress: messages, thoughts, tool
calls, plans, subagents, compaction, background task progress and permission
requests. Cloudfield's own calls to the agent, the history a restored session
replays, and mode, command and usage updates are not progress. How long it may
stay quiet depends on where the turn is, its phase:

| Phase | Limit |
| --- | --- |
| `awaiting-first-event`: the prompt was sent, nothing came back | `--first-event-grace` |
| `streaming`: the agent replied, no tool is running | `--stream-quiet-grace` |
| `tool-running`: a tool call or a compaction is open | `--stall-threshold` |
| `tool-running` between turns: background tasks hold the session | three times `--stall-threshold` |
| `awaiting-input`, `between-turns`, or the turn that asks for a missing report | `--stall-threshold` |

A stall that names a phase is a possible stall: the dashboard shows
**Possibly stalled**, never **Needs you** or **Disconnected**. **Disconnected**
needs the agent process or its connection to end. An agent that thinks for
minutes without streaming a thought parks at the short limit and returns to
`running` with its next update. A resume or a server restart starts the quiet
time over.

Cloudfield restarts nothing on its own. The session's Chat view shows the
agent's last progress, the phase and the connection, and offers:

- **Stop turn** cancels the turn. It costs nothing.
- **Open trace** opens the turn's model calls and tool calls.
- **Resend last prompt…** sends your last prompt again after you confirm. It
  costs tokens, and the agent may repeat tool actions it already took. A turn
  that is still running finishes first, so stop it to send now.

Messaging a stalled session usually gets it going. The echo of your message does
not count as agent output, so a hung agent still reads as stalled afterwards.

### Picking a disk floor

The automatic reserve suits most hosts; raise `--min-free-disk` for large
checkouts or a filesystem shared with other services. Host memory is checked
separately: a launch needs the larger of 2 GiB and 10% of RAM available, plus
512 MiB for it and each launch already starting. Memory ceilings are not
reservations, so idle sessions can share a host. Refusals and the cleanup order
are in [Recover from disk pressure](#recover-from-disk-pressure).

## Limits

- `aether.db` does not sync every write to disk. A killed server loses no
  committed write, but a power loss or kernel crash can drop the last few;
  the database stays consistent.
- A Standard agent survives a server restart only if its own process
  survived. Extra terminals in a session lose their old output streams.
- Cloudfield does not restart an agent killed for memory, and an exit code alone
  does not prove memory was the cause. A cgroup v1 host posts no
  out-of-memory note; check `docker inspect` and the kernel log there.
