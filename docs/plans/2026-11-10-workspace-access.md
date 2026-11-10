# Workspace-Scoped Access

**Goal:** a team's server has super admins, admins who own the workspaces
they create, and members who reach only the workspaces they are granted.
A workspace a member cannot reach does not appear in any list, event,
search, memory, count, error or git response.

**Status:** draft for the owner's review, 2026-11-10. The decisions dated
2026-11-10 are the owner's. Everything marked *Assumption* is open.

## Model

A member has one **server role**: `super_admin`, `admin`, `collaborator` or
`viewer`. A workspace has one **owner**, an admin or super admin. A **grant**
lets one member into one workspace. A grant has no role of its own: the
member's `collaborator` or `viewer` role is what they may do in every
workspace they are granted.

The role a member acts with in workspace `W`:

| Server role | Owns `W` | Granted `W` | Role in `W` |
| --- | --- | --- | --- |
| super_admin | – | – | admin |
| admin | yes | – | admin |
| admin | no | yes | collaborator |
| collaborator, viewer | – | yes | their server role |
| any other | no | no | none: `W` does not exist for them |

`permissions.Check` makes this decision. `Actor` carries the workspaces the
member owns and was granted. When a target names a workspace, `Check` first
finds the role there. With no role it returns `ErrNoAccess`, which the wire
reports as the same `not found` a missing ID gets. Then it applies the
existing role rules with that role. An `Actor` built without workspaces, at
a call site this change missed, reaches none of them: a missed check fails
closed.

Capabilities with no workspace (`Target{}`):

- `View`, `Launch` and `Push` stay role-only. Only member-scoped methods use
  them: home files, agent installs, config. Every workspace method now
  resolves its workspace.
- `WorkspaceAdmin` without a workspace is refused, except to a super admin.
- `CreateWorkspace` (new) is for admins and super admins.
- `ServerAdmin` (new) is for super admins only.

### Who sees whom

A super admin sees every member. Anyone else sees themselves, every super
admin, every member who owns or was granted a workspace they reach, every
member with a session in one (primary or extra repository) or whose account
or home such a session uses, and, as an admin, every pending member. A session author whose grant was revoked stays
visible there, since the workspace's history names them. A member the
caller does not see is absent from member and device lists and reads as
`not found` wherever an ID names them. An admin's device list shows a
device waiting on an invitation only when they created the invitation.

### Who may do what

| Action | super_admin | admin | collaborator / viewer |
| --- | --- | --- | --- |
| See and use a workspace | all | owned and granted | granted |
| Create a workspace | yes, owns it | yes, owns it | no |
| Delete, settings, environment, budget, templates, schedules, mirror (public or GitHub source) | any | owned | no |
| Grant or revoke members in a workspace | any member, any workspace | collaborators and viewers, owned workspaces | no |
| Change a workspace's owner | yes | no | no |
| Invite | any role, any workspaces | collaborator or viewer, at least one owned workspace, only owned workspaces | no |
| Approve a pending member, approve a device | anyone's | members below admin, and their own | own devices |
| Change a role, remove a member | yes | no | no |
| Server update, run limits, Sysbox, edge claim code and owner transfer, a mirror with a deploy key, reviewer link, everyone's audit, disk and system stats | yes | no | no |

## Storage

Migration v110 runs in one transaction:

```sql
ALTER TABLE workspaces ADD COLUMN owner_member_id TEXT NOT NULL DEFAULT '';
CREATE TABLE workspace_grants (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	member_id    TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	granted_by   TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	PRIMARY KEY (workspace_id, member_id)
);
CREATE INDEX idx_workspace_grants_member ON workspace_grants(member_id);
-- the earliest live admin becomes super admin
-- every workspace without an owner goes to that super admin
-- every approved, unmerged member gets a grant on every workspace
```

- Every existing member keeps exactly the workspaces they can reach today,
  which is all of them (owner decision).
- Members still pending at upgrade get no grants. Today they reach nothing,
  and approval now admits a member with no workspaces.
- A workspace owner is never deleted silently. `member.remove`, a demotion
  below admin and a merge are refused while the member owns workspaces, and
  the error names them and the command to reassign them.
- Invitations carry their workspaces. `identity_invitations` gains a
  `workspaces` column (JSON array). Invite-code records gain a `Workspaces`
  field. Accepting either creates the grants in the same transaction that
  creates the member.

`aether-server member role <member-id> <role>` writes the role straight to
the store, as `aether-server device approve` does. It is the plain,
scriptable path a customer portal or a locked-out operator can use.
`aether member role` is the same operation over RPC, and only super admins
may call it.

## Surfaces

Every path below goes through `permissions.Check` with the workspace filled
in, or filters a list by `Actor.Reaches`.

