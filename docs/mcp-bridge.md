# The MCP bridge

Host side of coordination: `coordination.md`.

An agent reaches its coordination mailbox through three MCP tools. The
thing serving them is the Aether server's own binary, staged and
bind-mounted read-only into the run container and launched by the harness
as a stdio MCP server (`aether-server mcp`, a hidden subcommand no operator
runs). It is built on the official Go MCP SDK; there is no hand-rolled MCP
framing anywhere.

The same binary and the same socket carry one thing that is not a tool at
all: the agent's own status reports (`aether-server report`, below).

## What the container gets

```
/opt/aether/aether-server         read-only  the staged bridge binary
/run/aether/                      read-only  the run's coordination directory
/run/aether/mcp.json              read-only  the MCP server config, for a registered harness
/run/aether/claude-settings.json  read-only  the status-reporter hooks, for an interactive claude run
/run/aether/co-authors            read-only  the trailers to end commits with
/run/aether/coord2.sock                      the socket the bridge dials (wire v2)
```

The binary and the directory are the two mounts, and both are Aether-owned
container paths. `runtime.ValidateMounts` refuses any caller-supplied mount
that targets or nests under them, which is what guarantees a credential home
or member configuration cannot shadow either one. They are therefore built
from server-constructed paths and appended after the caller's mounts have
been validated.

Nothing else crosses the boundary. No token enters the container: the
socket is the run's identity, and the mount is the only thing that grants
access to it.

## The three tools

| Tool | Coordination method | Result |
| --- | --- | --- |
| `aether_status` | `coord.status` | own identity, the peers this run may message, the files each pair shares, the unread count |
| `aether_send` | `coord.send` | the new message id |
| `aether_inbox` | `coord.inbox` | one batch of messages, oldest first |

The mapping is 1:1 and the tool set is closed - there is no fourth tool,
and nothing reachable here touches a control verb, git, or another run's
transcript.

The socket's *method* set has a fourth entry, `run.report`, which no tool
reaches. See "The status reporter" below.

### Acknowledgement stays below MCP

`coord.inbox` acknowledges the previous batch with an opaque token. That
token is never exposed to the agent: an agent that could present one could
retire a peer's message it had not read, and at-least-once delivery is the
one guarantee the mailbox makes.

The bridge holds the token instead, and promotes it only once the batch it
names has actually reached the agent - that is, once the MCP response
carrying the batch has been written.

Every staged token is keyed to the JSON-RPC id of the call that fetched it,
and only that call's own response can promote it. This is the safety
property the whole design rests on: inbox handlers really do run
concurrently, and a call the client abandons keeps running to its own
timeout, so a token that was not keyed to its own call could be promoted by
a *different* call's response and acknowledge a batch that reached nobody.
MCP does not hand a tool handler the id of the request underneath it, so
the bridge stamps it into the call's `_meta` on the way past and reads it
back out in the handler.

A single slot keeps inbox calls to one socket round trip at a time. The
handler claims it, not the transport's reader: the reader is the SDK's one
decoder goroutine, and waiting there would stall the whole MCP session -
including the cancellation that would end the wait.

Anything short of a completed write leaves the token unpromoted: a failed
write, a tool error, a cancelled call, a killed bridge. The batch is then
still unacknowledged and the next read delivers it again. A read that
returned nothing leaves the bridge holding no token at all.

Cancellation gets a second defence, because a context that is cancelled
does not un-read a batch the socket has already handed over: the round trip
itself is aborted when the call's context ends, so an abandoned call
normally takes delivery of nothing at all, and a token it staged before the
cancellation arrived is discarded rather than promoted.

### Failures

Every local socket failure - a missing socket, a refused connection, an EOF
or broken pipe because the server restarted, or a connection refused over
the per-run connection cap - is reported as an MCP **tool error** carrying
Aether `CodeUnavailable` (-32004), in the message text and under the
`aether/error_code` result metadata key. Errors the server itself returned
(`CodeDenied` for a peer that is not an authorized overlap, `CodeConflict`
for a full inbox or an exceeded rate, and the rest of `coordination.md`'s
table) pass through the same way with their own code.

