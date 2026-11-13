# Sessions in Cloud Hypervisor VMs

**Goal:** every session runs in its own virtual machine instead of a Docker
container. An idle session is saved to disk with every process still
running and restored on its next use, for every agent and session type.
Docker and Sysbox leave the server once nothing runs on them.

**Status:** approved by the owner on 2026-11-13. Decisions dated 2026-11-13
are the owner's. Everything marked *Open* is settled by the first
measurement (work item 1).

## Decisions (2026-11-13)

- The VM monitor is [Cloud Hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor).
  It can share a host folder into a VM (virtio-fs), save and restore a
  running VM, and accept live migration from its two previous releases.
- The member home stays one folder on the server, shared live into every
  VM of the member. Logins, token refreshes and agent settings keep a
  single copy, as today ([environment-home.md](../environment-home.md),
  [teams.md](../teams.md#agent-accounts)).
- The checkout moves onto the VM's own disk. Git work on it runs inside
  the VM; the server keeps the bare repo, session branches, diff history,
  evidence and transcripts. The server never mounts a VM's disk.
- Work lands on `main` behind one server setting, `session-runtime`
  (`docker` until the cutover, then `vm`). Docker stays the default until
  work item 13.

## A session VM

| Part | What it is | Lifetime |
| --- | --- | --- |
| Kernel | An Aether-built guest kernel shipped with each server release | Per release |
| Base disk | The session's image (standard, a saved member environment) flattened to a read-only disk | Until the image is swept |
| Root disk | A copy-on-write layer over the base disk | Disposable; rebuilt on a cold start |
| Data disk | `/workspace`, `/repos/<name>`, `/tmp`, `/var/lib/docker` | Until the session is deleted |
| Home | The member's home folder, shared live from the server | The member's |
| Saved state | The VM's memory and device state while it sleeps | Until the next wake |

Processes inside the VM:

- An in-VM helper, `aether-internal guest`, runs as PID 1. It mounts the
  filesystems, configures the network from the kernel command line, starts
  `dockerd` for sessions that nest Docker, runs the setup script and then the
  session's command as the session user, and reaps orphans.
- The helper serves one API to the server over the VM's host socket
  (vsock): start a process with argv, environment, user, directory and a
  terminal or pipes; stream its input and output; resize; signal; wait; and
  attach to the session's main process. It also forwards the coordination
  and signing sockets into the VM and reports memory, CPU, process count and
  out-of-memory kills.
- Everything Aether runs in a session today (`dev-exec`, `acp-keeper`,
  the agent, the MCP helper) runs unchanged on top of that API.

The VM monitor of each session runs in its own systemd unit inside
`aether.slice`, outside the server's process tree, so sessions keep running
through server restarts and updates. The server finds them again by run ID.

## Lifecycle

| Today (Docker) | VM |
| --- | --- |
| create, start | Build the root and data disks, start the VM; the helper runs the setup script, then the command |
| `docker exec`, attach | The helper API |
| pause on close | Save the VM to disk; the monitor exits |
| idle sleep (`docker stop`) | Save the VM to disk; the monitor exits |
| wake | Restore the saved VM. If it cannot be restored (another monitor or kernel version, a deleted file), cold-start from the disks and resume the conversation, as today |
| stop, destroy | Stop the VM; delete the root disk; the data disk follows the checkout retention |
| `docker commit` (Save environment) | Save the environment VM's root disk as a new base disk |

After a restore the helper corrects the clock and the server reconnects its
streams. Sessions sleep only between turns, so no model request is cut.

## Network

Each VM gets one tap device on an Aether bridge. VMs cannot reach each
other. A session with egress control reaches only the server's egress
proxy, which recognises a VM by its address. The cloud instance metadata
address is blocked. Port forwards dial the VM's address or relay through the
helper. The development browser runs inside the session VM.

## Checkout inside the VM

Until work item 6 lands, the host checkout directory is shared into the VM
the same way as the home, so the git engine works unchanged. Work item 6
then moves the checkout onto the data disk:

- A prepared disk per workspace and base commit; each session's checkout is
  a copy-on-write clone of it.
- The helper watches files and builds diff snapshots, the live patch, Files
  reads, writes and uploads, the final commit, subsession fetches and
  evidence. It sends git objects to the server; the server fetches session
  branches from the VM.
- A sleeping or ended session is shown from its last diff snapshot. Writing
  to one wakes it. Cleanup steps that need the checkout run before the VM's
  data disk is discarded, or in a VM started briefly for them.

## Resources

- vCPUs and memory come from the session's limits. Guest memory returns to
  the host through free page reporting and page-cache reclaim inside the
  guest.
- The launch capacity check counts VM memory.
- Hosts need `/dev/kvm`: bare metal, or a cloud instance with nested
  virtualization (on AWS, the Intel 7th and 8th generation families listed
  in the [EC2 guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nested-virtualization.html)).
  Saved VMs need local NVMe storage to wake quickly.

## Work items

Each item is one pull request through the merge gate, except item 6, which
may take several. Items in a wave run in parallel.

| # | Wave | Item | Owns |
| --- | --- | --- | --- |
| 1 | 1 | Measure on a KVM host | Scripts and results only, no product code |
| 2 | 1 | VM engine: the `runtime.Runtime` backend, monitor units, save and restore, limits, stats, capacity | `internal/runtime/vm` |
| 3 | 1 | In-VM helper and its API | `internal/guest`, `aether-internal guest` |
| 4 | 1 | Kernel, base disks from images, copy-on-write root disks | `internal/vmimage`, kernel build |
| 5 | 2 | Network: taps, isolation, egress, metadata block, port forwards | `internal/runtime/vm` network, `internal/egress` |
| 6 | 2 | Checkout inside the VM | `internal/gitengine`, the helper's git service |
| 7 | 2 | Home and caches shared live; Isolated tier | mounts, `internal/overlay`, ownership |
| 8 | 2 | Sleep and close save the VM; wake restores; fallback; agent wakeups held by the server | `internal/scheduler` sleep paths |
| 9 | 2 | Environment terminal and Save environment, assistant, agent updater, dictation, browser, cleanup helper, sudo and tiers | their scheduler and runtime files |
| 10 | 3 | Install, update, storage, backups, AWS host guide | `cmd/aether-server`, `internal/serversetup`, docs |
| 11 | 3 | Integration, dashboard end-to-end and real-agent suites on VMs; CI with KVM | tests, `.github` |
| 12 | 3 | Dashboard, CLI and docs wording | `web/`, `cmd/aether`, `docs/` |
| 13 | 3 | Security review, cutover, removal of Docker and Sysbox | everything Docker-specific |

Items 2, 3 and 4 meet at two Go interfaces. Item 3 owns the helper API
definition and its client; item 4 owns the image store (base disk for an
image, new root disk for a session, kernel path). Item 2 builds on both.

## Open

- Host memory per awake session, with free page reporting on.
- Whether Cloud Hypervisor saves and restores a VM with a virtio-fs share
  attached. If it does not, the home and caches reach the VM through the
  helper's file service instead.
- Save and wake times on local NVMe, and whether nested Docker containers
  and a dev server survive a save.
- The copy-on-write format for root and data disks (qcow2 backing files or
  reflink copies) on the filesystems servers use.
