# Environment terminal

The environment terminal is a persistent shell for your member account. It runs
in one server-side container with your member home mounted at `$HOME`, so files,
executables in `~/.local/bin`, and vendor login state survive reconnects and
new runs.

## Open it

From the CLI:

```sh
aether terminal
aether terminal --tab t2
aether terminal status
aether terminal stop
```

In the dashboard, open the terminal dock on the Board view. The dock starts
collapsed, so the board keeps the window; the chevron in its header strip opens
it, and so does `+` or a tab in that strip. It stays open until you reload the
page. The first open starts the environment; the dock says **Starting your
environment container** until the shell attaches, and shows the server's own
error if the start fails. Later tabs and tab switches reach a container that is
already up, so those say **Connecting to your environment**. The dock reconnects
and replays terminal output when the page or network reconnects. The stream ack
names the exact replay byte count, including a boundary that falls inside one
WebSocket frame. As each frame-sized replay operation arrives, the dashboard
splits only the boundary-crossing frame and starts one serial xterm write chain
behind the CSS-hidden surface; it does not retain the replay until the full
boundary arrives or allocate a browser-sized buffer from the declared length.
Only the slice containing the exact final replay byte is tagged `replay-end`;
live output and geometry stay in wire order behind xterm's write backpressure.
Terminal-generated replies and user input stay muted from the ack through the
final replay write callback. After that callback, two `requestAnimationFrame`
turns let the xterm DOM paint the settled state before the surface is revealed.
Closing a tab only detaches it; opening that tab again reattaches to its shell.
When a persistent dock host is replaced, `rebind` cancels the old replay drain
with its cancellation signal, drops the old socket, installs the new handlers,
and starts one fresh full replay after cancellation; the dock does not issue a
second reopen. If an `online` wake interrupts an incomplete replay, the client
cancels its parser and drain with the same cancellation signal, drops that
socket while keeping the terminal hidden, and reconnects for a fresh hidden
replay rather than revealing a partial history prefix. A final replacement
refusal settles the replay gate before its server error is shown.

## Run control and the Run Room

A run has one controller session at a time. A member needs the run's **Steer**
permission to acquire it. Viewing, presence, and run ownership by themselves do
not grant input access. A writable dashboard terminal, `aether attach`, or run
shell must acquire the controller lease; read-only attaches may coexist. A
second tab or connection from the same member is a different session, not a
second writer.

On a desktop, the owner's first run-terminal attach asks for control
automatically. The server grants it only when the run is unoccupied. Other
members start as read-only mirrors. A write request that cannot acquire the
lease is refused rather than silently becoming a second writer. `aether attach`
asks for control by default; use `aether attach --read-only <run>` to watch
deliberately. An occupied attach does not silently displace the current
controller. Open the Run Room and confirm **Take control** to perform an
occupied takeover. The confirmation names the current controller; takeover ends
that writable session and notifies it.

A run shell that cannot acquire the occupied lease remains connected as a
read-only mirror. Its terminal input and mobile key bar stay disabled; use the
dock's **Take shell control** action for an explicit takeover.

Control is tied to the logical terminal session. When a controller disconnects,
the server holds its lease for a **15-second reconnect window**. The same tab or
connection session can reclaim the lease during that window. The disconnected
session cannot write while it is away. A different tab cannot inherit it without
an explicit takeover, and after the window expires a normal acquisition can win.
**Control changes are acknowledged on the existing attach WebSocket.** A
dashboard terminal sends a text frame such as
`{"type":"control","request_id":17,"write":true}`. It may include
`"takeover":true` for an explicit Run Room takeover and the current
`"control_generation"` fence. The server answers on that same stream with
`{"type":"control","request_id":17,"ok":true,"has_control":true,
"control_session_id":"...","control_generation":8}`. Every result includes
`has_control`, even when false; refusals also carry `code` and `error`.
A refused duplicate acquisition does not revoke a lease the session still owns.
An unsolicited lease revocation is also a
`type:"control"` frame, but has no `request_id`; it reports the exact revoked
generation, and the displaced interactive session remains a read-only mirror.
The same-socket notification is used when an interactive attach loses **Steer**:
the terminal stays open and input is disabled. Raw legacy attaches retain the
named close (`1008`, `steer permission withdrawn`) instead. Input frames carry
the current `control_generation`, and stale input is rejected. Taking or
releasing control therefore does not reconnect or replay the terminal.
The terminal remains at its current screen while the lease changes. A
successful takeover fences the old writer; its input is rejected and its
session becomes a read-only mirror. Invalid, stale, or cross-member requests
are refused without changing the current writer.

