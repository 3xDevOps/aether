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
approve, pause), **kill** - plus session administration, bundled into roles:

| Role | Own runs | Others' runs | Session |
| --- | --- | --- | --- |
| viewer | - | view | read the feed |
| collaborator (default) | everything | view, steer, kill | launch runs, create sessions, use templates |
| admin | everything | everything | members, workspaces, budgets, templates, settings |

**Everyone is a collaborator by default, on purpose.** Teammates can steer each
other's agents with zero setup; every privileged act is attributed in the
session timeline instead of being prevented.

What v1 actually ships: the bootstrap identity is the admin, everyone who joins
afterwards is a collaborator. There is no command to promote, demote, or assign
the viewer role yet - `member list`, `member approve`, `member remove` and
`member color` are the member surface. Removal refuses to delete the last
admin.

Admin-only commands answer with a clear error otherwise:

```
aether: rpc error -32001: workspace.add requires the admin role
```

## Workspaces and sessions

- **Workspaces are admin-owned.** `aether workspace add <name> --image <image>`
  defines a repo and the container image its agents run in. One per project.
- **Sessions are collaborator-owned.** `aether session new <name> --workspace
  <name-or-id>` creates the shared context runs live in: members, event feed,
  templates, budget. A session pins a base branch (`--base`, default `main`).

Most commands take `--session`; with exactly one session they default to it.
That is why the solo path never mentions sessions.

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
| `aether runs` | Every run in the session, colored by owner, with conflict warnings. Prints a notice when any run is waiting on a human; `--attention` lists only those. |
| `aether who` | Who is online and which runs they are watching. |
| `aether attach <run>` | Raw PTY passthrough. Multiple people can attach at once; write access needs steer. |
| `aether inject <run> "..."` | Push an instruction into a running agent. Renders as a banner in your member color. |
| `aether pause` / `resume` / `kill <run>` | Suspend, thaw, terminate. Worktree and transcript survive a kill. |
| `aether handoff <run> <member>` | Transfer ownership, notification routing, and cost attribution. Overnight relay work. |
| `aether close <run> --outcome merged\|abandoned` | Clear a finished run off the attention board. |
| `aether inbox` | The shared approval queue; any steer-holder can decide. |
| `aether timeline` | The session's whole history; `--jsonl` exports it. |
| `aether cost --runs` | Token spend per member and per run. |
| `aether budget` | The session's spend cap and what has been used. |

### Attribution

Every member gets a stable color from a colorblind-safe palette at join time
(`aether member color <#rrggbb> [member-id]` overrides it). That color is the
same everywhere: run rows in `aether runs`, inject banners in transcripts,
timeline dots, overlapping diff hunks, dashboard cards. "Whose agent is doing
what" is meant to be answerable at a glance from any screen.

Every privileged act - steer, kill, approve, handoff, settings change - is
stamped into the session timeline with the actor. Permissive by default, always
attributed.

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

Logins never move between people. Each member runs `aether setup <harness>`
once, completes the vendor's own login flow in the container Aether hands them,
and their login state lives in a per-member server-side home mounted into
their runs. All of one member's runs share one login; nobody else's do. See
[harnesses.md](harnesses.md).

Agent *configuration* - skills, plugins, custom commands - syncs one way from
each member's laptop with `aether profile push` (and automatically, if the
local daemon is running). Secrets are excluded twice over: a per-harness
credential denylist plus a client-side content scan that blocks pushes
containing key material. Nothing ever syncs back down.
