# Subsessions: agents delegating to other agents

**Status:** Implemented in the pull request that adds `internal/children`.

**Goal:** An agent in any session (an ordinary session, a swarm integrator or a swarm
worker) can start a focused **subsession** on another agent or model, for
example a Claude run asking Codex for an adversarial review, then message it,
wait for it and receive its result. Every child is attached to its parent and
to the original root, is visible and controllable in the dashboard like any
session, and spends only accounts and limits its owner already authorized.

A **session** is one agent execution with its own container, checkout and branch.
A **swarm** is a mission: an integrator run that starts worker runs for
tasks. An **agent account** is a member's vendor login; a **share** lets one
other member launch on it. A **subsession** is a session started by another session
through `aether-internal subsession start`. Its **parent** is that session. Its
**root** is the top of its tree: the parent's root, or the parent itself. A
swarm's sessions and their descendants form one tree keyed by the swarm.

## 1. Decisions

1. **One scheduler.** A child is an ordinary session: the server launches it
   through the same `LaunchWithOptions` path, budget gate and
   `AuthorizeLaunch` boundary as `run.launch` and `worker.start`. A child is
   reserved in `run_children` under a pre-minted session ID
   (`LaunchOptions.ReservedRunID`, which, unlike a mission's
   `AssignedRunID`, carries no worker semantics), and the session row is created
   only at dispatch. Swarm workers stay mission attempts: their parent in the
   session snapshot is the swarm's current integrator, derived, never stored, and
   their lifetime stays with the swarm.
2. **Lineage is server state.** The parent is the session whose socket made the
   call; nothing on the wire names it. A message body, a role card or a
   report never grants launch rights.
3. **Who may delegate.** Authorization follows the calling session's current
   owner, who also owns every child it starts.
   - Ordinary sessions: subsessions are **on by default** for every Standard or
     Enhanced session. The owner turns them off for their sessions (Settings → Child
     sessions, `aether subsessions off`), or sets one session (launch dialog **Options →
     Subsessions**, session header **More → Allow subsessions**, `aether session
     --subsessions on|off`). A session's own setting overrides the member setting. An
     "on" counts only while the member who set it still owns the session, so a
     handoff never carries one member's choice to another; an "off" always
     counts. A handoff of a child or its parent detaches the child. Only the
     owner turns a session on; the owner or an admin turns it off. One member's
     choice never changes another member's sessions.
   - **One level deep.** A child cannot start subsessions; `child start` from
     one is refused. Goal: a session works on its own and dispatches fresh-context
     sessions for complex parallel work, which orchestrate their own
     subagents; nesting adds no capability and hides cost.
   - **Wakeable parents only.** The parent's report arrives as mail, so
     `child start` is refused from a session whose agent and mode cannot wake on
     mail (`<agent> in Standard mode has no idle wake; start it in Enhanced`),
     the same rule as a swarm worker's.
   - Swarm runs: the swarm's execution choices are the authorization.
     Creating a swarm already lets agents start sessions on those choices; the
     member setting does not apply.
   - Background (one-turn) sessions cannot start subsessions: they exit before a
     result arrives.
4. **Which agents and accounts.** These reuse the existing account shares
   and usage fallback; nothing widens them.
   - Swarm runs: an exact `(account, agent, mode, model)` choice of the
     swarm, as for `worker.start`, with the swarm's own fallback policy. The
     member's subsession settings never override a swarm's choices.
   - Ordinary sessions: any agent the owner can launch, on the owner's own
     account. There is no child-only fallback setting and no "use
     subscriptions shared with me" choice: a child uses its parent's launch
     fallback policy, so when its account has used 95% of its Claude or Codex
     quota the child moves as any launch does, and `--no-fallback` or an
     explicit order on the parent carries over.
   - `child options` names each agent's state: **ready**, **exhausted**,
     **not installed** or **not signed in**, with the exact reason. A child
     receipt names the account it launched on and whether it fell back.
   - Every launch and every queued dispatch re-runs `AuthorizeLaunch` for the
     owner and account, so a revoked share, a removed member or a turned-off
     setting stops new subsessions at once. Running subsessions continue, as
     running sessions do after a revoke today.
