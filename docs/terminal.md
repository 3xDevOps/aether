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

In the dashboard, open the terminal dock on the run board. The first open starts
the environment. The dock reconnects and replays terminal output when the page
or network reconnects. Closing a tab only detaches it; opening that tab again
reattaches to its shell.

The Agents setup step uses the same dock and types the install command for you.
Complete the vendor login there, then return to the wizard.

## Tabs and lifecycle

There is one environment container per member. `main` is its login shell. Other
tabs run another login shell in the same container and use names such as `t2`.
A member may have at most six tabs. A shell that exits closes its tab; opening
the environment again recreates the container if it stopped.

Stopping the environment stops its container and all tab processes. The member
home is not deleted. The next CLI or dashboard open starts a new container with
the same home.