Only the run owner or an administrator can enable protection. Enabling
protection immediately fences the current controller and cancels every queued
Run Room steer request. While protected, non-owner steering is refused and the
owner or an administrator must acquire control again before typing. Disabling
protection does not restore a controller or a cancelled request.

The **Run Room** is the collaboration surface for this run. It starts as a
collapsed vertical tab on the right of the terminal. The tab count includes
unanswered questions and queued steer requests. Opening it loads the durable,
per-run attributed message timeline and the current watcher and controller
status. Presence is a live view of attached members and does not decide who
may type.

**Comment** writes to that timeline for people watching the run and never sends
anything to the agent. **Send to agent** creates a steer request. A request
made by the current controller session, with its current lease, is eligible for
immediate PTY delivery. A request from another session, or from a run with no
current controller, is queued with a **45-second countdown**. The current
controller can choose **Approve now** or **Deny** during the countdown. When
the timer expires, Aether attempts delivery and records the result as **Sent**
(`sent`), **Not sent** (`not_sent`), or **Delivery uncertain** (`uncertain`).
**Sent** means the PTY write was accepted; it does not mean that the agent read
or answered the request. Retries use the
same message identity, so they do not create a second request.

Questions appear in the Run Room where they apply. **Answer** posts a
correlated reply. The server includes each run's unanswered-question count in
the normal run snapshot, so a fresh dashboard places that run in **Needs you**
before anyone opens its room. The card names the run owner and points to the
Run Room as the action. Questions and queued steers do not create a second
action inbox.

Room image attachments use the terminal upload rules: each message may include
up to eight actual PNG, JPEG, GIF, or WebP files, each no larger than 8 MiB.
Aether validates and stores the bytes in the target account's persistent home,
then records the generated container-visible
path with the room message. A local filesystem path from clipboard text is not
an upload. An attachment is a persistent file reference, not a second input
channel; steering still reaches every supported harness as serialized text
written to its run PTY. Aether does not require or provide a universal inbound
harness hook.

On a desktop the open Run Room is a right-side panel up to 420px wide. On a
phone it becomes a full-viewport sheet. The phone opens the run terminal as a
read-only mirror, including a run owned by that member; tap **Take control**
before the keyboard can send input. Phone terminals follow the already
acknowledged PTY size and do not resize the shared session.

`aether attach` remains a raw terminal stream: it consumes exactly the replay
byte count announced by the ack before treating following bytes as live. It
does not use the dashboard's segmented, ordered xterm transaction or hidden
surface. A writable CLI attach mutes its input by discarding keystrokes that
arrive before the announced replay has been written to your terminal; they are
dropped, not deferred.
Dashboard xterm may answer device-attribute and colour queries encountered while
parsing replay, but the replay gate mutes those terminal-generated replies along
with user input until the final replay callback; they do not reach the server.
The CLI is raw and has no xterm parser, so the announced replay and input
discard rules above remain in force.

The Agents setup step uses the same dock and types the install command for you.
Complete the vendor login there, then return to the wizard. Its **I've
installed and logged in** button runs `env save` for you, so that step does
not need the **Save environment** button below.

