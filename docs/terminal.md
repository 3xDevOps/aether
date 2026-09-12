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
identifies the replay byte count, so the dashboard mutes terminal-generated
replies until that scrollback is parsed. Closing a tab only detaches it;
opening that tab again reattaches to its shell.

`aether attach` mutes the same window, and does it by discarding: keystrokes
that arrive before the announced replay has been written to your terminal are
dropped, not deferred. A terminal answers the device-attribute and colour
queries the replayed scrollback still carries, and those answers reach the
server on the channel keystrokes use, where they would count as steering the
run. Anything typed - or piped on stdin - in that window goes with them, without
a message. The client has no other lever: the server decides what counts as
typing.

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
reflowed for the people watching it on a desktop. A run you own opens there as
a read-only mirror, where on a desktop it would already be steering; **Take
control** is a tap. While it is a mirror the terminal takes no input, so
tapping it does not raise the keyboard.

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

Taking control and handing it back keep the screen you are looking at:
the terminal reattaches with different permissions rather than redrawing
its scrollback, so the transition shows nothing beyond the button
changing.

### Reattaching after an update

Desktop terminals draw at the shared PTY's size, not independently at each
window's width. A smaller writer can reduce that grid; other viewers adopt
the resulting size without reporting it back as their own window size.
Fresh viewers request a redraw even when they cannot resize the session.

After a server restart, Aether reconstructs tracked terminal modes from the
recorded output, including bracketed paste, and carries them into the next
transcript. This helps surviving runs as well as new runs; it does not require
restarting an agent just because it predates the update.

Replay is still a bounded output tail, not a complete terminal screen
snapshot. Historical cursor-positioned output can have missing context or
have been drawn at another size. A live agent's resize-triggered redraw can
restore its current screen, but Aether cannot reconstruct missing historical
state or output that was never flushed before a crash. A finished run has no
live process to redraw that history.

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
