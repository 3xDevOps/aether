# MicroVM measurements (work item 1)

Measured 2026-11-13 on the KVM test host for the plan
[docs/plans/2026-11-13-microvm-sessions.md](../../docs/plans/2026-11-13-microvm-sessions.md).
Research only; nothing here ships. Raw logs of every run quoted below are in
[raw/](raw/).

## Answers

| Open question | Answer |
| --- | --- |
| Host memory per awake session | Claude Code VM (2 GiB): 606 MiB idle after a turn, all 2 GiB during `npm install`, 430 MiB after a guest reclaim, ~560 MiB after the next turn; the same at 1, 5 and 10 VMs. Same agent in a container: 168 MiB idle, 220 MiB after `npm install` plus 1.1 GiB of host page cache the host can drop. Only with `page_reporting.page_reporting_order=0` does freed guest page cache return (Q1) |
| Save and restore time, size | 2 GiB VM after a turn, memory `shared=on`: save 0.47-0.92 s, 620-638 MB on disk; restore to the agent answering 2.1-2.6 s (0.6-0.8 s on a VM that never slept). Worst case (guest RAM full): 0.98 s, 1.6 GB, 2.5-3.6 s. With `shared=off` the snapshot is always the full RAM size (Q2) |
| Save and restore with virtio-fs | **No.** It saves and restores, but every operation on every share then fails with EBADF (Q3) |
| Nested Docker and a dev server survive a save | **Yes**, both (Q4) |
| Copy-on-write format | Create: qcow2 overlay 5 ms, reflink copy 1 ms. Cloud Hypervisor's qcow2 is slow with `direct=on` (3.2k random-write IOPS) and close to raw for reads with `direct=off` (203k vs 237k random-read IOPS; 20-25k vs 215k random-write IOPS). Coordinator decision: qcow2 over a raw base, `direct=off` (Q5) |
| git on the VM disk vs virtio-fs | 40,000 files: `git add -A` 4.5 s on the VM's ext4 disk, 25-27 s over virtio-fs, 3.4 s on the host; `git status` 28-52 ms, 70-350 ms, 20 ms (Q5) |
| Idle CPU, tickless guest | 10 idle Claude Code VMs: 0.53-0.62% of one CPU each, 0.37% of the 16-CPU host in total; bare VMs 0.26%; containers 0.20% (Q6) |
| Home over a FUSE file service on vsock (coordinator's follow-up) | vsock round trip 74 us (p50), same as TCP over the tap. A path-forwarding FUSE prototype stats 40,000 files in 3.8 s (local ext4 74 ms) and reads them in 15.6 s cold (2.2 s) (Q7) |

## Conditions

| | |
| --- | --- |
| Host | AWS m8id.4xlarge, nested virtualization: 16 vCPU (Xeon 6975P-C), 61 GiB, Ubuntu 24.04, host kernel 7.0.0-1014-aws, no swap |
| Storage | /srv: 885 GB local instance NVMe, XFS, reflink=1. fio direct on the host: 6.5 GB/s 1M read, 247k 4k random read, 211k 4k random write IOPS |
| Monitor | Cloud Hypervisor v54.1 (latest release, 2026-11-11), virtiofsd 1.10.0 |
| Guest kernels | Both Linux 6.18.63, `vmlinux` booted by PVH, not Cloud Hypervisor's own kernel. **mine**: built here (`kernel/build-kernel.sh`, defconfig + kvm_guest.config + [kernel/aether1.config](kernel/aether1.config)), HZ=1000, no THP, sha256 afd476c1. **item 4**: `/srv/aether/shared/item4/vmlinux`, HZ=250, THP madvise, sha256 fb3ec21a. Both NO_HZ_IDLE. Runs labelled `k4` used item 4's; the rest used mine |
| Guest command line | `console=hvc0 root=/dev/vda rw init=/sbin/a1-init quiet page_reporting.page_reporting_order=0` |
| Session image | `ghcr.io/3xdevops/aether-standard:v0.8.7-beta.14` + [images/agents/Dockerfile](../../images/agents/Dockerfile) (Claude Code 2.1.308, Codex 0.164.0, Node 24.20.0) + sshd, tmux, fio ([image/Dockerfile](image/Dockerfile)), flattened with `docker export` and `mkfs.ext4 -d` to a 16 GiB sparse raw disk (4.3 GiB used) |
| VM | 2 vCPU, 2 GiB unless stated; `--balloon size=0,free_page_reporting=on`; memory `shared=off` unless stated; root disk a reflink copy of the base disk; one persistent tap on a host bridge with NAT; vsock; PID 1 is [image/init.sh](image/init.sh), a stand-in for item 3's helper. Boot to sshd: 1.1-1.7 s |
| Agents | The pinned CLIs in tmux, as Aether's Standard mode runs them (`claude --dangerously-skip-permissions`, `codex --dangerously-bypass-approvals-and-sandbox`), signed in to the real-agent suite's mock model ([mockmodel/](mockmodel/), built from `internal/server/mockmodel_integration_test.go`). One turn: the mock asks for one shell command, then answers. No real logins |
| Containers | The same image under Docker 29.1.3 on the same host: `--cpus 2 --memory 2g`, home bind-mounted, same agents and mock |
| Tenancy | Items 2, 3 and 4 ran VMs on the host at the same time. Per-process and per-cgroup figures are unaffected; host-wide figures (MemAvailable) were too noisy to use |

Host memory of a VM is the monitor's `RssAnon + RssShmem` (all guest RAM it
holds) plus virtiofsd's `RssAnon`. virtiofsd's VmRSS also counts the guest
pages it maps (529 MiB in one run), so it double counts. Host memory of a
container is its cgroup's `anon + shmem + kernel`; its page cache (`file`)
is listed apart because the host shares and drops it.

