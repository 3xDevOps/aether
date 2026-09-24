# Conflict coordination

Aether's conflict radar identifies active runs that edit the same files. When
coordination is enabled, each run also gets a small, durable channel for
communicating with radar-authorized runs; a current mission assignment may add
server-authorized peers before any file overlap exists. The channel is advisory:
it does not lock files, pause work, or decide which change wins.

Candidate verification and delivery is a separate authenticated service
described in [integration.md](integration.md). Coordination records remain
observations/evidence; they do not constitute an accepted submission, a
frozen candidate revision, or a landed upstream change. The mission-policy
adapter exposes a narrow agent integration CLI only to the current integrator:
`prepare`, `show`, `verify`, `request-delivery`, and `deliver`. Human review
remains the approval boundary; ordinary and worker runs do not receive these
commands.

## Run-mounted surfaces

The server keeps coordination data below its private data directory:

```
<data>/coord/                       0700  coordination root
<data>/coord/<run-id>/              0755  one run's coordination directory
<data>/coord/<run-id>/coord3.sock   0666  the v3 coordination socket
<data>/coord/<run-id>/co-authors    0444  server-generated commit trailers
```

The coordination mount contains no MCP configuration file. For the optional
manual bridge, use the `/tmp` configuration example in
[the MCP bridge guide](mcp-bridge.md).

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

Caller-supplied mounts are validated before these mounts are appended. A caller mount may not target or nest under `/run/aether`, `/opt/aether`, or
`/usr/local/bin/aether-internal`, so a credential home, profile, or worktree
cannot shadow the socket or either executable. The server fails closed if it
cannot stage and verify its binary: managed-container creation or launch is
refused rather than proceeding without the canonical CLI or bridge.
For enabled runs, the coordination directory and staged server binary are
therefore either present as verified or absent from a container; the canonical
read-only CLI mount is provisioned separately.

## Wire v3

The socket carries JSON-RPC 2.0 requests and responses, one request per NDJSON
line. The base coordination method set is:

| Method | Parameters | Result |
| --- | --- | --- |
| `coord.status` | none | v3 wire version, this run's identity and assignment, authorized peers, unread count, and capabilities |
| `coord.send` | `to_run_id`, `body`, `idempotency_key` | `message_id` |
| `coord.inbox` | optional `ack_token`, optional `wait_seconds` | oldest-first `messages` and the `ack_token` for that batch |
| `coord.ask` | `to_run_id`, `body`, `idempotency_key` | `question_id` |
| `coord.reply` | `question_id`, `body`, `idempotency_key` | `message_id` |
| `coord.report` | `outcome`, `summary`, optional `evidence_refs`, `idempotency_key` | durable `report_id`, outcome, summary, next action, evidence references, and automatic `evidence_ref` |

The `coord.*` wire and its six base methods are unchanged. Mission-assigned
runs additionally receive assignment-scoped `task.*` and `worker.*` methods
published by `coord.status`; the current integrator also receives exactly
`integration.prepare`, `integration.show`, `integration.verify`,
`integration.request_delivery`, `integration.deliver`,
`mission.question.ask`, `mission.clarification.complete`, `mission.plan.show`,
and `mission.plan.submit`. These
methods use the same run-authenticated socket but are not part of the base
`coord.*` set.
Every allow-list is derived from the current assignment, not from
caller-supplied roles or identities. A mission may authorize its integrator
and active worker runs as peers before any file overlap exists; ordinary runs
retain the radar active/grace authorization described below.