| Surface | Today | After |
| --- | --- | --- |
| `workspace.list` / `get` | all | reachable only; each carries `owner_member_id` and the caller's `role` |
| `run.list`, `run.get`, `run.pull`, `run.overlaps`, `run.close_check` | all | reachable workspaces only; an unreachable run is `not found` |
| run mutators (`runTarget`), `dev.*`, `files.*`, `run.git.*`, `run.pr.*` | role rules | role in the run's workspace; `repo` (extra workspace) is checked too |
| `run.launch`, `template.launch`, schedule fire, relaunch, children, handoff recipient | any workspace | primary and every extra workspace must be reachable by the launcher, and by the account owner on a shared account |
| `attention.list`, `insights.get`, `changes.list`, `presence.roster` | all workspaces when no ID is given | reachable workspaces only; insights' member outcomes are limited to them |
| `workspace.timeline`, `cost.report`, `budget.*`, `template.*`, `schedule.*`, `workspace.child_limits`, `profile.push`, `presence.heartbeat` | any workspace by ID | resolved and checked |
| team memory | gated by role | gated by workspace (resolver already present) |
| events feed (`aether-events`, `/ws/events`) | every workspace's events | each event passes `Reaches(event.workspace)`; server-scope and recipient events as before; the reachable set is reloaded when a grant changes |
| attach, ACP stream, terminal history | read-only open to all | `View` in the run's workspace; a revoked grant ends the stream |
| git over SSH | clone and fetch open; push role-only | both need the workspace: `View` to fetch, `Push` to push |
| coordination (`send`, `ask`, peers, memory, children) | any live run on the server | the run's owner must reach both its own and the target's workspace |
| watches, push, Slack | owner-keyed | evaluated with the owner's access; budget notices go to the workspace's admins and super admins, not every admin |
| account sharing | sharer and borrower | also: both reach every workspace of the launch; the revoke sweep stops a live run when either loses access |
| assistant, MCP connector, Slack actions, dashboard | through `dispatch` | inherit every RPC change; no separate path |
| audit, `system.stats`, `server.disk` | admin sees all | super admin sees all; others as before |
| `member.list`, `member.device.list`, attention names | all members | the members the caller sees ([Who sees whom](#who-sees-whom)) |
| `member.identity.*`, `workspace.access.grant`, `account.share` naming a member | any member | a member the caller does not see is `not found` |

## Changed commands and screens

New RPCs: `workspace.access.list`, `workspace.access.grant`,
`workspace.access.revoke`, `workspace.owner.set`.

```sh
aether workspace access <workspace>                       # owner and grants
aether workspace grant <workspace> <member-id>...
aether workspace revoke <workspace> <member-id>
aether workspace owner <workspace> <member-id>            # super admin
aether invite --github dana --workspace web --workspace api
aether invite --workspace web                             # invite code
aether member role <member-id> super_admin|admin|collaborator|viewer
sudo aether-server member role <member-id> super_admin   # on the server
```

- `aether workspace list` gains `OWNER` and `ROLE` columns.
- Dashboard **Members**: roles include Super admin. Only super admins see the
  role menu and Remove. The invite dialog picks workspaces, limited to the
  ones the inviter administers.
- Dashboard **Manage workspaces**: each workspace shows its owner. **Access…**
  lists grants and adds or removes members. Delete, settings, budget and
  templates show only where the caller is the workspace's admin.
- The dashboard's permission mirror (`web/src/lib/permissions.ts`) takes the
  caller's role per workspace from `workspace.list`.

## Decisions made while building

Each is a single check that is easy to change.

- Granting a pending member is refused: `approve them first: aether member
  approve <id>`.
- `workspace.owner.set` keeps the previous owner, unless a super admin, on
  the workspace as a grant, so their live sessions continue.
- Revoking is `in use` while the member has a live session whose primary or
  extra repository is in the workspace, or a schedule there.
- An invitation or code grants, when it is used, only the named workspaces
  its issuer still administers. A deleted workspace is skipped.
- A launch, relaunch or handoff needs Launch in every workspace of the
  session, extras included. A schedule fire re-checks its creator.
- Coordination to a run in an unreachable workspace answers `unknown
  session`, as for one that does not exist.
- An event stream reloads the member's access when a grant, owner or role
  changes, and at every revalidation tick.
- Budget notices go to the workspace's owner and the super admins.
- `changes.list` reports a deleted workspace only to super admins.
- A session exists for a caller only if they reach every one of its
  workspaces, extras included; a super admin always does. Otherwise it is
  absent from every list, attention, insights' recent sessions, watches and
  coordination, and its id answers as a missing session. Insights and cost
  totals still count it in its primary workspace.
- An event of a deleted session reaches only super admins, except
  `run.deleted`, which names the session's extra workspaces and reaches
  whoever reaches all of them.
- Edge ownership needs an admin or super admin; transfer and claim codes are
  super admin only.

## Threat cases

Each one runs in the integration matrix: super admin S, admin A, admin B,
collaborator U granted A's workspace, and member N with no grant.

1. N lists workspaces, runs, attention, insights and changes: sees none of
   A's or B's. Counts and totals are zero.
2. N calls `run.get`, `workspace.get` or `timeline` with an ID of A's: gets
   the same `not found` as for an ID that never existed.
3. N subscribes to the event feed with no filter, and with A's workspace
   ID: gets no event from A's workspace.
4. N clones or fetches A's repository over SSH: `not found`. U fetches but a
   viewer U cannot push.
5. N attaches read-only to a run of A's, or reads its ACP history: refused.
6. A cannot see B's workspace anywhere (1–5 with A in place of N).
7. A grants U into B's workspace, sets B's settings, or changes B's owner:
   `not found` or denied.
8. A promotes U to admin, or demotes S: denied.
9. A invites with role admin or into B's workspace: denied.
10. A approves a device that would admit as S: denied.
11. U launches with an extra workspace of B's; hands a run off to N;
    launches on an account shared by a member without A's workspace:
    refused.
12. U's run messages a run in B's workspace over coordination: refused.
13. A revokes U while U has a live session in A's workspace: `in use`.
14. A pre-change database: after upgrade every approved member reaches every
    workspace, the earliest admin is super admin, and the other admins are
    still admins.
15. Removing or demoting the last super admin: refused.
