# The MCP bridge

Aether's MCP bridge is an optional, manually configured in-container adapter
for six existing coordination tools. The canonical agent interface is the
`aether-internal` CLI, which is automatically available in every managed
container and can load a version-matched skill on demand. The bridge is not the
Release B orchestration interface and does not need to mirror its mission or
worker-management surface.

When coordination assets are available, the server stages verified executable
bytes read-only at the two in-container paths used by the existing surfaces:

```
/opt/aether/aether-server         optional MCP entry point and lifecycle hook
/usr/local/bin/aether-internal    canonical agent-facing coordination CLI
```

The run coordination directory carries the run socket and related server
assets:

```
/run/aether/coord3.sock           this run's v3 socket
/run/aether/co-authors            server-generated commit trailers
```

These are Aether-owned surfaces, not caller-provided mounts. Caller mounts
cannot shadow the coordination directory or either executable. If verified
staging fails, Aether refuses to create the managed container; it does not
silently launch a container without the canonical CLI or bridge binary.

The socket is the run identity and authentication boundary. The bridge and CLI
have no token, login, run-ID, or credential option. A connection to a run's
socket is that run; a binary can be present without a run identity, but then
run-bound operations are unavailable.

## Manual registration and discovery

Aether does not automatically register the MCP bridge, write a harness
configuration, or append a harness-specific MCP flag. To use MCP voluntarily,
create a user-managed configuration outside `/run/aether`, then point a
harness that supports MCP configuration at it. For example:

```sh
cat >/tmp/aether-mcp.json <<'EOF'
{"mcpServers":{"aether":{"type":"stdio","command":"/opt/aether/aether-server","args":["mcp"]}}}
EOF
claude --mcp-config /tmp/aether-mcp.json
```

The example is manual, and the path is not written or managed by Aether. Other
harnesses may use different configuration syntax. The staged MCP entry point
still reaches only the run socket belonging to the container.

The canonical CLI remains available without MCP. A shell-capable harness can
invoke `aether-internal` directly and request its live, version-matched skill
on demand. A container or terminal without a run identity can use general
help or the non-run skill guidance, but status, messaging, reporting, and
mission operations return unavailable. Lack of manual MCP registration is not
an overlap-only or notice-only mode.

See [coordination.md](coordination.md) for the established CLI commands and
wire limits. The optional bridge does not install a skill package or carry an
identity claim.

## The existing six-tool surface

The bridge exposes exactly six existing tools. Their parameters, receipts,
authorization rules, limits, idempotency behavior, and durable storage map to
the established v3 coordination methods. This is the bridge's complete
surface; it does not expose Release B mission, task, worker, takeover, or
integrator-management commands.

| MCP tool | v3 method | Parameters | Result |
| --- | --- | --- | --- |
| `aether_status` | `coord.status` | none | v3 identity, assignment, authorized peers, unread count, capabilities |
| `aether_send` | `coord.send` | `to_run_id`, `body`, `idempotency_key` | `message_id` |
| `aether_inbox` | `coord.inbox` | optional `ack_token`, optional `wait_seconds` | `messages`, `ack_token` |
| `aether_ask` | `coord.ask` | `to_run_id`, `body`, `idempotency_key` | `question_id` |
| `aether_reply` | `coord.reply` | `question_id`, `body`, `idempotency_key` | `message_id` |
| `aether_report` | `coord.report` | `outcome`, `summary`, optional `evidence_refs`, `idempotency_key` | durable `report_id`, outcome, summary, next action, evidence references, automatic `evidence_ref` |

The bridge returns the established structured result directly. This
presentation differs from any CLI envelope, but it does not add operations or
make MCP a Release B-parity interface.

`aether_send`, `aether_ask`, `aether_reply`, and `aether_report` require an
explicit `idempotency_key`; the bridge never invents one. For a retry after a
timeout or lost response, provide the same key and the same semantic inputs.
A different payload under an existing key is a conflict.

`aether_inbox` is at-least-once. The returned `ack_token` identifies exactly
the returned batch. Supplying it on the next call acknowledges that batch;
without it, the batch remains available. The bridge carries forward the last
acknowledgement token that reached the MCP stream when the next call omits
`ack_token`, while an explicit token remains supported. A cancelled call,
failed response write, or bridge process exit does not promote a staged token,
so the batch is delivered again. `wait_seconds` requests one bounded server
wait; it is not an unbounded poll.

`aether_ask` returns a durable `question_id`. `aether_reply` takes that ID
instead of a target run ID and routes only to the original question sender.
The question establishes the reply relationship; it cannot authorize an
unrelated message or cross a workspace boundary.

`aether_report` accepts only the established outcome values and requires a
non-empty summary. Before the server accepts it, Aether captures the run's
evidence, including a private Git evidence commit and a bounded PTY
transcript. The retained packet records factual context, provenance,
unresolved facts, and a next action, with unavailable or truncated sources
shown explicitly. Evidence is not an atomic environment snapshot and does not
assert that the outcome was verified. A failed capture or persistence step
leaves the outcome unaccepted and the runtime recoverable.

## MCP errors

Coordination failures are returned as MCP tool results with `isError`, not as
MCP session-level JSON-RPC failures. Aether's numeric code is in the result
metadata key `aether/error_code`; the text includes the method-qualified
message. Local socket failures such as a missing listener, EOF, broken pipe,
or a connection-cap refusal map to `CodeUnavailable` (`-32004`). Server
responses preserve their own Aether codes, including `CodeDenied` (`-32001`),
`CodeConflict` (`-32003`), `CodeNotFound` (`-32000`), and
`CodeInvalidParams` (`-32602`). This keeps an operation failure actionable
without tearing down the MCP session.

The method set is closed. The bridge cannot invoke a control verb, steer a
terminal, read Git, or access another run's transcript. Human steering still
uses Aether's host-side serialized PTY input path; MCP is not an inbound
terminal hook.

## `run.report` is not `coord.report`

`aether_report` is the MCP spelling of durable `coord.report`, and has the
same evidence-before-acceptance behavior as `aether-internal report`.

The staged binary at `/opt/aether/aether-server` also retains the separate
harness lifecycle command:

```sh
printf '%s\n' '{"hook_event_name":"Stop"}' | /opt/aether/aether-server report claude
/opt/aether/aether-server report codex '{"type":"agent-turn-complete"}'
/opt/aether/aether-server report pi --event session.idle
/opt/aether/aether-server report opencode --event session.idle
```

These callbacks invoke wire method `run.report` and update only the run's
current `working` or `waiting` status. They are not an agent outcome, are not
an MCP tool, and do not create a durable report or evidence receipt. Harness
callbacks are hidden lifecycle plumbing, not commands for an operator or
worker to run. The callback exits promptly even when status reporting is
unavailable so it cannot block the harness.

## Staging, retention, and shutdown

The staged binary is content-addressed. A run keeps the exact verified bytes
used at provisioning, even if the server binary is upgraded later. Aether
records the staged digest and coordination directory in the run sidecar before
creating the container, and recovery rebuilds the listener from surviving
sidecars.

Active runs and retained terminal TUI runs keep their socket, unread mailbox,
and MCP assets through a server restart. When the container is destroyed,
Aether releases the coordination directory and mailbox after required evidence
capture.
The coordination disable setting remains effective. When it is off, no run
socket or usable MCP bridge is provided: bridge calls and run-bound CLI
operations return unavailable. Every newly created managed container still
receives the staged CLI, but without a run socket it can provide only general
help or non-run skill guidance. Existing mounted assets become inert; the
conflict radar itself continues to operate.
