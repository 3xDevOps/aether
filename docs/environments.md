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
It includes bash, build-essential, certificates, curl, Docker (`docker.io`,
`docker-compose-v2` and `docker-buildx` from Ubuntu), findutils, Ubuntu's
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

An editor that attaches to a run over SSH installs its remote server in the
container ([terminal.md](terminal.md#editors)). The standard image has what
that takes: glibc, bash, curl and tar. A team image should keep them. `ssh`,
`scp` and `sftp` into a run need nothing from the image but `/bin/sh`;
`rsync` needs `rsync` installed in it, which the standard image does not
have.

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

Reporting success or failure leaves an interactive Standard or Enhanced run
open for follow-up; it does not start a cleanup timer. Working agents, agents
waiting for input, active paused runs and services do not expire because they
are quiet.

Explicitly closing an interactive run, or completing a swarm worker, keeps
its exact container for **seven days** by default (`--run-container-ttl=168h`).
The clock starts at close or worker completion, not at server restart. A
positive override changes that grace; zero selects the default, and a
negative duration requests immediate release.

A paused container still holds its process memory: pausing stops execution,
not RAM ownership. The grace is for recovering the same live processes, not
for keeping closed compute indefinitely. **Reopen** is available for eligible
retained interactive runs only while their exact runtime remains available
and their deadline has not passed; it does not restart stopped processes,
replay tools or create a replacement container. Completed swarm workers are
not reopened.

Compute, checkout and history have separate lifetimes. The finished checkout
is normally eligible for cleanup after **72 hours** (`--checkout-ttl`), but
required evidence or unresolved execution ownership protects it. Terminal
recordings, Enhanced session logs and recorded diff history survive checkout
cleanup and archiving; explicit run deletion removes them. Published Git
result branches and captured evidence are not removed just because the
seven-day container grace expires.
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

Every new terminal container also gets two read-only mounts for the
[assistant](assistant.md): `/run/aether-assistant`, the socket bound to your
member, and `/opt/aether/aether-server`. A terminal created before the update
has neither, and the assistant shows a restart prompt until you stop and
reopen it.

Environment images must include `/bin/sh`. The terminal starts `/bin/bash -l`
when `/bin/bash` is executable; otherwise it starts `/bin/sh -l`.

Until the environment is saved, only the member home is shared with runs. The
container layer outside `$HOME` belongs to that terminal container and is not
available to runs or a replacement terminal.

The workspace's setup script and variables still apply to runs; see
[Workspace environment](#workspace-environment).

## Docker inside runs

The standard image ships the `docker` CLI, `dockerd`, `docker compose` and
`docker buildx`. On a server host that has the [Sysbox](https://github.com/nestybox/sysbox)
container runtime, each run and each Environment terminal starts its own
`dockerd` before the agent, login shell or setup script. The daemon runs
inside the container, so it needs no `--privileged` and no host Docker
socket. `docker run`, `docker build` and `docker compose up` work in a run
as they do on a laptop.

`aether-server setup` installs Sysbox when you accept its Docker-inside-runs
question. On an existing server, press **Install** under **Settings >
Server** in the dashboard (admins only), or run on the server host:

```sh
sudo aether-server sysbox install
aether-server sysbox status     # prints on or off
```

`sysbox status` asks Docker, so run it as a user that can reach the Docker
socket, or with `sudo`.

The host must be Ubuntu or Debian on amd64 or arm64, run Docker, and have
Linux 5.19 or newer or a kernel with the `shiftfs` module. On any other host
the install stops with `Docker inside runs is unavailable on this host:
<reason>` and changes nothing; runs work without Docker inside.

`aether-server` carries the installer, `scripts/install-sysbox.sh`, so the
binary and the script are the same release. It downloads Sysbox CE 0.7.1,
verifies its pinned SHA-256, installs the package and checks that
`docker info` lists `sysbox-runc`. It exits 0 without changes when that
version is already installed, 3 when the host cannot run Sysbox, 4 when a registered
Sysbox failed its probe and could not be removed, and 1 on any other failure. Its output is shown as it runs; the dashboard keeps the last
8 KiB. When the dashboard starts the install, the server runs it in a
transient systemd unit, `aether-sysbox-install`, so restarting the server
does not interrupt apt; a server update waits until it finishes. If an
install is cut off anyway, running it again finishes the interrupted dpkg
step first. The script needs `curl`, downloads and verifies the package
before it edits `/etc/docker/daemon.json`, and waits up to 5 minutes for
the apt lock.

Sysbox's installer refuses to run while any container exists, stopped ones
included, if it must restart Docker, and it must when `/etc/docker/daemon.json`
lacks `bip` or `default-address-pools`. The script therefore adds a missing
or empty key itself, keeping the other keys and never changing one that is
set. It picks the first free /16 from `10.200.0.0` to `10.254.0.0`, then
`172.24.0.0` to `172.31.0.0`, then `192.168.0.0`: `bip` takes the first as
`x.y.0.1/16`, and `default-address-pools` takes the next with size 24. A /16
is free when it overlaps no IPv4 route, interface address or Docker network
subnet on the host. The 10.200 range comes first because 172.24 to 172.31 is
still inside Docker's default pools until Docker restarts. The script prints each value it writes, and creates the
file as `{}` if it is missing. If no free /16 is left it stops without
changing the file; set the key to a free range yourself and run it again.
The script does not restart Docker and running containers keep running;
Docker applies the new keys at its next restart.

The script ends by pulling `busybox:1.36` and running it with
`--runtime sysbox-runc -u 1000:1000` against a host directory owned by
1000:1000. It fails with "bind mounts are not ID-mapped on this host" only if
the container user cannot write the directory; a pull failure reports Docker's
own error. When the probe fails, the script runs `apt-get purge -y sysbox-ce`,
waits until Docker no longer lists `sysbox-runc`, and exits 1 with the probe's
reason and "Sysbox was removed again"; the `bip` and `default-address-pools`
keys stay. If the purge fails it exits 4. The dashboard then shows the install
as failed, and the server stops using `sysbox-runc` for new containers until an
install succeeds. Running the script again on a host that already has the
package runs the probe too, and exits 4 if it fails.

On Ubuntu 25.04 and later, an enforced AppArmor profile for `fusermount3`
allows FUSE mounts only under home, `/mnt`, `/run/user`, `/media`, `/tmp` and
`/cvmfs`, so every Sysbox container fails with `failed to pre-register with
sysbox-fs` ([sysbox#947](https://github.com/nestybox/sysbox/issues/947)).
When `/etc/apparmor.d/fusermount3` is loaded in enforce mode, the script
appends FUSE mount and unmount rules for `/var/lib/sysboxfs/**/` only, between
`# BEGIN aether sysbox` and `# END aether sysbox`, to
`/etc/apparmor.d/local/fusermount3` and reloads the profile, which stays
enforced. It retries the probe once after 3 seconds, because Sysbox can fail
intermittently when containers start concurrently
([sysbox#1044](https://github.com/nestybox/sysbox/issues/1044)). Any failure
after the script first changed the host purges `sysbox-ce`, removes the
override and restores `/etc/docker/daemon.json` (or deletes it if the script
created it), without restarting Docker. If `daemon.json` changed in between
beyond Sysbox's own `runtimes` entry, the script leaves it and says so.

The server picks its container runtime from `--container-runtime` (config
key `container-runtime`). With `auto` it keeps `sysbox-runc` once it sees it
and looks again once a minute while Docker lacks it, so a Sysbox you just
installed needs no restart. A change to the option itself does:

| Value | Meaning |
| --- | --- |
| `auto` (default) | `sysbox-runc` when Docker reports it registered, otherwise the daemon default. |
| `runc` | Never use Sysbox; Docker stays unavailable inside runs. |
| any other name | That runtime exactly, as registered in Docker. |

```sh
sudo aether-server config set container-runtime runc   # opt out
sudo systemctl restart aether-server
```

Run containers, Environment terminals and candidate verification containers
use the runtime. The browser companion and the server's short-lived utility
containers stay on the daemon default.

Things to know:

- If `dockerd` cannot start, the launch fails with the daemon's log tail:
  `runtime: dockerd did not become ready (exit 1): <last lines of the log>`.
  The full log is `/var/log/aether-dockerd.log` in the container. An image
  without `dockerd` skips the step silently.
- Sysbox keeps the container's `/var/lib/docker` in a host directory under
  `/var/lib/sysbox` and removes it with the container. Inner images,
  containers and volumes are discarded with it, and `aether env save` does
  not keep them. Pull or build again in each run, or bake what you need into
  the image. Admission's free-space floor covers that directory when
  `/var/lib/sysbox` shares a filesystem with Docker's data root.
- A port published by an inner container is on the run's own network
  namespace, not the host's. Reach it from the run at `127.0.0.1`; the
  run's browser companion opens it at `127.0.0.1` as well.
- Existing containers keep the runtime they were created with. After
  installing Sysbox, new runs and new Environment terminals get Docker
  within a minute; close and reopen an Environment
  terminal that is already open.
- A saved member image that predates Docker in the standard image has no
  Docker CLI. In the Environment terminal run
  `sudo apt-get install -y docker.io docker-compose-v2 docker-buildx` then
  `aether env save`, or `aether env reset` to return to the standard image.
- Without Sysbox the CLI is present but has no daemon:
  `Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?`

How the boundary differs is in [security.md](security.md#the-agent-container).
Running Aether's own test suites from inside a run is in
[testing.md](testing.md#running-the-docker-suites-inside-a-run).

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

## Workspace environment

A member's environment decides the image a run starts from. A workspace's
environment decides what every new run in that workspace starts with,
whoever launches it: a **setup script** that runs before the agent, and
**variables** set in the run's container. A variable can be marked
**secret**; its value is then never shown again.

An admin edits both under **Environment** on the workspace's Repository page
(workspace switcher > **Environment**) or with `aether workspace env`. Every
member can read the script, the variable names and the values that are not
secret.

A change applies to runs launched after it. A container that already exists
keeps the variables it was created with and never reruns the script, so
pausing, reopening or switching the mode of a run changes nothing in it.
Candidate verification containers ([integration.md](integration.md)) get the
workspace environment too. A member's own Environment terminal does not.

### Setup script

```sh
aether workspace env script                    # print it
aether workspace env script --file setup.sh    # replace it; - reads stdin
aether workspace env script --clear
```

For a Node project `setup.sh` can be one line:

```sh
npm ci
```

The server runs the script once in each new run's container, after the
container starts and before the agent's process does:

- **How:** `/bin/sh -ec '<script>'`, with standard error sent to standard
  output. It is POSIX `sh`, it stops at the first command that fails, and it
  is not a login shell.
- **Where:** `/workspace`, the run's checkout.
- **As:** the container's user: root in the standard image, otherwise the
  user the image or the agent sets.
- **With:** the container's environment: the workspace variables, the
  launching member's `$HOME` and tool caches
  ([environment-home.md](environment-home.md)), `AETHER_RUN_ID` and
  `AETHER_WORKSPACE_ID`.
- **For how long:** the launch waits until it exits; there is no time limit.
  `run.launch` calls are admitted one at a time, so every other launch on the
  server waits behind a slow script. Install dependencies in it and leave
  builds and tests to the agent.

If the script exits non-zero, the container is removed and the launch fails:

```
aether: rpc error -32603: provisioning: start container: runtime: setup script exited 1
setup script output:
npm error code E401
```

The run is listed as failed with `provisioning: start container: runtime:
setup script exited 1` as its reason. The script's output, the last 16 KiB
of both streams in the order they were printed, goes only to whoever
launched: `aether run` prints it and the dashboard's launch dialog shows it.
It is not stored with the run. The server log has the same output. In both,
the exact value of each secret is replaced with `***`.

### Variables

```sh
aether workspace env list
aether workspace env set NODE_ENV=test API_URL=https://api.example.test
aether workspace env unset NODE_ENV
```

A name must not be empty or contain `=`. The server sets some variables
after the workspace's, among them `HOME`, `TERM`, the `AETHER_*` run
identifiers and the `GIT_AUTHOR_*` and `GIT_COMMITTER_*` identity, so a
workspace variable with one of those names has no effect. A `PATH` variable
is kept, with `~/.local/bin` put in front of it.

### Secrets

A secret is a variable whose value the server stores and never returns. Its
value is read from stdin, so it stays out of your shell history:

```sh
aether workspace env set --secret NPM_TOKEN < token.txt
aether workspace env set --secret NPM_TOKEN     # on a terminal: prompts, input hidden
```

Stdin is taken whole, less one trailing newline. `aether workspace env list`
and the dashboard show a secret as its name alone, to admins as well. To
change one, set it again; there is no way to read it back.

Import a `.env` file to add many at once. Its values are stored as secrets
unless you pass `--plain`; an empty value has nothing to hide and is stored
plain either way:

```sh
aether workspace env import .env
aether workspace env import --plain .env
```

The file holds `NAME=VALUE` lines. Blank lines and `#` comments are skipped,
a leading `export ` is dropped, single quotes keep a value as written and
double quotes unescape `\n`, `\r`, `\t`, `\"` and `\\`, which is how a
value with line breaks is written. A `#` after a closing quote, or after a
space in an unquoted value, starts a comment. A later line replaces an
earlier one of the same name. The dashboard's **Import .env…** reads the
same format and lists what each line adds or replaces before anything is
saved.

A secret is hidden from the settings, not from a run. It is an ordinary
environment variable inside the container, so the agent can read it and so
can anyone who can launch a run in the workspace or message one.
[security.md](security.md#workspace-secrets) lists where the value is kept
and who can reach it.

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

The workspace environment is read with `workspace.environment.get` and
changed with `workspace.environment.set`, which needs the admin role;
[local-gateway.md](local-gateway.md) lists their parameters.