That step's **Connect GitHub** button opens the same dock and types the `gh
auth login` command instead. Finish the device login in your browser, then
**I've logged in** runs the rest of the connection and reports the account
and the signing key it registered. See
[environment-home.md](environment-home.md#connect-github).

## Sign in to agents

In the dashboard, click the OAuth URL printed in the terminal. For loopback
callbacks, Aether starts the local forward before it opens the authorization
page. In the CLI, run `aether forward terminal <callback-port>` before
completing the browser flow. Device-code logins need no forward, `gh auth
login --web` among them: gh prints a one-time code and a URL you open on
your own machine, and nothing calls back to the terminal. Login state under
your home is available to later runs without saving the environment.

## Save your environment

Install system tools and toolchains in this terminal, then select **Save
environment** in the terminal dock. The same actions are available from the
CLI:

```sh
aether env save
aether env reset
```

Saving pauses the terminal for the few seconds Docker needs to commit it.
New runs and workspace shells use the saved image; `aether env reset` stops
the terminal, removes that image, and makes the next open use the standard
image. See [environments.md](environments.md) for image selection and
persistence.

The dock splits stopping from resetting, each behind its own confirmation.
**Stop environment** does what `aether terminal stop` does: the container
stops and both your home files and your saved image remain, so a later open
starts it again. **Reset to standard** does what `aether env reset` does,
and is offered only once you have a saved image to discard. A failure is
reported in the dialog that caused it.

## Keys

These work in every terminal Aether draws - the environment dock, a run's
terminal, and a run shell - and only in the terminal that has focus, because
the terminal itself claims them before the shell sees them.

| Key | What it does |
| --- | --- |
| `Ctrl+Shift+C` | Copy the selection. A plain `Ctrl+C` copies too when text is selected, and interrupts when none is. |
| `Ctrl+Shift+V` | Native paste on Windows and Linux. Plain `Ctrl+V` is native too; on macOS use native `Cmd+V`. |
| `Ctrl+Shift+F` | Open the find bar. `Enter` goes to the next match, `Shift+Enter` back, `Esc` closes it. |
| `Ctrl+=` / `Ctrl+-` | Grow or shrink the terminal font, 8px to 32px. `Ctrl+Shift+=` grows too, since that is how a keyboard without a numpad types `Ctrl++`. |
| `Ctrl+0` | Back to the default 12px. |

`Cmd`, or the `Super`/`Windows` key, works as well as `Ctrl` for the zoom
keys. The font size is one preference across every terminal and survives a
reload; find searches the scrollback of the terminal it was opened in.
`Ctrl+Shift+=` is accepted for zoom, but `Ctrl+Shift+-` remains the shell's
`Ctrl+_` (readline undo or a vim keymap switch), as does a shifted `Ctrl+0`.
The zoom shortcuts normally cancel the browser's own page zoom; if a browser
keeps its accelerator, the page may zoom too.

Native paste is deliberately left to xterm and the browser for those keyboard
shortcuts; it does not depend on the asynchronous `navigator.clipboard` API.
That matters on Windows when clipboard-read permission is denied. When image
upload is enabled, a native paste containing actual image file data is handled
by the terminal's image paste listener; ordinary text remains native terminal
input. On macOS, `Cmd+V` is the native paste shortcut.

A paste reaches the agent as one block rather than as the Enter presses
its newlines would otherwise be, because the terminal is told the session
has bracketed paste on. An agent sets that mode once when it starts, long
before you open the terminal, so the server tracks it - along with the
cursor, autowrap, and mouse reporting - and restores it ahead of the
replay. Without that, a terminal opened hours into a run would disagree
with the agent about how a paste arrives, and only the agent could tell.

The terminal toolbar has named **Copy terminal selection**, **Copy last
screen**, **Paste into terminal**, and **Upload image to terminal** controls.
**Copy last screen** copies the rows currently on screen, which is how to copy
without a drag selection - a touch screen has none. On a touch screen both
copy controls carry their name beside them, since the tooltip that tells
them apart needs a pointer to hover. Copying an empty screen says so.
The toolbar's Paste control first uses the browser clipboard API to look for
an image and then falls back to text. If no usable clipboard read API
remains, or its reads are denied, it shows a visible **Paste unavailable**
error telling you to use the native paste shortcut or allow clipboard
access. Copy uses the same API with a selection fallback; if both routes
fail it shows **Copy unavailable** rather than silently dropping the copy.

### On a touch screen

A soft keyboard has no Esc, Tab, Ctrl or arrows, and an agent TUI needs all
four. Below the terminal, whenever it accepts input, is a key bar: **Ctrl**,
**Esc**, **Tab**, the four arrows, **Enter** and **Ctrl+C**. They send exactly
what those keys send. **Ctrl** is a modifier rather than a held key: tap it,
then type or tap one more key, and that key arrives as its control code -
`Ctrl` then `d` is `Ctrl+D`. It covers the letters, space, and `@ [ \ ] ^ _`,
applies to one key and then releases; a key it does not cover is sent as
itself and leaves **Ctrl** armed for the next one.

A run's terminal is sized by the people steering it: the PTY is the
smallest window among them, so nobody's screen is reflowed past what it
can show. Watching it read-only imposes nothing - except when you are the
only one attached, where there is no other screen to protect and the
terminal follows your window the way `ssh` does. A second attach ends
that, and the size is recomputed without you.

On a phone every terminal here - a run's, its shells, and this one - shows
the session at the size it already is rather than at the phone's own width,
and pans across it. It follows that size: when someone with a bigger screen
resizes the terminal, the phone redraws at the new one. Nothing a phone does
changes that size, watching or steering, so an agent's screen is never
reflowed by a phone.

On a phone every run - including one you own - opens as a read-only mirror,
where on a desktop an unoccupied owner's first attach may already have
acquired control. **Take control** is a tap. While it is a mirror the terminal
takes no input, so tapping it does not raise the keyboard.

### Paste or upload an image

An image paste must contain the actual file bytes. If the clipboard provides a
PNG, JPEG, GIF, or WebP file (including a disk-backed clipboard image), native
paste or the toolbar's **Paste into terminal** opens **Upload image to
terminal** with a preview. Select **Upload and insert** to send it. In a
browser or desktop app on Windows, macOS, or Linux, the **Upload image to
terminal** toolbar control opens the platform's file chooser directly; use
**Choose another image** in the dialog to replace a selection. The chooser is
the fallback when the clipboard exposes no image bytes.

The client accepts PNG, JPEG, GIF, and WebP files up to 8 MiB. The server
validates the decoded bytes and format again, so a misleading filename or MIME
type is not enough. A rejected selection stays in the dialog with the actual
validation error; server or connection failures appear as **Upload failed:**
with the server's detail. A native image-paste failure is shown as **Pasting
image failed:** with its detail.

Some clipboard managers expose only a path such as
`C:\Screenshots\shot.png` or `/home/me/shot.png`. That is text, not an image
file: a remote container cannot read a path on your local OS, and a browser
cannot auto-read arbitrary local paths. Choose the actual file in the chooser
instead. The server stores the selected bytes and returns a remote absolute
path; Aether inserts that path with shell quoting and does **not** press
Enter. Review or edit it, then press Enter yourself when it is ready.

Changing away from a run terminal no longer destroys its primary terminal
immediately. A recently visited terminal remains in browser memory, so returning
shows its parsed current screen and bounded scrollback at once. While it is
inactive, its socket closes intentionally: you are no longer **Watching**, and
it has no active control transport or geometry participation. The retained
surface is only the TerminalPane/xterm; the RunHeader, run tabs, actions, Run
Dock, and Run Room unmount while inactive, so their fixed IDs and auxiliary
resources are unique to the active route. This cache is not persistent across a
reload or browser tab.

On return, the dashboard reconnects with `resume` only when it retained the
server's nonempty `resume_id` for that PTY incarnation, and sends that ID with
the xterm-settled cursor. A valid same-incarnation resume supplies only the
bounded gap and keeps the current screen. If the ring, geometry, cursor, or
incarnation fence cannot serve that gap, the server sends a compact current
screen instead; the dashboard replaces the hidden surface and does not replay
the archive. Writes that settle while parked refresh the cache's normal and
alternate buffer weights. A completed entry whose session ended does not
reconnect unless that same run is relaunched; that transition records a refresh
while parked and performs a fresh bootstrap once active.

### Current screen and complete terminal history

A dashboard run attach requests `screen:true` and `interactive:true`. Its
bootstrap is a compact VT snapshot of the current viewport, cursor, modes,
colours, and alternate buffer, not every recorded byte from the run. The
browser keeps bounded live scrollback: a normal run requests up to 5,000 rows
and then adapts the normal and alternate buffers to the acknowledged geometry's
cell limit. A live, fallback, or finished run therefore opens at the current
screen rather than showing a historical timelapse. Input and terminal-generated
replies stay muted until the hidden snapshot is parsed and the settled surface
is revealed.

The complete archive is a separate operation. Use the dashboard's **Download
full terminal history** action, which makes an authenticated
`GET /api/runs/{run}/terminal-history` request and downloads the raw ANSI bytes
from all retained cast incarnations. It does not control or alter the PTY.
`aether attach` remains the raw CLI stream: it requests `screen:false`,
consumes the ack-declared replay byte count, and then treats following bytes as
live. CLI output is complete raw history, not the dashboard's compact snapshot.
The CLI's pre-replay input-discard rule remains unchanged.

### Reattaching after an update

Desktop terminals draw at the shared PTY's size, not independently at each
window's width. A smaller writer can reduce that grid; other viewers adopt that
size without reporting it back as their own window size. Fresh viewers can
request a redraw nudge, but the compact bootstrap keeps that redraw hidden
until the settled current screen is ready.

The current-screen checkpoint lives beside the transcript in the runtime
transcript directory as a versioned `<transcript>.screen` record. A checkpoint
stores the compact VT snapshot, its columns and rows, the cast incarnation ID
and byte offset, and segment byte/output counts. It is replaced atomically, so
a restart can resume from a complete record. A missing, invalid, or corrupt
checkpoint is not trusted: Aether reconstructs the screen safely from the
recorded cast output and resize events. If output was never persisted, it is
unavailable.

Starting a new PTY incarnation preserves the previous non-empty cast as an
old, timestamped artifact instead of truncating it. The current-screen
checkpoint is only a fast bootstrap; it is not the archive. The history
download and raw CLI replay can still read all retained cast incarnations,
subject to the run's retained-artifact lifecycle.

### Control availability

**Upload image to terminal** is disabled until the selected terminal has a
live attach and write permission. In a run's agent terminal, a read-only
mirror, a `Connecting`, `Reconnecting`, or `Offline` state, a starting run,
or a server-denied **Take control** state leaves image upload disabled. Take
control first when the run is steerable. A run that is finished or otherwise
not running says **This run is not running** beside its disabled control.
The run shell can open only for a `running` or `needs-attention` run whose
pause state is known and unpaused; otherwise its unavailable panel says
**Run shell unavailable: this run has no live container. The Terminal tab
replays its recorded output.** A shell that is refused says **You can view this
run but not open a shell in it**.
The environment dock shows **Starting your environment container** for its
first open and **Connecting to your environment** for a later attach; a
startup or attach failure displays the gateway's own error. These states do
not turn a read-only transcript into a writable terminal.

## Tabs and lifecycle

There is one environment container per member. `main` is its login shell. Other
tabs run another login shell in the same container and use names such as `t2`.
A member may have at most six tabs. A shell that exits closes its tab; opening
the environment again recreates the container if it stopped.

Stopping the environment stops its container and all tab processes. The member
home is not deleted. The next CLI or dashboard open starts a new container with
the same home.

Uploaded images are kept in the target account's persistent member home under
`$HOME/.aether/terminal-images/`. The server generates a name such as
`image-<random>.png`, writes the original bytes with private permissions, and
returns the absolute path visible inside that target container. The path is
not a local OS path, a workspace checkout path, or part of the Docker saved
environment image. Stopping the environment, recreating its container, or
running `aether env reset` leaves the member home (and these images) intact;
there is no automatic image cleanup.

To remove one known image from a terminal, substitute the exact generated
filename and run:

```sh
rm -- "$HOME/.aether/terminal-images/image-<random>.<ext>"
```

Do not replace the filename with a wildcard.