`coord.status` reports `wire_version: "v3"`, the run, workspace, and member
IDs, the recorded task, each currently authorized peer, and the six base
coordination capabilities (or the assignment-scoped capability set for a
mission run). The sender is never a parameter. An ordinary run can message
only a peer in the same workspace that the radar currently marks as
overlapping, or a peer in its ten-minute overlap grace period. A mission run
can also message its current assignment peers, which are shown with
`state: "mission"` even when no file overlap exists. A question reply is the
one correlation exception: `coord.reply` identifies its destination from the
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
peer or current mission-assignment set. Assignment-scoped task and worker
methods still enforce the role, mission, revision, generation, and current
authority checks on the server; they are not a general control API.

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
Use `aether-internal skill` to read this run's live assignment; use `aether-internal` to coordinate. Report a terminal outcome only after the assigned work is finished.
```

No skill package, manual identity argument, or credential setup is required.
Run `skill` before acting so the assignment and capabilities come from current
server state rather than copied prompt text.

All commands below run inside the container. Commands other than `skill` and
help write one JSON object followed by a newline. Successful commands use this
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
/usr/local/bin/aether-internal status
/usr/local/bin/aether-internal skill
```

`status` always emits the v3 JSON envelope; `--json` remains accepted but is
optional. Unknown flags and positional arguments are rejected. `skill` takes
no arguments and prints the CLI build version, live role and identity, then
the current phase and immediate actions. Its build version comes from the
mounted binary, which may predate newly published documentation; help describes
that binary's command syntax.

The task text in status and skill is a bounded summary (at most 512 bytes),
not the full assignment. A worker's skill prints an executable command with
its own task ID:

```sh
/usr/local/bin/aether-internal task show --task-id task-1
```

Use the actual command from skill, not the example ID above. Read the returned
task revision, objective, scope, exclusions, and evidence requirements before
acting. Workers may read and propose; they must not spawn workers, accept tasks,
or perform mission/integration operations. Ordinary runs have no mission
authority. Help documents syntax, not permission.

Every role gets `status`, `inbox`, and top-level help bootstrap commands.
An integrator's skill states its role before its phase guidance: turn the
objective into tasks for workers and coordinate them, not implement the
objective itself. Integrators additionally get task/worker help, list
commands using the current mission ID, integrator generation, approved
account/harness/mode choices, and active/total attempt allowance.
Integration guidance appears only after immediate actions in `active` or
`amendment_review`, not during initial planning or plan review. Use the full
status result for the current capability set.

Top-level and per-command help are available without a coordination socket:

```sh
/usr/local/bin/aether-internal --help
/usr/local/bin/aether-internal send --help
```

`skill` also works without the mounted socket. In that case it prints the
general workflow without claiming a run identity or assignment. Commands that
need run state return `-32004` and exit with status 4 when the socket is not
available. Message, question, reply, and report bodies read from flags, files,
or standard input are capped at 4 KiB before a request is sent.

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

### The mission plan gate

A mission is created in the `planning` phase, and no worker starts until a
human approves the plan. After approval, a change the integrator cannot make
alone goes back to the human as an amendment. The phase is mission state;
every method below is refused in the wrong phase with code `-32002`.

| Phase | Worker dispatch | Integrator task mutations |
| --- | --- | --- |
| `planning` | refused | `task propose`, `task revise`, `task abandon` allowed; `task accept` and `task accept-submission` refused |
| `clarified` | refused | same as `planning`; `mission question ask` returns the mission to `planning` |
| `plan_review` | refused | all refused; the plan is frozen while a human reads it |
| `active` | allowed | all allowed, within the limits on `task accept` below |
| `amendment_review` | allowed, for the already-approved set only | `task accept-submission` allowed; `propose`, `revise`, `abandon`, and `accept` refused |
| `rejected` | refused | all refused |

Transitions are exactly:

```
planning         --mission clarification complete-->  clarified
clarified        --mission plan submit------------->  plan_review
plan_review      --approve------------------------->  active
plan_review      --request changes----------------->  planning
plan_review      --reject-------------------------->  rejected
active           --mission plan submit------------->  amendment_review
amendment_review --approve------------------------->  active
amendment_review --request changes----------------->  active
clarified        --mission question ask------------>  planning
planning         --cancel-------------------------->  rejected
clarified        --cancel-------------------------->  rejected
plan_review      --cancel-------------------------->  rejected
rejected: terminal
```

