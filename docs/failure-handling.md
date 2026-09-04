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
| `--stall-threshold` | `10m` | How long a run may go with no PTY output and no file changes before it parks at needs-attention. |
| `--poll-interval` | `30s` | How often that is checked, and the granularity of the return to running. |
| `--checkout-ttl` | `72h` | How long a finished run's worktree is kept before the GC reclaims it. Negative disables the GC. |
| `--min-free-disk` | `1GiB` (`1073741824`) | Free bytes below which new runs are refused. Negative disables the floor. |

They are also `server.Config` fields (`StallThreshold`, `PollInterval`,
`CheckoutTTL`, `MinFreeDiskBytes`) and pass straight through to the
scheduler.

### Picking a stall threshold

The threshold is a bet about the longest legitimate silence. An agent
thinking, compiling, or waiting on a slow tool call produces no PTY output
and touches no files, and there is no way to tell that apart from a hang.

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
or typing on an attach) is usually what wakes it.

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

## What happens, per failure

### Server reboot, or a hard kill

State is SQLite and git, both durable, so nothing on the shutdown path needs
to run. On the next boot the scheduler reconciles every non-terminal run
against the runtime's actual containers:

- **The container survived** (the server died, the container did not):
  supervision reattaches to it, the PTY session is re-adopted, the diff
  watch restarts, and the run stays `running`. Attaches, injects and the
  eventual exit all work as if nothing happened. A kill that was accepted
  before the crash is re-issued.
- **The container is gone**: the partial work is committed as `wip:`, the
  run branch is published, and the run is marked `interrupted` with its
  checkout preserved.
- **The run never started** (it died between the row and the container): any
  container that was created is destroyed first - found by its sidecar or,
  in the narrow window before the sidecar exists, by the run ID the runtime
  persists as the container's creation key - and then the same wip-commit
  and interrupt applies.

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

Three cases never resume the pinned conversation, because the relaunch
could not read the transcript behind it. `claude --resume` on an ID it
cannot find prints `No conversation found with session ID: <id>` and exits
1, which would fail the relaunch outright.

The first two open a fresh conversation instead:

- The run was interrupted before its agent ever started (a `queued` or
  `provisioning` row). The ID is stamped when the row is created, so it
  names a conversation the harness never opened.
- The relaunch is by someone other than the run's owner. Steering others is
  allowed by default and a handoff transfers the run, but the container
  mounts the actor's credential home while the transcript lives in the
  owner's.

The third is refused: relaunching one interrupted row twice while the first
relaunch is still active fails with `agent conversation already resumed by
active run <id>`. Two agents appending to one transcript is not a
recoverable state, and the checkout guard never catches it because every
relaunch gets a checkout of its own. Once the first relaunch reaches a
terminal state, relaunching the original row resumes the conversation
again.

A run whose harness cannot pin a session (`pi`) falls back to `--continue`,
and so does a run row created before pinning existed. `--continue` names no
conversation: it resumes that member's most recent conversation at that
container path, which is not necessarily this run's own and not necessarily
one from this workspace. Treat that fallback as a convenience, not a
guarantee, and read the agent's first turn before steering it. The fallback
is sticky - a row that has no pinned ID never acquires one, because there is
no earlier conversation to name.

Relaunching a run that finished on its own does *not* resume: there is no
interrupted conversation behind it. It gets a session of its own instead.

### Disk pressure

Four things grow without bound, and the dashboard's gauge covers all four
(`GET /api/v1/disk`, shown in the status bar with the breakdown in its
tooltip):

| Growing | Reclaimed by |
| --- | --- |
| `checkouts/` | The TTL GC, once the run is terminal. |
| `transcripts/` | Nothing - they live as long as the run row. |
| `aether.db` (and its WAL) | Nothing - the event log accumulates. |
| `repos/` | Nothing - every push, run branch and reflog entry stays. |

The GC sweeps on boot and hourly. It only reclaims worktrees of runs that
reached a terminal state longer than `--checkout-ttl` ago, and never a path
an active run still names. **The branch is the artifact**: publishing
happens before the checkout is reclaimable, so reclaiming a worktree never
loses work.

Below `--min-free-disk`, `run.launch` and `run.relaunch` are refused with
`-32004` (unavailable) and a message naming the numbers. Everything else -
attaching, steering, pulling, closing runs - keeps working, which is what
you need to actually clear space.

If the filesystem cannot be read at all, the floor allows the run: the guard
exists to stop a disk from filling, not to stop the server.

### Agent stall or crash

No PTY output and no file changes past `--stall-threshold` parks the run at
`needs-attention` with a reason that leads with `stalled:`. A crash marks it
`failed`. Either way the worktree and transcript are preserved and the
partial work is committed as `wip:`.

The notification path from there:

- **Dashboard**: the sidebar badges how many runs are waiting on a human,
  those runs sort to the top, and the run card shows the reason.
- **CLI**: `aether runs` prints a notice when any run is waiting;
  `aether runs --attention` lists only those.

A clean agent exit also parks at `needs-attention` - the results are
committed and waiting to be looked at - so the badge is "runs that want a
human", not "runs that broke".

### SSH drop mid-attach

The PTY session belongs to the server, not to the connection, so a dropped
attach changes nothing about the run: reattaching resumes from the replay
ring. Input a member typed that the transport never delivered is dropped
whole. What reaches the agent is always an exact prefix of what the
connection delivered - never reordered, never duplicated - and a dead
connection's straggler bytes can never land after the attach unwound, so
they cannot interleave with the reattach's input.

## Where each row is proven

Every row above has a covering scenario or unit test; the map lives in
[testing.md](testing.md) so the suite and the map stay in one place.
