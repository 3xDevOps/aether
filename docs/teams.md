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

The first identity to link a fresh server becomes the admin and everyone who
joins afterwards is a collaborator, but an admin can change that:

```sh
aether member role 01m04mqes1z7wsdk4s90tx0pgg viewer
```

```
set 01m04mqes1z7wsdk4s90tx0pgg dana to viewer
```

`member list`, `member approve`, `member color`, `member git`, `member role`
and `member remove` are the member surface. Two rules hold no matter what you
type. **The last admin can neither be removed nor demoted** - `refusing to
delete the last admin` and `refusing to demote the last admin` - because a
server with no admin has no way back. **A role change lands on connections
that are already open**, not at next login: the role is re-read from the
store on every request, so a demotion takes effect mid-session. A live write
attach is re-checked every few seconds and dropped when steer goes away -
`detached: you can no longer steer this run` - and `aether attach
--read-only` still shows the terminal afterwards. `member remove` first stops
and destroys that member's environment terminal, then deletes the member row
and erases their member home. If cleanup fails, the command returns the
runtime error and leaves the member in place so an admin can retry. **It
refuses while any row still references the member** - runs, schedules and
pushed profile snapshots reference members with no cascade, so the delete
returns `in use` - and an admin deletes those first; what a run wrote stays
in the data directory until the run is deleted. A successful removal ends
every attach and live sync of theirs.

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

A workspace is the whole shared scope. It is a repository plus workspace
settings; a run and its shells use the selected agent account's saved image, or
the server's standard image when that account has not saved one. See
[environments.md](environments.md) for image selection and saving. Everything
the team shares hangs off the workspace: runs, the event feed, the approval
inbox, presence, templates, schedules, costs, and the budget. One workspace
normally covers one project.

Creating one is an admin operation:

```sh
aether workspace init myproject [--base <branch>]
aether workspace add myproject [--base <branch>]
```

Four settings belong to the workspace rather than to any run in it:

- **The base branch** is what every new run's worktree is cut from. `--base`
  sets it at creation; it defaults to `main`.