## Reproduce

From moss: `sync.sh` copies this directory to `/srv/aether/1/repo`;
`mockmodel/build.sh` and `fusevsock/build.sh` build and copy the two Go
programs. On the host: `kernel/build-kernel.sh`, `image/build-image.sh`,
`host/vm.sh net-up`, `host/mock.sh`, then the `tests/q*.sh` named in each
section (`tests/q1-matrix.sh`, `q2-matrix.sh`, `rest.sh` hold the exact
parameter sets). `host/vm.sh` holds every Cloud Hypervisor and virtiofsd
command line.

## Q1: host memory per awake session

`tests/q1-memory.sh` (`KIND=vm|ctr AGENT=claude|codex N=...`): boot N
sessions, one agent turn each, 60 s idle, `npm install` of
[tests/package.json](tests/package.json) (Next 15, React 19, ESLint,
Tailwind, Vitest: 301 packages, 471 MB `node_modules`) in all at once, 60 s
after, then VM only: the guest writes `8G` to its root
`/sys/fs/cgroup/memory.reclaim`, 20 s after; one more turn, 60 s after.
Sampled every 2 s during the install. MiB per session, average (max).

| Session | N | Booted | Idle after a turn | npm install peak | 60 s after | Guest reclaim | Next turn |
| --- | --- | --- | --- | --- | --- | --- | --- |
| VM, Claude Code | 1 | 133 | 607 | 2041 \* | 1660 | 443 | 567 |
| VM, Claude Code | 5 | 132 | 606 | 1753 (2041) \* | 1685 | 434 | 561 |
| VM, Claude Code | 10 | 132 | 606 | 1678 (1759) \* | 1674 | 437 | 559 |
| VM, Claude Code, item 4 kernel | 1 | 105 | 612 | 1993 | 1625 | 431 | 586 |
| VM, Claude Code, item 4 kernel, installs at once | 10 | 103 | 613 | 2002 (2024) | 1632 | 427 | 553 |
| VM, Codex | 1 | 135 | 1320 | 2028 \* | 1708 | 500 | 740 |
| Container, Claude Code | 1 | - | 168 + 17 cache | 578 + 979 cache | 220 + 1086 cache | - | - |
| Container, Claude Code, installs at once | 10 | - | 168 + 17 cache | 599 (630) + 1065 cache | 221 + 1085 cache | - | - |
| Container, Codex | 1 | - | 97 + 521 cache | 501 + 1471 cache | 163 + 1517 cache | - | - |