`reject` is refused on an amendment: an amendment is approved or sent back for
changes, and the integrator drops it by abandoning its tasks or revisions.
`mission.cancel` is how a human ends a mission before any plan is approved,
including one whose integrator never submitted a plan. It is refused in
`active`, `amendment_review`, and `rejected`.

Only the current integrator may use these commands, and only for its own
mission; none of them takes a mission ID:

```sh
/usr/local/bin/aether-internal mission question ask \
  --body 'Which checkout flow should this replace?' \
  --idempotency-key mission-ask-1
/usr/local/bin/aether-internal mission plan show --wait 30
/usr/local/bin/aether-internal mission clarification complete \
  --idempotency-key mission-clarify-1
/usr/local/bin/aether-internal mission plan submit \
  --summary 'What will be built and why.' \
  --idempotency-key mission-submit-1
```

`ask`, `clarification complete`, and `submit` require an explicit
`--idempotency-key`; unlike `send` and `ask --to`, the CLI never generates one
for them. `--body-file` and `--summary-file` accept a path or `-` for standard
input, and both bodies are capped at 4 KiB. `mission question ask` asks the
accountable human, who answers in the dashboard; `ask --to <run-id>` asks a
peer agent run, which answers with `reply`. They are separate mailboxes.

`mission plan show` returns the phase, plan version, integrator generation,
open question count, and the feedback of the most recent request for changes,
plus every question and review round. `--wait` asks the server to wait up to 30
seconds for one of those to change; it returns unchanged when the wait elapses.
A value outside 0 through 30 is a usage error before any request is sent.
Waiting for a human is not being blocked: do not report an outcome while
waiting.

Questions are optional. `mission clarification complete` is how the integrator
declares that the objective is specified well enough to plan; it is refused
while a question the integrator asked is unanswered. Asking a further question
from `clarified` returns the mission to `planning` until that question is
answered. An initial plan can only be submitted from `clarified`.

While the mission is in `planning` or `clarified`, revising a task replaces the
draft: the previous revision is superseded and the new one becomes current
without any human action, so the review always reads the latest draft.

`mission.plan.decide` is the human boundary. It is not an agent command and is
not reachable from the run socket; the accountable human or an admin approves,
requests changes, or rejects from the dashboard. Approval accepts exactly the
revisions the submitted round recorded, in one transaction, and moves the
mission to `active`. `mission.cancel` is the same kind of human-only method.
Rejecting and cancelling both move the mission to `rejected`, and the server
then cancels the integrator run.

An answer to a mission question and every plan decision are also typed into
the integrator's terminal as one `aether:` line naming the command to run
next. The integrator is always interactive (TUI): `mission.create` and
`mission.replace-integrator` refuse any other mode with `-32602` and
`integrator mode must be tui: a headless integrator exits after one turn and
cannot be asked or told`. `mission.create` needs the integrator's exact
account, harness, and `tui` mode among the execution choices;
`mission.replace-integrator` accepts any listed account and harness in `tui`,
so a swarm whose choices are all headless can still get an interactive
integrator. Both refuse, with `-32602` and `integrator harness <name> cannot
launch in tui mode: <cause>`, a harness the integrator's account cannot start
in `tui`, such as one whose definition that account no longer has. Workers may
still run headless. If the integrator's
harness has already exited, the line lands in the shell left on its terminal
and is read as a command line there.

#### Amendments to an approved plan

In `active`, a submit sends an amendment: the pending set is every
non-abandoned task's highest proposed revision at or above its current
revision. Submitting at least one such task or revision is the only
precondition. The mission moves to `amendment_review`, and while a human reads
it:

- Already-approved work keeps running. Workers still start, retry, cancel, and
  their submissions are still accepted.
- Nothing the amendment introduces can start: a pending revision is `proposed`,
  and only a task's current accepted revision is ever reserved. `worker start`
  on a task that has an item in the round under review is refused outright.
- `task propose`, `task revise`, `task abandon`, and `task accept` are refused.

