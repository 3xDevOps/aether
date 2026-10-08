# Member environments

A member's environment is the container image used by their own container
shell, the **Environment** page in the dashboard, and by every run they
launch, including runs on another member's shared agent account. If the member has saved an image, Aether uses it. Otherwise,
Aether uses the server's standard image. Sharing your agent account never
lends your image; see [teams.md](teams.md#agent-accounts).

## The standard image

The standard image is published with each release:

```
ghcr.io/3xdevops/aether-standard:<tag>
```

The standard image is based on Ubuntu 24.04; its packages and toolchains are
defined in [`images/standard/Dockerfile`](../images/standard/Dockerfile).
It includes bash, build-essential, certificates, curl, findutils, Ubuntu's
distribution Git, the GitHub CLI, GnuPG, grep, jq, the OpenSSH client, pkg-config,
Python 3 with venv, ripgrep, sudo, unzip, Go, Node and npm via fnm, uv, and Rust
via rustup. Git uses the normal Ubuntu package, not a custom source build,
on both amd64 and arm64. The server selects this image through
`--standard-image`; the default is the image matching the server build.
Teams that need a shared baseline can publish their own image and point
`--standard-image` at it.

The image ships `gh` and `ssh-keygen` for GitHub's sake: connecting GitHub
in your environment and signing commits inside a run need them, so
nothing has to be installed first. A team publishing its own standard image
should keep both, and keep gh at 2.81.0 or newer: that release added
`gh auth status --json`, which is how the server reads a member's login
back.

### Git in run environments

Managed commits use native `git update-ref --stdin` prepared transactions
inside the run container. Stock Ubuntu 24.04 Git 2.43 supports this primitive;
no custom Git build or new Ubuntu host upgrade prerequisite is needed.
Publication sends one dereferencing `update HEAD NEW OLD`: Git checks the
expected old object ID as a native compare-and-swap. After Git acknowledges
`prepare`, Aether verifies that symbolic `HEAD` still names the expected
branch while Git holds both the `HEAD` and branch locks, then commits or
aborts that same transaction. Custom environments lacking the required native
transaction support fail closed, without an unchecked branch-update fallback.
This is a capability requirement, not a claimed minimum Git version for every
environment; unrelated repository reads do not depend on this transaction.

Managed commits use a separate selected-path index, `git commit-tree`, and
atomic reference publication. They preserve native author/committer identity,
configured signing, and Aether's coauthor trailers, but **do not run commit
hooks** (`hooks_run=false`). Native `reference-transaction` hooks remain in
effect and can veto preparation. When a repository requires commit hooks, run
native `git commit` in that run's terminal instead; the managed commit action
is not a commit-hook-enforcing substitute.

A saved member image takes precedence over an updated standard image. Updating
the server alone therefore does not replace the software in saved environments
or existing run containers. If a custom image lacks the required native Git
support, choose one of these paths:

- Update the server's standard image pin as described below, then use
  **Reset to standard** or `aether env reset`. Reset discards the saved image's
  container-installed customizations; preserve anything needed first. Stop and
  reopen your environment and start a **new run**.
- To retain customizations, install your distribution's Git package with
  native prepared-transaction support in your environment, then
  `aether env save` and start a **new run**. Ubuntu 24.04's stock Git supports
  the required primitive; a special source installer is not needed.

For a custom standard image, rebuild it with the required native Git support
and repoint `--standard-image`; saved member images still need one of the
two paths above. See [install.md](install.md#git-inside-run-environments) for
native transaction failure diagnostics.
An already-running container keeps its old software after either save or reset.

### Updating the standard image

Docker pulls a tag it does not already hold, so the standard image is
refreshed only when the tag changes. Each release uses its own tag, and
the default follows the server build, so `aether server update` brings a
new image with it ([install.md](install.md#upgrading)).

A server that was started with `--standard-image` keeps that value across
an update, so the pin is what has to move. A tag naming one release never
moves in a registry either, and a digest names one image forever; both are
repointed:

```sh
sudo aether-server config set standard-image <a newer image> && sudo systemctl restart aether-server
```

A tag an operator reuses (`:latest`, or a team's own image) keeps whatever
the daemon already has until an admin repulls it on the server host:

```sh
docker pull ghcr.io/3xdevops/aether-standard:latest
```

A container keeps the image it started from, and nothing recreates it
while it runs, so a refreshed standard image reaches a member only when
they stop their environment and open it again.

Workspace creation does not choose an image. The command needs only the
workspace name and, optionally, its base branch:

```sh
aether workspace init <name>
aether workspace init <name> --base <branch>
```

## Container process lifecycle

Every newly created run and environment container enables Docker's
minimal init. Init adopts and reaps orphaned descendants and forwards signals
to the agent or shell.

This is a creation-time setting. A container that was already running, or
that survived a server restart, is not retrofitted or recreated just to add
init; it keeps the runtime settings it started with. A newly created terminal
container or run receives the setting.

### Closed-run compute grace

Closing or finishing a retained TUI run, or completing a swarm worker, keeps
its exact container for **one hour** by default (`--run-container-ttl=1h`).
The clock starts at completion, not at server restart. A positive override
changes that grace; zero selects the default, and a negative duration requests
immediate release. Working agents, agents waiting for input, active paused
runs and services are not expired because they are quiet.

A paused container still holds its process memory: pausing stops execution,
not RAM ownership. The grace is for recovering the same live processes, not
for keeping closed compute indefinitely. **Reopen** is available for eligible
retained interactive runs only while their exact runtime remains available
and their deadline has not passed; it does not restart stopped processes,
replay tools or create a replacement container. Completed swarm workers are
not reopened.

Compute, checkout and evidence lifetimes are separate. The finished checkout
is normally eligible for cleanup after **72 hours** (`--checkout-ttl`), but
required evidence or unresolved execution ownership protects it. Published
Git result branches and captured evidence are not removed just because the
one-hour container grace expires; retained history has its own policies.
**Free container…** releases retained compute early without deleting the run's
history. **Delete** is the separate destructive run-removal action.

On recovery, older retained deadlines are shortened to the earlier of their
existing deadline and durable completion time plus the current compute grace.
Deadlines are never extended and active/reopened runs are not migrated.
Failed evidence preservation or runtime cleanup keeps ownership and retries
automatically. The run's retention deadline, cleanup-pending state and safe
operation-level failure explain why resources can remain after the deadline;
expiry is not permission to discard uncaptured work.

## Resource limits and launch admission

New run and member-environment containers get generous automatic limits,
intended for hosts with at least 32 GiB RAM and moderate agent swarms:

| `aether-server serve` flag | `0` selects |
| --- | --- |
| `--run-cpus` | Up to 8 CPU cores, capped at the server host's CPU count |
| `--run-memory` | 8 GiB (`8589934592` bytes) |
| `--run-pids` | 4096 tasks (processes and threads) |

Positive values override the corresponding limit; negative values and
non-finite CPU values are rejected. These are server-wide creation defaults,
not per-run sliders. The matching `server.Config` fields are `RunCPULimit`,
`RunMemoryBytes` and `RunPidsLimit`. Persist flags through
`aether-server config set`, for example:

```sh
sudo aether-server config set run-memory 12884901888
sudo systemctl restart aether-server
```

That selects a 12 GiB memory ceiling for newly created containers. Existing
containers keep their original limits; restarting the server does not resize
or recreate them. Browser companions retain their separate limits.
Docker must report support for CPU quotas, memory and swap limits, and PID
limits. Missing controller support refuses new provisioning instead of letting
Docker silently drop a ceiling; this check also applies when host swap is off.
The memory limit is a **ceiling, not a reservation or guarantee**: idle agents
do not reserve 8 GiB each. Docker gets no extra swap allowance beyond that
memory ceiling. Container stdout/stderr uses Docker's rotating `local` log
driver (`max-size=10m`, `max-file=3`); this does not cap Aether's transcripts,
agent caches or files in the checkout/home.

Before provisioning a new run, member environment, browser or agent updater
container, Aether checks capacity with a five-second probe deadline.
Existing-environment execs and reopening live or paused retained containers
do not reserve another container's startup allowance. Reopening only resumes
existing compute: it does not restart stopped processes or create a replacement
container if unpausing fails. Starting new compute still requires admission.
Admission checks the Aether data filesystem, the Docker storage filesystems
and available host memory:

- Each filesystem must retain `max(5 GiB, min(5% of its size, 20 GiB))`.
  A positive `--min-free-disk` overrides that reserve in bytes; `0` selects
  the automatic reserve, and a negative value disables the disk guard.
- Host available memory must retain `max(2 GiB, 10% of host RAM)`.
- The new provisioning and each other outstanding provisioning additionally
  need 1 GiB disk and 512 MiB available memory. These short-lived admission
  reservations are released on completion, error or cancellation, not held
  for a run's lifetime.

Admission uses available memory, not the sum of every container's maximum,
so idle/thinking agents can coexist. It refuses new provisioning rather than
evicting active runs; idempotently retrieving an already-created run does not
require another admission. A burst of later allocations can still exhaust
host resources. These defaults reduce contention; they are not an uptime
guarantee or a substitute for host capacity planning.

Docker capacity is measured only after verifying a local Unix-socket daemon
against its data-root engine ID. A Unix socket alone is not proof of locality.
Classic storage checks Docker's data root and the actual layer directory:
`overlay2`, or `vfs/dir` (the verified `vfs` parent before the first layer).
These directories may occupy different filesystems. For a containerd image
store, supported `overlayfs`/`native` snapshotters use the actual
content and snapshotter roots exported by containerd's `PluginInfo` API at
Docker's reported `Containerd.Address`; each filesystem is checked separately.
There is no guessed `/var/lib/containerd` fallback. Linux `MemAvailable` and
`MemTotal` are read only after local identity verification, not replaced with
swap capacity.

Remote or unverifiable daemons, unsupported storage drivers, missing
containerd exports and failed probes refuse new provisioning with an explicit
capacity error. Storage usage may still report an unknown aggregate filesystem
when the roots span different filesystems; admission checks each root.
See [admission failures](failure-handling.md#picking-a-disk-floor) for diagnosis
and disk-override behavior.

Native agent tools remain in the same container as the agent. A limit can
contain a workload without keeping its agent process alive; completed writes
in the mounted home and checkout are distinct from a recoverable live session.
See [the execution-boundary decision](harness-integration.md#execution-boundary-keep-native-tools-with-the-agent).

## Install in your environment

Open your environment with `aether terminal`, or select **Environment** in
the dashboard's header (under **More navigation** if space is tight). This
is where a member installs system tools and language
runtimes, for example with `sudo apt-get install -y postgresql-client`,
Homebrew, or a language toolchain. The terminal is a persistent shell with
the member home mounted at `$HOME`.

Environment images must include `/bin/sh`. The terminal starts `/bin/bash -l`
when `/bin/bash` is executable; otherwise it starts `/bin/sh -l`.

Until the environment is saved, only the member home is shared with runs. The
container layer outside `$HOME` belongs to that terminal container and is not
available to runs or a replacement terminal.

Workspace environment variables and the workspace setup script still apply to
runs. They are workspace settings, not image selection.

## Save the environment

Save from the **Environment** page with **Save environment**, its only
primary button, or run:

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

Runs that are already running keep their existing containers. New runs the
member launches, their workspace shells, and the next environment open use
the saved image.
The terminal that was saved keeps running, so its committed state is already
available to later containers.

Saving requires a running terminal. If the terminal is not running, open it
first and then save.

## Reset to standard

Reset from the **Environment actions** menu > **Reset to standard…** on the
**Environment** page, or run:

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
