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
`prepare`, `show`, `verify`, `request-delivery`, and `deliver`. Ordinary and
worker runs do not receive these commands.

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
Every run receives its own authenticated socket, independently of conflict
policy. Member terminals and verification containers receive the CLI without
borrowing a run's identity.

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

The six advertised coordination methods are unchanged. Native hooks use
`coord.hook.status`, an internal endpoint with a separate request budget, not
an additional agent capability or MCP tool. Absent or null parameters retain
the ordinary read-only result and authorization of `coord.status`. A params
object opts into bounded observation: `wait_seconds` is 0–30 (zero when
omitted on the wire), and `seen_message_ids` is at most 100 opaque message
IDs. The result adds optional `wait_supported`, `unread_message_ids`, and
`wake_admitted` fields; false/empty values may be omitted on the wire.
Observer mode never returns bodies or acknowledgement tokens and never
consumes the inbox. An admitted wake response is an input action; ordinary
status is not. The CLI helper supplies its own 30-second default and always
emits all four of its documented result fields.

Mission-assigned runs additionally receive assignment-scoped `task.*` and `worker.*` methods
published by `coord.status`; the current integrator also receives exactly
`integration.prepare`, `integration.show`, `integration.verify`,
`integration.request_delivery`, `integration.deliver`,
`mission.question.ask`, `mission.plan.show`, and `mission.start`. These
methods use the same run-authenticated socket but are not part of the base
`coord.*` set.
Every allow-list is derived from the current assignment, not from
caller-supplied roles or identities. A mission authorizes its integrator
and active worker runs as peers before any file overlap exists, on top of
the radar active/grace authorization described below.

`coord.status` reports `wire_version: "v3"`, the run, workspace, and member
IDs, the recorded task, each currently authorized peer, and the six base
coordination capabilities when conflict coordination is enabled. Assignment
capabilities extend that baseline; they do not replace it. Development
capabilities come from the live development service, not the assignment.
The sender is never a parameter. An ordinary run can message
only a peer in the same workspace that the radar currently marks as
overlapping, or a peer in its ten-minute overlap grace period. A mission run
can message those same radar peers plus its current assignment peers, so a
worker that overlaps a run outside its mission can still answer the overlap
notice. Status lists the assignment peers first with `state: "mission"` even
when no file overlap exists, carrying the overlapping files when the radar
also reports one; radar peers outside the mission follow with `state:
"active"` or `"grace"`. Any other run is refused. A question reply is the
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
- ordinary requests have a per-run transport budget of 30 requests per burst,
  refilling at one request per second. Malformed envelopes and unknown methods
  consume this budget too;
- valid `coord.hook.status` and `run.report` envelopes each use their own
  per-run budget with the same limits. Native inbox observations and lifecycle
  reports cannot spend foreground orchestration allowance or each other's
  allowance; reconnecting does not reset any budget. Malformed envelopes and
  unknown methods still spend the ordinary budget. Admission happens before
  method-specific work, so repeated lifecycle reports remain bounded even
  when the scheduler has no state change to persist;
- `wait_seconds` is a server-side wait from 0 through 30 seconds;
- each run socket accepts at most 16 concurrent connections, and inactive
  connections are reaped after five minutes;
- each request line is limited to 64 KiB;

The method set is closed. Development operations use an explicit `dev.*`
allow-list and are scoped to this run's development terminals, browser, control
leases and captures; they cannot steer the primary harness. There is no generic
RPC command, Git engine, caller-selected run identity or socket override.
Assignment-scoped task and worker methods still enforce the role, mission,
revision, generation, and current authority checks on the server.

## Delivery, acknowledgement, and retries

Delivery is at least once. An inbox read returns one oldest-first batch and an
opaque `ack_token`. Omitting `ack_token` on the next read acknowledges nothing,
so the same batch and token can be delivered again. Supplying the token on the
next read acknowledges exactly that batch while fetching the next batch. An
empty inbox has no token. Tokens are durable across server restarts while the
run's container and coordination data are retained.

The batch is frozen until acknowledged. New arrivals can increase `status`
unread counts or native observer IDs without changing the current inbox batch.
Handle that batch, then pass its exact token to `inbox --ack`; the returned
next batch includes later arrivals. Re-reading without `--ack`, including with
`--wait`, does not advance past an outstanding batch.

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

Aether does not type automated coordination messages into terminals. There
are three ways to notice mail:

1. **An active inbox wait wins.** An agent expecting a reply should use
   `aether-internal inbox --wait 30`. The current tool call receives the
   original attributed message and acknowledgement token; a native observer
   must not start an extra turn for that arrival.
2. **A native wake can resume a live idle harness.** The loaded pi, OMP, and
   version-matched OpenCode integrations observe unread IDs with bounded
   helper waits and use their native session APIs for a follow-up. The
   server admits that wake only for the current eligible TUI run.
3. **A boundary hook supplies context at the next native event.** Claude
   Code, Codex, Copilot CLI, Gemini CLI, and Cursor CLI command hooks do not
   watch an already-idle session. Mail arriving after their last hook waits
   for the next supported boundary or explicit inbox read.

