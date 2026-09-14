# The MCP bridge

Aether's MCP bridge is the optional in-container interface to the same
coordination service exposed by `/usr/local/bin/aether-internal`. It does not
add a second protocol or authority layer. Both interfaces speak coordination
wire v3 over the run's Unix socket.

The server stages a version-matched copy of its own binary and mounts it
read-only at both executable paths:

```
/opt/aether/aether-server         hidden MCP entry point and lifecycle hook
/usr/local/bin/aether-internal    agent-facing coordination CLI
```

It also mounts the run's coordination directory read-only:

```
/run/aether/coord3.sock           this run's v3 socket
/run/aether/mcp.json              optional harness MCP configuration
/run/aether/co-authors            server-generated commit trailers
```

The executable mounts use the same verified bytes. They are server-constructed
assets, not caller-provided mounts. Caller mounts cannot target or nest under
`/run/aether`, `/opt/aether`, or `/usr/local/bin/aether-internal`, so a profile,
credential home, or worktree cannot shadow them. If staging or verification
fails, Aether mounts neither coordination surface and records coordination as
unavailable instead of handing a container an unverified binary.

The socket is the only identity and authentication boundary. The bridge has
no token, login, run-ID, or credential option. A connection to
`/run/aether/coord3.sock` is the run that owns that socket. No caller-supplied
sender identity is sent in a tool parameter.

## Registration and discovery

A harness profile that supports an externally supplied MCP configuration is
registered automatically at launch. The server writes `mcp.json` into the run
coordination directory and appends the profile's MCP flag. Claude Code is the
shipped profile with this registration:

```sh
claude --dangerously-skip-permissions "Describe the assigned change" \
  --mcp-config /run/aether/mcp.json
```

The generated configuration is:

```json
{"mcpServers":{"aether":{"type":"stdio","command":"/opt/aether/aether-server","args":["mcp"]}}}
```

The config and executable are read-only. Nothing is written into the
worktree or the member's configuration home. A harness without MCP
registration still receives the overlap notice, but it does not receive these
tools. A run started without the registration cannot gain it without a
relaunch.

A coordinated task-bearing run also receives this short discovery instruction
in its launch prompt:

```
Use `aether-internal skill` to read this run's live assignment; use `aether-internal` to coordinate and report your outcome.
```

The instruction is the automatic discovery path for harnesses with or without
MCP. It does not install a skill package and does not carry an identity claim.
See [coordination.md](coordination.md) for the CLI commands and wire limits.

## MCP tools and exact parity

The bridge exposes exactly six tools. Their parameters, receipts, authorization
rules, size limits, idempotency behavior, and durable storage are the
corresponding v3 coordination methods, not MCP-specific variants.

| MCP tool | v3 method | Parameters | Result |
| --- | --- | --- | --- |
| `aether_status` | `coord.status` | none | v3 identity, assignment, authorized peers, unread count, capabilities |
| `aether_send` | `coord.send` | `to_run_id`, `body`, `idempotency_key` | `message_id` |
| `aether_inbox` | `coord.inbox` | optional `ack_token`, optional `wait_seconds` | `messages`, `ack_token` |
| `aether_ask` | `coord.ask` | `to_run_id`, `body`, `idempotency_key` | `question_id` |
| `aether_reply` | `coord.reply` | `question_id`, `body`, `idempotency_key` | `message_id` |
| `aether_report` | `coord.report` | `outcome`, `summary`, optional `evidence_refs`, `idempotency_key` | durable `report_id`, outcome, summary, next action, evidence references, automatic `evidence_ref` |

MCP returns the structured v3 result directly. The CLI wraps the same result
in its `schema_version` and `ok` JSON envelope; this presentation difference
does not change the operation or receipt.

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
wait from 0 through 30 seconds; it is not an unbounded poll.

`aether_ask` returns a durable `question_id`. `aether_reply` takes that ID
instead of a target run ID and routes only to the original question sender.
The question establishes the reply relationship, so a reply can land after
ordinary overlap grace expires; it cannot authorize an unrelated message or
cross a workspace boundary.

`aether_report` accepts only `success`, `failure`, or `blocked` and requires a
non-empty summary. Before the server accepts it, Aether captures the run's
evidence, including a private Git evidence commit and a PTY transcript capped
at 16 MiB. The retained packet records factual context, provenance,
unresolved facts, and a next action, with explicit unavailable or truncated
sources. Evidence expires after 30 days. It is not an atomic environment
snapshot and does not assert that the outcome was verified. A failed capture
or persistence step leaves the outcome unaccepted and the runtime recoverable.

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
capture. With `--conflict-coordination=false`, new runs receive no bridge
mount, CLI mount, socket, or MCP configuration. Existing mounted assets become
inert and coordination calls return unavailable; the conflict radar itself
continues to operate.
