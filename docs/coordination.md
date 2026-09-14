# Conflict coordination

Aether's conflict radar identifies active runs that edit the same files. When
coordination is enabled, each run also gets a small, durable channel for
communicating with the other runs that the radar authorizes. The channel is
advisory: it does not lock files, pause work, or decide which change wins.

## Run-mounted surfaces

The server keeps coordination data below its private data directory:

```
<data>/coord/                       0700  coordination root
<data>/coord/<run-id>/              0755  one run's coordination directory
<data>/coord/<run-id>/coord3.sock   0666  the v3 coordination socket
<data>/coord/<run-id>/mcp.json      0444  optional MCP server configuration
<data>/coord/<run-id>/co-authors    0444  server-generated commit trailers
```

Inside a container, the run directory appears at `/run/aether`. Only
`coord3.sock` is served. A socket from an older wire version is not rebound;
affected runs must be relaunched with the current server.

The server also mounts one verified, read-only staged binary at both of these
paths:

```
/opt/aether/aether-server
/usr/local/bin/aether-internal
```

The first path serves the hidden MCP entry point and the existing harness
lifecycle hook. The second path is the agent-facing coordination CLI. The
coordination directory and both executable mounts are constructed by Aether,
not requested by a run.

The container has no coordination token or identity flag. The mounted socket
is the identity: a connection accepted by a run's socket is treated as that
run. `aether-internal` has no socket, run-identity, login, or credential
option and always uses `/run/aether/coord3.sock`.

Caller-supplied mounts are validated before these mounts are appended. A
caller mount may not target or nest under `/run/aether`, `/opt/aether`, or
`/usr/local/bin/aether-internal`, so a credential home, profile, or worktree
cannot shadow the socket or either executable. The server fails closed if it
cannot stage and verify its binary. In that case the run still launches, but
coordination is unavailable and the terminal receives only the normal overlap
notice.

## Wire v3

The socket carries JSON-RPC 2.0 requests and responses, one request per NDJSON
line. The coordination method set is exactly:

| Method | Parameters | Result |
| --- | --- | --- |
| `coord.status` | none | v3 wire version, this run's identity and assignment, authorized peers, unread count, and capabilities |
| `coord.send` | `to_run_id`, `body`, `idempotency_key` | `message_id` |
| `coord.inbox` | optional `ack_token`, optional `wait_seconds` | oldest-first `messages` and the `ack_token` for that batch |
| `coord.ask` | `to_run_id`, `body`, `idempotency_key` | `question_id` |
| `coord.reply` | `question_id`, `body`, `idempotency_key` | `message_id` |
| `coord.report` | `outcome`, `summary`, optional `evidence_refs`, `idempotency_key` | durable `report_id`, outcome, summary, next action, evidence references, and automatic `evidence_ref` |

`coord.status` reports `wire_version: "v3"`, the run, workspace, and member
IDs, the recorded task, each currently authorized peer, and all six
capabilities. The sender is never a parameter. A run can message only a peer
in the same workspace that the radar currently marks as overlapping, or a
peer in its ten-minute overlap grace period. A question reply is the one
correlation exception: `coord.reply` identifies its destination from the
question and remains allowed for that question even after ordinary overlap
grace expires. It cannot be used to send an unrelated message or cross a
workspace boundary.

A run may open conversations with at most eight distinct peers. Existing
conversations remain usable when this limit has been reached. The server
also enforces these bounds:

- message, question, and reply bodies are at most 4 KiB;
- status returns at most 32 peers and 16 files per peer, with total and
  truncation metadata; task and path strings are capped at 512 bytes;
- idempotency keys are at most 256 bytes and may not contain control lines;
- an inbox holds at most 100 unacknowledged messages;
- sends allow a burst of five, then one message per five seconds;
- inbox reads allow a burst of ten, then one read per second;
- every non-empty, bounded request line consumes a per-run transport budget
  of 30 requests per burst, refilling at one request per second. This charge
  happens before JSON, method, or parameter parsing, so malformed and unknown
  requests cannot bypass it;
- `wait_seconds` is a server-side wait from 0 through 30 seconds;
- each run socket accepts at most 16 concurrent connections, and inactive
  connections are reaped after five minutes;
- each request line is limited to 64 KiB;

The method set is closed. A connection cannot invoke a control verb, access
Git, read another run's transcript, or address a run outside the authorized
peer set.

## Delivery, acknowledgement, and retries

Delivery is at least once. An inbox read returns one oldest-first batch and an
opaque `ack_token`. Omitting `ack_token` on the next read acknowledges nothing,
so the same batch and token can be delivered again. Supplying the token on the
next read acknowledges exactly that batch while fetching the next batch. An
empty inbox has no token. Tokens are durable across server restarts while the
run's container and coordination data are retained.

`coord.send`, `coord.ask`, `coord.reply`, and `coord.report` are mutations and
require an explicit idempotency key on the wire. Repeating a mutation with the
same key and the same semantic inputs returns its original receipt; reusing a
key with different inputs is a conflict. MCP callers must supply the key.
The CLI generates one only when `--idempotency-key` is omitted and prints
`idempotency-key: ...` to stderr before the network call; save and reuse that
value if the response is lost.

A successful receipt means the server durably stored the operation. It does
not mean that a peer has read the message or understood it. A timed-out
request may have succeeded; retry it with the same idempotency key and use the
returned receipt.

Accepted messages, questions, and replies are attributed to their originating
run and appended to the workspace timeline. These coordination notices are
server-originated events with an empty actor identity; ownership changes cannot
rewrite their historical attribution. The timeline records durable server
acceptance; it does not imply that the recipient has read the item.

## `aether-internal` CLI

A task-bearing coordinated run receives this launch instruction automatically:

```
Use `aether-internal skill` to read this run's live assignment; use `aether-internal` to coordinate and report your outcome.
```

No skill package, manual identity argument, or credential setup is required.
Run `skill` before acting so the assignment and capabilities come from current
server state rather than copied prompt text.

All commands below run inside the container. Every command except `skill`
writes one JSON object followed by a newline. Successful commands use this
shape:

```json
{"schema_version":"v3","ok":true,"result":{}}
```

Failures use the same envelope with an `error` object:

```json
{"schema_version":"v3","ok":false,"error":{"code":-32001,"message":"..."}}
```

Use `error.code` for branching. The message is method-qualified and suitable
for logs. The process exits with 0 on success, 1 for an internal failure, 2
for usage or protocol input errors, 3 for denied, conflicting, or invalid
state, and 4 when a run or coordination surface is not found or available.

The stable error codes are:

| Code | Meaning |
| --- | --- |
| `-32700` | parse error |
| `-32600` | invalid request |
| `-32601` | method not found |
| `-32602` | invalid parameters |
| `-32603` | internal failure |
| `-32000` | run or other resource not found |
| `-32001` | denied |
| `-32002` | invalid state |
| `-32003` | conflict or limit reached |
| `-32004` | unavailable |

### Inspect the assignment and peers

```sh
/usr/local/bin/aether-internal status --json
/usr/local/bin/aether-internal skill
```

`status` requires `--json`; `skill` takes no arguments and prints the current
v3 assignment plus the short coordination workflow.

### Send a message

```sh
/usr/local/bin/aether-internal send \
  --to run-peer \
  --body 'I am editing src/example.go.' \
  --idempotency-key send-example-1
```

The result contains `message_id`. `--body-file path` reads a body from a
file, and `--body-file -` reads it from standard input. A body can also be
provided as the positional text after the peer run ID.

### Read and acknowledge the inbox

```sh
/usr/local/bin/aether-internal inbox --wait 30
/usr/local/bin/aether-internal inbox --ack ack-example-1 --wait 30
```

Use the `ack_token` returned by the first command as the value of `--ack` on
the next command. `--wait` asks the server to wait once for up to 30 seconds
when no message is ready; it is not a client polling loop. If the process or
connection ends before the result is consumed, do not acknowledge the token
and read again.

### Ask a question and reply

```sh
/usr/local/bin/aether-internal ask \
  --to run-peer \
  --body 'May I update src/example.go?' \
  --idempotency-key ask-example-1

/usr/local/bin/aether-internal reply \
  --question-id question-example-1 \
  --body 'Yes, proceed.' \
  --idempotency-key reply-example-1
```

The first command returns the durable `question_id`. Use that value with
`--question-id` when replying; a reply does not take a target run ID. `ask`
and `reply` accept the same `--body-file` and standard-input forms as `send`.
The example IDs are ordinary non-secret values. Replace them with the peer
run ID and question ID returned by the run's own status and inbox results.

### Report an outcome

```sh
/usr/local/bin/aether-internal report \
  --outcome success \
  --summary 'Completed the assigned change.' \
  --evidence-ref evidence-example-1 \
  --idempotency-key report-example-1
```

`--outcome` is exactly one of `success`, `failure`, or `blocked`. A non-empty
summary is required. `--evidence-ref` may be repeated, and `--summary-file`
accepts a file or `-` for standard input. The result contains a durable
`report_id` and the server-created `evidence_ref`.

Before accepting `coord.report`, Aether captures evidence for the run. The
capture retains a private Git evidence commit and the PTY transcript up to
16 MiB, then stores factual context, provenance, unresolved facts, and a next
action with the report. Evidence is retained for 30 days. Unavailable or
truncated sources are recorded explicitly. The capture is not an atomic
environment snapshot and does not claim that the reported work was verified.
If capture or durable storage fails, the outcome is not accepted, and the
runtime resources remain recoverable.

Finalization and event publication are crash-safe. Finalization writes a
durable pending publication row and a deterministic evidence-event ID. The
event is appended before the report is marked published; if an append result
is uncertain, retrying the same ID reconciles the existing event rather than
creating a duplicate. Pending rows are traversed with a stable cursor and
wraparound, while attempts, next-attempt time, and the last error are durable.
Transient failures remain eligible for service-lifetime retries; permanent
event conflicts are quarantined with their error visible for operators. A
caller may therefore receive an internal or unavailable error after capture
while the finalized report remains retryable under its original idempotency key.

The shipped CLI and MCP bridge allow the full two-minute evidence-capture
budget plus a small framing margin (and still honor an earlier caller
deadline). Captures from 30 through 120 seconds are reachable without
changing the socket protocol.

## Lifecycle status is separate

`coord.report` is the durable worker outcome described above. It is exposed
through `aether-internal report` and the MCP tool `aether_report`.

`run.report` is different. It is the harness lifecycle hook that updates the
run's transient `working` or `waiting` status. Harness callbacks invoke it by
running `/opt/aether/aether-server report <harness>` over the same run socket.
It is not an agent outcome, is not exposed as an `aether-internal` command, and
has no durable evidence receipt. A lifecycle callback may fail without
blocking the agent; the run then falls back to its normal stall handling.

Coordination also does not provide a universal inbound terminal hook. When a
human steers a run, Aether delivers the request through the harness's
serialized PTY input path and records the delivery separately.

## Retention and shutdown

Active runs and explicitly retained terminal TUI runs keep their socket,
unread mailbox, and timeline entries through a server restart. Recovery
rebinds `coord3.sock` for those runs. When a run's container is destroyed,
Aether releases the coordination directory and mailbox after any required
evidence capture has completed. With `--conflict-coordination=false`, no new
coordination socket or mounts are created and every coordination request is
unavailable; the conflict radar itself remains active.
