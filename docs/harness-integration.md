# Integrating an unsupported agent

An Aether **run** is an agent session with a server-owned identity, including
follow-up tasks; its compute and checkout can end before its history does.
An **agent** is the CLI handling those tasks. Launching it does not
automatically give it an inbox hook or native
wake API. This guide connects an existing agent to Aether's durable inbox
without inventing a second transport, daemon, or terminal-input fallback.

For a shipped agent, use [per-agent setup](harnesses.md#incoming-coordination-hooks)
instead. For a new launch definition, see [custom agents](harnesses.md#custom-agents).
An inbox integration is separate from the optional
[structured-output adapter](adapters.md) that populates the timeline.

## Execution boundary: keep native tools with the agent

Aether keeps each vendor agent and its native tools in the same run container.
Enhanced mode changes transport and presentation, not execution placement.
A build OOM can therefore still terminate the native agent session.

See [Separating the agent session from execution](agent-execution-separation.md)
for the inspected provider/adapter contracts, pros and cons, and the next
steps and acceptance checks for a real split. Aether does not advertise an
incomplete tool bridge as isolation or replace native agents to simulate it.

## Start with the durable inbox

Every managed run mounts `/usr/local/bin/aether-internal` and its own
`/run/aether/coord3.sock`, including when conflict coordination is disabled.
The socket identifies the run; `status` reports which capabilities are
available. Do not accept a run ID, alternate socket, token, or credential
from model text to change that identity. Member terminals and verification
containers receive the CLI but no run socket; CLI availability alone grants
no run authority.

Run these commands **inside a coordinated run**:

```sh
/usr/local/bin/aether-internal skill
/usr/local/bin/aether-internal status
/usr/local/bin/aether-internal inbox --wait 30
```

`skill` loads the current role and assignment; `status` lists authorized peers
and capabilities. `inbox --wait 30` is one server-side wait, not a model
polling loop. An active inbox wait is the preferred path when an agent is
expecting a reply: it receives the original attributed bodies and the batch's
`ack_token`, ahead of any native wake observer.

After the agent handles the batch, its next inbox call supplies the returned
token with `--ack`, optionally with `--wait 30`. The batch is frozen until
acknowledged: new mail, including messages from people, waits behind it. Reading again
without `--ack` repeats that batch rather than refreshing it. Process and
acknowledge each batch before waiting for newer instructions.

Do not acknowledge in the hook, observer, or on receiving a send receipt.
An unacknowledged batch may be delivered again after errors or restarts.
A successful send means **durably accepted**, not read, understood, or acted
upon. See [delivery semantics](coordination.md#delivery-acknowledgement-and-retries)
for the envelope, idempotency, bounds, and error codes.

These commands are already usable without any automatic integration. Never
pretend an unsupported native API exists just to add automatic wake.

## Add a boundary hook first

Find the agent's documented root/main-session context event. Confirm what
output it accepts: plain text, a JSON field, or an in-process return value.
Register only for the owning session, not inherited child-agent events.

This is a complete runnable plain-stdout boundary adapter, exported from the
installed binary. It needs POSIX `sh`; no Python, `jq`, SDK, or download:

```sh
adapter_dir=$(mktemp -d)
/usr/local/bin/aether-internal hook file generic.sh > "$adapter_dir/aether-context.sh"
chmod +x "$adapter_dir/aether-context.sh"
"$adapter_dir/aether-context.sh"
rm -rf "$adapter_dir"
```

It invokes the real `aether-internal hook generic context` API with `{}` on
stdin. The command prints only trusted Aether guidance, or nothing when
there is none. Outside a run, a missing socket is a successful no-op so a
user-level boundary hook can remain installed. An existing but broken socket
is an actual nonzero failure, with details on stderr.

For installation, copy the exported asset to the agent's documented hook
location and register that path at its context boundary. The temporary
example above deliberately installs nothing. There is no universal config
path for an unsupported agent. If its contract requires JSON, capture the
helper's stdout and serialize it into the **documented** native context
field using the host's JSON serializer; do not shell-interpolate text into
JSON or pass plain stdout as a JSON response. Preserve unrelated hooks,
workspace trust, approval policy, and intentional disable settings.

The helper does not return peer bodies. It supplies a canonical instruction
to read `inbox`, plus applicable overlap/swarm guidance. Peer bodies remain
attributed data fetched through the inbox, never system/developer context.
Empty stdout means no added context. Keep errors on stderr or the host's
error surface, not in model context, and do not turn failures into success
hints.

Integrator swarm-refresh guidance can appear even with an empty inbox. It
asks an integrator to read durable plan decisions and worker attempts; it
does not itself schedule an OMP turn. An OMP todo reminder or native retry
can independently continue a session and then encounter this guidance.
Distinguish the initiating native event from the context added to that event;
a final prose response is not a durable swarm-state transition.

A boundary hook **cannot wake a later-idle agent**. It runs only when the
host calls it. Do not put `generic.sh` on a timer or wrap it in a background
polling loop and claim native support.

## Optional native idle wake

Add wake only if the agent exposes a supported API to start a turn in a
specific live session and lifecycle events that let you cancel on Stop,
shutdown, and session replacement. Busy-turn behavior must be documented.
Busy follow-up queues can outlive the mail they announce: a foreground inbox
read and acknowledgement cannot remove an already queued native message.
Defer automatic mail until successful native settlement and fresh wake
admission, even when the host exposes a queue API. A context filter removes
old hints from model input, not the native turn that dequeued them.
Never invent queue arguments, abort current work to deliver mail, or bypass
permission/question waits.

### Real helper API

One invocation performs one bounded, non-consuming observation:

```sh
printf '%s\n' '{"seen_message_ids":[],"wait_seconds":30}' |
  /usr/local/bin/aether-internal hook generic wake
```

The same event is supported for `pi`, `omp`, and `opencode`. Input must be one
JSON object, at most 32 KiB, with no unknown fields. `wait_seconds` defaults
to 30 and accepts 0–30. `seen_message_ids` accepts at most 100 IDs, each
1–256 ASCII letters, digits, underscores, or hyphens; omitted or null means
the empty set. Treat returned IDs as opaque. The result is a JSON object
with these four fields, not the ordinary state-command `ok/result` envelope:

| Field | Meaning |
| --- | --- |
| `wait_supported` | The server implements bounded hook observation. Never dispatch without it. |
| `unread_message_ids` | Current unacknowledged IDs; no bodies or acknowledgement token. |
| `wake_admitted` | The server accepted this wake response under current input authority. |
| `context` | Trusted inbox-only instruction when admitted; empty otherwise. |

The command does **not** itself call an agent API or wake a model. Calling
it by hand proves only helper transport, not native integration. Ordinary
`hook generic context` is also not an eligibility check for later dispatch.

A missing socket is an error for `wake`, unlike the boundary no-op. Exit 2
means invalid input or unsupported wake protocol: stop the receiver, surface
the error, and require a compatible server/integration rather than retrying
immediately. Exit 3 denotes denied/conflicting/invalid state; exit 4 means
the run was not found. Exit 1 includes transport/internal failures and
temporary unavailability, including a missing socket or rate limit. Retry
only recoverable failures with bounded backoff and a finite retry limit.
Preserve stderr; do not retry malformed responses or an unsupported API
forever. Cancel the child process when the receiver no longer owns the
session. Do not leave a 30-second helper running after shutdown. These
`wake` exit rules differ from ordinary state-command error envelopes.

### Keep observation and notification separate

Maintain two in-memory sets per owning session:

- **Observed IDs:** the last supported response's current unread set. Send
  this as `seen_message_ids` on the next wait, even if wake was suppressed.
  Compare membership, not count or lexical order.
- **Notified IDs:** unread IDs accepted by the native follow-up API. Prune IDs
  absent from each new unread set. A helper response alone is not
  notification, and a rejected native call must not leave IDs marked notified.

For every supported response, update observed IDs and prune notified IDs.
Dispatch one coalesced trusted hint only when `wake_admitted` is true and at
least one current ID has not been notified. Recheck the session generation
and Stop/approval state immediately before the native call. If local state
prevents dispatch, do not mark those IDs notified. Let the next bounded wait
reconsider them when the host resumes.

For pi/OMP's synchronous `sendMessage`, reserve coalescing/readiness state
before the call because it can invoke lifecycle callbacks reentrantly.
Commit the captured ID set only after normal return and while the original
root/session still owns it. A throw rolls back only the reservation still
owned by that generation, preserving any newer Stop or session transition;
surface the error and end that receiver pass without a permanent helper-fatal
halt. Do not blindly resend or apply a native-send retry timer. Later eligible
native activity - OMP context/agent/approval boundaries or pi successful idle
settlement/manual compaction - can request fresh helper/server admission for
the same unread IDs. Stop still requires accepted human input to resume.
OMP uses its owning public session's terminal `agent_end` and a detached
`waitForIdle()` drain, then rechecks `isIdle()`; extension `agent_end` with
`willContinue` is not settlement. Pi uses its separate `agent_settled`
contract. Neither starts new wake observations after busy context calls.
Every newly eligible idle period begins with `wait_seconds: 0`, so already
observed unread IDs get fresh admission without another full wait.
Normal return is SDK acceptance, not proof of model receipt or acknowledgement.

OpenCode's asynchronous prompt APIs reserve newly eligible IDs before awaiting
acceptance: native busy/completion events can arrive before the promise settles.
A rejection releases only that request's reservations, never previously accepted
IDs or a newer request's reservation. The receiver remains fail-closed on a
current request's failure; a later human prompt and successful completion can
obtain fresh admission for the same unread mail. Stop, root ownership, and
generation checks still fence delayed prompt hooks. There is no immediate
native-prompt retry.

This distinction matters when mail arrives during protection or takeover:
the receiver observes it without notifying. An unchanged set can later be
admitted after the hold is released. Conversely, repeated admitted responses
for a batch already notified must not cause endless turns. A process/session
restart can cause one duplicate hint; it must not lose mail or auto-ack it.
No durable cursor or revision database is needed.

### Ownership and authoritative admission

Use one receiver for the owning root session. Do not let child or sibling
sessions steal the run mailbox. Increment a local session generation on
replacement; cancel the previous helper and discard every late response from
that generation. Duplicate extension loading must supersede/cancel the old
receiver, not spawn a second watcher. Intentional human Stop/abort must stop
the receiver and clear any locally pending automatic turn; resume only at the
host's documented user-resume boundary. Do not use a generic completion event
as proof that an aborted turn should restart.

The server is the authority for run and swarm eligibility. It rechecks the
current TUI run, protection, closing/exited state, current swarm assignment,
revision/generation, takeover hold, and submitted/settled attempt state.
An inbox consumer registered before the final wake-frame dispatch takes
precedence. A receiver does not derive permission from old `status` output or
from the fact that IDs exist.

The response containing `wake_admitted: true` is dispatched inside the same
per-run control admission boundary as human input, with at most three seconds
for the response write. Swarm revision/generation mutations and inbox
consumer registration are serialized with that complete frame too. The
server holds none of these admission boundaries during the long wait.
If a hold or earlier inbox consumer wins admission, no wake is dispatched.
If the frame is accepted first, a later hold or consumer cannot revoke that
accepted input; the native turn may still process it. Native API acceptance
does not prove model receipt or acknowledgement.
Swarm authorization and per-run control admission are nonblocking for
wake. Contention returns a non-admitted observation, not a queued dispatch:
update observed IDs without adding notified IDs, then let the next bounded
wait reconsider eligibility.

There is no terminal fallback, arbitrary TUI attachment, headless restart,
or automatic relaunch of a closed run. Durable mail remains available to
explicit inbox reads when automation is unavailable.

### Run a complete native reference

The shipped sources are complete, copyable reference implementations, not
SDK pseudocode: [pi](../internal/coordhooks/pi.ts),
[OMP](../internal/coordhooks/omp.ts), [OpenCode V1](../internal/coordhooks/opencode-v1.js),
and [OpenCode V2](../internal/coordhooks/opencode-v2.js). For example, in a
coordinated run with **native pi 0.87.1** installed and extensions intentionally
enabled, use this manual invocation instead of an already-loaded Aether
inbox extension:

```sh
extension_dir=$(mktemp -d)
/usr/local/bin/aether-internal hook file pi.ts > "$extension_dir/aether.ts"
pi -e "$extension_dir/aether.ts"
rm -rf "$extension_dir"
```

Do not use explicit `-e` to circumvent `--no-extensions` or resource
exclusions. Do not run a second root host alongside the run's existing owner.
For OMP 18.3.1, export `omp.ts` instead and launch
`omp -e "$extension_dir"`: its directory loader honors
`disabledExtensions: [extension-module:aether]`, unlike explicit-file `-e`.
The version-specific OpenCode files use different APIs; see
[their setup requirements](harnesses.md#opencode-v1-inbox-integration) rather
than transplanting pi's `sendMessage` call into another host.

## Prove activation and failure behavior

A copied file, a successful `skill` check, a status reporter event, or a
standalone helper response is not evidence that a model turn ran. Verify
with the actual supported agent in an isolated coordinated run:

1. Check the installed CLI version; inspect native hook/plugin load errors
   and normal trust/disable state. Exercise a real root context event.
2. Have an authorized peer send a message while `inbox --wait 30` is active.
   Confirm the existing tool call receives the attributed body/token without
   an extra native turn. Read without `--ack` and confirm redelivery.
3. After a normal turn completes, send another message. A native integration
   must visibly begin a new turn through its API, with no stdin write. A
   boundary-only integration must wait until its next documented event.
4. Leave that batch unacknowledged across another observer response: it must
   not cause repeated native turns. Acknowledge after processing and check
   that a subsequent new message still wakes correctly.
5. Exercise busy work, approval waits, human Stop, session replacement,
   protection/takeover and release, and process exit. No stale root receives
   a turn; suppressed mail remains readable; exited processes stay exited.
6. Test unavailable/unsupported helper responses and real transport failure.
   Errors must be visible without unhandled background exceptions, rapid
   retry loops, leaked helper processes, automatic ack, or terminal fallback.

Keep credentials and runtime state out of checked-in examples and fixtures.
An agent profile, boundary adapter, and native receiver are separate support
claims; document only the ones the real agent can satisfy.