The code deliberately does not go in the MCP envelope's own error field:
the JSON-RPC layer under MCP reserves that range for transport states
(-32004 there means "the server is closing"), so an Aether code put there
would tear the MCP session down instead of telling the agent what
happened.

### Connections

The bridge dials the socket per tool call and closes it again. A server
restart rebinds the socket to a new inode, so a held connection would have
to be re-dialled anyway; dialing per call also keeps the bridge inside the
server's per-run connection cap and well under its idle read deadline
without any reconnection bookkeeping. A tool call made while the socket is
missing returns `CodeUnavailable`; the next one, after the listener is
back, simply works.

## Harness registration

Reaching the bridge is per-harness, and the harness registry
(`internal/harness`) owns it. A launch profile carries one field for it -
the flag its CLI takes for an externally supplied MCP server config. Claude
Code sets `--mcp-config`; every other shipped profile leaves it empty.

For a run whose profile carries the flag, provisioning writes the config
into the run's coordination directory and appends the flag pointing at it,
so the container is launched as:

```
claude --dangerously-skip-permissions "<task>" --mcp-config /run/aether/mcp.json
```

```json
{"mcpServers":{"aether":{"type":"stdio","command":"/opt/aether/aether-server","args":["mcp"]}}}
```

The config is server-written and read-only (0444), and it lives beside the
socket in `/run/aether`. Nothing is written into the worktree - an
`.mcp.json` there would show up in the run's diff - and the member's
configuration home is never modified by this bridge.

A harness with no registration is provisioned exactly like any other run,
mounts and all; it is simply never told about the bridge, and degrades to
the overlap notice in its terminal. The arguments and the config are
decided at launch, so a run that was started without them can only gain
them by being relaunched.

The status reporter is registered the same way and in the same directory:
a profile that carries status arguments gets its settings document written
there and the arguments appended, for an interactive run only.

An argv override in the server config (scheduler `Harnesses`) is respected
verbatim: the registry's MCP and status flags belong to the CLI the registry
ships, and nothing checks that an overridden command still is that CLI, so
neither is appended to it. The overridden harness degrades to notice-only
coordination and to the stall threshold the same way.

## Other MCP services

This bridge is Aether's run-coordination MCP service only. It is not a
general-purpose proxy for vendor tools or for MCP services named in a member's
configuration. A copied local MCP setting still needs its executable or
service available in the remote runtime image and its own authentication.
Inside a run, `localhost` means that remote runtime, not the operator's laptop.
Aether does not install or forward an arbitrary local MCP process.


### What the end-to-end tests cover

The coordination E2E (`internal/server`, `integration` tag) has a host half
and a container half.

The host half runs against the in-process runtime: the launch wiring, the
config document, the argument, all three kill-switch positions, and a real
MCP client driving the real bridge over a real coordination socket into the
real mailbox and workspace timeline.

The container half runs against real Docker. Its failure mode is the reason
it exists: a run that cannot reach the bridge degrades to notice-only,
which is a legal state, so nothing short of a positive assertion would
catch a broken mount, a binary that will not execute, or a permission a
non-root agent does not have. It asserts the two binds are realized and
read-only, that the staged binary runs as
`/opt/aether/aether-server mcp` under a non-root container user, and that
two overlapping runs complete a status/send/inbox round trip through it.
See docs/testing.md for how it stages a binary that really has the
subcommand. The real-harness smoke tests
(`internal/harness/smoke_integration_test.go`) cover the argv half against
the actual CLIs.

## The status reporter

`run.report` is the fourth method on the socket and the only one an agent
does not call through a tool. It carries two fields - a state, `working` or
`waiting`, and the user-visible reason - and returns an empty object. The
run is the socket it arrived on, exactly as for the three mailbox methods.

