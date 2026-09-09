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

In the dashboard, open the terminal dock on the run board. The dock starts
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
New runs and workspace shells use the saved image; reset stops the terminal,
removes the saved image, and makes the next open use the standard image. See
[environments.md](environments.md) for image selection and persistence.

## Keys

These work in every terminal Aether draws - the environment dock, a run's
terminal, and a run shell - and only in the terminal that has focus, because
the terminal itself claims them before the shell sees them.

| Key | What it does |
| --- | --- |
| `Ctrl+Shift+C` | Copy the selection. A plain `Ctrl+C` copies too when text is selected, and interrupts when none is. |
| `Ctrl+Shift+V` | Paste. Plain `Ctrl+V` works as well. |
| `Ctrl+Shift+F` | Open the find bar. `Enter` goes to the next match, `Shift+Enter` back, `Esc` closes it. |
| `Ctrl+=` / `Ctrl+-` | Grow or shrink the terminal font, 8px to 32px. `Ctrl+Shift+=` grows too, since that is how a keyboard without a numpad types `Ctrl++`. |
| `Ctrl+0` | Back to the default 12px. |

`Cmd`, or the `Super`/`Windows` key, works as well as `Ctrl` for the zoom keys.
The font size is one preference across every terminal and survives a reload;
find searches the scrollback of the terminal it was opened in.

Both shifted forms are left to the shell; `Ctrl+Shift+-` is `Ctrl+_`,
readline's undo and vim's keymap switch.

The zoom keys are also a browser's own page-zoom accelerators. The terminal
cancels the key, and the desktop app binds no competing zoom. Where a browser
keeps the accelerator for itself, the page zooms as well; use the desktop app
if that gets in the way.

## Tabs and lifecycle

There is one environment container per member. `main` is its login shell. Other
tabs run another login shell in the same container and use names such as `t2`.
A member may have at most six tabs. A shell that exits closes its tab; opening
the environment again recreates the container if it stopped.

Stopping the environment stops its container and all tab processes. The member
home is not deleted. The next CLI or dashboard open starts a new container with
the same home.