`approve` returns the mission to `active` with the round's revisions accepted;
`request changes` also returns it to `active`, with the feedback recorded and
the proposals still `proposed`, so the integrator can revise and submit again,
or drop them.

The integrator still accepts small revisions of approved tasks itself with
`task accept`. The server refuses that accept, with code `-32002`, when any of
these holds:

- the task has never been approved, so the revision is new work;
- the revision declares `"material": true`;
- an `expected_paths` entry is not covered by the approved scope union (the
  union of `expected_paths` over every accepted current revision of the
  mission, which includes the task's own);
- the revision drops an exclusion carried by the task's current accepted
  revision;
- belongs to a task whose latest review round was sent back with `request
  changes`; only a round that approves the task again lifts that hold.

Each of those has to go through `mission plan submit` instead. `material` is
the proposer's own declaration on the revision JSON, not something the server
infers: it is an audit fact recorded on the revision, and the sixth reason a
revision needs a human round. Declare `expected_paths` on every task, because
after approval anything outside them is a widening that needs an amendment.

Drop a pending revision without touching the task:

```sh
/usr/local/bin/aether-internal task abandon \
  --task-id task-1 --revision 3 \
  --expected-integrator-generation 4 \
  --idempotency-key abandon-revision-3
```

Without `--revision`, the whole task is abandoned. With it, only that pending
revision is marked abandoned; the task, its current revision, and the accepted
set version are untouched.

`aether-internal skill` prints only the current phase's immediate guidance:
clarify in `planning`, discover task revision JSON and submit in `clarified`,
wait in `plan_review`, continue approved work while waiting in
`amendment_review`, dispatch and review submissions in `active`, and stop
without reporting in `rejected`. Plan version, open question count, and latest
feedback accompany integrator phase guidance. Run `skill` again after the
phase changes. Neither plan approval nor Replace integrator is an agent action.

### Inspect and manage mission tasks and workers

Task and worker commands use the authority attached to the run's socket. They
do not accept a caller-supplied role or identity:

```sh
/usr/local/bin/aether-internal task show --task-id task-1
/usr/local/bin/aether-internal task list --mission-id mission-1
/usr/local/bin/aether-internal worker list --mission-id mission-1
/usr/local/bin/aether-internal worker inspect --attempt-id attempt-1
```

Task revisions are bounded JSON specifications. Pass them with
`--revision-file path` or `--revision-file -` for standard input (or use
`--revision '<json>'`). Every task mutation (`propose`, `revise`, `accept`,
`accept-submission`, and `abandon`) requires a stable `--idempotency-key`;
integrator mutations also require the observed
`--expected-integrator-generation`. Accepting a submission additionally
requires `--expected-accepted-set-version`. If the server's exact submission
result reports non-empty `ScopeViolations`, the caller must explicitly assess
those deviations by passing `--scope-disposition '<reason>'`; the client does
not generate or infer a path list, and an empty reason is not an assessment.

Both `task propose --help` and `task revise --help` describe the author-supplied
fields of the protocol's `TaskRevision`. The minimal valid revision has
non-empty `title` and `objective` strings:

```json
{"title":"Fix checkout","objective":"Reject expired sessions"}
```

For a useful plan, also declare paths and evidence requirements before approval:

```json
{
  "title": "Fix checkout",
  "objective": "Reject expired sessions",
  "scope": {
    "expected_paths": ["internal/checkout/"],
    "exclusions": ["internal/checkout/generated/"]
  },
  "evidence_requirements": [
    {"kind": "transcript", "detail": "Retain test output showing expired sessions are rejected"}
  ]
}
```

`scope.expected_paths` and `scope.exclusions` are arrays of repository-relative
paths. Each evidence requirement has a non-empty `kind` and optional `detail`.
Kinds name retained evidence sources, such as `transcript` or `git`, not test
types. Describe the required test output in `detail`; do not use `test` as a kind.
Set `"material": true` for an amendment changing scope, constraints, or success
criteria. Do not copy server-managed IDs, revision numbers, status, or
timestamps from a response. A revision supplies the whole spec, not a patch.
JSON input is capped at 32 KiB; save files outside the read-only `/run/aether`.

For an integrator in `planning`, `clarified`, or `active`, after preparing
`/tmp/aether-task.json` and replacing the example mission ID:

```sh
/usr/local/bin/aether-internal task propose \
  --mission-id mission-1 --revision-file /tmp/aether-task.json \
  --idempotency-key propose-checkout-1
```

Use a fresh key for each new operation, retaining it for uncertain retries.
Workers may propose splits or revisions only under their assigned task scope;
they cannot accept them or dispatch workers.

Which of these the server accepts depends on the mission phase, as the table
above states: `propose`, `revise`, and `abandon` in `planning`, `clarified`,
and `active`; `accept` only in `active`, and only within the limits listed
under amendments; `accept-submission` in `active` and `amendment_review`; and
nothing at all in `plan_review` or `rejected`. `abandon` takes an optional
`--revision <n>` that drops one pending revision instead of the task.

Worker starts and retries require explicit `--dispatch-key` values. The
dispatch key is also the idempotency key for that operation, so replaying the
same command cannot create a second attempt. Worker cancellation requires its
own `--idempotency-key` and the observed
`--expected-integrator-generation`.

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

### Integrator candidate integration

A candidate freezes selected accepted revisions for combined verification and
human-approved delivery. Only the current mission integrator may use these
agent commands:

```text
integration prepare
integration show
integration verify
integration request-delivery
integration deliver
```

Each accepts `--params-file FILE|-` and optional `--json`. JSON input is
limited to 32 KiB; `-` reads standard input. The run socket supplies caller
identity. Parameter JSON cannot grant a role or approval. Results use the
normal `schema_version`, `ok`, `result`, and structured `error` envelope.
There is no agent `decide`, `approve`, generic RPC, list, resolve, patch, or
delete command.

Use each command's `--help` for its required JSON fields. Prepare requires
`target_ref`, `expected_target_revision`, and `idempotency_key`; the server
resolves omitted workspace, mission, and accepted inputs. Explicit
`submissions` select an ordered subset of current accepted revisions.

Create parameter files in a writable location, not the read-only
`/run/aether` mount:

```sh
aether-internal integration prepare --params-file /tmp/aether-prepare.json --json
aether-internal integration show --params-file /tmp/aether-show.json --json
aether-internal integration verify --params-file /tmp/aether-verify.json --json
aether-internal integration request-delivery --params-file /tmp/aether-request.json --json
```

Verification is asynchronous; poll `show` for its durable result. Wait for a
human delivery decision before executing the approved request:

```sh
aether-internal integration deliver --params-file /tmp/aether-deliver.json --json
```

Other mission work may continue, but changing the accepted submission set
invalidates an older candidate's mission binding. A replacement integrator
may continue a candidate when the accepted set is unchanged.

After an uncertain outcome, retry the same parameters and mutation identity.
Do not replace an `idempotency_key` merely because the connection failed;
delivery retries use the same `request_id` and `request_version`. A denial,
conflict, or invalid state remains a structured server error. See
[Candidate integration](integration.md) for parameter records, retained
verification evidence, human decisions, and exact delivery.

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

A worker's **success or failure** report is one-shot and terminal. Success
submits the attempt and the server then stops that worker. Failure fails the
attempt without treating it as a task result. **Blocked is nonterminal**: it is
a durable observation and does not stop the worker or submit the task.
Waiting on a peer uses ask/inbox, never report; waiting on human review uses
the plan wait command, not an outcome. Read the inbox once more before a
terminal report and take no new work afterwards.

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
evidence capture has completed. With `--conflict-coordination=false`, a newly
created container still receives the read-only canonical CLI mount, but no
coordination socket or borrowed run identity is provided. Run-bound
coordination requests are unavailable; `--help` and the general, identity-free
`skill` workflow still work, and the CLI cannot authorize run operations. The
optional MCP bridge has no usable socket. The conflict radar itself remains
active.
