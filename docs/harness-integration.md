# Integrating an unsupported agent

This guide connects an agent CLI that Aether does not ship to Aether's durable
mailbox. A **run** is one agent session that Aether launches in a container.
Other runs and people send it messages, which Aether stores until the agent reads
them. Read [Agents](harnesses.md#custom-and-unlisted-agents) first to register
the CLI as a custom agent. For a shipped agent, you do not need this guide.

The levels build on each other. Stop at the first one that is enough.

1. Explicit inbox reads. Works for any agent with no integration.
2. A boundary hook, which tells the agent about mail at a moment the CLI chooses.
3. Native idle wake, which starts a turn in a session that is waiting.

Native tools run in the agent's container in every mode. Enhanced mode does not
move them elsewhere ([Enhanced runs](enhanced-runs.md#limits)).

## Get a working run

Every managed run mounts `/usr/local/bin/aether-internal` and its own socket,
`/run/aether/coord3.sock`. The socket identifies the run, so the CLI needs no
run ID, token or credential. Member terminals get the CLI but no socket, so the
commands below work only inside a run.

1. Register the agent:

   ```sh
   aether agent add <name> --standard "<cmd> {task}" --background "<cmd> -p {task}"
   ```

2. Install the executable into `~/.local/bin` and sign in
   ([Agents](harnesses.md#set-up-an-agent)).
3. Launch it with `aether run "<task>" --agent <name>`.

Aether gives the agent its [instructions](coordination.md#agent-instructions) after
the task prompt. A launch with no task gets none, so tell the agent's own startup
mechanism to run `aether-internal skill` and `aether-internal memory brief`.

## Explicit inbox reads

Inside a coordinated run:

```sh
/usr/local/bin/aether-internal skill
/usr/local/bin/aether-internal status
/usr/local/bin/aether-internal inbox --wait 30
```

`skill` loads the role and assignment. `status` lists the run's peers and what it
may do. `inbox --wait 30` is one server-side wait, not a polling loop. It returns
the messages and an `ack_token`. The agent's next `inbox` read acknowledges that
batch; to acknowledge it without reading on:

```sh
/usr/local/bin/aether-internal ack <ack_token>
```

Until acknowledged, the batch stays frozen and new mail, including mail from
people, waits behind it. An unacknowledged batch can be delivered again after an
error or restart. A successful send means the message is stored, not that the
other agent read it ([delivery](coordination.md#delivery-acknowledgement-and-retries)).
Never run `inbox` or `ack` from a hook, since a read acknowledges the agent's
last batch, and never acknowledge on a send receipt.

## Add a boundary hook

A hook announces mail when the CLI reaches an event you choose. Find the agent's
root-session context event and what output it accepts: plain text, a JSON field or
an in-process return value. Register it only for the main session, not child
agents.

Export a ready-made adapter from the installed binary. It needs POSIX `sh`:

```sh
adapter_dir=$(mktemp -d)
/usr/local/bin/aether-internal hook file generic.sh > "$adapter_dir/aether-context.sh"
chmod +x "$adapter_dir/aether-context.sh"
"$adapter_dir/aether-context.sh"
rm -rf "$adapter_dir"
```

It calls `aether-internal hook generic context` and prints only Aether's own
guidance: an instruction to read the inbox, plus any overlap or swarm notes. It
prints nothing when there is no mail. Peer message bodies are never in the output.
They stay in the inbox as attributed data.

- Outside a run, a missing socket is a successful no-op, so a user-level hook can
  stay installed. A broken socket exits nonzero with the reason on stderr.
- Copy the script to the agent's documented hook location and register it at the
  context event. There is no universal path.
- If the CLI needs JSON, capture the script's stdout and put it in the documented
  field with a JSON serializer. Do not interpolate text into JSON.
- Keep errors on stderr, not in the model's context.
- Preserve the agent's other hooks, workspace trust, approval policy and any
  setting the user has turned off.

A hook runs only when the agent calls it, so it cannot wake an idle agent. Do not
run it on a timer.

## Add native idle wake

Add wake only if the agent has a supported API to start a turn in a specific live
session, and lifecycle events that let you cancel on Stop, shutdown and session
replacement. Do not write to the agent's terminal. Aether has no terminal-input
fallback.

One call performs one bounded, non-consuming check of the mailbox:

```sh
printf '%s\n' '{"seen_message_ids":[],"wait_seconds":30}' |
  /usr/local/bin/aether-internal hook generic wake
```

The input is one JSON object of at most 32 KiB. `wait_seconds` defaults to 30 and
accepts 0 to 30. `seen_message_ids` holds at most 100 IDs of 1 to 256 letters,
digits, underscores or hyphens. The output is a JSON object:

| Field | Meaning |
| --- | --- |
| `wait_supported` | The server supports the wait. Never dispatch without it |
| `unread_message_ids` | Current unacknowledged IDs, with no bodies |
| `wake_admitted` | The server allows a wake right now |
| `context` | A trusted instruction to read the inbox. Empty unless admitted |

The command does not wake a model itself. Your receiver sends `context` to the
agent through its native API. The exit codes:

| Exit | Meaning | What to do |
| --- | --- | --- |
| 0 | A response | Read the fields |
| 1 | Transport failure, missing socket, rate limit | Retry with bounded backoff and a finite limit |
| 2 | Invalid input or unsupported protocol | Stop the receiver and show the error |
| 3 | Denied, conflicting or invalid state | Stop and show the error |
| 4 | The run was not found | Stop |

The same `wake` event also works for `pi`, `omp` and `opencode`.

The receiver follows these rules:

- Keep two in-memory sets per session. **Observed IDs** are the unread IDs from the
  last response; send them as `seen_message_ids` on the next call. **Notified
  IDs** are the unread IDs the native API accepted. A rejected native call must
  not leave IDs marked notified.
- Dispatch one hint only when `wake_admitted` is true and at least one current ID
  is not yet notified. Check the session and the user's Stop and approval state
  immediately before the call.
- Use one receiver for the root session. Replacing a session cancels the old
  receiver and discards late responses. Children never take the mailbox.
- A human Stop stops the receiver until the host's documented user-resume event.
- Do not wake during an approval wait or a busy turn. Wait for successful
  settlement, then start the next check with `wait_seconds: 0`.
- The server decides eligibility (protection, takeover, swarm state). A hold the
  server applies later cannot revoke a wake it already accepted.
- A restart can cause one duplicate hint. It must not lose mail or acknowledge it.

Working references: [pi](../internal/coordhooks/pi.ts),
[oh-my-pi](../internal/coordhooks/omp.ts),
[OpenCode V1](../internal/coordhooks/opencode-v1.js) and
[OpenCode V2](../internal/coordhooks/opencode-v2.js). Do not copy pi's
`sendMessage` call into another host. Each host's API differs.

## Check that it works

A copied file, a `skill` check or a helper response does not prove a model turn ran.
Test with the real agent in a coordinated run:

1. Check the CLI version and its hook or plugin load errors and trust state.
2. Have a peer send a message while `inbox --wait 30` is active. The call should
   return the body and token. Read again without acknowledging and confirm the
   batch repeats.
3. After a turn completes, send another message. A native integration starts a
   turn with no terminal write. A boundary-only integration waits for its next event.
4. Leave a batch unacknowledged across another check. It must not start repeated
   turns. After acknowledging, a new message must wake the agent again.
5. Try a busy turn, an approval wait, a human Stop, session replacement,
   protection or takeover, and process exit. No stale receiver wakes anything, and
   suppressed mail stays readable.
6. Break the helper on purpose. Errors must show, with no retry loop, leaked
   helper process, automatic acknowledgement or terminal fallback.

## Limits

- Hooks and wake apply only to Standard runs with a task. Background runs and
  taskless launches get neither.
- A Background or exited process is never restarted by mail.
- Keep credentials and run state out of checked-in examples and tests.
