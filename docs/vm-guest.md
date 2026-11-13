# The VM helper

A session that runs in a Cloud Hypervisor VM, rather than a Docker container
(see the [plan](plans/2026-11-13-microvm-sessions.md)), has one Aether process
inside it from the first instruction on: `aether-internal guest`, the helper.
It is PID 1 of the VM, does what Docker and its init do for a container, and
serves the API the server uses in place of `docker exec` and `docker attach`.
The code is in `internal/guest/helper`; the server's client of the API is
`internal/guest`.

The helper is the `aether-server` binary itself. The kernel boots the
read-only helper disk, which holds that binary as `/init`, and passes it the
argument `guest`.

## Boot

In order, as PID 1:

1. Mounts `/proc`, `/dev` (devtmpfs) and `/sys`, finds the disk whose virtio
   serial is `root`, mounts it on `/mnt`, moves the three mounts into it and
   switches root to it. The helper stays PID 1; nothing is executed.
2. Mounts `/dev/pts`, `/dev/shm`, the cgroup v2 hierarchy on `/sys/fs/cgroup`
   and a tmpfs on `/run`.
3. Reads its configuration from `/proc/cmdline` (below), mounts the share and
   the data disk and binds what the configuration names.
4. Brings up `lo` and the VM's network device, sets the hostname, and writes
   `/etc/hostname`, `/etc/hosts` and, when DNS servers are given,
   `/etc/resolv.conf`.
5. Creates the session cgroup `/sys/fs/cgroup/session` (below).
6. Listens on every forwarded socket.
7. Serves the API on vsock port 1024. It accepts connections from the host
   only (vsock CID 2): a process in the VM that reached it through vsock
   loopback could start processes as root.

A step that fails is logged on the serial console and reported by the API's
`Info` as `boot_error`; the helper still serves, so the server can show the
error. When it cannot serve at all, it powers the VM off.

## Kernel command line

The server builds these arguments with `guest.Config.KernelArgs`, which puts
them after the kernel's own and ends the line with `-- guest`, the argument
that starts the helper. Every process in the VM can read `/proc/cmdline`, so
nothing secret goes there; environments and setup scripts arrive over the API.
x86 kernels cap the whole line at 2047 bytes, and `KernelArgs` fails past it.

| Argument | Meaning |
| --- | --- |
| `aether.hostname=<name>` | Host name of the VM. |
| `aether.net=<address>/<prefix>,<gateway>` | IPv4 address of the VM's one network device and its default route. |
| `aether.dns=<server>[,<server>...]` | Name servers for `/etc/resolv.conf`. Without it the image's file stays. |
| `aether.mount=<volume>,<source>,<target>[,ro]` | Bind `<source>` of a volume at `<target>`, in command-line order. |
| `aether.sock=<port>,<path>` | Listen on `<path>` and relay each connection to vsock port `<port>` on the host. |

Paths are escaped per segment as in a URL, so a space or a comma in a path
does not split an argument.

Two volumes exist. `share` is the virtio-fs share tagged `aether`, mounted on
`/run/aether-guest/share`; its binds are `nosuid,nodev`. `data` is the disk
with serial `data`, mounted on `/run/aether-guest/data`; a missing source
directory is created with the mode and owner its target has in the image, so
`/tmp` keeps its sticky bit. A source is opened beneath its volume and never
through a symlink that leaves it, and a target is created as a directory or
an empty file to match the source.

## Forwarded sockets

The server's Unix sockets cannot cross virtio-fs, so the helper listens on
the path the session expects, such as `/run/aether/coord3.sock` or
`/run/aether-signing/agent.sock`, and relays each connection to vsock CID 2.
Cloud Hypervisor delivers a connection to port `N` to the host socket
`<vsock socket>_<N>`, which the server points at the real socket.

While the host socket does not answer, as during a server restart, the
helper stops listening on the path, so a connection there is refused exactly
as one to a stopped server's socket is, and the coordination client retries
it for 60 seconds. The helper listens again as soon as the host answers. The
one connection it accepted before it noticed waits up to 60 seconds for the
host; if its client hangs up first, for example because the call timed out,
its request never reaches the server.

When the path is an entry in a read-only share, as the coordination socket in
the run's shared directory is, the helper binds its own socket over the
entry. The server re-creates that socket on every restart, and virtio-fs then
drops the bind; the helper watches the mount table and binds again. The first
connection after that can be refused once, which the coordination client
retries.

