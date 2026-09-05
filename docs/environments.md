# Member environments

A member's environment is the container image used by every container that
member receives: agent runs, workspace shells, and the environment terminal.
If the member has saved an image, Aether uses it. Otherwise, Aether uses the
server's standard image. A member's image is never used for another member.

## The standard image

The standard image is published with each release:

```
ghcr.io/3xdevops/aether-standard:<tag>
```

Its contents are pinned in `images/standard/Dockerfile`: Ubuntu 24.04 with
bash, build-essential, certificates, curl, findutils, Git, grep, jq,
pkg-config, Python 3 with venv, ripgrep, sudo, unzip, Go, Node and npm via
fnm, uv, and Rust via rustup. The server selects this image through
`--standard-image`; the default is the image matching the server build. Teams
that need a shared baseline can publish their own image and point
`--standard-image` at it.

Workspace creation does not choose an image. The command needs only the
workspace name and, optionally, its base branch:

```sh
aether workspace init <name>
aether workspace init <name> --base <branch>
```

## Install in the environment terminal

Open the environment terminal with `aether terminal` or from the dashboard's
terminal dock. This is where a member installs system tools and language
runtimes, for example with `sudo apt-get install -y gh`, Homebrew, or a
language toolchain. The terminal is a persistent shell with the member home
mounted at `$HOME`.

Until the environment is saved, only the member home is shared with runs. The
container layer outside `$HOME` belongs to that terminal container and is not
available to runs or a replacement terminal.

Workspace environment variables and the workspace setup script still apply to
runs. They are workspace settings, not image selection.

## Save the environment

Save from the terminal dock with **Save environment**, or run:

```sh
aether env save
```

Saving pauses the terminal for the few seconds Docker needs to commit its
running container. Aether stores the committed image on the server's Docker
daemon and never pushes it to a registry. The tag is:

```
aether/member-<member-id>:<unix-seconds>
```

The saved tag is recorded on the member. After the new image is active, every
older `aether/member-<member-id>` tag is removed. A tag a run container still
uses stays until the next save or reset. The command prints `saved <tag>`,
then `new runs and terminals start from this environment`.

Runs that are already running keep their existing containers. New runs,
workspace shells, and the next environment terminal open use the saved image.
The terminal that was saved keeps running, so its committed state is already
available to later containers.

Saving requires a running terminal. If the terminal is not running, open it
first and then save.

## Reset to standard

Reset from **Reset to standard** in the terminal dock's Stop dialog, or run:

```sh
aether env reset
```

Reset stops the terminal, forgets the member's saved image, and removes the
member's saved tags from the server's Docker daemon. The next terminal open
and all new runs use the standard image. The command prints
`environment reset to the standard image`.

There is no save history or other undo. To fix a bad save, repair the
installation in the terminal and save again, or reset to standard.

## Missing saved images

Saved images exist only on the server's Docker daemon. If an operator prunes a
saved tag, the member's runs and terminal fail with an error that names the
missing tag and tells the member to run `aether env reset`. Aether does not
silently fall back to the standard image.

## Status and protocol

`aether terminal status` reports `image`, the image used by the current
terminal container, and `saved image`, the member's saved image when one
exists. The dashboard gets the same saved reference as `saved_image` in
`terminal.status`; `image` continues to identify the current container image.

The member-scoped control-channel methods are:

- `env.save` returns `{"image":"<tag>"}`.
- `env.reset` returns `{}`.
- `terminal.status` includes `saved_image`, omitted when it is empty.

Both environment methods use the normal protocol error path. Saving without a
running terminal returns an invalid-state error telling the member to open the
terminal first.
