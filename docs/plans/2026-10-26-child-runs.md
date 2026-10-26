# Child runs: agents delegating to other agents

**Status:** Implemented in the pull request that adds `internal/children`.

**Goal:** An agent in any run (an ordinary run, a swarm integrator or a swarm
worker) can start a focused **child run** on another agent or model, for
example a Claude run asking Codex for an adversarial review, then message it,
wait for it and receive its result. Every child is attached to its parent and
to the original root, is visible and controllable in the dashboard like any
run, and spends only accounts and limits its owner already authorized.

A **run** is one agent execution with its own container, checkout and branch.
A **swarm** is a mission: an integrator run that starts worker runs for
tasks. An **agent account** is a member's vendor login; a **share** lets one
other member launch on it. A **child run** is a run started by another run
through `aether-internal child start`. Its **parent** is that run. Its
**root** is the top of its tree: the parent's root, or the parent itself. A
swarm's runs and their descendants form one tree keyed by the swarm.

## 1. Decisions

1. **One scheduler.** A child is an ordinary run: the server launches it
   through the same `LaunchWithOptions` path, budget gate and
   `AuthorizeLaunch` boundary as `run.launch` and `worker.start`. A child is
   reserved in `run_children` under a pre-minted run ID
   (`LaunchOptions.ReservedRunID`, which, unlike a mission's
   `AssignedRunID`, carries no worker semantics), and the run row is created
   only at dispatch. Swarm workers stay mission attempts: their parent in the
   run snapshot is the swarm's current integrator, derived, never stored, and
   their lifetime stays with the swarm.
2. **Lineage is server state.** The parent is the run whose socket made the
   call; nothing on the wire names it. A message body, a role card or a
   report never grants launch rights.
3. **Who may delegate.** Authorization follows the calling run's current
   owner, who also owns every child it starts.
   - Ordinary runs: the owner turned on **Child runs** (Settings → Child
     runs, `aether children on`), or allowed it for that one run (launch
     dialog **Options → Child runs**, run header **More → Allow child runs**,
     `aether run --children on`). A run's own setting overrides the member
     setting and covers its descendants. An "on" counts only while the member
     who set it still owns the run, so a handoff never carries one member's
     choice to another; an "off" always counts. A handoff of a child or its
     parent detaches the child. Only the owner turns it on; the owner or an admin
     turns it off. One member's choice never enables another member's runs.
   - Swarm runs: the swarm's execution choices are the authorization.
     Creating a swarm already lets agents start runs on those choices; the
     member setting does not apply.
   - Background (one-turn) runs cannot start children: they exit before a
     result arrives.
4. **Which agents and accounts.** These reuse the existing account shares
   and usage fallback; nothing widens them.
   - Swarm runs: an exact `(account, agent, mode, model)` choice of the
     swarm, as for `worker.start`, with the swarm's own fallback policy. The
     member's child-run settings never override a swarm's choices.
   - Ordinary runs: any agent the owner can launch, on the owner's own
     account by default, or on an account shared with the owner that the
     agent names. Two member settings, offered together the first time the
     owner turns child runs on and editable in Settings:
     - **Allow automatic fallback** (on by default, as for every launch
       today): when the child's account has used 95% of its Claude or Codex
       quota, at launch or mid-run, the child moves to the account with the
       most usage left among the owner's own and those shared with them. The
       agent and model never change. Off keeps every child on its account.
     - **Use subscriptions shared with me when I don't have my own** (off by
       default): when the owner is not signed in to the requested agent, the
       child starts on an account shared with them that is signed in.
   - A parent launched with an explicit fallback order or `--no-fallback`
     passes that policy to its children, so descendants only narrow it.
   - `child options` names each agent's state: **ready**, **exhausted**,
     **not installed** or **not signed in**, with the exact reason. A child
     receipt names the account it launched on and whether it fell back.
   - Every launch and every queued dispatch re-runs `AuthorizeLaunch` for the
     owner and account, so a revoked share, a removed member or a turned-off
     setting stops new children at once. Running children continue, as
     running runs do after a revoke today.
5. **Limits, per tree.** A workspace admin sets them (Repository → Workspace
   settings, `aether workspace settings --child-*`). Defaults:
   - `child_max_live` 8: children busy at once (provisioning or working).
     More are **queued**, not refused, and start as slots free, fairest
     parent first (fewest busy children, then oldest request). A child that
     reported its result, or that is parked waiting on its own children,
     holds no slot, so a reviewer never deadlocks its parent.
   - `child_max_depth` 3: root 0, its children 1, and so on.
   - `child_max_total` 100: children ever reserved in the tree.
   Detached children keep their tree, so detaching never resets limits.
   Workers stay uncapped as today, but their children share the swarm's one
   allowance. The workspace budget admits each child against its owner at
   launch and again at dispatch.
6. **Duplicates and loops.** `child start` takes an idempotency key scoped
   to the parent; a retry with the same key and inputs returns the first
   receipt, different inputs are refused with `-32003`. Starting the same
   agent on the exact task text of the parent or an ancestor is refused as a
   delegation loop; depth and total bound everything else.
7. **Results.** A child's success, failure or blocked report reaches its
   parent's inbox as a durable message (`correlation_id` = report ID), the
   same outbox path a worker report takes to its integrator, and wakes an
   idle parent. A child that ends without a report sends one `child ended`
   message. Mail survives restarts and compaction until acknowledged;
   `child list` returns each child's latest report at any time.
8. **Waiting and completion.** A child is **unfinished** while it is queued,
   provisioning, working, waiting or needs input. A parent parked with
   unfinished children is `waiting-agents` ("waiting for 2 child runs"), as
   an integrator with live workers is. A parent's success report is refused
   while it has unfinished attached children: cancel or detach them, or
   report `waiting`. A parent's turn ending does not stop its children.
9. **Cancel and detach.** When a run ends (killed, failed, closed or
   deleted), its attached unfinished children are stopped, finished ones
   detached and kept for review, and queued ones cancelled; each child's own
   children follow.
   `child detach` (or **Detach** in the dashboard) keeps a child running on
   its own; it keeps its lineage and tree, and stops counting toward its
   parent's waiting and completion. Pausing a parent does not pause
   children. A swarm's teardown stops its workers, and their children follow.
10. **Questions and messages.** Parent and child can always message each
    other (`send`, `ask`, `reply`), even when the parent is protected. A
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
12. **Usage.** A child is its own run, so its spend lands once in
    `run_costs` on the account segment that paid; `aether cost`, budgets and
    Insights treat it as any run. Tree totals add descendants' rows, split
    by paying account. Unmetered runs are counted, never shown as $0, and
    subscription usage is not dollars.

## 2. Nudge

Child runs are off until the owner turns them on. The dashboard suggests the
feature on a run whose agent tried `child start` while it was off, and in the
launch dialog when two or more agents are launchable. **Not now** snoozes it
for 7 days, then 30 days; the third **Not now**, or **Don't show again**,
stops it for good. The state is stored per member on the server. The nudge
opens Settings or allows child runs for that one run; it never turns on the
member setting.

## 3. Limits of this design

- Budgets admit runs; they do not stop running ones. A tree can pass the
  workspace cap by what its live children spend before they end.
- A parked parent that wakes for mail other than a child's result works
  beside its busy children, so a tree can briefly exceed `child_max_live`.
- Only Claude Code, OpenCode and omp report dollars. Codex, Gemini and pi
  children are unmetered.
- A child gets the workspace's environment and secrets like any run of the
  same owner; it does not get the parent's transcript or files outside its
  checkout.
- A child works in the parent's primary repository only.
