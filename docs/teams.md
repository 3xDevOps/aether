# Running Aether with a team

One Aether server can serve several people. Each person is a **member** with a
**role**, and everyone's agents run side by side in the same **workspaces** (a
workspace is one repository plus its settings). A solo setup is a team of one,
so there is nothing to switch on: you add members.

You need a running server and a first member, who is the admin. See
[quickstart.md](quickstart.md). New terms are in the
[glossary](README.md#concepts).

## Join the server

There are three ways to join. Pick the one that matches how your team reaches
the server.

| Method | Use it when | Who approves |
| --- | --- | --- |
| Tailnet | Everyone is on your Tailscale network | An admin, once per person |
| Edge | People sign in with GitHub through an [edge](edge.md) (a relay that lets people reach the server without sharing a network) | The invitation, then a device approval |
| Invite code | A person has neither, and you can send a code | Nobody: the code is the approval |

### Over a tailnet

The server must have tailnet sign-in on. `aether-server setup` turns it on when
you answer yes to its tailnet question ([install.md](install.md#first-boot)).
The teammate joins your tailnet and runs:

```sh
aether link my-server
```

Tailscale proves who they are, and the server adds them as a pending
collaborator. `aether link` prints `linked to <server> as <name> (collaborator,
pending); an admin runs: aether member approve <member-id>`. Until an admin
does, their commands fail with `membership pending admin approval; ask an admin
to run: aether member approve <member-id>`.

```sh
aether member list              # the PENDING column marks who waits
aether member approve mem-rbekp4h6fx
```

Start the server with `--tailnet-auto-join` to skip approval when your tailnet
already is your team. [networking.md](networking.md) covers tailnet identity and
revocation.

### Through an edge

An admin invites a teammate by GitHub login, or by the verified primary email of
their GitHub account:

```sh
aether invite --github dana --role collaborator
aether invite --email dana@example.com --role viewer
```

`--role` is `viewer`, `collaborator` (the default) or `admin`. The command prints
an invite link of the form `https://<edge host>/join/<server id>/<invitation
id>#<secret>`. It is shown once. In the dashboard, **Members → Invite…** shows it
with **Copy link**. Send it to the teammate, who opens it and selects **Join** in
the desktop app, or in a terminal runs:

```sh
aether login            # or aether login --json, for a script or coding agent
aether link '<invite link>'
```

What happens next depends on the server's `edge-access` policy
([edge.md](edge.md#access-policies)). Under `approved-devices` (the default), the
teammate's device waits until an admin approves it from a **Needs you** card or
[the commands below](#approve-devices), and `aether link` prints `waiting for an
admin of <server> to approve this device (<label>, <fingerprint>)…`. Under
`account`, signing in admits the device and creates the member at once.

An invitation expires after 7 days, and an invited login or email matches only
within 24 hours of the teammate's last GitHub sign-in at the edge. If you see
`open this edge in a browser to confirm them`, open any edge page to sign in
again. List and revoke invitations with `aether invite list` and `aether invite
revoke <invitation-id>`.

| Error | Meaning |
| --- | --- |
| `<account> is invited to this server; open the invitation link the admin sent, or run: aether link <link>` | They ran `aether link <server id>` instead of using the link |
| `the invitation link is not valid for this server: wrong, revoked or expired; ask the admin for a new one` | The link was used, revoked or expired |

### Approve devices

Each client install is a **device** with its own key. Under `approved-devices`,
every new device waits for approval, a member's own second laptop included.
Approve from the dashboard's **Needs you** card, or:

```sh
aether device list                         # yours; an admin sees everyone's
aether device approve <code or device-id>
aether device revoke <device-id>
sudo aether-server device review           # on the server
```

`aether device approve` shows who the device would be admitted as and asks
`approve it? [y/N]`. Check the member and role first: whoever holds an approved
device gets that member's access. There is no flag that skips the question.
Any answer other than `y`, or no answer on stdin, fails with `device approve:
not approved; a person must check the device shown above and confirm with y in
their own terminal: aether device approve <code or device-id>, or approve it
from the dashboard's Needs you card`. If the only admin has no approved device,
the refusal includes a code, and the admin runs `sudo aether-server device
approve <code>` on the server. At most 10 devices wait per account.

### By invite code

For someone with neither a tailnet nor a GitHub sign-in. An admin mints a
one-time code, and the teammate redeems it, which registers their SSH key:

```sh
aether invite --ttl 3600      # seconds; default 86400
aether link my-server:2222 --invite <code> --name "Dana"
```

The member is a collaborator at once, because the code was the approval. A code
stops working when the admin who minted it stops being an admin.

### Link or merge sign-ins

One person can reach the server through a tailnet login, an SSH key and GitHub
accounts. They join one member only through an explicit step, never by matching
emails or names. Add a GitHub account to your own member with **Profile →
Sign-ins → Link a GitHub account** or `aether member link --github dana`. Open the
link it prints while signed in with that account, then approve the new device
from your existing sign-in.

Someone who already became two members can merge them. Run `aether member merge
start` on one sign-in, which prints a code valid for 10 minutes. On the other,
run `aether member merge <code>` (options `--keep <member id>`, `--keep-admin`,
`--yes`), then `aether member merge accept` back on the first. Both show what is
discarded before you confirm. The survivor takes the other member's runs,
schedules, devices and accounts, and the lower of the two roles. The absorbed
member's home moves to `<data-dir>/merged/<merge id>/home` and their private
signing key to `signing-key` beside it. Both hold credentials, so remove the
directory with `sudo rm -r` once nothing in it is needed. A merge is
refused, with the reason, when either member is pending or has an unfinished run
or terminal. Nothing undoes a merge.

### Remove access

`aether member remove <id>` deletes a member, their environment and their
private signing key, and closes their connections. `aether device revoke <id>` refuses a device key everywhere.
`aether member unlink <member-id> github:<user id>` unbinds one GitHub account,
revokes the devices that signed in with it and deletes the connectors its
sign-in authorized. `aether invite revoke <id>` kills
a link.

`aether member remove` fails with `in use` while runs or schedules still belong
to the member. Delete those first. `aether member transfer <member id>` makes
another admin the server's owner at its edge.

### Single sign-on

Aether has no identity provider settings of its own: sign your tailnet in
through yours, such as Okta, Google Workspace, Microsoft Entra ID or GitHub
([Tailscale's providers](https://tailscale.com/docs/integrations/identity)), and
have people join [over the tailnet](#over-a-tailnet). Each such member is a
login from your provider, and the `tailnet-auto-join` setting
([install.md](install.md#server-options)) admits them without approval. A user
you [share](https://tailscale.com/docs/features/sharing) the server's machine
with signs in through their own provider.

Removing someone in your provider stops new sign-ins, not devices already
signed in. Those reach the tailnet until the user is suspended in Tailscale or
their keys expire. Suspend them in the Tailscale admin console, or let [user and
group provisioning](https://tailscale.com/docs/features/user-group-provisioning)
do it when you deactivate them in Okta, delete them in Microsoft Entra ID, or
suspend them in Google Workspace.

That closes the tailnet path only. Their SSH key, approved devices (which also
work on the direct SSH port), GitHub sign-ins at the edge and ChatGPT or Claude
connectors stay open. Remove the member to close them all:

```sh
aether member list                 # their member id
aether member remove <member-id>   # deletes their SSH key, devices, GitHub sign-ins and connectors
```

It fails with `in use` until their runs and schedules are gone
([Remove access](#remove-access)).

## Roles

| Role | Own runs | Other members' runs | Workspace |
| --- | --- | --- | --- |
| viewer | Cannot launch | View | Read the feed |
| collaborator (default) | Everything | View, message, steer, stop | Launch runs, push, use templates, set schedules, add memory notes |
| admin | Everything | Everything | Also members, workspaces and their settings, budgets, saving templates, `aether server update` |

Steering a run means typing into its terminal, messaging its agent, answering
its permission prompts and pausing or resuming it. Pushing means writing to the
workspace's git repository. Everyone who joins is a collaborator, so teammates
can message and stop each other's agents with no setup. Every such act is
recorded in the workspace timeline with who did it. Change a role with
`aether member role <id> viewer|collaborator|admin`. It applies to connections
that are already open: a demoted member's live attach drops with
`detached: you can no longer control this run`. Member approvals,
removals and role changes, account shares and connector writes are kept in the
audit history; `aether audit` shows yours, or everyone's to an admin
([connectors.md](connectors.md#audit-history)).

The last admin cannot be removed or demoted: `refusing to delete the last admin`
or `refusing to demote the last admin`. Admin-only commands fail for others with
`aether: rpc error -32001: workspace.add requires the admin role`.

To limit who can control runs:

```sh
aether protect <run>                                    # owner and admins only
aether workspace settings --message-others admins-only  # every member's runs
aether workspace settings --message-others everyone     # the default
```

A refused member sees `workspace restricts steer of others' runs to their owner
and admins`. `aether unprotect <run>` lifts a protection.

## Workspaces

A workspace is a repository plus its settings. Runs, the activity feed,
templates, schedules, costs and the budget all belong to one. A
run and its shells use the launching member's saved image, or the server's
standard image ([environments.md](environments.md)).

An admin creates a workspace. In the dashboard, open **Manage workspaces** from
the workspace selector, select **Add workspace**, choose **GitHub**, pick a
repository you can read and accept its first revision. From the CLI:

```sh
aether workspace import ~/code/myproject
aether workspace import https://github.com/acme/myproject
aether workspace add myproject [--base <branch>]
```

`import` and `add` refuse a name already in use and print its id.

Each member links their own clone, which adds an `aether` git remote.
`aether workspace import <path>` links a clone whose `origin` matches the
workspace's publish destination; otherwise run
`aether link my-server --repo ~/code/myproject --workspace myproject`. The
client keeps one linked clone per workspace: linking another clone of it
replaces the first, and `aether status` lists them.

Branches named `aether/run-*` belong to the server and cannot be force-pushed or
deleted. Every other branch behaves like a normal git remote. `aether pull <run>`
brings a run's branches into the linked clones.

| Setting | Default | What it does |
| --- | --- | --- |
| Base branch | `main` | New runs branch from it. Set with `--base` at creation |
| Publish destination (**Publish changes to**) | The `origin` of the first clone linked | The git URL each run checkout gets as `origin`, where run branches are pushed for review |
| Source mirror | Off (local-only) | Follows an upstream repository read-only |
| Message-others policy | `everyone` | Whether collaborators can message and stop each other's runs |

A run can also check out other workspaces beside its own; see [Working across
repos](quickstart.md#working-across-repos).

A later clone that points elsewhere does not overwrite the publish destination.
Change it on purpose; a change reaches new runs only:

```sh
aether workspace origin --workspace myproject https://github.com/acme/myproject.git
aether workspace origin --workspace myproject --clear
```

A `git@github.com:` address is stored as its HTTPS form, because runs can
authenticate over HTTPS only.

### Source mirror

A local-only workspace lets collaborators push the base branch directly. A
mirror makes an upstream repository own it instead. An admin configures one:

```sh
aether workspace mirror configure --workspace myproject \
  --source https://github.com/acme/myproject.git --branch main --auth github
aether workspace mirror refresh --workspace myproject
```

`--auth` is `public`, `github` (your own GitHub login on the server, read access
is enough) or `deploy-key` (Aether prints a public key to add as a read-only
deploy key). A new mirror starts pending: verify it and adopt its first revision
before the first run. Fast-forward updates apply on their own. If upstream
rewrote history, the base stays put until an admin reviews the candidate and runs
`aether workspace mirror adopt --workspace myproject --generation <n> --commit
<reviewed-sha> --yes`. `aether workspace mirror status` shows where it stands.

Pushing a mirrored base branch is rejected. Update the upstream and refresh:

```
aether: rejected write to protected ref refs/heads/<base>: this mirrored base is owned by the upstream; use the configured upstream to update it
```

A scheduled run refreshes the mirror first and creates no run if that fails. See
[security.md](security.md#workspace-source-mirrors).

### Delete a workspace

`aether workspace delete myproject --yes` removes the workspace with its runs,
checkouts, transcripts, templates, budget and costs. Member accounts,
local clones and the upstream repository stay. It never stops work: stop active
runs and remove schedules first. The error names what blocks it.

## Work together

Commands that depend on a workspace take `--workspace <name-or-id>` and default
to the only workspace. With several, they insist: `--workspace is required when
more than one workspace exists`. `aether runs` lists runs from every workspace.

| Command | What it does |
| --- | --- |
| `aether runs` | Every run you can see, colored by owner, with an overlap column. `--needs-you` lists only runs that need you |
| `aether who` | Who is online and which runs they watch |
| `aether attach [--read-only] <run>` | Attach to a run's terminal. Several people can attach at once |
| `aether message <run> "..."` | Message a run's agent. It shows as a banner in your color |
| `aether pause`, `resume`, `kill <run>` | Suspend, thaw or stop a run. The checkout and transcript survive a kill |
| `aether runs --needs-you` | Runs waiting on you. `aether runs <run-id>` shows what one needs and the command that answers it |
| `aether timeline` | The workspace history. `aether timeline <run-id>` shows one run's. Filter with `--member`, `--type`, `--limit`. `--jsonl` exports |
| `aether changes` | Run changes since a cursor: status, reports, mail, pull requests and lifecycle. `--follow` keeps polling ([coordination.md](coordination.md#follow-changes-from-a-cursor)) |
| `aether cost --runs` | Token spend per member and run |

The dashboard shows each run as **Needs you**, **Working**, **Paused**, **Done**
or **Failed**. **Needs you** means something blocks the run that you can
resolve, such as a permission or question, a stalled run of yours, or an unreviewed
finish. Everyone else sees that run as **Working** with
"Waiting for Alice" ([failure-handling.md](failure-handling.md#needs-you)).

Answer an agent's plan or permission prompt in the run's Session view
(Enhanced) or its terminal (Standard).

### Hand off and finish runs

`aether handoff <run> <member>` changes the run's owner and notifications at
once, with no acceptance step. The agent account, cost attribution and the
container's home stay the launcher's.

When a run is handed off or finishes, Aether saves an **evidence packet**: the
objective, base and final git revisions, changed files and a transcript copy. It
records what Aether observed and does not prove a command succeeded or that
anyone reviewed the result. Packets expire after 30 days, and a run whose
checkout exceeds 64 MiB gets no finish packet. If saving fails, Aether keeps the
checkout and transcript instead of deleting them.

```sh
aether close <run> --outcome merged|abandoned   # clear a finished run off Needs you
aether archive <run>                            # hide a finished run; unarchive restores it
aether delete <run>                             # stop it and remove its checkout and records
```

These need the same permission as stopping a run. A published run branch stays in
the workspace repository after `delete`.

### Task templates and schedules

A template is a saved launch: agent, task, mode, model, effort and parameters. An admin
saves and deletes templates. Any collaborator launches them and sets schedules.

```sh
aether template save nightly-triage --agent claude --task "triage new issues" \
  [--mode standard|enhanced|background] [--model <model>] [--effort <level>] \
  [--param key=value] [--budget <usd>]
aether run --template nightly-triage [--param key=value]
aether schedule set nightly-triage "0 6 * * *"
```

`--mode` defaults to `background`, and `--budget` is an advisory per-run cost
hint in USD. Use `{{name}}` in the task for a parameter. The dashboard's
Templates page fills parameters inline before **Launch** and edits their
defaults; any run's composer can insert a filled-in template as a draft. Cron is five-field
syntax or an `@descriptor`, in UTC. A schedule that was due while the server was
down waits for its next occurrence. `aether template list|delete` and `aether
schedule list|delete` manage the rest. A `--model` the agent has no flag for
fails with `scheduler: agent "<agent>" has no model flag; launch it without a
model`, and an `--effort` it does not take fails with the error in
[harnesses.md](harnesses.md#choose-an-effort).

### Attribution

Each member has a color, shown on run rows, message banners and timeline dots.
Change yours with `aether member color <#rrggbb>` and your display name under
**Profile → Display name**. Every privileged act (message, stop, approve, hand
off, change settings) is recorded in the timeline with who did it.

Set your git identity under **Profile → Git identity**, or `aether member git
--name "Dana Lee" --email dana@example.com`. The agent in a run commits as the
member who launched it, fixed when the container starts. Aether's own commits
(when a run finishes, is stopped or is recovered) use the run's current owner.
With no identity set, commits use your display name and `<member-id>@aether.local`,
which matches no GitHub account. Signed commits show **Verified** on GitHub only
if the commit email is verified there
([security.md](security.md#github-credentials-and-signing-keys)).

Anyone who messages a run is added to its co-authors. Aether's commits end with a
`Co-authored-by:` line for each, and the run's agent finds the list in
`/run/aether/co-authors` ([coordination.md](coordination.md)).

### Conflict radar

When two runs change the same file, `aether runs` shows it in the `OVERLAP`
column and the dashboard puts a warning on both cards that opens the shared
files. It warns and does not block. Changes merged in from `main` do not count.

Agents in one workspace can also message each other. `aether-internal status`
inside a run lists the runs it can reach, and protection and the message-others
policy limit this the same way. Every message appears in **Activity** as an
**Agent message** ([coordination.md](coordination.md#seeing-agent-messages)).

### Budgets

```sh
aether budget                                  # show
aether budget set --limit 25 --warn 20         # hard cap and soft warning, in USD
aether budget set --override                   # admit new runs past the cap
aether budget set --override=false             # and stop again
```

An omitted flag keeps its value, and `--limit 0` clears the budget. At the cap,
new runs are refused and running runs finish. Claude Code reports spend in
Background and Enhanced runs, and OpenCode and omp in Enhanced runs and
Background runs over ACP. An Enhanced run's spend counts as each turn ends.
Every other run is unmetered, so totals are a floor. Deleting a run or a member
keeps their spend in the totals. Only an admin sets a budget. Every admin is
[notified](networking.md#notifications) once when spend reaches the warning and
once at the cap; a budget change that puts spend back below either re-arms it.

### Team memory

Runs and members keep shared notes, and every new run is given the pinned ones.
See [memory.md](memory.md).

## Agent accounts

Each member signs in to each agent once, in their own environment (`aether agent
add <name>`, then `aether terminal`). That home is mounted into every run they
launch. A member's **agent account** is that vendor login and the subscription
behind it. One member can let a collaborator launch runs on theirs.

The owner shares under **Profile → Agent account sharing → Share account**. The
recipient selects **New run → Options → Account** and picks `<name> (shared)`
From the CLI:

```sh
aether account share <member-id>      # owner
aether account list
aether run "triage the failures" --agent codex --account <owner-member-id>   # recipient
aether account revoke <member-id>     # owner
```

A run on a shared account belongs to whoever launched it and uses their image,
home, git identity and GitHub login. The shared account supplies the agent login,
the vendor quota and the cost attribution. The recipient needs neither the agent
nor a login of their own, but must connect their own GitHub first, because the
run pushes and opens pull requests as them. [security.md](security.md#account-sharing)
lists exactly what is shared.

> [!WARNING]
> `omp` shares the owner's whole `~/.omp/agent` directory. A recipient's run can
> plant code that runs in the owner's own omp sessions with the owner's home, gh
> token and signing key. Share an `omp` account only with someone you would give
> your home to.

- Sharing is one-way. Admins get no implicit access to anyone's account.
- Revoking blocks new launches, but runs already going continue. Stop them with
  **Kill run** in the run header's **More** menu, or `aether kill <run-id>`.
- An agent you defined with `aether agent add` runs only on your own account. A
  shared account shows it disabled as **Your account only**.
- An owner with no login shows **Not logged in**. A CLI launch fails with `Grace
  is not logged in to claude: no login at ~/.claude/.credentials.json in their
  home; Grace logs in to claude in their own environment terminal`.
- Every member's containers mount their own Claude Code and Gemini login files
  in place, so a token refresh rewrites the file instead of replacing it. A
  server on Docker Engine older than 26.0 mounts none, and cannot share either.
  Recipients see the owner's refreshed login, and a run on a shared account
  keeps the owner's login mounted. A container started before v0.7.7-beta.1
  can still replace its member's login file, which detaches the owner's login
  from that member's runs on shared accounts: those runs then sign in as the
  member. Restart such environments with **Stop environment** in **Profile**,
  or `aether terminal stop`, and relaunch such runs.
- Before the agent starts, the server checks that the container sees the
  owner's login file itself, not the recipient's own. Otherwise the launch, or
  the move to the account, fails with `runtime: /root/.claude/.credentials.json
  in the container is inode <n>, not .claude/.credentials.json in <owner's
  home> (inode <m>); Docker lists the mount, so container runtime "<name>" did
  not apply it`, or `...; Docker did not mount it (container runtime
  "<name>")`.
- Shared accounts need Docker Engine 26.0 or newer on the server, or the launch
  fails with `runtime: docker engine API "1.44" cannot mount a path beneath a
  member home; that needs API 1.45 (Docker Engine 26.0) or newer`.

### Usage fallback

When a `claude` or `codex` launch's account has used 95% or more of its
subscription quota, Aether starts the run on another account with usage left.
Other agents always launch as requested, and so does an account whose usage
cannot be read (an API-key login, signed out, or a vendor read over 3 seconds).

Candidates are your own account and every account shared with you whose owner is
logged in to the agent. The one with the most usage left wins. In the launch
dialog, **Options → Fall back to another account** turns it off and lists
**Fallback accounts**, which you untick or reorder. From the CLI:

```sh
# If Grace's account is exhausted, try Lin's, then your own
aether run "triage the failures" --agent claude --account <grace-member-id> \
  --fallback <lin-member-id>,<your-member-id>

# Launch on the requested account even when it is exhausted
aether run "triage the failures" --agent claude --no-fallback
```

Passing both flags fails with `--fallback and --no-fallback cannot be used
together`. With no qualifying account
the run launches on the requested one. A run that fell back stays owned by its
launcher. Its header reads **On `<name>`'s account (fallback)**, and `aether
runs` appends ` (fallback)` to the `ACCOUNT` column.

A run also moves to another account mid-run when its agent reports the usage
limit. Aether starts a new container on the next account and resumes the agent's
session there. The run keeps its ID, checkout and branch, but processes in the
old container, terminals included, end. This works for Enhanced and Background
runs of `claude` and `codex` and Standard `claude` runs
([harnesses.md](harnesses.md#usage-limit-signals)). Cost stays with the account
that spent it. The account that ran out is not read again: the agent's report
counts as exhausted. The move picks an account with usage left as a launch
does; when none has, it tries one whose usage cannot be read, in the same
order, since the run would stop anyway. Your own account qualifies there only
with a login, or an API key the agent signs in with in that mode:
`ANTHROPIC_API_KEY` for Claude, `CODEX_API_KEY` for a Background Codex run
([per-agent setup](harnesses.md#per-agent-setup)). An account the run already used is
never tried again: your own, or a shared one whose login the server confirmed
in the run's container. A shared account whose login never reached the
container spent none of its quota, so it stays a candidate. A run that cannot
move parks or fails with a reason that says why
([Usage limit mid-run](failure-handling.md#usage-limit-mid-run)).

### Saved launch choices

The server keeps one set of saved launch choices per member, so every device
and the built-in assistant can start from the same ones. They hold the agent
to preselect, each agent's mode and model (at most 32 agents, names up to 64
bytes with no control characters), the account to launch on, the
fallback policy: `automatic` (every account you may launch on), `ordered` (at
most 8 member IDs, your own allowed), or `off`, and the
[permission tier](security.md#permission-tiers) the launch form preselects. They hold no credentials and
grant nothing. `run.launch` does not read them: a client fills a launch from
them, and the launch is authorized as if nothing were saved. The built-in
assistant's and the connectors' launch tools fill each field a launch leaves
out from them ([Launch options](connectors.md#launch-options)).

The dashboard's launch panel opens on them, the right half of **New run**
launches the last agent, mode, model and permission tier at once, and the
panel saves the agent, mode and model of each launch, and the permission tier
as soon as you change it. It saves the account and fallback only when you select
**Save as my default** under **Options**. **Settings → Launch** shows them,
with each invalid choice and its reason, and changes or clears them.

`launch.defaults.get` returns your choices with a `revision`, which is 0 when
none are saved. It also checks each choice now. `invalid` lists the choices a
launch could not use, with the reason: an agent not installed on the account,
an Enhanced mode that is not installed, or an account or fallback member who
stopped sharing with you. `unknown` lists what it could not check:

- a model with no current model list for that agent and account;
- a model not in that list: it may be a full model ID for a listed alias,
  which launches accept;
- a model for your own agent definition, which a launch passes only if the
  definition sets model arguments;
- an agent with no login file, which still launches if an API key in the
  environment signs it in;
- every saved agent, when the account's agents could not be listed.

Choices that fail a check stay saved until you replace them.

`launch.defaults.set` replaces all your choices (`defaults`) or deletes them
(`clear: true`). It takes the `revision` you last read. If someone saved since
then, it fails with code `-32003`, and the error's data holds the current
choices. Saving an account or fallback member who has not shared with you
fails with `account_member_id: permission denied: member <id> has not shared
their account with you`. Both methods act only on the caller, admins included.
A save records whether the built-in assistant or the member made it
(`source`). The built-in assistant saves only after you approve a card that
spells out every choice, naming the account and each fallback member.

A [member merge](#link-or-merge-sign-ins) keeps the survivor's saved choices.
Any account or fallback member that names the absorbed member, in anyone's
choices, is moved to the survivor, and those choices get a new `revision`.

## Child runs

An agent can start a **child run**: a session with a completely fresh context
on any agent and model you have installed, such as Codex reviewing what Claude
wrote. A child can orchestrate its own subagents. Agents dispatch one for a
complex task that splits into parallel parts, as a dispatch-and-forget
shortcut, or when you ask for session orchestration; small lookups
stay with the agent's own subagents. The child appears under its parent in the
dashboard, with its purpose, agent, model, account and state, and you open,
message, take over or stop it like any run. Its result reaches the parent's
inbox, as does a notice when it has sat idle or waiting for over an hour, so
the parent can wake or stop it. The agent commands are in
[coordination.md](coordination.md#child-runs).

Child runs go one level deep: a child cannot start children. A parent must be
a Standard or Enhanced run that wakes when mail arrives; `child start` from a
Standard run of an agent with no idle wake is refused with
`<agent> in Standard mode has no idle wake; start it in Enhanced`. A
Background run cannot start children.

### Turn child runs off

Child runs are on for every Standard or Enhanced run you own. **Settings →
Child runs** or:

```sh
aether children off                         # every run you own
aether children on
aether run "fix the login bug" --agent claude --children off   # this run only
```

A child runs on your own account and uses its parent's launch fallback
policy: when its Claude or Codex account has used 95% of its quota, the
child moves to the account with the most usage left among yours and those
shared with you, as [usage fallback](#usage-fallback) does for every launch,
unless the parent was launched with `--no-fallback`. The agent and model never
change.

**Allow child runs** in a run's **More** menu, or the **Child runs**
switch in the launch panel, allows or stops them for that one run without changing your
setting. Only the run's owner allows it, and a handoff to another member voids
it. An admin can turn it off. Handing a run to another member detaches it from
its parent and from its children, so no lineage spans two owners.

When a parent is closed (merged, abandoned, or completed and accepted), its
children are closed with it: finished ones are relabeled the same way and
working ones are stopped first. A child its parent cancels ends `completed`
with the reason `stopped by parent run <id>`.

### Limits and usage

A workspace admin sets how many children each parent may have busy at once:

```sh
aether workspace settings --child-max-live 8
```

`--child-max-live` (default 8, at most 64) counts a parent's busy children;
more wait in a queue and start as slots free, oldest first. A child that
reported or waits on a human holds no slot. A queued child is cancelled with
its parent. Every child is also admitted against the workspace
[budget](#budgets) when it is queued and when it starts.

Each child's spend counts once, on the account that paid. `aether children
<run-id>` and the parent's **Child runs** section add up the parent and its
children, split by account. A run that reports no cost (Codex, Gemini, pi) is
counted as unmetered, so totals read "at least".

- A budget admits runs; it does not stop running ones. A parent can pass the
  cap by what its live children spend.
- A parked parent woken by other mail can work beside its busy children, so
  it can briefly exceed `--child-max-live`.
- A child gets the workspace's environment and secrets like any run of yours,
  but not its parent's transcript. It works in the parent's repositories,
  extra `--repo` ones included, from the parent's last commit, and inherits
  the parent's permission tier.
- Deleting a child takes its spend out of the parent's total; the
  workspace totals and `aether cost` keep it.
- Turning child runs off stops queued children from starting; running ones
  continue. Kill them from the parent's **Child runs** section or with
  `aether kill <run-id>`.

## Limits

- Roles are viewer, collaborator and admin. There are no custom roles.
- The permissive default lets any collaborator stop any run. Use `aether protect`
  or `--message-others admins-only` to narrow it.
- A run's extra repos (`aether run --repo`) share its primary workspace's
  environment, account and budget. Templates take one workspace.
- Evidence packets expire after 30 days.
- A merge of two members cannot be undone.
