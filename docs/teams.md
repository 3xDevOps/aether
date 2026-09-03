# Running Aether with a team

One server, several people, everyone's agents side by side. Nothing here is a
separate mode: a solo setup is a team of one, and turning it into a team is
adding members.

Prerequisite: the server is up and someone has linked to it, which made that
person the admin. See [quickstart.md](quickstart.md).

## Joining

### Over a tailnet (primary)

The teammate joins your tailnet, then:

```sh
aether link my-server
```

That is the whole procedure. Tailscale proves who they are, the server
registers them as a **pending collaborator**, and an admin approves:

```sh
aether member list
```

```
ID                          NAME      ROLE          PENDING
01m04mfxyf0rhwegab83btsz2y  admin     admin
01m04mqes1z7wsdk4s90tx0pgg  dana      collaborator  pending
```

```sh
aether member approve 01m04mqes1z7wsdk4s90tx0pgg
```

Until then their commands fail with `membership pending admin approval`.
Approval is per person, once. `--tailnet-auto-join` on the server drops the
approval step entirely, for teams whose tailnet already is the team.

Full detail on tailnet identity, tagged nodes, and revocation is in
[networking.md](networking.md).

### By invite code (fallback)

For people connecting from outside a tailnet. An admin mints a one-time code:

```sh
aether invite --ttl 3600      # seconds; default 86400
```

The teammate redeems it, which registers their SSH key and burns the code:

```sh
aether link my-server:2222 --invite <code> --name "Dana"
```

```
linked to my-server:2222 as Dana (collaborator)
```

Invited members are collaborators immediately - no approval step, because the
code was the approval. Nobody needs shell access to the server box to join.

## Roles

Three capabilities on runs - **view**, **steer** (attach-write, inject,
approve, pause), **kill** - plus **push** to the workspace repo and
`workspace_admin`, bundled into roles:

| Role | Own runs | Others' runs | Workspace |
| --- | --- | --- | --- |
| viewer | - | view | read the feed |
| collaborator (default) | everything | view, steer, kill | launch runs, push, use templates |
| admin | everything | everything | members, workspaces, budgets, templates, settings, server self-update |

The viewer row is a real choice, not a placeholder: `aether member role <id>
viewer` assigns it. It is for the person who should watch the work and read the
feed without being able to start, steer, or kill anything.

**Everyone is a collaborator by default, on purpose.** Teammates can steer each
other's agents with zero setup; every privileged act is attributed in the
workspace timeline instead of being prevented.

The bootstrap identity is the admin and everyone who joins afterwards is a
collaborator, but an admin can change that:

```sh
aether member role 01m04mqes1z7wsdk4s90tx0pgg viewer
```

```
set 01m04mqes1z7wsdk4s90tx0pgg dana to viewer
```

`member list`, `member approve`, `member color`, `member role` and `member
remove` are the member surface. Two rules hold no matter what you type. **The
last admin can neither be removed nor demoted** - `refusing to delete the last
admin` and `refusing to demote the last admin` - because a server with no admin
has no way back. **A role change lands on connections that are already open**,
not at next login: the role is re-read from the store on every request, so a
demotion takes effect mid-session. A live write attach is re-checked every few
seconds and dropped when steer goes away - `detached: you can no longer steer
this run` - and `aether attach --read-only` still shows the terminal
afterwards. Removing a member ends every attach and live sync of theirs.

Setting someone to the role they already hold is a harmless no-op, and a
pending member's role can be changed before they are approved - approval and
role are separate questions.

Admin-only commands answer with a clear error otherwise:

```
aether: rpc error -32001: workspace.add requires the admin role
```

