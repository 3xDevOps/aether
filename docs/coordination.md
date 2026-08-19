# Conflict coordination


The conflict radar (`internal/overlap`) detects that two active runs are
editing the same file. Coordination gives those two agents a bounded
channel to settle it themselves: one advisory notice in each terminal, and
a small run-to-run mailbox reachable on a per-run unix socket.

Nothing here blocks, locks, or arbitrates. The radar chips stay up for the
humans either way.

## Server data directory

```
<data>/coord/                       0700  coordination root, server-private
<data>/coord/<run-id>/              0755  bind-mounted into the run container
<data>/coord/<run-id>/coord.sock    0666  the coordination socket (wire v1)
<data>/coord/<run-id>/mcp.json      0444  harness config, written at provision
```

`mcp.json` is written only for a run whose harness profile registers MCP;
its content belongs to the harness registry (`mcp-bridge.md`).

The per-run directory is what the container sees (at `/run/aether`), and
the agent inside it is not root - hence the traversable directory and the
world-writable socket. The mount is the whole authentication: whoever
connects on a run's socket *is* that run, so no token ever enters a
container. The coordination root above it stays 0700 so nothing on the
host can reach another run's socket by walking the tree.

The socket file deliberately survives process shutdown: its presence is
the record that the run was provisioned, and its name is the wire version
its container was provisioned against.

## Restart recovery

On start the server walks `<data>/coord/`:

- **Coordination enabled.** A run that is still active gets every
  wire-version socket present in its directory rebound - the socket files
  on disk are the record of what was provisioned - and the rebind creates a
  new inode, which is what makes a bridge holding the old one redial. A run
  that is no longer active has its directory removed and its mailbox rows
  deleted.
- **Coordination disabled.** Old sockets are unlinked and nothing is
  recreated. The directory and its read-only config stay where a live
  container has them mounted; they are simply inert.

## Wire v1

Three methods, served as JSON-RPC 2.0 over the NDJSON framing the control
channel uses (`internal/protocol`). Nothing else is reachable: no control
verb, no git, no other run's transcript.

| Method | Params | Result |
| --- | --- | --- |
| `coord.status` | none | own run identity, the peers this run may message (state `active` or `grace` with an expiry), and the unread count |
| `coord.send` | `to_run_id`, `body` | `message_id` |
| `coord.inbox` | optional `ack_token` | one batch of `messages` plus the `ack_token` that binds it |

The exact bytes are pinned by the golden fixtures in
`internal/protocol/testdata/coord-v1/`.

### Connection limits

The agent behind the socket is only semi-trusted, so its connections are
bounded like everything else it can spend:

- **16 concurrent connections per run socket.** Anything past that is
  closed immediately rather than queued, so one run cannot take the server
  to its file descriptor limit and break the SSH listener with it.
- **5 minutes idle.** The deadline is reset on every request, so an active
  bridge is never cut off; one that connects and then goes silent is
  dropped and simply redials on its next tool call. The same bound arms
  every response write, so a connection that stops reading responses is
  dropped too instead of wedging its handler.
- **64 KiB per request line**, well above the 4 KiB body cap plus JSON
  escaping.

A bridge dials per tool call and redials after EOF, so it holds one
connection at a time; these limits are far above anything normal use
reaches.

### Authorization

A run may only message a peer the radar currently has it in file conflict
with, or had until less than 10 minutes ago (the grace window, so an
in-flight reply still lands). A clearing the radar witnesses live anchors
the window at that moment; one it only finds by re-reading the overlap
index anchors at the last time the pair was seen overlapping instead, so a
late discovery never opens a fresh window. Grace state, like the peer cap
and the rate buckets, lives in process memory: a restart clears it, so a
window that straddles the restart is not honoured on the other side.
Anything else is `CodeDenied`.
This is what keeps the
mailbox from becoming a general agent-to-agent channel. Overlaps are
workspace-scoped by construction, so a cross-workspace target can never
pass; two runs in different sessions of one workspace can overlap, so a
cross-session send within a workspace is allowed.

**The edge is agent-derived, so a run may reach 8 peers at most.** The
radar computes an overlap by intersecting the two runs' own diff
snapshots, and a run controls its own: one that touches every tracked file
has a file set that is a superset of everyone else's, and the radar then
reports it as overlapping with every active run in the workspace. Nothing
here can tell that apart from a genuinely wide refactor, and a size
threshold would only misfire on the honest one, so what is bounded is the
reach rather than the edge. Each run may open a conversation with 8
distinct peers; the ninth is refused `CodeConflict`. Messages to a peer it
has already messaged are unaffected. Like the send-rate bucket, the count
is per process and a restart clears it.

### Delivery

At-least-once. Each read delivers a batch under one opaque, run-scoped
token and returns that token; the batch leaves the unread set only when a
later read presents it. A token that is absent, unknown, or another run's
acknowledges nothing, and an outstanding batch is returned again under its
original token until it is acknowledged - so a response lost between the
server and the agent costs a duplicate, never a silently dropped "I'll
wait". Tokens live in `run_messages`, so they survive a restart.

The rows are retired with their reader: releasing a run's coordination
deletes its mailbox, and recovery deletes the mailbox of any run whose
directory it removes. The timeline notes remain the audit trail.

### Caps and failures

| Condition | Code |
| --- | --- |
| Body over 4 KiB, missing target, self-send | `CodeInvalidParams` |
| Unknown run | `CodeNotFound` |
| Target has finished | `CodeUnavailable` |
| Target inbox at 100 unacknowledged messages | `CodeConflict` |
| Send rate exceeded (burst 5, one per 5 s) | `CodeConflict` |
| Inbox read rate exceeded (burst 10, one per second) | `CodeConflict` |
| Sender already talking to 8 distinct peers | `CodeConflict` |
| Peer is not an authorized overlap | `CodeDenied` |
| Coordination disabled | `CodeUnavailable` |

## Notice and audit

The first overlap between a pair injects one banner into each agent's
terminal through the existing inject path, naming the peer run, its owner,
its task, and the shared files. The owner's display name, the task, and
each shared path render quoted, so member-, agent- or repo-chosen text
cannot smuggle control sequences into the terminal or the reading agent's
stdin. It fires once per pair and re-arms after the overlap clears.
Delivery is best-effort and event-driven: a banner that cannot be injected
because the run has no live terminal yet is retried on the next overlap
change, so an overlap first seen in a restart window whose file set never
changes again can go unannounced - the radar chip still stands for the
humans either way.

Both halves are audited on the session timeline, as notes attributed to the
owner of the run each one belongs to: a delivered notice
(`coordination notice: run <peer> is also editing <files>`) on the run that
was told, and every message (`coordination message to run <peer>: <body>`)
on the run that sent it. A cross-session send within one workspace is
additionally noted on the receiving run's session
(`coordination message from run <peer>: <body>`, still attributed to the
sender's owner), so the humans supervising the target see both halves of
the exchange. A notice is stamped only once its banner has
actually reached a terminal, so the feed never says an agent was told when
its run had no live session.

## Kill switch

`--conflict-coordination=false` (server config `CoordinationDisabled`)
turns the feature off: no notices, no listeners, no directories, no
mailbox writes, no timeline entries, and every `coord.*` call fails
`CodeUnavailable` before it touches anything. The radar and its chips are
unaffected.

## Not in this component

This package owns the host side and the wire. The read-only container
mounts, the staged bridge binary, and the in-container MCP server that
turns these three methods into agent tools live in `internal/mcpbridge`
and the scheduler's coordination lifecycle - see `mcp-bridge.md`, whose
"Harness registration" section covers the per-harness launch-profile field
that points an agent at the bridge.