5. **Limit, per parent.** A workspace admin sets `child_max_live` (default
   8; Repository → Workspace settings, `aether workspace settings
   --child-max-live`): subsessions one parent has busy at once (provisioning or
   working). More are **queued**, not refused, and start as slots free,
   oldest request first. A child that reported its result or waits on a
   human holds no slot. There is no depth or total limit: one level deep
   bounds the tree. The workspace budget admits each child against its owner at
   launch and again at dispatch.
6. **Duplicates.** `child start` takes an idempotency key scoped to the
   parent; a retry with the same key and inputs returns the first receipt,
   different inputs are refused with `-32003`. There is no loop detection:
   a child cannot start subsessions.
7. **Results.** A child's success, failure or blocked report reaches its
   parent's inbox as a durable message (`correlation_id` = report ID), the
   same outbox path a worker report takes to its integrator, and wakes an
   idle parent. A child that ends without a final report sends one `child ended`
   message with its latest report and branch; a child whose turn ends without
   a report sends its parent a provisional report, as a swarm worker does. Mail survives restarts and compaction until acknowledged;
   `child list` returns each child's latest report at any time.
8. **Waiting and completion.** A child is **unfinished** while it is queued,
   provisioning, working, waiting or needs input. A parent parked with
   unfinished subsessions is `waiting-agents` ("waiting for 2 subsessions"), as
   an integrator with live workers is. A parent's success report is refused
   while it has unfinished attached subsessions: cancel them, or report
   `waiting`. A parent's turn ending does not stop its subsessions.
9. **Cancel and close.** A child its parent cancels ends `completed`
   (`stopped by parent session <id>`): its work stands, it is no failure, and it
   stays closable. When a person closes a session, its subsessions are closed with
   it: finished ones relabeled, working ones stopped and closed, queued ones
   cancelled. When a session ends otherwise (killed, failed or deleted), its
   attached unfinished subsessions are stopped, finished ones detached and kept
   for review, and queued ones cancelled; each child's own subsessions follow.
   There is no agent or dashboard detach; only a handoff to another member
   detaches a child, which keeps its lineage and stops counting toward its
   parent's waiting and completion. Pausing a parent does not pause
   subsessions. A swarm's teardown stops its workers, and their subsessions follow.
10. **Questions and messages.** Any session may message any live session its owner
    may message, including siblings and sessions outside its tree (see
    coordination.md); there is no peer cap and no listing requirement. Parent
    and child can always message each other, even when the parent is
    protected. A
    child asks its parent first. A decision only a human can make is a
    `needs-input` report on the child: it shows **Needs you** on the child
    with the child's own attribution, and the parent is told in its inbox.
11. **Checkouts and reviews.** At `child start` the server publishes the
    parent's branch and checks the child out from that commit on its own
    branch. Uncommitted parent changes are not included; the receipt says
    so. `child fetch` publishes the child's branch and copies it into the
    parent's checkout at `refs/remotes/children/<run-id>`. When the parent's
    branch has moved past the commit a child started from, `child list` and
    the dashboard mark it **stale**. Siblings never share a checkout.
12. **Usage.** A child is its own session, so its spend lands once in
    `run_costs` on the account segment that paid; `aether cost`, budgets and
    Insights treat it as any session. Tree totals add descendants' rows, split
    by paying account. Unmetered sessions are counted, never shown as $0, and
    subscription usage is not dollars.

## 2. No nudge

Subsessions are on by default, so nothing suggests them. The opt-in nudge, its
per-member snooze state and the extra choices offered with it are removed.

## 3. Limits of this design

- Budgets admit runs; they do not stop running ones. A parent can pass the
  workspace cap by what its live subsessions spend before they end.
- A parked parent that wakes for mail other than a child's result works
  beside its busy subsessions, so a parent can briefly exceed `child_max_live`.
- Only Claude Code, OpenCode and omp report dollars. Codex, Gemini and pi
  subsessions are unmetered.
- A child gets the workspace's environment and secrets like any session of the
  same owner; it does not get the parent's transcript or files outside its
  checkout.
- A child works in the parent's primary repository only.