\* In these runs each install started only after the previous VM's
finished (the start held its ssh session open; fixed for the item 4 kernel
runs), so the peak is the sample taken right after the install. Installs
took 33-37 s alone and 39-44 s with 10 at once.

- **Where a VM's memory goes:** guest page cache. The guest's own used
  memory (Claude Code, tmux, sshd, kernel) is ~215 MiB; after `npm install`
  the guest holds 1.37 GiB of page cache and the host holds all of it. A 2 GiB VM grows to 2 GiB on
  any file-heavy work and stays there until the guest frees memory.
- **Per-VM cost does not change from 1 to 10 VMs.** VM vs container: 3.6x
  idle (606 vs 168), 2x after a reclaim (430 vs 220). The container's page
  cache is shared between containers and dropped by the host under
  pressure; a VM's is private to the VM.
- **Codex holds twice Claude Code's memory** because on first start it
  copies its 426 MiB release into the home (see Other findings).
- **Home over virtio-fs** (first run: memory `shared=on`, default page
  reporting order, `drop_caches` instead of `memory.reclaim`): 123, 626,
  2040, 1866, then 1278 after `drop_caches`. The difference is the
  reporting order, below.

### Free page reporting: the order decides whether page cache comes back

`tests/fpr-check.sh`, `tests/fpr-cache.sh`, 2 GiB VM:

| Guest frees | `page_reporting_order` 9 (default, 2 MiB blocks) | 0 (4 KiB) |
| --- | --- | --- |
| A 1.2 GiB allocation, memory `shared=off` | 1.32 GiB -> 186 MiB in 13 s | not run |
| A 1.2 GiB allocation, memory `shared=on` | 1.31 GiB -> 172-178 MiB in 13 s; host `Shmem` falls with it (1.31 GiB -> 175 MiB), so the pages are freed, not just unmapped | not run |
| Its page cache after `npm install` (`drop_caches`) | 1.74 GiB -> 981 MiB at 20 s, 977 MiB at 60 s | 1.74 GiB -> 165 MiB at 20 s |