## Sessions and processes

`Boot` starts the session the way Docker starts a container, each step only
after the one before succeeded:

1. With `nested_docker`, and when the image has `dockerd` and `docker` on
   Docker's default `PATH`, `dockerd -H unix:///var/run/docker.sock --group
   <main gid> --cgroup-parent /session` as root, logging to
   `/var/log/aether-dockerd.log`. It gets the main process's environment
   with that `PATH`, never the session's, which starts in the member's home. It must answer within
   60 seconds.
2. The setup script, as `/bin/sh -ec "exec 2>&1; <script>"` with the main
   process's user, environment and directory, and no input. A nonzero exit returns its code
   and the last 16 KiB of its output, and the main process never starts.
3. The main process, under the ID `main`.

A later `Boot` call, also from a restarted server, returns the first call's
result. Setup runs once per boot, and the server gives every cold start a new
root disk.

`Spawn` starts any other process. Its ID is the caller's and cannot be used
again while the helper keeps the process's record, so a spawn whose reply was
lost is found again rather than started twice; `main`, `setup` and `dockerd`
are reserved. The user is `uid:gid`,
`uid` or empty for root, with supplementary groups and the home taken from
the uid's passwd entry. `HOME`, Docker's default `PATH` and, on a terminal,
`TERM=xterm` are added when the environment lacks them. A relative program
is found from the working directory, which must exist; `Boot` creates the main
process's, as Docker does. With `no_new_privileges`, as Docker's `no-new-privileges` security option,
neither the process nor its descendants can gain privileges through a
set-user-ID program or file capabilities; Go cannot set this between fork and
exec, so such a process starts as the helper binary, which sets it and then
executes the program. `Boot` gives the setup script and dockerd the main
process's setting. A process runs in its own session, either as the leader of a new terminal or with three pipes. A
program that cannot be executed fails the spawn with exit code 127 when it
was not found and 126 otherwise. A process a signal killed exits with 128
plus the signal number. The helper keeps the records of the 32 most recent
exited processes, and the main process's for good. It reaps every orphan.

PID 1 passes `SIGHUP`, `SIGINT`, `SIGQUIT`, `SIGTERM`, `SIGUSR1`, `SIGUSR2`,
`SIGALRM` and `SIGWINCH` on to the main process, as `docker-init` does. When
the main process exits the helper keeps running, and the server reads the
exit code and powers the VM off.

## Output and input

The helper keeps each output stream under offsets that count its bytes from
the process start. `Read` names an offset and waits for bytes there; a negative
offset returns the current end at once, so a reader that starts from now has
a known place to ask from again. Reading at an offset also acknowledges the
bytes before it. A lossy stream keeps its newest 256 KiB, and a reply whose
offset is past the requested one says what was dropped. A `lossless` process
blocks once 256 KiB of its output are unacknowledged. A stream ends once its
process and every descendant closed it, or one second after the process
exited with what the pipe then still holds, which a lossless stream keeps
for its reader however late it comes.

Because the output stays in the VM, a restarted server attaches by ID and
reads on from what the helper still holds, and so does a client whose
connection a save and restore cut.

`Write` takes the caller's deadline as the deadline of the write itself and
reports how many bytes the process took; it never closes the input.
`CloseStdin` closes a pipe process's input.

## Limits, stats and the clock

The session cgroup's `pids.max` is the session's process limit from `Boot`.
Every process the helper starts begins in its leaf `/sys/fs/cgroup/session/procs`,
and dockerd puts nested containers beside it, so they count too. The helper
itself stays outside.

`Stats` counts the whole VM, which is the session: CPU time outside idle and
I/O wait from `/proc/stat`, memory in use as `MemTotal - MemAvailable`, the
kernel's OOM kill counter, and the session cgroup's task count.

A restored VM's clock stopped while it was saved. The server calls
`SetClock`, through `guest.Client.SyncClock`, after every restore.

## Versions

`Info` reports the helper's protocol number. A helper in a VM restored from a
save can be older than the server; when its protocol differs from the
client's `guest.Protocol`, `Ping` fails with `guest.ErrIncompatible` and the
session needs a cold start. Fields added to the API do not change the
number; a change an older peer would misread does.

The API is `internal/guest/guestpb/guest.proto`. After changing it, run
`make proto`, which regenerates the Go code with pinned tools and needs only
Go.