`aether server update` is gated the same way: `rpc error -32001: server.update
requires the admin role` for anyone but an admin. Any member can still read
`aether server update --status`. See [install.md](install.md#upgrading) for
the command.

## Workspaces

A workspace is the whole shared scope. It is a repo plus the container image
its agents run in, and everything the team shares hangs off it: runs, the
event feed, the approval inbox, presence, templates, schedules, costs and the
budget. One per project.

Creating one is an admin operation. `init` takes the server's neutral
image; `add` requires an administrator-approved one:

```sh
aether workspace init myproject [--image <image>] [--base <branch>]
aether workspace add myproject --image <image> [--base <branch>]
```

Two settings belong to the workspace rather than to any run in it:

- **The base branch** is what every new run's worktree is cut from. `--base`
  sets it at creation; it defaults to `main`.
- **The steering policy** decides whether collaborators may steer and kill
  each other's runs. It is permissive by default; an admin restricts it to
  owners and admins:

  ```sh
  aether workspace settings                              # show
  aether workspace settings --steer-others admins-only
  aether workspace settings --steer-others everyone      # back to the default
  ```

  The dashboard's Workspace settings dialog has the same switch. A member
  refused by it reads
  `workspace restricts steer of others' runs to their owner and admins`.
  `aether protect <run>` does the same for one run alone, whatever the
  policy; `aether unprotect <run>` lifts it. Both are for the run's owner or
  an admin, and both land on anyone already attached.

Scoped commands - `run`, `budget`, `cost`, `inbox`, `who`, `timeline`,
`template`, `schedule` - take `--workspace <name-or-id>` and default to the
only workspace when there is exactly one. That is why the solo path never
types it. With more than one they insist:
`--workspace is required when more than one workspace exists`.

Two commands sit outside that rule. `aether runs` takes no `--workspace` at
all: it lists every run you can see, so a teammate's work in another workspace
is never hidden from you. And `aether profile push --allow-secret` requires
`--workspace` outright, with no default, because the override is only worth
recording against a named timeline.

Every member links their own clone to the server:

```sh
aether link my-server --repo ~/code/myproject
```

which adds the `aether` git remote. Run branches (`aether/run-*`) are
server-owned - clients cannot force-push or delete them, because the branch is
the artifact. Every other branch behaves like a normal git remote.

## Working together

| Command | What it does |
| --- | --- |
| `aether runs` | Every run you can see, colored by owner, with conflict warnings. Prints a notice when any run is waiting on a human; `--attention` lists only those. |
| `aether who` | Who is online and which runs they are watching. |
| `aether attach [--read-only] <run>` | Raw PTY passthrough. Multiple people can attach at once; write access needs steer, and without it the attach falls back to read-only by itself. |
| `aether inject <run> "..."` | Push an instruction into a running agent. Renders as a banner in your member color. |
| `aether pause` / `resume` / `kill <run>` | Suspend, thaw, terminate. Worktree and transcript survive a kill. |
| `aether protect` / `unprotect <run>` | Limit steering and killing one run to its owner and admins, whatever the workspace policy says. |
| `aether handoff <run> <member>` | Transfer ownership, notification routing, and cost attribution. Overnight relay work. Refused for a viewer or a pending member, since neither can own a run. |
| `aether close <run> --outcome merged\|abandoned` | Clear a finished run off the attention board. |
| `aether inbox` | The shared approval queue; `aether inbox approve\|deny <request-id>` decides, and any steer-holder can. `--all` includes decided requests. |
| `aether timeline` | The workspace's whole history; filter with `--run`, `--member`, `--type`, `--limit`, export with `--jsonl`. |
| `aether cost --runs` | Token spend per member and per run. |
| `aether budget` | The workspace's spend cap and what has been used. |
| `aether sync --live <local-dir> <run>` | Live-overlay a local directory onto a run's worktree. Local edits that collide are preserved as `*.aether-conflict` files. |

### Task templates and schedules

A template is a saved launch: agent, task, mode, and parameters. Save one,
launch it by name, or put it on a cron schedule:

```sh
aether template save nightly-triage --agent claude --task "triage new issues" \
  [--mode headless] [--param key=value] [--budget <tokens>]
aether template list
aether run --template nightly-triage [--param key=value]
aether template delete nightly-triage
```

```sh
aether schedule list
aether schedule set nightly-triage "0 6 * * *"
aether schedule delete nightly-triage
```

Cron expressions are standard five-field syntax or an `@descriptor`, in UTC.
A schedule that was due while the server was down does not catch up; it waits
for the next occurrence.

### Attribution

Every member gets a stable color from a colorblind-safe palette at join time
(`aether member color <#rrggbb> [member-id]` overrides it). That color is the
same everywhere: run rows in `aether runs`, inject banners in transcripts,
timeline dots, overlapping diff hunks, dashboard cards. "Whose agent is doing
what" is meant to be answerable at a glance from any screen.

Every privileged act - steer, kill, approve, handoff, settings change - is
stamped into the workspace timeline with the actor. A server update is
stamped into every workspace's timeline, since it affects all of them.
Permissive by default, always attributed.

### Conflict radar

Per-run diff snapshots feed an overlap index. When two runs touch the same
file, `aether runs` shows it in the `OVERLAP` column and the dashboard puts a
chip on both run cards naming the file and the other member. It is early
warning, not locking - nothing is blocked.

On top of that, overlapping runs can message each other directly through their
agents (on by default; `--conflict-coordination=false` turns it off). See
[coordination.md](coordination.md) and [mcp-bridge.md](mcp-bridge.md).

### Budgets

```sh
aether budget                                  # show
aether budget set --limit 25 --warn 20         # hard cap and soft warning, in USD
aether budget set --override                   # admit new runs past the cap
aether budget set --override=false             # and stop again
```

An omitted flag keeps its current value, so `--warn` alone edits only the
warning threshold. `--limit 0` clears the budget.

At the cap, new runs are refused and running runs finish. Note that runs whose
harness reports no token usage are counted as *unmetered* - `aether cost` says
so explicitly, and the totals are a floor rather than the real spend.

## Agent setup is per person

Logins never move between people. Each member runs `aether agent add <name>`
once (`aether setup <harness>` re-runs just the login later),
completes the vendor's own login flow in the container Aether hands them,
and their login state lives in a per-member server-side home mounted into
their runs. All of one member's runs share one login; nobody else's do. See
[harnesses.md](harnesses.md).

Agent *configuration* - skills, plugins, custom commands - syncs one way from
each member's laptop with `aether profile push` (and automatically, if the
local daemon is running). Secrets are excluded twice over: a per-harness
credential denylist plus a client-side content scan that blocks pushes
containing key material. Nothing ever syncs back down.