See [per-harness setup and limits](harnesses.md#incoming-coordination-hooks).
All integrations add only a trusted instruction to read the inbox. They
never acknowledge a batch or promote a peer's body into system/developer
instructions. The agent reads the original, attributed payload through `inbox`.

Hooks also direct agents with overlapping edits to `status`. Integrators are
told to refresh `mission plan show` and `worker list` when the mission phase,
open-question count, or integrator generation changes. Those APIs remain
authoritative; no terminal notice is required.

Claude Code, Codex, Copilot CLI, Gemini CLI, and Cursor CLI keep hook context
in the transcript, so their hooks announce each notice once per state: the
unread count, the mission state, and the overlapping files. A changed state is
announced again; a Stop still blocks while mail is unread. The hook records
what it announced in `$TMPDIR/aether-hook-<run>-<harness>.json`; a missing
record repeats the notice. pi, OMP, and OpenCode add context per model call
without keeping it, so their notices repeat.

Every mission-worker report, whatever its outcome, is forwarded to the
current integrator as an ordinary inbox message: `from_run_id` is the
reporting worker, `body` is its complete report summary, and `correlation_id`
is the report ID. This message
uses the same durable delivery and explicit acknowledgement as peer messages.
If the inbox is full, the existing report-publication retry retains the work.

Wake admission rechecks live run and mission authority, including protection,
human takeover, current assignment/revision/generation, and submitted or
settled attempts. A held run keeps its mail. Releasing a hold allows a later
bounded observer response to admit still-unread mail; it does not restart an
agent. The admitted response is dispatched inside the same per-run control
boundary as human input, with a write bound of at most three seconds, not
while holding control through the long wait. A hold committed first prevents
dispatch; input already accepted before a later hold may still be processed.

Mission revision and generation changes are serialized with the complete
wake-frame dispatch as well; checking authority once before writing is not
enough. Inbox-consumer registration is also ordered against that final write:
an earlier registered consumer keeps priority, but a consumer registered
after an admitted frame cannot revoke the already-accepted wake.
Mission/control admission is nonblocking for this optional action: if either
boundary is busy, wake is suppressed rather than queued behind its holder.
The receiver updates observed IDs without adding notified IDs, so a later
bounded observation can reconsider mail without losing or acknowledging it.

Opening a mission worker's dashboard terminal defaults to read-only viewing;
viewing alone does not acquire human control. Use **Take control** to request
write access and **Release** to return control. Ordinary owner terminals keep
their existing automatic write behavior.

Changing an attached read-only mirror to **Write** commits the mission
takeover hold only after the
live terminal is ready, inside the same admission boundary. An explicit
release (`Write=false` on the interactive control stream) clears the hold
only after read-only readiness succeeds. A failed transition retains the
previous lease and hold; readiness is restored or the stream fails closed.
Disconnecting or letting the control lease expire does **not** clear the
durable mission hold, so it cannot silently re-enable native wake.

Native integrations also honor the owning session's Stop/abort and cancel
stale helper results on shutdown or session replacement. They do not override
approval waits. See the harness-specific resume behavior before relying on
automatic wake after a Stop.

Missing, disabled, untrusted, unsupported, or failed integrations do not lose
mail. Inspect the real harness error and read the inbox explicitly. There is
no silent PTY fallback, attachment to an arbitrary running TUI, or automatic
restart of an exited or closed run. Headless runs may use supported boundary
hooks while alive, but do not receive native idle wake.

Accepted messages, questions, and replies retain their originating run in the
workspace timeline. **Durable acceptance**, **an admitted wake**, and
**explicit acknowledgement** are different events: neither a send receipt,
hook execution, nor native API acceptance proves model receipt or processing.
Only the agent's later `inbox --ack` acknowledges the returned batch; there is
no terminal-delivery stamp.

For a native-trigger investigation, retain separate evidence of the send
receipt/message ID, helper wake admission, native follow-up acceptance, the
resulting model turn, and the agent's explicit inbox read and acknowledgement.
A reply after a manually supplied prompt proves messaging, not an idle wake.
Configured files or a helper-only check do not prove that the root harness
loaded the integration or received the turn.

## `aether-internal` CLI

A task-bearing run receives this capability-neutral launch instruction automatically:

```
Use `aether-internal skill` to read this run's live identity, capabilities, and any assignment before acting. Use only available capabilities and report only what you verified.
```

No skill package, manual identity argument, or credential setup is required.
Run `skill` before acting so the assignment and capabilities come from current
server state rather than copied prompt text.

All commands below run inside the container. State commands write one JSON
object followed by a newline. `skill`, help, and `hook` have their own output
formats. Successful state commands use this shape:

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
optional. Unknown flags and positional arguments are rejected. `skill` prints
the CLI build version, live role and identity, current phase and immediate
actions. `skill terminal`, `skill browser`, and `skill git` load focused
development guidance when the relevant live capabilities are advertised.
Its build version comes from the mounted binary, which may predate newly
published documentation; help describes that binary's command syntax.

The task text in status and skill is a bounded summary (at most 512 bytes),
not the full assignment. A worker's skill prints an executable command with
its own task ID:

```sh
/usr/local/bin/aether-internal task show --task-id task-1
```

Use the actual command from skill, not the example ID above. Read the returned
task revision, objective, scope, exclusions, and evidence requirements before
acting. Workers may read and propose; they must not spawn workers, accept tasks,
or perform mission/integration operations. A worker's skill also tells it to
check the inbox after reading the task, before each commit, and before
reporting, and that `status` lists its sibling workers. Ordinary runs have no
mission authority. Help documents syntax, not permission.

Every role gets `status`, `inbox`, and top-level help bootstrap commands.
An integrator's skill states its role before its phase guidance: turn the
objective into tasks for workers and coordinate them, not implement the
objective itself. Integrators additionally get task/worker help, list
commands using the current mission ID, integrator generation, and approved
account/harness/mode choices.
Integration guidance appears only in `active`. Use the full
status result for the current capability set.

Top-level and per-command help are available without a coordination socket:

```sh
/usr/local/bin/aether-internal --help
/usr/local/bin/aether-internal send --help
```

`skill` also works without the mounted socket. In that case it prints short,
capability-neutral discovery, not an assignment, tool registration or claim
that a terminal, browser, Git credential or image reader exists. Commands that
need run state return `-32004` and exit with status 4 when the socket is not
available. Message, question, reply, and report bodies read from flags, files,
or standard input are capped at 4 KiB before a request is sent.

`skill` prints one line with the inbox hook state for this run's harness,
named by `AETHER_HARNESS`. `skill --hooks` checks the standard hook
configuration locations for every harness without changing them. It reports
configured, missing, invalid, disabled, or unverified installations and prints
the matching file-export command, destination, merge instructions, and
reload/trust steps. Install only the current harness.
Configured on disk does not mean loaded or trusted. Explicit configuration
paths, additional project ancestors, packages, and runtime overrides can be
outside this bounded check.

### Native hook files and handler

```sh
aether-internal hook file
aether-internal hook file claude.json
aether-internal hook --help
```

The files are embedded in the mounted binary, so installation needs no download.
Exporting a file needs no socket. Merge JSON hook entries rather than replacing
existing settings; copy extensions only after checking the destination.
See [harness installation](harnesses.md#incoming-coordination-hooks) for each
destination, and [authoring an integration](harness-integration.md) for an
unsupported harness.

The supplied files invoke `aether-internal hook <harness> <event>`. Command hooks
read native JSON on stdin and write that harness's native JSON response.
Extension hooks use event `context` and receive plain trusted context text.
With no pending mail or applicable overlap/mission guidance, stdout is empty.
A missing coordination socket also produces no output, so a user-level hook
can stay installed outside Aether. An existing but broken socket or other
failure returns nonzero and the actual error on stderr, never a success hint.
Stop hooks request at most one continuation for pending mail or an integrator's
final mission refresh, even with an empty inbox. Native repeat-stop guards
prevent a continuation loop; ordinary and worker runs with empty inboxes do
not get a forced refresh.

Native receivers additionally use one bounded observation per invocation:

```sh
printf '%s\n' '{"seen_message_ids":[],"wait_seconds":30}' |
  /usr/local/bin/aether-internal hook generic wake
```

`wake` is available for `pi`, `omp`, `opencode`, and `generic`; omitted
`wait_seconds` defaults to 30. Its JSON result has `wait_supported`,
`unread_message_ids`, `wake_admitted`, and `context`. Context is an inbox-only
instruction and is empty unless admission succeeded. This helper observes;
it does not itself call a native API or start a model turn.

The receiver sends its last observed unread-ID set as `seen_message_ids`,
even when that response did not admit a wake. It separately remembers IDs
handed to the native API and prunes them when they leave the unread set;
synchronous native rejection is not notification. Only admitted IDs not yet
notified cause a follow-up. Thus mail
held by protection can wake after release, while the same unacknowledged
batch cannot cause endless turns. A session/process restart may produce one
duplicate hint; durable inbox delivery and explicit acknowledgement are
unchanged. An unsupported server is an error to stop on, not a reason to
retry immediately. See the [helper contract](harness-integration.md#optional-native-idle-wake)
for error handling and lifecycle requirements.

### Development terminals, browser and captures

Development is independent of conflict coordination and mission assignment.
Run `status` first: command help describes syntax, while `capabilities` is the
live server allow-list. Every method in the following closed families has a
thin typed CLI command:

| Command family | Subcommands |
| --- | --- |
| `terminal` | `list`, `start`, `output`, `screen`, `screenshot`, `input`, `resize`, `wait`, `stop` |
| `browser` | `status`, `open`, `pages`, `navigate`, `snapshot`, `action`, `screenshot`, `viewport`, `wait`, `console`, `network`, `reset`, `close` |
| `control` | `status`, `acquire`, `release` |
| `artifact` | `list`, `get`, `delete`, `retain` |

All use `aether-internal <family> <subcommand> [--params-file FILE|-] [--json]`.
Omitting `--params-file` sends `{}`; use this for `terminal list`, `browser
status`, and unfiltered `artifact list`. Other operations need the fields in
their command-specific `--help`. A file or stdin must contain one strict JSON
object, at most 48 KiB before and after encoding. Unknown fields and `run_id`
(even empty, null, or differently cased) are rejected before dialing. Development
control requests and responses stay within 64 KiB. Screenshot responses contain
artifact metadata and a server-assigned read-only path, never image base64.

```sh
aether-internal skill terminal
aether-internal terminal start --help
printf '%s\n' '{"name":"app","command":["npm","run","dev"],"cols":100,"rows":30}' |
  aether-internal terminal start --params-file -
aether-internal terminal list
aether-internal control acquire --help
aether-internal terminal screen --help
aether-internal terminal screenshot --help
aether-internal artifact list
```

Retain the returned `terminal_id` and `incarnation`. Read raw/text output with
`terminal output`, current styled cells with `terminal screen`, and an image
with `terminal screenshot`: these are three different observations. Follow
output cursors and gap/truncation flags. Screen continuation carries both row
and column offsets plus `expected_screen_revision`; restart paging on a changed
revision rather than combining frames. Honor protocol-error/unsupported-graphics
indicators. A successful `wait` RPC does not mean a successful app: inspect
`matched`, `timed_out`, and the process's exit state/code.

Before input, resize or stop, acquire a surface
`{"kind":"terminal","id":"<terminal_id>","incarnation":"<incarnation>"}`
with your own opaque `control_session_id`. Use the returned
`controller.control_generation` for mutations and release that exact lease
when finished. Re-observe stale ownership instead of blindly replaying input
or stealing a human's control. Input is fenced by incarnation and controller,
not redraw revisions. A viewer attach does not impose geometry; resize is an
explicit controlled action. Releasing control or detaching does not stop the
app; `terminal stop` stops the owned process group.

Browser startup is lazy and works on a standard **headless Ubuntu server**.
There is no X11, Wayland, Xvfb, desktop session, host browser or disabled
sandbox requirement. The isolated companion shares the run network namespace
so an app's run-local URL is reachable; it does not mount the checkout, member
home, credentials or Docker socket.

```sh
aether-internal skill browser
aether-internal browser status
printf '%s\n' '{"url":"http://127.0.0.1:3000","control_session_id":"agent-browser-1","control_generation":0}' |
  aether-internal browser open --params-file -
```

The first `open` omits `session_id`, uses generation zero and a nonempty
controller ID, and atomically bootstraps the companion's control ownership.
Retain `result.page` and `result.control`. Later opens require the existing
session and controller fence; a missing session never silently creates another
browser. For regular acquisition use
`{"kind":"browser","id":"browser","incarnation":"<session_id>"}`.
Page operations carry `session_id`, `page_id`, and `page_revision`; mutations
also carry `control_session_id` and `control_generation`. Coordinate actions
use the observed `viewport_id`; semantic actions use revision-scoped node IDs
from `snapshot`. Re-observe after navigation/DOM/viewport changes. `reset` is
explicit, invalidates old identities, and requires a new control acquisition.
Browser status also reports `state`: `not_started`, `creating`, `running`,
`paused`, `session_lost`, or `unavailable`. An unreachable or failed Chromium
session reports `available: false`, `running: false`, and its actual error.
Failed initial creation may return an opaque
`pending:` session ID. It is a recovery fence, not a usable page session.
Acquire the browser surface with that exact incarnation, then explicitly
`browser reset` with the acquired lease to recover. The broker destroys only
the recorded owned companion; another `open` never silently recreates it.

Browser observers receive the latest complete frame when joining an active
stream, even when the page is static; slow viewers do not queue an image
history. On controller release, revocation or disconnect, the server clears
held browser keys, buttons and touches before admitting another controller.
If cleanup cannot be confirmed, new control remains fenced.

Page inventories and physical-input acknowledgements use cached, best-effort
titles without waiting for a DOM read. Inventories refresh titles asynchronously;
navigation clears the old title. Explicit page observations wait at most one
second for a title. Session, page, revision, URL, and viewport fences remain live.

Terminal wait/stop and browser navigation/action/wait bounds are 30 seconds.
First browser open has a 90-second bootstrap budget and reset has 30 seconds.
Terminal screenshots have a 90-second total budget, including first companion
startup; the isolated renderer has 30 seconds for page/font setup, VT replay,
and PNG capture, plus five seconds for its response. Cancellation closes that
renderer context without closing the app's browser session.
The transport adds a three-second framing margin; browser navigation/action/wait
also allow the companion's five-second response margin. Earlier caller
cancellation still applies.

Use the full loop: edit, run, observe, interact, correct, and verify the changed
behavior. Browser snapshots/console/network and terminal output/screens are
useful diagnostics but not image-based evidence. Open the screenshot artifact
path with a harness image-consuming tool **only if one actually exists**. If
the harness cannot read images, report that exact limitation and the checks
actually performed; never infer visual correctness from text alone. Transient
captures require live **Steer** and backing-account access; they are not durable
evidence references.

Before deleting captures, stopping the run or submitting a terminal worker
report (including headless completion), explicitly retain the reviewed selection:

```sh
printf '%s\n' '{"artifact_ids":["<capture_id>"],"verification_notes":"Observed behavior and verification limits","idempotency_key":"review-captures-1"}' |
  aether-internal artifact retain --params-file -
```

`artifact retain` returns `result.packet_id`. Read back that packet in the
dashboard's evidence view, including its selected captures and notes, then pass
the ID to the existing `report --evidence-ref <packet_id>`. Retention does not
report an outcome or prove verification. Select 1–64 captures; optional notes
must be valid UTF-8 and at most 4096 bytes. Retained and staged capture files
together are bounded to 64 captures and 128 MiB per run, with an 8 MiB limit
per capture. Transient captures separately have a 64-capture/128 MiB run bound.

Retention copies immutable capture bytes and their original observation
metadata; the packet's later Git snapshot is a separate boundary, not proof of
the checkout state when pixels were captured. Unknown capture-time Git state
stays unknown. Deleting a transient capture does not remove its retained copy.
Retained copies use existing evidence expiry and broader workspace **View**
access, not private live-session permissions. Review pixels, URLs and notes for
secrets before retaining; no automatic public upload or PR attachment happens.

Retention rechecks current authority after acquiring the run lock, before
opening each source, and at publication. A busy authorization admission returns
`authorization admission in progress; retry retention`, not a success or an
automatic retry. Inspect evidence after an uncertain result; if explicitly
retrying, reuse the same idempotency key, ordered capture IDs and exact notes.
Changed selections or notes are refused rather than returning the earlier
packet. An expired packet is also refused, including after its bytes have been
cleaned up. Use a new key for a new retention request.

### Native Git and pull requests

`aether-internal skill git` teaches native `git`/`gh`; there is no second agent
Git engine or Git RPC. The topic is offered with a live development execution
capability, not as proof that `gh`, credentials or push permission are present.
Check the actual run environment and inspect `git status --short`,
`git diff`, `git diff --cached`, `git branch --show-current`, `git remote -v`,
and `gh auth status` before relying on them. Stage exact intended paths, verify
the behavior/staged diff, and preserve existing author/signing configuration
and `/run/aether/co-authors` trailers.

The workspace mirror's **import source**, the run checkout's **origin**, and a
fork PR's **base repository/branch** and **head owner/branch** are distinct.
Inspect them explicitly; do not push to a guessed mirror URL or rewrite origin
implicitly. Inspect existing PR metadata before native `gh pr create`/update
to avoid duplicate or wrong-base PRs. Follow the assignment's approval and
mission integration boundaries; discovery does not authorize PR merging,
automatic screenshot publication, or disabling commit signing.

Every pull request description ends with `Opened from Aether run <run id>`;
`skill git` prints that line with the run's `run_id` from `status` filled in.
The count of agent-made pull requests is derived from this line.

Custom or argv-overridden harnesses still receive the staged CLI and run socket
but no guessed vendor startup flags. In taskless mode their authors must invoke
`aether-internal skill` through their own native startup mechanism or manually.
No fake task, permanent repository settings or implicit MCP registration is
created to make discovery appear to work.

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
/usr/local/bin/aether-internal ack ack-example-1
```

After handling the batch, pass its `ack_token` to `ack`. `ack` refuses with
exit 3 and names the current batch's token when the token is stale or
mistyped, and returns `waiting`, the number of messages queued for the next
read. `inbox --ack <token> --wait 30` acknowledges and reads the next batch in
one call. `--wait` asks the server to wait once for up to 30 seconds
when no message is ready; it is not a client polling loop. If the process or
connection ends before the result is consumed, do not acknowledge the token
and read again.

Use this bounded wait while actively coordinating instead of keeping a model
turn alive to poll. The active inbox waiter takes priority over native wake
observers. Check the inbox before waiting or reporting even when integrations
are installed; a hint is not a read or an acknowledgement.

### Mission phases

A mission is created in `planning`. The integrator asks the accountable human
a question only if the objective is genuinely ambiguous, proposes tasks, and
runs `mission start`. No human approves the plan, and no worker starts before
`mission start`. The phase is mission state; every method below is refused in
the wrong phase with code `-32002`.

| Phase | Worker dispatch | Integrator task mutations |
| --- | --- | --- |
| `planning` | refused | `task propose`, `task revise`, `task abandon` allowed; `task accept` and `task accept-submission` refused |
| `active` | allowed | all allowed |
| `completed` | refused | all refused |
| `cancelled` | refused | all refused |

Transitions are exactly:

```
planning  --mission start------------->  active
active    --integrator reports success-->  completed
planning  --cancel-------------------->  cancelled
active    --cancel-------------------->  cancelled
completed, cancelled: terminal
```

`mission.cancel` is the human's stop button. It is not reachable from the run
socket; the accountable human or an admin cancels from the Missions page or
with `aether swarm cancel`. Cancelling stops every live worker and the
integrator run.

Only the current integrator may use these commands, and only for its own
mission:

```sh
/usr/local/bin/aether-internal mission question ask \
  --body 'Which checkout flow should this replace?' \
  --idempotency-key mission-ask-1
/usr/local/bin/aether-internal mission plan show --wait 30
/usr/local/bin/aether-internal mission start \
  --mission-id mission-1 --idempotency-key mission-start-1
```

`question ask` and `start` require an explicit `--idempotency-key`; unlike
`send` and `ask --to`, the CLI never generates one for them. `--body-file`
accepts a path or `-` for standard input, and the body is capped at 4 KiB.
`mission question ask` asks the accountable human, who answers on the Missions
page or with `aether swarm answer`; `ask --to <run-id>` asks a peer agent run,
which answers with `reply`. They are separate mailboxes. Questions are allowed
only in `planning`.

`mission plan show` returns the phase, integrator generation,
`plan.accepted_set_version` (including zero), and open question count, plus
every question. `--wait` asks the server to wait up to 30 seconds for a
change, including an accepted-set version change; it returns unchanged on
timeout. A value outside 0 through 30 is a usage error before any request is
sent. Waiting for an answer is not being blocked: do not report an outcome
while waiting.

`mission start` is refused while a question is unanswered or when no task is
proposed. It rechecks that the accountable human may still launch runs, then,
in one transaction, accepts every proposed revision with the integrator run as
the accepting run and moves the mission to `active`. While the mission is in
`planning`, revising a task replaces the draft: the previous revision is
superseded and the new one becomes current.

In `active`, `task accept` accepts any proposed revision, so new or revised
work needs no human step: propose or revise it, accept it, and dispatch it.

Every worker report (success, failure, or blocked) reaches the integrator's
inbox as an ordinary message: `from_run_id` is the worker, `body` is the
report summary, and `correlation_id` is the report ID; `worker list` shows the
outcome. A worker whose run ends without reporting sends nothing, so check
`worker list` before declaring completion. The integrator's hooks announce a
changed mission phase or open-question count once. The integrator is always
interactive (TUI): `mission.create` and
`mission.replace-integrator` refuse any other mode with `-32602` and
`integrator mode must be tui: a headless integrator exits after one turn and
cannot be asked or told`. `mission.create` needs the integrator's exact
account, harness, and `tui` mode among the execution choices;
`mission.replace-integrator` accepts any listed account and harness in `tui`,
so a swarm whose choices are all headless can still get an interactive
integrator. Both refuse, with `-32602` and `integrator harness <name> cannot
launch in tui mode: <cause>`, a harness the integrator's run owner cannot
start in `tui` on that account, such as one whose definition the run owner no
longer has, or the run owner's own member-defined harness on another
member's account, where it never runs. Workers may
still run headless. If the integrator's
harness has already exited, the line lands in the shell left on its terminal
and is read as a command line there.

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
ask, propose, and start in `planning`; dispatch, review, deliver, and report
in `active`; and stop in `completed` or `cancelled`. Open question count
accompanies integrator phase guidance. Run `skill` again after the phase
changes. Replace integrator is a human action, not an agent one.

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
requires `--expected-accepted-set-version`. Read its exact value from
`aether-internal mission plan show` at `result.plan.accepted_set_version`;
do not infer it from worker counts or an assumed zero.
Acceptance advances this compare-and-swap version. After a stale-version
refusal, inspect the current plan and submissions before choosing a new
operation; an uncertain retry keeps its original key and inputs.
If the server's exact submission result reports non-empty `ScopeViolations`,
the caller must explicitly assess those deviations by passing
`--scope-disposition '<reason>'`; the client does not generate or infer a
path list, and an empty reason is not an assessment.

Both `task propose --help` and `task revise --help` describe the author-supplied
fields of the protocol's `TaskRevision`. The minimal valid revision has
non-empty `title` and `objective` strings:

```json
{"title":"Fix checkout","objective":"Reject expired sessions"}
```

For a useful plan, also declare paths and evidence requirements:

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
  ],
  "depends_on": ["task-1"]
}
```

`scope.expected_paths` and `scope.exclusions` are arrays of repository-relative
paths. Each evidence requirement has a non-empty `kind` and optional `detail`.
Kinds name retained evidence sources, such as `transcript` or `git`, not test
types. Describe the required test output in `detail`; do not use `test` as a kind.
`depends_on` lists the IDs of tasks in the same mission whose output this task
needs. Until every one of them has an accepted submission for its current
revision, `task show` reports the task as `blocked` with a `dependency` blocker
naming the task, and `worker start` is refused with code `-32003` and a
message ending in `task task-2 waits for task task-1`. A new revision of a
dependency has no accepted submission yet, so the dependent waits again until
one is; the `depends_on_revision` a projected dependency reports is the
dependency's revision when the row was written, not the one readiness checks.
A `depends_on` entry that names an unknown, abandoned, or self ID, or that
would form a cycle, is refused and the revision is not written. The cycle
check counts every task's current revision and its latest pending draft, so
reversing a dependency takes two steps: get the revision that drops the old
edge accepted, then propose the reversed one.
Do not copy server-managed IDs, revision numbers, status, or
timestamps from a response. A revision supplies the whole spec, not a patch:
a revision without `depends_on` drops the dependencies of the previous one.
JSON input is capped at 32 KiB; save files outside the read-only `/run/aether`.

For an integrator in `planning` or `active`, after preparing
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
above states: `propose`, `revise`, and `abandon` in `planning` and `active`;
`accept` and `accept-submission` only in `active`; and nothing at all in
`completed` or `cancelled`. `abandon` takes an optional
`--revision <n>` that drops one pending revision instead of the task.

`worker start` needs only `--task-id`:

```sh
/usr/local/bin/aether-internal worker start --task-id task-example --harness claude
```

`--harness`, `--mode`, and `--account-owner-id` select one approved execution
choice and may be omitted when only one choice matches; an ambiguous selection
is refused with every choice listed. The mission, integrator generation, and
current task revision are read from the assignment, and the dispatch key
defaults to `<task-id>-r<revision>`. The dispatch key is also the idempotency
key, so replaying the same start cannot create a second attempt. Worker
retries require an explicit `--dispatch-key`. Worker cancellation requires its
own `--idempotency-key` and the observed
`--expected-integrator-generation`.

A worker attempt remains `launching` while its run is queued or provisioning.
Once the scheduler confirms `running` or `needs-attention`, the attempt is
persisted as `running` with `started_at`, and `worker start`, `worker inspect`,
and `worker list` return that durable state. Recovery can confirm an existing
live attempt without relaunching it; an observation error or uncertain owner
does not establish that execution started. Attempt state is not a model
readiness signal: `running` does not prove that the model read its assignment.

Worker completion and mission phase are separate: the mission stays `active`
after every worker attempt finishes. It moves to `completed` only when the
integrator reports success, which also stops any leftover workers. When a
mission ends, a worker that already submitted is retained and its attempt
recorded `completed`; every other live worker is killed and its attempt
recorded `cancelled`. A success report before `mission start` is refused with
code `-32002`, and so is one while a mission candidate holds an approved
delivery request that has not run, has not expired, and no later delivery to
the same ref replaced; the error names the `integration deliver` parameters. A read-only
investigation with nothing to deliver reports success once its findings are
gathered; do not submit a fake integration candidate just to change the phase.

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
delivery. Only the current mission integrator may use these
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

Verification is asynchronous; poll `show` for its durable result. A mission
candidate needs no human delivery decision: once verification passes,
`request-delivery` returns a request already approved on behalf of the
accountable human, who must still hold Push on the workspace. Deliver it:

```sh
aether-internal integration deliver --params-file /tmp/aether-deliver.json --json
```

After delivery, report success; that completes the mission. If `prepare`
reports a conflicted candidate, prepare and deliver an ordered subset of the
accepted submissions that applies cleanly, then revise the conflicting task so
a new worker redoes it from the advanced target, accept its submission, and
prepare that submission.

Other mission work may continue, but changing the accepted submission set
invalidates an older candidate's mission binding. A replacement integrator
may continue a candidate when the accepted set is unchanged.

After an uncertain outcome, retry the same parameters and mutation identity.
Do not replace an `idempotency_key` merely because the connection failed;
delivery retries use the same `request_id` and `request_version`. A denial,
conflict, or invalid state remains a structured server error. See
[Candidate integration](integration.md) for parameter records, retained
verification evidence and exact delivery.

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
Terminal worker reports wait for concurrent mission operations to leave admission
before returning the receipt; ordinary lock contention is not a report conflict.
The receipt is not task acceptance: a success report without required user
evidence leaves the task in **Review**.

**Success and failure are terminal**: a run holds one terminal report. After
it, a report under any new idempotency key, `blocked` included, fails with
`CodeConflict` (`-32003`); the same key and inputs replay the original
report. Relaunching the run (**Relaunch** on its card, or `aether relaunch
<run>`) supersedes the terminal report, including one whose evidence capture
failed and was never accepted, so the reopened agent can report again under a
new idempotency key; the superseded report's key then fails with
`CodeConflict`, as does a report whose evidence capture was still running
when the relaunch landed. **Blocked is nonterminal**: a run may file any
number of blocked reports, before or after one another.

What a report does depends on the run:

- **Ordinary run** (no mission assignment). Success or failure finishes the
  run once the agent's turn ends: Aether commits the work (`aether:` for
  success, `wip:` for failure), publishes the run branch, and records
  `completed` or `failed`, which moves the run out of **Working**. The turn
  ends when the agent reports itself **Idle** with no **Needs input**
  request open; a permission or question prompt is not the end of the turn,
  and the run finishes once it is answered. A report that reaches the server
  after the turn already ended finishes the run at once. A harness without a
  status reporter never says its turn ended, so there the first
  `--poll-interval` check two minutes after the report finishes the run; a
  harness with one is finished by the turn end alone, however long the agent
  keeps working, unless the run stalled into **Idle** by that check, which
  then finishes it. A run with a **Needs input** request open is never
  finished by the check; it waits for the owner's answer. A finish that fails
  is retried by the same check two minutes later. If the agent process exits
  first, the run takes the reported status, whatever its exit code, even when
  the report reaches the server after the exit or was made while the run was
  still starting; a report a relaunch superseded never changes the run. The
  commit the exit already published keeps the exit's `aether:` or `wip:`
  prefix, and the run's status and report are the record. A TUI run keeps its
  paused container for relaunch, exactly like a closed run, with the reason
  `agent reported success; retained container` or
  `agent reported failure; retained container`; a headless run, or a TUI run
  with a negative `--run-container-ttl`, records `agent reported success` or
  `agent reported failure`. The finished run carries `outcome_unseen: true`
  until its owner opens it (`run.seen`) or a status change such as Close or
  relaunch clears it. A Close or Kill that lands first wins, and Close still
  re-labels a finished run as merged or abandoned. Blocked moves the run to
  **Idle** with the reason `blocked: <summary>` the next time its turn ends
  (or it stalls, on a harness without a status reporter), until the agent
  resumes; a **Needs input** request does not show it. The newest blocked
  report decides the reason: an older one the server retries delivering after
  it changes nothing.
- **Mission worker.** Success submits the attempt and the server then pauses
  and retains that worker's exact container with the reason
  `worker finished; retained container`. Failure does the same without
  treating the attempt as a task result. A finished worker is never
  relaunched, and its outcome is left for the integrator to review rather
  than marked unseen. Capacity is released only after execution has stopped
  and retention/evidence cleanup has settled. Blocked is a durable
  observation: it does not stop the worker or submit the task. A worker may
  file several blocked reports and still report success or failure after
  them.
- **Mission integrator.** Success completes an `active` mission and stops
  any leftover workers; it is refused in `planning`. Failure leaves the
  mission in its phase, so a human can recover it with Replace integrator.
  Either way the integrator run then finishes like an ordinary run once its
  turn ends. Report success only after the verified candidate is delivered,
  or, with nothing to deliver, once the findings are gathered.

Waiting on a peer uses ask/inbox, never report; waiting on a question's
answer uses `mission plan show --wait`, not an outcome. Read the inbox once more before a
terminal report and take no new work afterwards.
Finish runtime verification and collect any required terminal/browser captures
before that report; accepted terminal outcomes may clean up the worker's
development resources. A capture receipt is not proof that an image was viewed.

Reports publish durable timeline/evidence records, not terminal keystrokes.
The integrator reads `worker list` and `worker inspect --attempt-id` for
attempt outcomes; a blocked worker's report is additionally forwarded to its
ordinary inbox with the worker's attribution. Use `inbox --wait 30` while
actively coordinating, then refresh durable mission/worker state before
waiting again or declaring completion.

Before accepting `coord.report`, Aether captures evidence for the run. The
capture retains a private Git evidence commit and the PTY transcript up to
16 MiB, then stores factual context, provenance, unresolved facts, and a next
action with the report. Evidence is retained for 30 days. Unavailable or
truncated sources are recorded explicitly. The capture is not an atomic
environment snapshot and does not claim that the reported work was verified.
If capture or durable storage fails, the outcome is not accepted, and the
runtime resources remain recoverable.

Mission submission acceptance distinguishes source availability from
completeness. A readable retained transcript satisfies a `transcript`
requirement even when it reaches the 16 MiB cap. `mission.show` exposes that
source as `available: true, truncated: true`; it is partial evidence, not a
complete transcript or proof that verification passed. Acceptance checks the
packet's workspace, run, retained revision, expiry, and retained bytes.
Missing or unreadable required sources remain unavailable.

An older server may have recorded a capped transcript as unavailable on a
still-proposed submission. After upgrading, the next authorized
`task accept-submission` revalidates the retained packet and updates those
facts atomically with acceptance. It does not infer availability from the
detail text or repair an expired or missing packet. Retrying an accepted
operation preserves its receipt even after later evidence expiry, while
still enforcing current authorization.

Candidate integration has a separate completeness policy: a truncated source
listed in `required_sources` remains refused. See
[candidate source validation](integration.md#candidate-identity-and-assembly).

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

The two meet on an ordinary run. After a success or failure `coord.report`,
the next `waiting` (idle) hook with no input request open is the end of the
turn that reported, so it finishes the run instead of parking it. A hook that
only opens or closes an input request is not a turn end, but closing the last
open request on a reported run that is already idle finishes it. After a
blocked `coord.report`, the next idle hook parks the run with the
`blocked: <summary>` reason instead of `agent idle`; the first `working` hook
after that park clears it.
The scheduler applies a report as part of its durable publication, so a
server restart or a temporarily unreachable run is retried, not lost.

Coordination also does not provide a universal inbound terminal hook. When a
human steers a run, Aether delivers the request through the harness's
serialized PTY input path and records the delivery separately.

## Retention and shutdown

Active runs, explicitly closed TUI runs, and completed mission runs keep their
socket, unread mailbox, and timeline entries through a server restart.
Completed workers retain their exact paused (or already exited) container,
checkout, row, and member account for `--run-container-ttl` (default 7 days).
The dashboard run detail, transcript, diff, evidence, and worker inspection
surfaces remain available; retention does not grant live input or wake authority.
Completed workers cannot be relaunched, including after a human relabels their
outcome. Explicit Kill, Delete, and worker cancellation still destroy the
container; a negative TTL requests immediate cleanup. Recovery rebinds
`coord3.sock` for retained runs. When a run's container is destroyed,
Aether releases the coordination directory and mailbox after any required
evidence capture has completed. With `--conflict-coordination=false`, runs
still receive their identity socket and per-launch discovery hint.
`coord.status` reports the live method allow-list; conflict and mission
operations remain disabled rather than inheriting authority from the socket.
The conflict radar itself remains active.
