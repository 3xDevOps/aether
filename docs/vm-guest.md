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
3. Writes the boot sysctls that harden the kernel against an unprivileged
   agent ([Kernel hardening](#kernel-hardening)).
4. Reads its configuration from `/proc/cmdline` (below), mounts the volumes
   it names and binds what it names from them, then mounts each checkout
   disk ([Checkouts](#checkouts)).
5. Brings up `lo` and the VM's network device, sets the hostname, and writes
   `/etc/hostname`, `/etc/hosts` and, when DNS servers are given,
   `/etc/resolv.conf`.
6. Creates the session cgroup `/sys/fs/cgroup/session` (below).
7. Listens on every forwarded socket.
8. Serves the API on vsock port 1024. It accepts connections from the host
   only (vsock CID 2): a process in the VM that reached it through vsock
   loopback could start processes as root.

A step that fails is logged on the serial console and reported by the API's
`Info` as `boot_error`; the helper still serves, so the server can show the
error. When it cannot serve at all, it powers the VM off.

## Kernel hardening

A Default-tier session runs the agent as an unprivileged user (uid 1000),
and root in the VM waits for the sudo gate ([security.md](security.md#sudo-in-a-default-session)).
The guest kernel shrinks the surface that user has to reach root in the VM
without the gate, in two places.

The kernel config (`images/kernel/config-x86_64`) turns unprivileged BPF off
(`CONFIG_BPF_UNPRIV_DEFAULT_OFF` starts `unprivileged_bpf_disabled` at 2),
restricts `dmesg` to root, builds in YAMA, and zeroes freshly allocated
memory and hardens the copy-to/from-user and SLUB-freelist paths. KASLR is
not built in: Cloud Hypervisor boots the uncompressed `vmlinux` through PVH,
which does not run the decompressor that would relocate the kernel, so
`RANDOMIZE_BASE` has no effect; it belongs with a future bzImage boot.

The helper writes the rest at boot, since they have no compile-time default,
each a cold-boot step - a VM saved before an upgrade keeps its old settings
on wake until its next cold start:
`kernel.unprivileged_bpf_disabled=1` (latches unprivileged BPF off, so not
even root in the VM can set it back to 0, unlike the config's 2),
`kernel.kptr_restrict=2` (every kernel pointer in `/proc`, such as
`/proc/kallsyms`, reads as zero for all users, hiding module and heap
addresses from any in-VM reader), `kernel.yama.ptrace_scope=1` (a process may
ptrace only its own descendants), and `kernel.io_uring_disabled=2` (io_uring
is off for everyone; `io_uring_setup` returns `EPERM`, which the harnesses
fall back from). A missing one, where the kernel lacks the feature, is
skipped; any other failure is reported as a `boot_error`.

Two surfaces stay open by design. Unprivileged user namespaces remain on,
because the in-VM browser's Chromium sandbox needs them; through one a process
gains `CAP_NET_ADMIN` over a fresh network namespace and can reach `nf_tables`,
the largest remaining uid-1000-to-root path. And `perf_event_open` stays
available to the unprivileged (the upstream `perf_event_paranoid` has no level
that denies it outright); closing either would need a seccomp filter on the
agent's process tree, a design decision left to the owner.

Root in the VM, which a Root-tier session, a nested-Docker session, or a
Default session whose `sudo` was approved has, is still root in its own VM: it
can relax these at runtime where the kernel allows it (not unprivileged BPF,
which is latched), and escape the session cgroup ([Checkouts](#checkouts)).
The VM, not these settings, is what isolates it from the host and from other
sessions.

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
| `aether.mount=<volume>,<source>,<target>[,ro\|,overlay]` | Bind `<source>` of a volume at `<target>`, in command-line order. |
| `aether.checkout=<serial>,<key>,<target>` | Mount the checkout disk with virtio serial `<serial>` for the git service under `<key>` and bind its checkout at `<target>`. |
| `aether.sock=<port>,<path>` | Listen on `<path>` and relay each connection to vsock port `<port>` on the host. |
| `aether.serve=<port>,<path>` | Listen on vsock port `<port>` for the host and relay each connection to `<path>` (below). |

Paths are escaped per segment as in a URL, so a space or a comma in a path
does not split an argument.

The volumes:

| Volume | What it is | Source |
| --- | --- | --- |
| `share` | The virtio-fs share tagged `aether`, on `/run/aether-guest/share`. Binds are `nosuid,nodev`, and `virtiofsd` drops `CAP_MKNOD` and `CAP_SETFCAP`, so the guest cannot land a device node or a file-capability binary on the host. Only checkouts left on the server's disk use it. | A path in the share |
| `home` | The [file service](#the-file-service), on `/run/aether-guest/home`. Binds are `nosuid,nodev`. | The index of a shared host file |
| `data` | The disk with serial `data`, on `/run/aether-guest/data`. A missing source directory is created with the mode and owner its target has in the image, so `/tmp` keeps its sticky bit. | A path on the disk |
| `cache` | The disk with serial `cache`, on `/run/aether-guest/cache`: the session's copy of the member's tool caches. Its root is mode `1777` and has no `lost+found`. | `.` |
| `state` | The disk with serial `state`, on `/run/aether-guest/state`: the session's own agent databases, which outlive the VM. A missing source directory is created as on `data`. | A path on the disk |
| `helper` | The helper's own executable, the server binary the VM booted with, bound read-only. | `.` |

A source is opened beneath its volume and never through a symlink that
leaves it, and a target is created as a directory or an empty file to match
the source. `overlay`, on a `home` mount, mounts the source read-only below
a writable layer at `overlay/<source>` on the data disk, whose root takes the
source's owner and mode: the guest's writes stay in the VM.

## The file service

The member home, its login files, the coordination directory and every other
host file a session mounts, except the checkouts, reach the VM through the
file service, not virtio-fs, so a VM can be saved and restored with them
mounted. It is package `internal/guest/home`, API
`internal/guest/guestpb/home.proto`.

- The host serves it from the VM's monitor (`aether-server vm-monitor`), in
  the VM's systemd unit, on `<vm dir>/home.sock`, which Cloud Hypervisor
  reaches as vsock port 1023. It keeps running through a server restart, and
  the VM ends if it stops. `launch.json` lists what it shares.
- The helper mounts it with FUSE and forwards each operation by path. It
  keeps every inode, so a restored VM keeps its open files and working
  directories. The service keeps nothing a restore needs: a handle it does
  not know, as after a restore, is opened again by path, and is stale
  (`ESTALE`) when the file is gone or another replaced it.
- Every file shows as owned by the session user, and every file the guest
  creates on the host gets that owner, its mode without set-user-ID and
  set-group-ID bits, and no device nodes; `chown` changes nothing. A session
  needs no ownership pass over the home.
- The service trusts nothing from the VM, which any process in it can
  reach: paths are resolved beneath their share without following a
  symlink, only regular files and directories are opened, and a read-only
  or overlaid share is never written.
- The guest trusts what it learned of a name or a file for one second, so a
  change from another session shows within it. An `O_APPEND` write appends
  at the host's end of the file.
- `flock` and `fcntl` locks hold only within one VM, and memory maps are not
  shared across VMs. A SQLite database or a lock file in the home is not
  protected against the member's other sessions, as it is under Docker;
  [harnesses.md](harnesses.md#agents-in-session-vms) says how each agent's
  databases are kept apart.
- A VM can hold at most 128 requests in flight, each no larger than one
  1 MiB write, and the unit's `MemoryMax` bounds the rest.

## Forwarded sockets

The server's Unix sockets cannot cross virtio-fs or the file service, so the
helper listens on the path the session expects, such as
`/run/aether/coord3.sock` or `/run/aether-signing/agent.sock`, and relays each
connection to vsock CID 2. Cloud Hypervisor delivers a connection to port `N`
to the host socket `<vsock socket>_<N>`, which the server points at the real
socket.

While the host socket does not answer, as during a server restart, the
helper stops listening on the path, so a connection there is refused exactly
as one to a stopped server's socket is, and the coordination client retries
it for 60 seconds. The helper listens again as soon as the host answers. The
one connection it accepted before it noticed waits up to 60 seconds for the
host; if its client hangs up first, for example because the call timed out,
its request never reaches the server.

When the path is an entry in a read-only share, as the coordination socket in
the run's shared directory is, the helper binds its own socket over the
entry. The server re-creates that socket on every restart, and the guest
then drops the bind; the helper watches the mount table and binds again. The first
connection after that can be refused once, which the coordination client
retries.

## Served sockets

The other way round, `aether.serve=<port>,<path>` makes the helper listen on
vsock port `<port>` for the host, which reaches it through Cloud
Hypervisor's vsock socket with `CONNECT <port>`, and relay each connection
to the Unix socket `<path>` in the VM. Once that socket accepts, the helper
sends one zero byte and then relays; a socket that is not there yet gets
the connection closed. `guest.DialServed` does the host's side and waits for
the byte, so a server can tell a socket that is up from one that is not.
The dictation engine's VM serves its engine's socket this way.

## The development browser

A session VM boots with the browser image's disk, serial `browser`, read-only,
and serves the companion's control socket
`/run/aether-guest/browser/control/browser.sock` to the host on vsock port
2001. `Browser` drives the companion. `START` mounts the disk once per boot
under an overlay whose only write is the `/aether-control` mountpoint, makes
that read-only, and mounts `/proc` (other users' processes hidden), a
read-only `/sys` and a `/dev` with only `null`, `zero`, `full`, `random`,
`urandom` and `tty`. Every start gets a fresh `/tmp` (512 MiB), `/dev/shm` and
control directory, and runs the program in the cgroup `session/browser`, with
the request's memory cap, no swap, 512 processes and its CPU cap. Being inside
the session cgroup, its memory and processes also count toward the session's
`memory.max` and `pids.max`, so the two together never take the helper's
reserve. It runs in new mount and IPC namespaces: the helper binary, still
root, makes the root its own with `pivot_root` and detaches the VM's, then
becomes the process's user without new privileges, installs a seccomp filter
that fails `socket` and `socketpair` of `AF_VSOCK` and `io_uring_setup`, whose
rings can make sockets too (vsock is not namespaced, and the VM's forwarded
host sockets are on it), and executes the program. A `chroot` would keep
Chromium from making the user namespaces its sandbox needs. One companion runs
at a time; a `START` of the one that runs, under the same process ID,
succeeds. `PAUSE` and `RESUME` freeze and thaw the cgroup; `STOP` kills
everything in it and unmounts its `/tmp`, `/dev/shm` and control directory, so
their memory goes back. `Stats` leaves the browser cgroup's out-of-memory
kills out of the session's.

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
process's, as Docker does. Every process starts as the helper binary in `/`
with an empty environment, so its Go runtime ignores the program's
`GOMEMLIMIT` or `GODEBUG`; it receives the program's environment on a
memfd, enters the working directory and executes the program. Go's fork
holds the helper until the child's exec, so a child that reached the
[file service](#the-file-service) the helper serves would hang the VM; for
the same reason the helper keeps no file on the file service open across
a spawn, since the child's exec would close it and wait on the helper. A
start that does not report back within 10 seconds is killed and fails. With
`no_new_privileges`, as Docker's `no-new-privileges` security option,
neither the process nor its descendants can gain privileges through a
set-user-ID program or file capabilities; Go cannot set this between fork and
exec, so the helper binary sets it before the exec. `Boot` gives the setup
script and dockerd the main process's setting. A process runs in its own session, either as the leader of a new terminal or with three pipes. A
program that cannot be executed fails the spawn with exit code 127 when it
was not found and 126 otherwise. A process a signal killed exits with 128
plus the signal number. The helper keeps the records of the 32 most recently
exited processes. An older one stays while its exit was not reported or its
output not read to its end, but only until 128 more processes have exited,
and once its output was read only its exit code is kept. The main process's
record it keeps for good. It reaps every orphan.

PID 1 passes `SIGHUP`, `SIGINT`, `SIGQUIT`, `SIGTERM`, `SIGUSR1`, `SIGUSR2`,
`SIGALRM` and `SIGWINCH` on to the main process, as `docker-init` does. When
the main process exits the helper keeps running, kills every process left in
the session cgroup, as a container's end does, and drops the clean page cache
(`/proc/sys/vm/drop_caches`) so the host gets the memory back. It does not
write `memory.reclaim`: from the helper, which serves the home, that stalls
on the home's own pages. The server reads the exit code and powers the VM
off, unless its checkouts live on its own disks ([Checkouts](#checkouts)):
then the VM stays up for the git work that ends a session on its checkout,
the last snapshot, the final commit, branch publication and evidence, until
the session is removed, started again, or 10 minutes pass. A stop whose end
of the main process the helper never confirmed powers the VM off at once.
Meanwhile the VM reads as exited: exec and attach are refused, as for a
stopped container.

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
Its `memory.max` is the guest's `MemAvailable` when the helper starts, less
48 MiB: what the kernel and the helper already hold, the free memory the
kernel keeps back, and 48 MiB for the helper to grow stay outside the
session. A VM with checkouts keeps up to 96 MiB more out of it, a
sixteenth of its memory at most, for the [git service](#the-git-service)'s
anonymous memory, which its 256 MiB cap does not keep from the helper: a
256 MiB VM's session gets 16 MiB less, a 1 GiB VM's 64 MiB less, and from
1.5 GiB on 96 MiB less, about 1% of the default 8 GiB.
A session that filled the VM would leave the helper no memory to
serve the file service, and pages the session faults back in from it would
never arrive, so the kernel would thrash instead of killing a process. The
helper, as PID 1, is never the kernel's OOM victim. The file service holds
at most 8 MiB of requests at once and the kernel queues the rest; what it
keeps per file the kernel caches, about 1 KiB, is not bounded, and the
kernel frees it under memory pressure.
Every process the helper starts begins in its leaf `/sys/fs/cgroup/session/procs`,
and dockerd puts nested containers beside it, so they count too. The helper
itself stays outside.

`Stats` counts the whole VM, which is the session: CPU time outside idle and
I/O wait from `/proc/stat`, memory in use as `MemTotal - MemAvailable`, the
kernel's OOM kill counter, and the session cgroup's task count.

A restored VM's clock stopped while it was saved. The server calls
`SetClock`, through `guest.Client.SyncClock`, after every restore. Before a
save it calls `Sync`, which writes every filesystem's dirty data to its
disk, so the cache disk of a saved VM that is deleted without a restore
holds what the session wrote. `Sync` then drops the guest's page cache and
waits 3 seconds, while free page reporting hands the freed memory to the
host, so the saved state holds only what the session uses.

## Freezing the root disk

`Freeze` flushes the root filesystem and holds every write to it, as
`fsfreeze` does, so the server can copy the root disk whole for **Save
environment**. Reads go on, and so do writes to the data disk and the share.
`Thaw` ends it and reports whether the filesystem was still frozen; thawing
one that is not frozen is not an error. The helper thaws by itself at the
deadline the request names, at most 10 minutes, so a server that dies
mid-save never leaves the VM unwritable, and a copy that outlasted the
deadline can tell. A second `Freeze` moves the deadline. `PowerOff` thaws
first, since its sync would wait on a frozen filesystem.

## Checkouts

A session's checkout and each of its extra repositories live on a disk of
their own, not in a directory on the server. The server keeps one prepared
disk per workspace, base commit and owner, under
`<data dir>/vm/images/prepared/`: an ext4 filesystem whose `checkout/` holds a
self-contained clone at the base commit on the branch `aether-prepared`, owned
by the session's user. The first session on a base builds it (1.7 seconds for
40,000 files on local NVMe); every later one gets a qcow2 disk over it in
`<data dir>/checkouts/<run-id>/disk.qcow2` in constant time (17 ms). That disk
follows the checkout's retention, not the VM's.

The helper mounts each checkout disk on `/run/aether-guest/checkouts/<key>`,
binds its `checkout/` at `/run/aether-guest/data/git/<key>` for the git
service and at the target for the session.

### The git service

The git service runs the server's own git engine over the checkouts, with
the same hardening the server uses for a checkout on its disk: a root-owned
scratch repository on the data disk, the checkout's config, hooks and
attributes never consulted. So Changes, the live patch and staging behave as
for a Docker session. It is `aether-internal guest git-service`, a process of
its own that PID 1 starts again whenever it ends, waiting up to 30 seconds
after a start that did not last: PID 1 reaps every child it has, which would
collect a git's exit before the engine's own wait. Its cgroup,
`/sys/fs/cgroup/git`, sits beside the session, whose cap and pause leave it
alone, and has a `memory.max` of 256 MiB; an OOM there kills one git, and
when the service itself ends, the helper kills whatever is left in it and
clears the lock files a killed git leaves before starting the next one, and the next one records what changed meanwhile once the
server reconnects. The service's Go heap aims at 64 MiB (`GOMEMLIMIT`), a
file travels to and from it in chunks, and git runs with
`core.bigFileThreshold=16m`, `pack.windowMemory=64m` and `pack.threads=1`.
So a file over 16 MiB shows as binary in a VM session's diffs, where a
Docker session shows its text up to the patch limit: without the threshold
git reads such a file whole and a 100 MiB one passed the cap. With the
session holding all its cap leaves on a 1 GiB VM, a snapshot and commit of
40,000 changed files and a 100 MiB file, then a 64 MiB upload and read,
keep the git cgroup's anonymous memory near 60 MiB; the rest is page cache
the kernel reclaims. It serves `internal/guest/guestpb/git.proto` on vsock
port
1100, to the host only, as the API is:

| Call | What it does |
| --- | --- |
| `Setup` | Called before `Boot` on every boot and server restart. When the checkout's top directory belongs to someone other than the session user, as when a session starts again from an image with another user, it hands every file of the checkout to that user. On a checkout's first boot it sets `aether.base`, `aether.branch`, `aether.workspace` and the origin remote, and renames `aether-prepared` to the run branch, running git as the checkout's owner so the session can keep writing everything git wrote. Then it starts the checkout's diff watch. |
| `Watch` | Streams the checkout's diff watch, starting it if it does not run: each snapshot's `run.diff` payload, each moved HEAD, and at most one file change report a second. A snapshot or HEAD counts as delivered only once the server acknowledges it; a refusal, or no `Watch` open at all, is a history gap the watch retries, as a failed publication on the server is. |
| `Flush` | Takes the last snapshot, returns once `Watch` delivered it, and stops the diff watch until the next `Watch`. |
| `Objects` | Streams a pack of what `wants` reach and no `haves` do. |
| `Patch` | Renders the live diff. |
| `Head` | Returns the commit HEAD names. |
| `Stage` | Writes a tree of the whole checkout: within the evidence bound for evidence, or over a copy of the checkout's own index for the final commit, as the server stages a checkout on its disk. |
| `Install` | Indexes a pack the server sends into the checkout with `index-pack --strict`, applies ref updates in one transaction, and for a commit makes the staged index the checkout's. It runs git as the checkout's owner. |
| `Tree`, `ReadFile`, `WriteFile`, `FileDiff` | The Files view on the checkout, including uploads. |
| `Sync` | Serves a live overlay's endpoint (`aether sync --live`) at the checkout. |

The server treats every reply as data from the session. It copies a
snapshot's tree and HEAD into the session's `<run-id>.diffsnap/` store with
`git index-pack --strict`, which checks each object and that every object one
names is in the pack or the store, and refuses a pack over 128 MiB. That
limit bounds the compressed bytes, not what they expand to. So every git
command that indexes a VM's objects - this receive, a published HEAD's, and
evidence - runs single-threaded under a fixed 512 MiB allocation limit, git's
large-file threshold. A few pack bytes that claim a larger object fail that
one command instead of exhausting host memory, while a legitimate large file,
which git streams rather than allocates, still indexes. The server then
records the snapshot only when the tree is a tree within the snapshot's
128 MiB bound, HEAD is a commit, and the base lies between the fork point and
HEAD. The bound check stops as soon as the listing passes the bound, so a tree
whose recursive expansion is enormous cannot burn a CPU core. The store
borrows the workspace repository's objects, which hold the fork point. A moved HEAD is published by fetching it from that store into
the workspace repository, as for a checkout on the server's disk. While the
VM is not running, the session's patch is its last recorded snapshot, also on
a server switched back to `session-runtime docker`.

The final commit is built on the server, so the member's signing key never
enters the VM. The VM stages the checkout; the server copies the tree,
checks it, and writes the commit with `git commit-tree`, authored by the
member, committed by Aether, the message cleaned as `git commit -m` cleans
it, and signed. `Install` then moves the staged objects into the checkout,
adds the commit object, the only one the server sends, and makes it HEAD,
moving HEAD only from the commit it was staged on. As on the server's disk,
the commit has no bound on the checkout's size; its new objects, like a
moved HEAD's, may take up to 4 GiB, and the server keeps 128 MiB of disk
free. The server records the committed tree; while the VM does not run,
evidence retains that tree, and without one capture fails and cleanup keeps
the checkout's disk, since only the VM can read it. Pausing a VM whose
checkouts live on its own disks freezes the session cgroup with the
helper's `FreezeSession`, as Docker's pause freezes a container's, and
leaves the VM running: the git service is outside the session, so Close
pauses the session first and then commits it, as for a container, and the
Files view of a paused session reads the live checkout. Exec into a paused
session is refused. Root in the VM, which a sudo-tier session and nested
Docker have, can move a process out of the session cgroup, and with it out
of a pause, the session's process limit and the kill when the main process
exits. A subsession's branch reaches a parent in a
VM the same way: the server packs what the parent lacks and `Install` sets
`refs/remotes/children/<id>`.

A live overlay is pinned by the server as for a checkout on its disk, then
relayed to the VM, whose endpoint pins the root to the checkout again, gives
what it creates to the checkout's owner, and keeps its staging under
`/run/aether-guest/data/git/mutagen`. Live sync to a sleeping session wakes
it and holds it awake while the overlay runs.

While the VM does not run, because the session sleeps or ended, its Files
view and diffs come from the last snapshot the server recorded: nothing can
have changed the checkout since. A Files write or upload to a sleeping
session wakes it first; one to an ended session, whose VM was removed,
fails. Removing the checkout
deletes its disk and first copies into the session's diff history what that
history borrowed from the workspace repository, so the history outlives the
workspace's own objects.

## Versions

`Info` reports the helper's protocol number. A helper in a VM restored from a
save can be older than the server; when its protocol differs from the
client's `guest.Protocol`, `Ping` fails with `guest.ErrIncompatible` and the
session needs a cold start. Fields added to the API do not change the
number; a change an older peer would misread does.

The API is `internal/guest/guestpb/guest.proto`. After changing it, run
`make proto`, which regenerates the Go code with pinned tools and needs only
Go.