Freed page cache is scattered 4 KiB pages; at order 9 only whole 2 MiB
free blocks are reported, so most of it never returns. The guest must also
free the cache before anything returns: nothing does that on its own
without memory pressure, so the helper needs a proactive reclaim step
(`memory.reclaim` on the guest's root cgroup works on 6.18).

### Kernel same-page merging (KSM)

`tests/q1-ksm.sh`: the 10-VM run with memory `shared=off,mergeable=on`,
host KSM on (`pages_to_scan=2000`, `sleep_millisecs=20`). KSM only merges
private memory, so it rules out `shared=on` (Q2).

| Phase | Per VM | Merged away per VM | Net per VM |
| --- | --- | --- | --- |
| Idle after a turn | 593 | 360 | 233 |
| npm install peak | 1988 | 185 | 1803 |
| 60 s after | 1622 | 665 | 957 |
| Guest reclaim | 413 | 94 | 319 |
| Next turn | 549 | 276 | 273 |

ksmd used 49 s of CPU over the 290 s run: 17% of one CPU at this scan rate.

## Q2: save and restore

`tests/q2-save-restore.sh` via `tests/q2-matrix.sh`: one Claude Code turn,
then `ch-remote pause` + `snapshot file://...` + stop the monitor + `sync`
(save). For each restore mode: reset the root disk to its saved reflink
copy, evict the snapshot from the host page cache (`posix_fadvise
DONTNEED`), restart the mock and destroy every host socket to the VM (`ss
-K`; a server drops a sleeping client's connections), start
`cloud-hypervisor --restore source_url=file://...,memory_restore_mode=<mode>,resume=true`,
then time: monitor reports Running, ssh answers, the agent answers a new
prompt sent with `tmux send-keys`. Baseline: the same prompt on the live VM
before the save, 0.62-0.77 s. RSS is the monitor's.

| Memory | VM | RSS before | Save (snapshot call) | On disk | Mode | Running | ssh | Answered | RSS after / 30 s later |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `shared=on` | 1 GiB | 638 MiB | 0.44 s (0.22) | 633 MB | copy | 0.51 s | 0.92 s | 2.76 s | 639 / 590 MiB |
| | | | | | ondemand | 0.12 s | 0.85 s | 3.03 s | 639 / 589 MiB |
| | | | | | copyonwrite (falls back to copy) | 0.47 s | 0.73 s | 2.29 s | 638 / 592 MiB |
| `shared=on` + guest reclaim before save | 1 GiB | 634 -> 499 MiB | 0.40 s (0.19) | 494 MB | copy / ondemand / cow | 0.39 / 0.07 / 0.39 s | | 2.28 / 2.46 / 2.23 s | 532 / 517 MiB |
| `shared=on` | 2 GiB | 643 MiB | 0.92 s (0.37) | 638 MB | copy / ondemand / cow | 0.53 / 0.08 / 0.47 s | | 2.51 / 2.55 / 2.29 s | 657 / 609 MiB |
| `shared=on`, item 4 kernel | 2 GiB | 625 MiB | 0.47 s (0.25) | 620 MB | copy / ondemand / cow | 0.46 / 0.07 / 0.46 s | | 2.16 / 2.13 / 2.14 s | 630 / 598 MiB |
| `shared=on` + guest reclaim before save | 2 GiB | 654 -> 531 MiB | 0.41 s (0.21) | 525 MB | copy / ondemand / cow | 0.41 / 0.07 / 0.41 s | | 2.26 / 2.79 / 2.28 s | 553 / 534 MiB |
| `shared=on`, RAM filled with random data (worst case) | 2 GiB | 1.58 GiB | 0.98 s (0.51) | 1.6 GB | copy / ondemand / cow | 1.07 / 0.05 / 1.75 s | | 2.94 / 2.48 / 3.63 s | 1.61 / 1.56 GiB |
| `shared=off` | 1 GiB | 636 MiB | 0.46 s (0.22) | 1.1 GB (full) | copy | 0.74 s | 0.94 s | 2.10 s | 1030 / 982 MiB |
| | | | | | ondemand | 0.06 s | 0.80 s | 2.27 s | 1024 / 983 MiB |
| | | | | | copyonwrite | 0.08 s | 0.47 s | 3.56 s | 550 / 537 MiB (122 anon + 428 file-mapped) |
| `shared=off` | 2 GiB | 687 MiB | 1.80 s (0.43) | 2.1 GB (full) | copy | 1.82 s | 2.02 s | 3.24 s | 2.0 / 1.5 GiB |
| | | | | | ondemand | 0.05 s | 0.77 s | 2.42 s | 1.9 / 1.6 GiB |
| | | | | | copyonwrite | 0.07 s | 0.41 s | 3.52 s | 578 / 540 MiB |

- **`shared=on` (memfd) gives snapshots and restores sized to what the
  guest uses**: Cloud Hypervisor skips the memfd's holes, which free page
  reporting punches. With `shared=off` it writes and restores every byte of
  guest RAM, and `copy`/`ondemand` leave the host holding the full VM size
  (the guest does not re-report pages it reported before the save).
- `copyonwrite` applies to private memory only and falls back to `copy`
  with `shared=on` without logging it. With `shared=off` it keeps RSS low
  by mapping the snapshot file, but answers slowest (3.5 s).
- Most of the 1.5-2 s between Running and Answered is ssh plus the agent
  reconnecting after its kept-alive connection is reset; on the live VM the
  same prompt takes 0.6-0.8 s.
- Recommended: memory `shared=on`, `page_reporting_order=0`, a guest
  `memory.reclaim` before each save, restore mode `copy` or `ondemand`.

## Q3: save and restore with virtio-fs attached

`tests/q3-virtiofs.sh`: 1 GiB VM, memory `shared=on`, two shares (home at
`/root`, `work` at `/mnt/work`), virtiofsd 1.10.0 `--cache=auto
--sandbox=chroot`. Before the save a guest process holds a share file open
and appends every second, and a shell has its cwd in the share. virtiofsd
exits when the monitor stops (it serves one front-end), so the restore
starts a new one on the same socket.

| Check | Result |
| --- | --- |
| `ch-remote snapshot` with shares attached | ok, 125 MB |
| Restore and resume, guest processes alive, ssh | ok |
| Read a file written before the save | FAIL: `Bad file descriptor` |
| Process holding an fd keeps appending | FAIL: `Bad file descriptor` |
| File edited, created or deleted on the host while saved | FAIL: `Bad file descriptor` |
| List a directory, stat a file known before the save | FAIL: `Bad file descriptor` |
| Create a file after restore | FAIL: `Bad file descriptor` |
| Shell with cwd in the share | FAIL: `ls` gets `Bad file descriptor` |
| Home share read and write | FAIL: `Bad file descriptor`; sshd logs `Could not chdir to home directory /root` |

**No.** The guest's FUSE node IDs refer to inodes the new virtiofsd never
opened, and neither virtiofsd 1.10 nor Cloud Hypervisor carries that state
across. Item 2 measured the same on kernel 7.2.8 and showed a remount
cannot fix it while any process holds an fd or cwd in the share; the
coordinator moved the home to a guest FUSE file service over vsock (Q7).

## Q4: docker compose and a dev server across a save

`tests/q4-compose.sh`: 2 GiB VM, `dockerd` 29.1.3 in the guest
(`/var/lib/docker` on the root disk, overlay2 on ext4, iptables-nft), a
compose stack of `redis:7-alpine` and `nginx:alpine` publishing 8080, and a
Node HTTP server on 3000. Save, 30 s asleep, restore (`copy`).

| Check after restore | Result |
| --- | --- |
| Both containers running, same `StartedAt`, `RestartCount` 0 | yes |
| Redis key set before the save | `before-save` |
| nginx from inside the VM / from the host on `<vm>:8080` | 200 / 200 |
| `web` reaches `cache` by service name | yes |
| Dev server from inside the VM / from the host on `<vm>:3000` | same PID answers / same PID answers |
| A new container starts | yes |
| Guest clock | 31 s behind the host; nothing corrects it |

**Yes**, both survive. dockerd needs no legacy xtables kernel options: it
runs on iptables-nft over `NF_TABLES` + `NFT_COMPAT`.

## Q5: copy-on-write disks, and git

`tests/q5-disks.sh`, 4 GiB base disk of random data on /srv (XFS,
reflink=1).

Create (10 each):

| | Time | Space |
| --- | --- | --- |
| qcow2 overlay, `qemu-img create -f qcow2 -b base.raw -F raw` | 5 ms | 196 KiB |
| Reflink copy, `cp --reflink=always` | 1 ms | ~0 (10 overlays + 10 copies: 1 MiB) |
| Full copy, `cp --reflink=never` | 1.0 s + 3.5 s `sync` | 4 GiB |

I/O in a 2 vCPU VM, each disk fresh, fio `libaio --direct=1` in the guest,
all four files evicted from the host page cache first (`direct=off` run
only). IOPS for 4k QD32, MiB/s for 1M QD8:

| Disk | CH disk option | Random write, first / again | Random read | Seq write | Seq read |
| --- | --- | --- | --- | --- | --- |
| qcow2 overlay (`backing_files=on`) | `direct=on` | 3.2k / 3.4k | 11.5k | 274 | 411 |
| reflink copy, raw | `direct=on` | 19.8k / 63.5k | 175k | 2632 | 8752 \* |
| full copy, raw | `direct=on` | 140k / 144k | 156k | 4023 | 9022 \* |
| qcow2 overlay | `direct=off` | 19.9k / 25.4k | 203k | 1470 | 1568 |
| reflink copy, raw | `direct=off` | 215k / 255k | 237k | 1785 | 5417 |
| full copy, raw | `direct=off` | 230k / 247k | 266k | 1762 | 4120 |

\* Above the host's own direct sequential read (6.5 GB/s); not explained,
treat as unverified. With `direct=off` writes land in the host page cache.

- Item 4 traced the `direct=on` collapse: Cloud Hypervisor reads a qcow2's
  backing file synchronously in the queue thread. With `direct=off`, qcow2
  reads match raw; writes stay 8-10x slower than a raw copy.
- The first write to a reflinked block costs ~3x (XFS unshares it); the
  copy had 16k extents after the `direct=on` run.
- **Decision (coordinator, not this item):** qcow2 over a raw base with
  `direct=off`, one path. It works on ext4 hosts, where reflink is
  unsupported, and all sessions share one host page-cache copy of the base;
  heavy writes go to the raw data disk.

git on a 40,000-file tree (400 directories, 0.5-8 KB files, 205 MB),
`gc.auto=0`; cold = guest page cache dropped (not the host's):

| Where | `git add -A` (all new) | commit | `status` warm | `status` cold | `status` after 1000 edits | `add -A` after edits |
| --- | --- | --- | --- | --- | --- | --- |
| VM ext4 data disk (virtio-blk, raw) | 4.46-4.48 s | 130-158 ms | 28-53 ms | 72-82 ms | 30-36 ms | 207-257 ms |
| virtio-fs, `--cache=auto` | 27.2 s | 1.8 s | 175-347 ms | 397 ms | 235 ms | 902 ms |
| virtio-fs, `--cache=always` | 25.2 s | 1.7 s | 69-80 ms | 378 ms | 87 ms | 630 ms |
| Host XFS, no VM | 3.37-3.40 s | 117-121 ms | 19-22 ms | 20 ms (host cache kept) | 22 ms | 150 ms |

The checkout on the VM's disk is within 1.3-2.6x of the host; over
virtio-fs `git add -A` is 6x slower than on the VM's disk and `status`
1.5-12x.

## Q6: idle CPU

`tests/q6-idle-cpu.sh`: 10 sessions (1 vCPU, 1 GiB VMs; containers
`--cpus 1`), one agent turn, 60 s settle, then 120 s of the monitor's
utime+stime (all threads, vCPUs included) or the container cgroup's
`usage_usec`.

| Sessions | Guest kernel | Per session, % of one CPU | All 10, % of the 16-CPU host |
| --- | --- | --- | --- |
| 10 VMs, Claude Code idle | item 4 (HZ=250, NO_HZ_IDLE) | 0.53-0.62 | 0.37 |
| 10 VMs, Claude Code idle | mine (HZ=1000, NO_HZ_IDLE) | 0.58-0.70 | 0.39 |
| 10 VMs, no agent | item 4 | 0.25-0.28 | 0.16 |
| 10 containers, Claude Code idle | host | 0.19-0.22 | 0.13 |

An idle VM costs ~0.26% of a CPU; an idle Claude Code costs 0.20% in a
container and ~0.33% inside a VM (its timers exit to the host). HZ barely
matters with a tickless guest.

## Q7: home over a FUSE file service on vsock

The coordinator asked for this after Q3. [fusevsock/](fusevsock/) is a
read-only prototype: a guest FUSE filesystem (go-fuse v2, entry and attr
TTL 1 s, 16 pooled connections) that forwards lookup, getattr, readdir and
read by path to a host server on the VM's hybrid vsock socket. Not a design
proposal: no write path, no invalidation, no batching. `tests/q7-fusevsock.sh`,
2 vCPU VM, item 4 kernel.

Round trips, guest to host (p50 / p99):

| Reply size | vsock | TCP over the tap |
| --- | --- | --- |
| 64 B | 74 / 306 us | 77 / 206 us |
| 4 KiB | 74 / 139 us | 90 / 134 us |
| 128 KiB | 305 / 933 us | 345 / 941 us |

The 40,000-file tree, cold = guest page cache dropped:

| Where | Walk (`find -type f`) | Stat every file | Stat again after 2 s | Read all, cold | Read all again |
| --- | --- | --- | --- | --- | --- |
| Guest ext4 root disk | 18 ms | 74 ms | 245 ms | 2.2 s | 0.43 s |
| Local passthrough FUSE in the guest | 287 ms | 211 ms | 284 ms | 6.8 s | 3.0 s |
| fusevsock | 3.9 s | 3.8 s | 3.8 s | 15.6 s | 4.2 s |

Each uncached lookup costs about one round trip (~95 us), so a tree walk is
50x local. Fine for agent config and logins; slow for caches with
thousands of files (`~/.npm`, `~/.cache`). Longer TTLs with host-driven
invalidation and READDIRPLUS would cut the repeat cost; the first walk
stays one round trip per entry.

## Other findings

- **Codex copies its release into the home.** On first start Codex 0.164.0
  copies its 426 MiB release (308 MiB `codex`, 77 MiB
  `codex-code-mode-host`, 41 MiB `codex-voice-host`) to
  `~/.codex/packages/app-server-daemon/releases/0.164.0-x86_64-unknown-linux-musl/`,
  clones a plugins git repo into `~/.codex/.tmp/plugins`, and runs from the
  copy. A VM's page cache then holds both copies (423 MiB under
  `/usr/local/lib/node_modules`, 520 MiB under `/root`). With the home on a
  vsock file service, that copy and every page of the binary cross it.
- **Restoring one snapshot twice hangs TCP.** The guest resumes its
  kept-alive connections; if the host side moved on (an earlier restore of
  the same snapshot used them), the guest's segments are out of window, no
  reset comes, and the agent's request hangs ~2 min until TCP gives up.
  A single restore after a real sleep gets a reset and reconnects at once.
- **Cloud Hypervisor v54 needs `image_type=` on every `--disk`**
  (`Image type required for disk`) and `backing_files=on` to open a qcow2
  overlay's backing file.
- **Persistent taps survive restores.** A tap made with `ip tuntap add` and
  enslaved to the bridge before boot is reopened by name on restore.
- **PID 1 gets no `PATH`.** A shell init without one cannot start dockerd
  (`userland-proxy-path is not set`: it cannot find `docker-proxy`).

## Not measured

- **Enhanced (ACP) sessions.** Only the CLIs in tmux (Standard mode) ran;
  the ACP adapters add a Node process each.
- **Real model traffic and logins.** The mock answers in microseconds, and
  no logins may reach the host; agents talking to real APIs hold more
  context in memory.
- **Container memory at 5 sessions.** That run failed (a script bug, since
  fixed); 1 and 10 cover the range.
- **Memory with the home over the vsock file service.** The service is a
  read-only prototype; Q1's VMs keep the home on the root disk, Q1's first
  run on virtio-fs.
- **The sequential-read anomaly in Q5** (guest above host direct reads)
  is unexplained.
- **Host-wide memory deltas.** Other items' VMs ran on the host
  throughout.