- **The checkout Origin** is the git URL every new run checkout gets as its
  `origin` remote - the place a run branch can be pushed for review. Normally
  the first `aether link --repo` (or the Repository step in the local
  dashboard's onboarding wizard) reads that clone's own `origin` and records it.
  Recording happens only when the caller may push - a viewer may not - and the
  clone's origin is one the server accepts. When either does not hold, the link
  succeeds and nothing is recorded; set it later with `aether workspace origin`.
  **First one wins.** A later link from a teammate whose clone points somewhere
  else does not overwrite it, so one member's fork cannot silently redirect
  everyone's runs. Change it deliberately:

  ```sh
  aether workspace origin                          # show
  aether workspace origin https://github.com/acme/myproject.git
  aether workspace origin --clear
  ```

  A `github.com` origin in scp-like or ssh form - `git@github.com:acme/app.git`
  or `ssh://git@github.com/acme/app.git` - is recorded as
  `https://github.com/acme/app.git`. gh's credential helper authenticates
  HTTPS only and nothing in the member home authenticates SSH, so the HTTPS
  form is the one a run can push to. Every other host is recorded verbatim,
  and an SSH origin on another host cannot be pushed to from a run: give the
  workspace an HTTPS URL if runs need to push there.

  The remote is set when a run's checkout is created, so a change reaches new
  runs only; runs already going keep the URL they were given. With no checkout
  Origin recorded, run checkouts have no usable `origin`, but `aether pull`
  still brings their server branch into your clone.
- **The source mirror** is optional and separate from checkout Origin. A
  workspace with no mirror is **local-only**: collaborators and the daemon
  may push its base branch in the normal way. An administrator can configure a
  read-only upstream source:

  ```sh
  aether workspace mirror configure --workspace myproject \
    --source https://github.com/acme/myproject.git --branch main --auth public
  ```

  `--branch` defaults to the workspace base branch. Public mode fetches
  credential-free HTTPS. For a private GitHub repository use
  `--auth deploy-key` with its HTTPS URL; Aether prints a public key and the
  exact `https://github.com/<owner>/<repo>/settings/keys/new` URL. Add the key
  as a read-only repository deploy key, then verify it. For generic SSH, use
  `--auth deploy-key` with an `ssh://` source and
  `--known-hosts-file <file>` containing the verified host key. The private
  key is never printed or sent to a member.

Configuration starts **pending**. In the dashboard's Workspace **Source
control** panel, use **Verify** or **Refresh**; from the CLI:

  ```sh
  aether workspace mirror refresh --workspace myproject
  aether workspace mirror status --workspace myproject
  ```

  The panel shows source, branch, accepted and observed candidate commits,
  and check times. A forward-only source update becomes **ready** and moves
  the mirrored base. A rewrite or local/server divergence retains the
  candidate without moving the accepted base; an administrator must review
  and explicitly **Adopt candidate** (`aether workspace mirror adopt
  --workspace myproject --generation <n> --yes`). **Disable** requires
  confirmation and returns the workspace to local-only; remove any GitHub
  deploy key separately because disabling cannot revoke it remotely.
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

The checkout Origin does not change workspace base ownership. For run
completion, Aether publishes each run's `aether/run-*` branch to the workspace
repo; candidate delivery is a separate review path, not another run-branch
publication. `aether pull` still brings a published run branch into your clone.
Pushing that run branch to checkout Origin is somebody's own act: the agent
inside the run can `git push origin <branch>`, or you can push it from your
clone after reviewing and merging.

## Candidate integration

Candidate integration is the review boundary for combining retained run
submissions. It does not change run ownership or the mission scheduler. A
candidate is prepared from an ordered list whose entries carry
`workspace_id`, `run_id`, `evidence_ref`, and `retained_revision`, plus a full
`target_ref` in the form `refs/heads/<branch>` and its exact
`expected_target_revision`. Optional `required_sources` names are checked
while the packet is copied; a required unavailable or truncated source fails
preparation rather than becoming an empty observation. The candidate stores an
evidence snapshot and candidate-owned Git refs and transcript artifacts, so
later source cleanup does not change what was reviewed.
Packet snapshots are bounded to 1 MiB total per candidate.

Assembly happens in a server-owned isolated checkout, never in a live run
checkout. Inputs are applied in their submitted order while preserving each
source base. A conflict leaves the journal and checkout available for explicit
file resolutions; the candidate freezes only after every input is applied.
Once frozen, its candidate revision and inputs cannot be edited. The review
surface labels evidence as observations: a server verification records the
exact argv, observed image, runtime identity and working directory, bounded
resource and timeout details, setup/environment provenance, exit and bounded
output, and checks that the frozen tree was not changed, but a passed
verification is not proof of semantic correctness.

Delivery is a human gate over the exact candidate revision, verification IDs,
target ref, expected target revision, and action. The caller and approver must
still be active members with the existing **Push** capability when the action
is performed; no candidate operation grants a new permission. On a local
target, `update_ref` uses an atomic expected-old compare-and-swap and refuses
if the target moved. A mirrored target cannot be updated directly: `proposal`
creates the public `refs/heads/aether/proposal-<request-id>` ref and a private
receipt without changing the upstream-owned base. A human fetches that proposal
and pushes it through the normal protected upstream review route; **proposed**
does not mean landed.

Candidate-owned evidence has its own bounded lifetime: candidates live for 30
days, and verification results and delivery requests expire no later than the
candidate (verification validity is 24 hours). Original evidence packets may
be deleted or expire after ownership transfer without deleting the candidate's
copies. A missing or checksum-mismatched owned artifact makes the candidate
unavailable and blocks verification and delivery; it is never silently
reconstructed from the original packet. Expiry or deletion first fences new
actions, then cleans owned resources; recoverable resources remain when that
transition or cleanup cannot be completed. See [integration.md](integration.md)
for the method-level contract and [failure-handling.md](failure-handling.md)
for restart and cleanup behavior.

In a **local-only** workspace, the base branch is client-writable. It is
usually already there by the time the second member links: whoever created the
workspace pushed it. Nothing guarantees that, so the local dashboard wizard's
**Push now** button (step 4 of [quickstart.md](quickstart.md)) compares your
clone with the workspace's copy of the branch before it pushes, and reports
what it found instead of failing with git's
`! [rejected] main -> main (fetch first)`:

- **Not there yet** - nobody has pushed the branch. Your clone seeds it,
  exactly as the first member's would.
- **Level** - both sides are the same commit. Nothing to push; carry on.
- **Your clone is ahead** - you cloned, then committed. The push runs as
  usual.
- **The workspace is ahead** - your clone is missing commits a teammate
  pushed. **Fast-forward my clone** moves your branch up to the workspace's
  commit, fast-forward only: no merge commit, and the commits you made are
  never rewritten. With another branch checked out, only the branch ref
  moves and your working tree is left alone; an uncommitted change the
  fast-forward would overwrite stops it, in git's own words.
- **Diverged** - you both committed since. Aether does not force-push and
  does not merge for you; the wizard prints the `git fetch`, `git log`,
  `git rebase` and `git push` commands and you decide.

In a **mirrored** workspace, the source mirror owns the base branch. `git push
aether <base>` and the dashboard's **Push now**/`repo.push` are rejected by
design, as are local-daemon base pushes:

```
aether: rejected write to protected ref refs/heads/<base>: this mirrored base is owned by the upstream; use the configured upstream to update it
```

Do not work around that rejection: an administrator refreshes the mirror,
reviews a retained candidate, and uses **Adopt candidate** only when a rewrite
or divergence is intentional. Runs always branch from the accepted mirror
commit captured at launch.

## Working together

| Command | What it does |
| --- | --- |
| `aether runs` | Every run you can see, colored by owner, with conflict warnings. Prints a notice when any run is waiting on a human; `--attention` lists only those. Archived runs are hidden; `--archived` lists only those, with their deletion date. |
| `aether who` | Who is online and which runs they are watching. |
| `aether attach [--read-only] <run>` | Raw PTY passthrough. Multiple people can attach at once; write access needs steer, and without it the attach falls back to read-only by itself. |
| `aether inject <run> "..."` | Push an instruction into a running agent. Renders as a banner in your member color. |
| `aether pause` / `resume` / `kill <run>` | Suspend, thaw, terminate. Worktree and transcript survive a kill. |
| `aether delete <run>` | Stop the run if it is live, then remove its checkout, transcript, evidence, and run records. Needs the same permission as `kill`. A published run branch stays in the workspace repo and the timeline keeps the history. |
| `aether archive <run>` / `unarchive <run>` | Hide a finished run from the board and default `aether runs`, or restore it. Needs the same permission as `kill`; only a merged, abandoned, failed, or interrupted run can be archived. Archiving itself removes nothing; the server deletes the run on the printed date. See [failure-handling.md](failure-handling.md) for what the checkout TTL GC reclaims sooner. |
| `aether protect` / `unprotect <run>` | Limit steering and killing one run to its owner and admins, whatever the workspace policy says. |
| `aether handoff <run> <member>` | Transfer ownership and notification routing immediately. The recipient needs no acceptance handshake; Aether records the actor and both owners and captures handoff context. The agent account and its cost attribution do not change. |
| `aether close <run> --outcome merged\|abandoned` | Record the finish outcome and clear a finished run off the attention board after its automatic evidence capture. |
| `aether inbox` | The shared approval queue; `aether inbox approve\|deny <request-id>` decides, and any steer-holder can. `--all` includes decided requests. |
| `aether timeline` | The workspace's whole history; filter with `--run`, `--member`, `--type`, `--limit`, export with `--jsonl`. |
| `aether cost --runs` | Token spend per member and per run. |
| `aether budget` | The workspace's spend cap and what has been used. |
| `aether sync --live <local-dir> <run>` | Live-overlay a local directory onto a run's worktree. Local edits that collide are preserved as `*.aether-conflict` files. |
| `aether forward <run-id|terminal> <port> [--local <port>]` | Forward a run or environment terminal port to loopback for callbacks such as agent OAuth. The local port defaults to the forwarded port. |
| `aether member git [--name <name>] [--email <email>] [member-id]` | Show or set the name and email every commit made for that member is authored as. Members set their own; an admin can set anyone's. |
| `aether github connect` | Finish connecting GitHub after `gh auth login` in your environment terminal: sets up git credentials there, generates and registers a commit signing key. See [environment-home.md](environment-home.md#connect-github). |
| `aether workspace origin [--workspace <name-or-id>] [<url>\|--clear]` | Show or set the checkout Origin run checkouts use for pushing review branches. Needs the push capability. |
| `aether workspace mirror status\|configure\|refresh\|adopt\|disable` | Admin-only source-mirror lifecycle. Configure takes `--source`, optional `--branch`, `--auth public\|deploy-key`, and optional `--known-hosts-file`; refresh/adopt/disable require `--workspace`. |
| `aether account list` / `share <member>` / `revoke <member>` | List usable agent accounts, or grant and revoke access to your own account. |
| `aether files ls <workspace|run> [path]` / `aether files cat <workspace|run> <path>` | Browse or read files from a workspace base tree or live run checkout. The dashboard's **Files** view also edits workspace base, live-run files, and your own persistent member configuration. |

### Handoff and finishing runs

Handoff is an immediate transfer. `aether handoff <run> <member>` changes the
run owner and notification routing without waiting for the recipient to accept.
The timeline and Run Room record the handoff actor, outgoing owner, and incoming
owner. A system entry points to the handoff evidence packet, or says that
evidence is unavailable when preservation did not succeed. The transfer does
not switch the selected agent account or its cost attribution.

Aether captures an evidence packet automatically when a run is handed off and
when it finishes. The finish capture happens before automatic checkout or
transcript cleanup. A packet is a factual record of the run at capture time,
including its objective and identity, capture time and event-log boundary, base
and retained Git revisions, changed-file facts, source availability, unresolved
facts, next action, and provenance. Source metadata says when a source is
unavailable or truncated and gives the reason when one is known.

The packet retains the repository state in a private Git evidence commit, so
the diff remains available after the live checkout is removed. A rendered patch
is bounded to 1 MiB per request and reports `truncated` when that bound is
reached. A copy of the PTY transcript is retained separately, capped at 16 MiB.
The packet and its retained Git and transcript objects expire after 30 days.
After expiry they are unavailable rather than silently replaced with a partial
result.

Evidence is provenance, not independent verification. It records what Aether
observed or what a harness, agent, or member reported; it does not prove that a
command succeeded or that the result was reviewed. The Git tree and transcript
are captured sources, not one atomic snapshot of the container, its processes,
environment, or credentials. Treat a missing source as unavailable and a
truncated source as incomplete.

If required preservation fails, Aether reports the failure and keeps the
recoverable run resources instead of deleting the checkout or transcript
silently. A handoff itself is not rolled back solely because its evidence
packet could not be captured. Unresolved facts remain inspectable in the Run
Room's evidence drawer. Use a fact's **Answer** action to open the composer
with that fact prefilled, then edit and send a normal room comment. Evidence
facts do not enter **Needs you**; only unanswered Run Room questions do. This
is not a separate action inbox, blocker, or task model.

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

Scheduled runs use the same launch gate as manual runs. A mirrored workspace
refreshes its source before each scheduled launch; an offline, authentication,
missing-source, rewrite, divergence, or other failure means that occurrence
does not create a run. Fix the mirror or use the explicit one-shot cached-base
retry for a still-unchanged accepted commit; the next scheduled occurrence
does not silently reuse a cache.

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

That attribution reaches git too. Each member has a git identity - the real
name and email their commits are authored as - collected by the local
dashboard's onboarding wizard and shown or changed with `aether member git`. The
agent in a run container commits with the identity of the member who launched
it, baked into the container when it is created, and the commits Aether makes
itself when a run finishes, is killed, or is recovered are authored as the
run's current owner with Aether as the committer. A member who has set no
identity keeps the fallback: their display name, or their member id when
the display name cannot be a git author name, at `<member-id>@aether.local`,
which maps to no upstream account.

Once that member has connected GitHub, those commits are signed with the
key in their environment home - the agent's own, from the home's
`.gitconfig`, and Aether's, with the same key read on the server. GitHub
shows a signature as **Verified** only when the committer address is a
verified address on the account that registered the key. So the agent's own
commits show as verified when the member's git email is on their GitHub
account, and Unverified when it is not - the `<member-id>@aether.local`
fallback above never is. Aether's commits are committed as Aether, so they
stay unverified either way;
[security.md](security.md#github-credentials-and-signing-keys) has the
detail.

Whoever else steers a run is credited as a co-author of it. Steering is
injecting a message or typing into the run's own agent terminal - a shell
tab in the run's container is not steering - and it adds that member, once,
to the run's steerers. Every commit Aether makes for the run then ends with
one `Co-authored-by:` trailer per steerer, deduplicated by address, so two
members sharing one address produce a single line. The run's own agent gets
the same list in `/run/aether/co-authors` and is told in its task prompt to
end its commits and pull requests with those lines - see
[coordination.md](coordination.md). Aether's own commits credit everyone who
steered the run; the file the agent reads credits everyone the run involves,
its owner included, less the address that container already authors as. A
handoff moves the roles the server still decides: the incoming owner becomes
the author of Aether's own commits and the outgoing one joins the steerers.

Two limits, both from one fact: a container's `GIT_AUTHOR_*` and
`GIT_COMMITTER_*` are fixed when it is created, from the member who launched
it, and never move while it lives. Changing a git identity while a run is
live refreshes that run's `/run/aether/co-authors`, but the agent's own
commits in that run keep the identity it started with; later runs use the
new one. And after a handoff the agent's own commits are still authored as
the outgoing owner, while Aether's end-of-run commits are authored as the
current one - which is why the incoming owner is on the co-author list at
all, and the outgoing one is not.

One consequence worth naming: a member who changes their git identity while
they own a live run is credited on that run's remaining agent commits under
the new address, because the container is still authoring under the old one.
The commits carry both addresses rather than losing the change, which is the
useful answer.

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

Deleting a run keeps its cost counted. `aether delete` removes the run's own
record, but its numbers stay folded into its workspace's and its member's
totals, so `aether cost`, `aether budget`, and the cap itself see the same
spend before and after the delete. Removing the member with `member remove`
does not clear that folded spend either: it stays in the workspace's total
under the member's now-gone ID.

## Agent accounts

Each member runs `aether agent add <name>` once, then opens `aether terminal`
to install the agent and complete the vendor login. The member's server-side
home is mounted into every container that person receives, so all their runs
share the same login and installed files. See [harnesses.md](harnesses.md).

A member may explicitly let another collaborator launch against that account:

```sh
# Account owner
aether account share <member-id>
aether account list

# Recipient
aether run "triage the failures" --agent codex --account <owner-member-id>

# Account owner
aether account revoke <member-id>
```

The dashboard exposes the same existing account-sharing controls on **Members**
and an **Account** picker in the launch dialog. The run is owned by the
authenticated launcher, whose identity is used for the timeline and Git author.
The selected account supplies its saved environment, complete home and
credentials, configuration roots, custom harness definitions, vendor quota, and
cost attribution.

Sharing is directional. It does not let the recipient open the owner's
environment terminal, and admins get no implicit account access. It does let a
root process in the recipient's run read or change every file and credential in
the shared home. Revocation blocks new launches and relaunches but does not
stop existing runs; stop them first if access must end immediately.

The dashboard's bottom-left status bar reads the selected account's
subscription quota through the read-only `account.usage` RPC. An empty
`account_member_id` means the caller's account; selecting another account
requires the same explicit share as a launch, and an admin has no implicit
access. The server rechecks that grant after each provider read, so a revoke
during a request cannot return the owner's data. This read never copies a
credential to the client or to a run. Native OAuth credentials are read only
for Claude Code and Codex; API-key logins and other harnesses are reported as
unsupported, and native reauthentication remains the owner's terminal action.

Normal successful usage is cached for 60 seconds. `refresh:true` bypasses that
success cache only within a 10-second request floor; failures wait at least
60 seconds and honor a provider retry deadline. A stale row keeps only a
same-credential successful window, reports its error and stale status, and is
not presented as current after a reset has passed without a fresh measurement.

### Mission identity and current authority

Mission work does not introduce a second identity or credential boundary.
Record these roles separately:

- **Actor:** the authenticated human, originating run, or server action that
  performed an operation. An integrator run is the actor for dispatches it
  makes.
- **Authorizing human:** the human who authorized the mission or consequential
  action.
- **Run owner:** the member responsible for the run's workflow and
  notifications.
- **Account owner:** the member whose selected account supplies the run's
  image, home, configuration, and native credentials.

The actor is not rewritten as the authorizing human, run owner, or account
owner merely because the operation was performed on somebody's behalf. A
mission's finite concurrency and total-attempt limits bound Aether admission;
they do not narrow what the selected whole-home credentials can do inside the
container.

Release B rechecks the current member role, account-sharing authority,
mission assignment, and control/assignment generation at each consequential
operation. Presence, a skill, an integrator label, or a run ID does not grant
authority. There is no separate eligible-controller administration, grant
expiry, or per-worker approval product, and taking control does not grant
access to another member's account.

The durable worker takeover hold has its own generation and is independent of
the ephemeral control lease. `mission.worker.release` is a human-only
compare-and-swap action: the caller supplies the worker run ID and observed
`expected_takeover_generation`, and Aether rechecks current steering authority
and control admission before clearing the hold. A stale generation, revoked
authority, or foreign holder is refused without clearing it; an integrator or
worker cannot release the hold through its assignment socket. Releasing a
hold changes control state, not account sharing.

A mission starts in the `planning` phase and dispatches no worker until a human
approves its plan. The integrator may ask clarifying questions, then declares
clarification complete (`clarified`) and submits the plan for review
(`plan_review`). After approval the mission is `active`; a further plan
submitted from `active` is an **amendment** and puts the mission in
`amendment_review` until the same human decides it.

Three control-channel methods carry that decision. Each needs
the `run.launch` permission (collaborator or admin) and is refused unless the
authenticated member is the mission's accountable human or holds the `admin`
role, so an accountable human demoted to viewer can no longer answer, decide,
or cancel, and an admin must take over:

- `mission.question.answer` answers one clarifying question the integrator
  asked. The answering member is the session, never a request field.
- `mission.plan.decide` approves, requests changes to, or rejects one plan
  version. Approval re-resolves the mission's own launch admission first, so an
  accountable human who lost `run.launch` or the integrator's account share
  cannot carry the mission past the gate. Requesting changes and rejecting do
  not, so a plan whose accountable human lost that admission can still be
  closed out by an admin. Rejecting an amendment is refused: the approved plan
  stands either way, so an amendment is approved or sent back for changes and
  the integrator abandons its tasks or revisions to drop it.
- `mission.cancel` ends a mission in `planning`, `clarified`, or `plan_review`
  by moving it to `rejected`, whether or not a plan was ever submitted. A round
  under review is recorded as a `reject` decision with the feedback
  `swarm cancelled`. It is refused once a plan is approved and on a mission
  that already ended.

Reading the gate is not deciding it. `mission.show` stays a View read: every
member sees the questions, the answers, the plan summaries, and the feedback
attached to each review round.

Rejecting a plan or cancelling the mission cancels its integrator run. That
cancellation needs no per-run Kill check: the run is the mission's own reserved
integrator and the decider is already the accountable human or an admin.
Cancellation is the reconcile loop's job and is retried every pass until the
run is terminal, so a rejection survives a server restart.

`mission.replace-integrator` is the recovery when an integrator run exits, in
`planning`, `clarified`, `plan_review`, `active`, and `amendment_review` alike.
It changes neither the phase nor the plan version and leaves an undecided
review round decidable. A rejected mission refuses it.

The integrator does not poll for any of this. The server types one `aether:`
line into its terminal when a human answers or decides, when a worker
reports, and when a worker's run ends without a report, each naming the
command to run next. A notice never reaches a retired integrator run. See
[coordination.md](coordination.md#the-mission-plan-gate).

An amendment does not stop the approved plan. While a mission is in
`amendment_review` the integrator still starts, retries, cancels, and inspects
workers on approved tasks and still accepts their submissions; it cannot
propose, revise, abandon, or accept task revisions, and it cannot dispatch a
task whose revision is in the round under review.

After approval the integrator accepts later revisions of already-approved tasks
itself, but only within what the human approved. The server refuses
`task.accept` and directs the revision through `mission.plan.submit` when the
revision:

- belongs to a task that was never approved (new work);
- is declared `material` by its proposer;
- widens the approved scope - an `expected_paths` entry outside the union of
  the approved tasks' expected paths;
- drops an exclusion the task's approved revision carried;
- belongs to a task whose latest review round a human sent back for changes;
  only a round that approves the task again lifts that hold.

`material` is the proposer's own declaration, not a server inference. It is
recorded on the revision and is the sixth reason a revision needs a human
round.

Every approved round is auditable without reading history: the plan review row
records who submitted it, from which phase, who decided it and when, and one
plan item per task in the round with that task's revision, whether it was new
work, whether it was material, and which paths or exclusions widened the
approved scope. Each revision records its proposer, and, once accepted, the
member or integrator run that accepted it.

Mission progress has the same evidence boundary as the dashboard: a worker
report or process success does not make a task **Done**. The current task
revision must have an accepted submission with required evidence available,
and any scope deviation must carry an explicit disposition. The resulting
evidence remains provenance of what Aether captured or a participant reported,
not independent verification.

Agent configuration is not watched or inventoried automatically. Open
**Agents → Configuration** in either dashboard to choose a local directory,
review its files, and explicitly import or update the remote configuration.
The local onboarding Agents step uses the same importer. Known credential
names and runtime/history defaults are skipped locally; remaining bytes are
uploaded and server-scanned, so do not assume all secret content stays local.
Directory-wide count and byte budgets do not truncate imports; bounded batches
carry the full eligible selection and report progress or an explicit failure.
The import writes the authenticated member's persistent home immediately,
including for active runs using that account. An agent may need to reload its
configuration.

The **Files** view browses and edits that own-member configuration beside
workspace base and live-run files. It has explicit **Save** or
**Commit to <branch>** actions, Ctrl/Cmd-S, syntax highlighting, find/replace,
dirty tabs that survive navigation, unload warnings, and no autosave or
force-save. A failed or stale save keeps the draft; **Reload from server**
deliberately discards it. Configuration files are complete UTF-8 text up to 64
MiB. Binary and oversized files are read-only. New configuration files accept nested relative paths and
refuse overwrite. Every `config.*` method
requires `Launch` and targets only the authenticated member's own home; an
admin cannot select another member.

New browser-import files are mode `0644`; existing remote permission bits are
preserved. Local executable mode and symlinks cannot be represented by the
browser. Imports preserve empty and arbitrary binary regular files up to
64 MiB each, without a directory-wide file-count or aggregate-size ceiling.
The server rejects unsafe paths, symlink components, hardlinks, and nonregular
files. A shared account uses the same read-write home rather than
an isolated per-run copy. A snapshot pin records launch provenance, not an
isolated writable home or a promise that home edits wait for later runs.
Editing does not rebuild the installed-agent image.

The manual CLI profile surface remains available for operators who need
snapshots or rollback:

```sh
aether profile push --agent claude
aether profile status --agent claude
aether profile rollback --agent claude <snapshot-id>
```

`profile push` is explicit and not run by the sync daemon. Its repeatable
secret flags are `--skip-secret <file>` and
`--allow-secret <file>`; the latter requires `--workspace <workspace>`:

```sh
aether profile push --agent claude --skip-secret <file>
aether profile push --agent claude --allow-secret <file> --workspace <workspace>
```

Browser imports and **Files** edits do not create profile snapshots. A manual
push or rollback overlays snapshot files into the same persistent home; it
does not remove unlisted files or create isolated configuration for a run.