What calls it is the harness's own lifecycle hook, running
`/opt/aether/aether-server report <harness>` with the hook's event JSON on
stdin. That subcommand is hidden like `mcp`: no operator runs it. It maps
the event, dials the socket, and exits 0 whatever happens - an unmapped
event never dials at all, and a failure is one line on stderr, which the
harness shows only for a non-zero exit. A hook that breaks or slows the
agent would be worse than a run card that is briefly wrong.

Reading the event and the round trip that follows share one budget, set
under the timeout the harness gives the hook, so a harness that hands over
an open pipe cannot leave the reporter waiting on it either. A payload past
the reporter's size cap is reported on stderr rather than truncated: half a
JSON document maps to nothing, which would look exactly like an event Aether
ignores.

Which harnesses have a reporter, what the settings document looks like, and
what each state does to the run: [harnesses.md](harnesses.md) and
[failure-handling.md](failure-handling.md).

The wire method set is closed all the same: `run.report` is four of four,
and nothing else - no control verb, no git, no other run's transcript - is
reachable from inside a container.

## Staging the binary

The server stages its own binary - `/proc/self/exe`, or whatever
`scheduler.Config.ServerBinary` names - at
`<data>/runtime/bin/aether-server-<sha256>`:

- the running binary is hashed, and a staged copy that already matches its
  digest is reused;
- otherwise it is copied to a temp file in the same directory, made `0555`,
  fsynced, renamed into place, and the directory fsynced;
- the staged file is hashed again before it is ever mounted.

Content addressing is what lets a container outlive an upgrade: a run
provisioned against one build keeps mounting exactly those bytes after the
server binary underneath has been replaced.

**Fail-closed.** If staging, hashing, or verification fails, the run gets no
coordination at all - no bridge mount, no coordination mount - and the
reason is recorded on the run's timeline. A mount that could not be
verified is never handed to a container. The run itself still launches:
coordination is advisory, and an agent without the bridge still sees the
overlap notice in its terminal, which is exactly the notice-only
degradation a harness without MCP support gets.

## Durability and cleanup

Before the container is created, the run's sidecar
(`<data>/scheduler/<run-id>.json`) records the staged digest and path and
the coordination directory (the socket file's name, not the sidecar, is
what records the wire version). That write is the reference that keeps the staged
binary alive, and it happens first on purpose: a crash between staging and
the container existing must not leave a container holding bytes nothing
claims.

The sidecars are the whole reference set, so they are also how recovery
rebuilds it. On startup, after runs have been reconciled against their
containers, any staged binary no surviving sidecar names is deleted. Once
this process has staged, its own build is additionally retained; a copy the
previous process left with no referencing sidecar is collected like any
other, and the next launch simply re-stages the same bytes.

Staging and collection are serialized against each other, so a collection
can never delete a binary a concurrent launch has just verified and is
about to mount. It also means any dot-prefixed temp file a collection sees
is the leftover of an install killed mid-copy, never a copy in flight, and
those are reclaimed too.

A run's assets are released only after its container has been destroyed:
the sidecar is removed (an atomic unlink), the state directory is fsynced,
the coordination directory is released and its mailbox rows deleted, and
only then are unreferenced staged binaries collected.

## Kill switch

With `--conflict-coordination=false` the scheduler is never given the
coordination seam: nothing is staged, no directory is provisioned, no
mounts are added, and nothing already on disk is collected. Assets a
previous process left inside a live container stay exactly where they are
and go inert - `coord` unlinks the sockets behind them, so a bridge still
running in there gets `CodeUnavailable` and nothing else.

Turning it back on affects new containers only. A run created while the
switch was off has no mounts, no config, no `--mcp-config` and no
`--settings` argument, and cannot gain them; it stays notice-only, and
judged on silence alone, until it is relaunched.
