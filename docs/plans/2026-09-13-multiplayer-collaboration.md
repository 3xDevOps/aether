# Multiplayer collaboration: implementation plan

**Status:** Approved product direction after the 2026-09-14 UX review, not a
list of shipped capabilities. Proposed commands below are not available today.
This is the planning source of truth for subsequent implementation sessions;
exact flags, schemas, timing, storage limits, and delivery dates are not frozen.

**Context:** The initial ideation proposed Run Rooms, delegated execution,
missions, work contracts, swarms, evidence packets, shared engineering memory,
and flow intelligence. Later directions included persistent named agents,
agent-proposed missions, and incident/release governance. Independent analysis
checked those ideas against Aether's runtime and authority model. The discussion
then made an agent-facing CLI and skill, with an integrator agent orchestrating
workers, a central requirement and examined both relevant Orca projects.

The approved UX review kept the core features but removed unnecessary choices,
states, and administrative chores. This revision applies those decisions to the
same document rather than maintaining a competing plan. The nine feature groups
below replace the previous eighteen-item inventory; later directions remain
explicitly deferred rather than becoming implicit implementation requirements.

**Guiding rule: fewer visible concepts and required steps, while the server
handles the necessary complexity.** Simplify the interaction, not enforcement.

Aether is a self-hosted server, CLI, and dashboard for coding agents. A
**workspace** is a repository and its shared settings and history. A **run** is
one agent execution with its own container, checkout, branch, and terminal
transcript. A **harness** is the coding-agent CLI Aether launches. A **mission**
is a durable objective spanning tasks and runs, coordinated by an **integrator**
agent. Ordinary single-agent runs do not require a mission.

The product has two everyday workflows:

1. **Work together on a run:** watch, comment, take control, send an instruction,
   or hand responsibility to a teammate with the current context attached.
2. **Delegate a mission:** authorize an objective and limits, let an integrator
   coordinate workers, then review evidence for the combined result.

The feature sections are implementation slices, not nine new screens or boards.

---

## 1. What changes from the original proposal

| Original idea | Recommendation |
|---|---|
| Run Rooms | Keep. One shared conversation, visible presence, and Take control / Release control. Enforce control on the server across input paths, not just the dashboard composer. |
| Delegated execution | Keep. Extend the existing account-sharing experience without a general-purpose policy builder. Distinguish Aether admission rules from actual access to native credentials. |
| Missions | Keep. One objective and one integrator. Separate work, execution attempts, evidence acceptance, and Git integration internally. |
| Work contracts | Put expected scope and shared interfaces in the task specification. Keep negotiation and observed overlap; remove separate contract administration and scope leases. |
| Swarms | Saved mission configurations using the same CLI, integrator, and template mechanisms. No second orchestrator or swarm lifecycle. |
| Evidence packets | Move earlier and capture automatically. They underpin handoff, review, retry, forks, and integration without becoming paperwork. |
| Engineering memory | Defer durable workspace records until the coordination loop works. Initially retain decisions in mission discussions and evidence packets. |
| Flow intelligence | Extend the existing attention board and approval inbox. Add analytics only after the underlying states are reliable. |
| Persistent agents and autonomous missions | Deferred directions, not a committed release. Persistent identity does not mean unlimited lifetime, spending, or authority. |

**The core product should be: humans delegate bounded objectives; integrator
agents coordinate execution; Aether preserves authority, state, evidence, and
human control.**

---

## 2. Existing Aether behavior that the design must respect

The repository-grounded analysis established these distinctions. Consult the
current operational guides and source when implementing; this table is not a
claim that the proposed features already exist.

| Existing behavior | Consequence |
|---|---|
| Multiple members can write to the same run terminal. | A driver lease is new runtime enforcement, not just presence UI. |
| Account sharing exposes the selected account's complete writable home, credentials, environment, and configuration. | Workspace or harness selectors cannot restrict what those credentials can actually do inside the container. |
| Revocation blocks subsequent launches and relaunches, not existing containers. | Revoke access now needs a distinct stop action and cannot recall copied credentials. |
| Workspace budgets refuse new launches once recorded spend reaches the cap. Running work continues; some harnesses are unmetered. | Do not advertise hard per-run dollar ceilings without a different enforcement mechanism. |
| The coordination socket authenticates the originating run and exposes a deliberately narrow interface. | This is a strong foundation for an agent CLI; do not give containers a human's SSH credentials. |
| Peer messaging currently depends on observed file overlap. | Missions need explicit communication relationships before workers edit anything. |
| Diff snapshot objects depend on the run checkout and disappear with its cleanup. | Durable evidence must retain its referenced objects independently. |
| `close --outcome merged` records a human declaration. | It is not proof of Git integration. |
| The approval service records decisions. That record alone is not an execution barrier. | Consequential actions need enforcement on the actual execution path. |
| Workspace identifiers organize work, but current permissions are not per-workspace tenant isolation. | New missions or grants must not imply private workspaces. |

Sources: [teams](../teams.md), [security](../security.md),
[coordination](../coordination.md), [MCP bridge](../mcp-bridge.md),
[permissions](../../internal/permissions/permissions.go),
[snapshot storage](../../internal/gitengine/snapshot.go),
[run closure](../../internal/scheduler/steer.go), and
[approval decisions](../../internal/approvals/inbox.go).

---

## 3. What to borrow from Orca

Two relevant projects named Orca were inspected rather than assuming which one
was intended:

- **[araa47/orca](https://github.com/araa47/orca)** provides an agent-facing CLI,
  a coordination skill, worker spawning and monitoring, and reusable team-role
  prompts. Its [integrator role](https://github.com/araa47/orca/blob/main/skills/sprint-team/references/integrator.md)
  merges validated work and checks the combined result.
- **[stablyai/orca](https://www.onorca.dev/docs/cli/orchestration)** documents a
  more explicit model of tasks, execution attempts, supervised workers,
  messages, and gates. Its [skill system](https://www.onorca.dev/docs/cli/skills)
  uses small discovery instructions that load version-matched guidance from
  the CLI.

The strongest ideas to adopt:

1. **The agent operates the orchestrator through a CLI.**
2. **The skill teaches workflow; the runtime implements authority and state.**
3. **A work item and its current execution attempt are different objects.**
4. **Workers ask the coordinator questions through a routable channel**, not an
   inaccessible local question dialog.
5. **Completion is explicitly reported**, including failure.
6. **Messages remain available until acknowledged.**
7. **Contact loss is not proof that a worker stopped.**
8. **Results remain inspectable after runtime resources are released.**

Do not copy either implementation wholesale:

- Aether already has server-owned containers, transcripts, Git isolation, and
  recovery; it does not need another tmux supervisor.
- Do not copy a merge-first, validate-afterward integration workflow onto a
  protected delivery target.
- Do not equate a worker's success report with independently verified completion.
- Do not expose runtime-global reset or cleanup authority to ordinary workers.
- Do not make agents perform routine release/retain bookkeeping after every
  report. The server should preserve results and release settled resources.

**For Aether, use one integrator agent for orchestration and integration.**
There is no separately configurable coordinator role in the initial scope.
These projects are design references, not new runtime dependencies or evidence
that Aether already implements their behavior.

---

## 4. Foundational contracts

These rules apply to every feature. They are correctness requirements, not
extra choices that users must configure on each run.

### Identity is not one field

Record:

- **Actor:** the authenticated human, originating run, or server action that
  performed an operation.
- **Authorizing human:** who granted an automated operation its authority.
- **Run owner:** the human responsible for the workflow and notifications.
- **Account owner:** whose native model account and environment back execution.
- **Execution configuration:** the selected image, configuration snapshot, and
  credential references, never secret values.

The actor must not be rewritten as the owner merely because an agent acted on
that person's behalf. Show the controller and relevant ownership on the run;
keep the complete record inspectable without requiring five identity choices
at launch.

An execution configuration records what Aether supplied. It cannot prove that a
mutable home never changed or that a process used no other reachable credential.

### Four statements must remain distinct

> The agent reported success.
>
> A command exited successfully.
>
> The required evidence was accepted.
>
> The accepted change was integrated.

None implies the next automatically. A task shown as Done means its required
evidence was accepted, not merely that its worker stopped.

### Authority is enforced outside the model

A skill, prompt, message, or role name cannot grant permissions.

The server checks current authority for every consequential operation. A worker
cannot become an integrator by changing an environment variable or passing
another run's ID. Native subagents inside one container are not automatically
separate Aether workers with independent identities.

### Durable decisions, replaceable processes

Tasks, messages, approvals, evidence, and attempt identities survive the
integrator process. Presence is ephemeral. Authority must not be inferred from
presence.

An **attempt** is one authorized execution of a task, backed by an Aether run.
A retry creates a new attempt; the task remains the same work item. An internal
**authority generation** changes when control or the integrator assignment is
replaced, allowing the server to reject commands from an obsolete assignment.

### No false guarantees

- Terminal delivery is not agent comprehension.
- A heartbeat is not progress.
- A worktree is not complete credential isolation.
- A budget estimate is not a hard spending limit.
- A locally append-only audit log is not immutable against the host administrator.

---

## 5. Refined feature specification

### F1. Run Rework: controlled collaboration

**Purpose:** several humans can contribute to one live run without becoming
competing invisible input sources. A Run Room is the collaborative view of an
existing run, not another execution object.

#### Required behavior

- Each run exposes who is watching, who is controlling, its shared conversation,
  and open blockers.
- Collaborators can comment and send to agent; viewers remain read-only.
- A sidebar on the right, initially collapsed, can be pulled up and serve as a feature rich chatroom. Comments and Injections both get sent and displayed here. 
	- Comments are cosmetic - they are, as their name suggests, intended for a way for humans in the run to send each other messages. 
	- Steer Requests are send to agent instructions that will be injected directly to steer the agent from someone who doesn't have full native active steering control over the agent's terminal.
		- Obviously, steer requests won't work if the run is under protected status. Injection attempts for the run should be removed immediately after the run owner clicks "protect run"
		- Steer requests from other collaborators cannot influence the session immediately. By default, Injection requests displayed as queued in the chat and have a 45 second countdown (make a visual for this on the UI) before they are injected officially to the agent. During this time, the current control user of the run have the option to deny the injection or to approve it for injection immediately.  
	- The same "image upload" logic for the terminal should apply here. Steer requests can contain references to images, in which case users can upload images to the server and the server will convert the image to a persistent file so that the eventual message sent to the agent represents the image as /path/to/image.png  
- Use **Take control** and **Release control**. No request queue or token-passing
  ceremony initially.
- Taking occupied control requires a clear confirmation and notifies the current
  controller. Existing permissions still apply.
- A solo user gets uncontended control through the interaction, without a
  separate request/approval step.

One current control lease authorizes full terminal interactive steering. The server records
its holder and authority generation, and rejects stale input after transfer,
revocation, or expiry. These details stay out of the everyday interaction.
Control is session-bound: a second tab or connection does not become another
simultaneous writer merely because it belongs to the same member.

A documented reconnect window expires disconnected control. Reconnection does
not restore control that somebody else acquired or replay raw input. Losing
control leaves observation available when the member still has view access.

The promise is **serialized Aether-mediated human control**, not prevention of
every mutation by a process that already has filesystem or credential access.

#### Conversation, input, and interruption

Agent-to-agent injections and questions can still use structured correlation
through the CLI in F4; that is not a reason to burden everyday conversation.
#### Delivery contract

Persist instruction identity and authorization before attempting input. Report
**Sent**, **Not sent**, or **Delivery uncertain**, separately from agent replies.

#### Steering Delivery Rework

Rework the current approach and serialize injection and steering messages with hooks instead of standard input. Write a hook (or multiple hooks if the file is incompatible with multiple cli agents) that is compatible with most mainstream coding agents that will programmatically allow us to steer and inject messages into sessions in the most streamlined way possible. Document how users are able to install this hook. 
#### Human versus integrator control

An authorized integrator may hold steering authority over its workers. A human
takeover fences that authority for the affected worker. Integrator follow-ups
remain visible as messages but cannot bypass the human through another input
path. Releasing control explicitly returns the worker to eligible orchestration;
it does not stop unrelated workers or undo work already accepted by the agent.

Emergency stop remains independent of the driver lease. Administrative stop
authority must not silently become credential-use authority. Taking control
never grants additional account permissions.

### F2. Evidence packets, handoffs, and forks

**Purpose:** transfer an inspectable state of work, not a persuasive summary.
Evidence should improve existing actions, not introduce another workflow.

#### Automatic capture

Capture a factual evidence summary when work is submitted, handed off, or
finishes. Build deterministic facts without requiring an agent to fill in a
report or make an extra model call. Agents and humans may add explanations.

An **evidence packet** is a manifest of retained state and supporting observations,
plus that optional commentary. A **snapshot** identifies captured repository
contents; it is not a clone of a running environment.

#### Packet contract

- objective and current recorded plan;
- run and task/attempt identity where applicable;
- base commit, submitted commit or retained tree;
- changed files, relevant patch intervals, and explicitly excluded material;
- supplied image and configuration references;
- observed command results and supporting output;
- unresolved assumptions, failures, security concerns, questions, and approvals;
- relevant decisions and peer messages;
- next proposed action and its author;
- creator, capture time, and event-log boundary.

Every statement must have provenance: server-observed, harness-reported,
agent-authored, human-reviewed, or imported from an external system.
Do not claim all commands executed for a harness whose terminal is the only
available observation surface.

#### Retention and consistency

- Retain referenced objects independently of the live checkout, including
  objects currently reached through Git alternates and referenced transcript
  segments.
- An accepted packet cannot silently change when its branch moves.
- A capture taken while files are changing must not claim an atomic point-in-time
  environment snapshot.
- For verification, execute against a frozen candidate tree in a separate checkout.
- Missing, truncated, expired, or inaccessible evidence must be visible.
- Packet access follows the underlying authorization boundary. Export is explicit;
  transcripts, screenshots, and untracked files can contain secrets.
- Set bounded retention and storage policy before implementation. Retention is
  not a promise to store unlimited output forever.

Automatic release of settled runtime resources must preserve submitted work and
required evidence first. If preservation fails, report the failure and retain the
recoverable resources; do not turn cleanup into silent data loss.

#### Handoff

Preserve `aether handoff <run> <member>` as an immediate authorized transfer.
Notify the recipient and attach the current factual summary and open questions.
Do not require a recipient acceptance handshake or wait for narrative generation.
Record outgoing owner, incoming owner, and actor.

Ownership transfer must not silently change accounts or confer credential grants.
A missing summary does not prevent stopping or handing off a run. Missing
required evidence can still prevent accepting the work.

#### Fork

A fork is an explicit later action that creates:

> a new run from retained repository state, selected evidence, and newly
> authorized execution configuration.

It does not claim to clone hidden model context, running processes, browser
sessions, or credentials. Preserve a link to the source and state what was
restored or excluded. Fork and export are not required steps in finishing a run.

**Acceptance:** after the source container and checkout are removed, a reviewer
can inspect retained changes and evidence. Handoff succeeds without a recipient
handshake and labels unavailable context. A later fork restores the declared
repository contents. Unsupported or missing evidence is unavailable, not verified.

### F3. Delegation grants and execution gates

**Purpose:** delegate deliberate authority without calling unrestricted
credential sharing restricted execution.

#### Extend account sharing, not a policy builder

Start from the existing account-sharing surface. Show who receives access, what
that access actually exposes, when it expires, and how to revoke it. Per-launch
approval is an optional setting, not the normal path.

Do not initially expose a general matrix of launch/steer/approve/push permissions,
token limits, dollar limits, retry limits, and credential modes. Mission-wide
execution limits belong in mission authorization, where orchestration needs them.

#### Separate two contracts internally

**Aether-operation authority** determines what Aether admits: the grantee,
authorized account and launch scope, expiry, revocation, and operations allowed
within an approved mission.

**Execution credential authority** determines what a container can actually do:
the repositories its Git credential can access, reachable services and production
environments, provider spending limits, and whether credentials can be copied or
altered.

An Aether-operation grant does not create a credential boundary.

**Initial recommendation:** retain explicitly labeled trusted whole-home account
sharing. Do not add no-push or repository-only switches that imply restrictions
the mounted credentials bypass. Workspace and harness selectors constrain launch
admission, not the credentials' subsequent reach.

Restricted execution requires separately scoped credentials or a genuinely
enforcing credential/action service. That is a later security architecture
decision, not a mode added to the initial sharing dialog.

#### Close the transitive-control gap

If someone can steer a credential-bearing run, they can influence what those
credentials do. A grant must therefore determine who may control delegated runs,
not merely who may launch them. Default team-wide steering must not silently
broaden a narrow grant.

The same applies to handoff, relaunch, integrator replacement, templates, and
scheduled launches. An admin may stop execution without receiving implicit
permission to use somebody else's account.

#### Mission authority before worker spawning

A human authorizes the objective, workspace/repository, allowed accounts and
harnesses, eligible controllers, orchestration operations, and mission-wide
concurrency and total attempt allowance. Retries consume the same attempt
allowance rather than creating an unbounded second pool.

Present a concise launch summary with safe defaults and inspectable limits, not a
new per-worker permissions form. Within that approved boundary, the integrator
can dispatch and coordinate without asking a human at every step. It must ask
before expanding scope or authority. New grants are not implied by templates,
role names, or skill text.

#### Revocation

Expose two distinct outcomes:

1. **Revoke future use:** deny subsequent launches and newly authorized operations.
2. **Revoke and stop affected execution:** withdraw authority and terminate
   affected runs, reporting any failures.

Neither recalls a credential copied outside the container. Name any required
provider-side revocation. Record grant creation, use, expiry, and revocation.
Check expiry at use, not only in a periodic cleanup job.

#### Approval gates

A **gate** is a requirement checked before a specified action proceeds. A
consequential approval binds to the exact request version: action, account,
execution configuration, relevant repository or artifact revision, limits,
authorized approver, and expiry where applicable.

Changing the request invalidates the approval. Recording approved is insufficient
unless the execution path checks it. Do not require human approval of routine
task transitions that remain inside the authorized mission.

#### Budget honesty

Keep the distinction between admission budgets, concurrency/attempt limits,
estimated spend, metered stop thresholds with possible overshoot, and
provider-enforced hard limits. These are enforcement facts, not five budget modes
users must choose from. Unmetered is unknown, not zero.

Concurrent admission must not let multiple requests independently consume the
same remaining allowance. Do not promise a hard dollar ceiling without enforcing
it where spending occurs.

**Acceptance:** expiry or revocation between approval and provisioning prevents
launch. A third teammate cannot obtain account-use power by taking control of a
delegated run. Relaunch, scheduling, retry, and worker spawning apply the same
current authority checks. Mission limits hold under concurrent dispatches.

### F4. Agent coordination CLI and skills

**Purpose:** make Aether's coordination system usable by any shell-capable coding
agent, with or without the Model Context Protocol (MCP).

This is a central feature, not optional polish for the conflict radar.

#### Architecture

Use the existing run-authenticated socket as the foundation:

```text
worker or integrator agent
        |
        +-- Aether coordination CLI
        +-- optional MCP interface
                    |
           same coordination service
                    |
       authority, tasks, messages, evidence
```

- agents will invoke this tool as `aether-internal`
- Automatically stage a version-matched CLI and coordination instructions into
  participating containers.
- Do not require manual skill installation, identity flags, or human login.
- Derive the calling run from its socket, not command-line identity claims.
- Bind task-attempt and integrator authority server-side.
- Keep CLI and MCP as interfaces to the same behavior.
- Scope discovery, messages, and orchestration to authorized relationships.
- Do not expose a human SSH key, dashboard token, Docker socket, or unrestricted
  control RPC to a worker.

Mission execution must not silently degrade into uncoordinated workers when a
required orchestration capability is unavailable. Preserve ordinary standalone
runs, but refuse mission operations whose required interface cannot be provided.

#### Proposed command surface

Use a namespace such as **`aether-internal`**; These are proposed command families, not existing commands
or frozen flags.

| Command family                                    | Purpose                                                                               |
| ------------------------------------------------- | ------------------------------------------------------------------------------------- |
| `status --json`                                   | Own identity, current assignment, capabilities, and coordinator.                      |
| `skill`                                           | Version-matched instructions for the caller's actual worker or integrator assignment. |
| `task show` / `task propose`                      | Inspect the current task or propose additional work.                                  |
| `send`                                            | Attributed messages to authorized peers.                                              |
| `inbox --wait` / `inbox --ack`                    | Bounded waiting, replay, and explicit acknowledgement.                                |
| `ask` / `reply`                                   | Durable, correlated questions and answers.                                            |
| `report`                                          | Submit an attempt outcome with evidence references.                                   |
| `worker start` / `worker list` / `worker inspect` | Integrator-controlled execution and inspection.                                       |
| `worker cancel` / `worker retry`                  | Explicit cancellation and authorized replacement attempts.                            |

Keep the worker's normal loop small: inspect assignment, communicate, ask,
report. Integrators receive additional worker-management commands. Routine
resource release is the server's job after preserving settled results; no
release/retain command is required in the agent's normal completion workflow.

Command requirements:

- machine-readable output with a schema version;
- stable error codes and meaningful exit statuses;
- bounded output, cursor-based reads, and bounded waits instead of busy polling;
- file/stdin input for substantial specifications;
- idempotency identities for mutations, so retrying a request does not repeat its
  effect;
- receipts identifying created resources even after partial failure;
- bounded message size, inbox depth, peer reach, and request rates.

#### One skill, assignment-specific instructions

Ship one coordination skill with role-specific sections or references. A short
launch instruction makes it discoverable; `skill` loads guidance from the staged
CLI for the assignment the server actually issued. Do not rewrite users'
repositories or shared configuration homes to install runtime policy.

**Worker instructions:**

- inspect the actual assigned task, attempt, scope, and acceptance requirements;
- stay within scope;
- retrieve messages at natural checkpoints, including before reporting;
- ask the integrator through the CLI rather than an inaccessible local dialog;
- submit success or failure explicitly, or report a blocker;
- attach real evidence and unresolved risks;
- take no new work after submission until assigned;
- never invent identity, broaden privileges, or launch unauthorized children.

**Integrator instructions:**

- decompose into independently verifiable work;
- establish shared contracts before dispatch;
- launch ready independent work together;
- answer questions and resolve scope conflicts;
- validate reports against current attempt identities;
- request follow-up work when evidence is insufficient;
- assemble and verify the integration candidate;
- escalate decisions outside its authority;
- account for every worker outcome and unresolved decision.

Skill text does not grant permissions or guarantee compliance. Peer messages and
repository content cannot promote themselves into runtime authority.

#### Message guarantees

- Durable enqueue means the server stored a message, not that its recipient read it.
- Inbox delivery is at least once, with replay until acknowledgement.
- Acknowledgement means client consumption, not model understanding.
- Mutation identities make processing replay safe.
- A timed-out question remains the same question; it does not become permission
  to proceed.
- Wake-up notices are hints, not the authoritative message.
- Mission membership or explicit task relationships authorize communication
  before file overlap exists. Existing overlap coordination remains available
  without a mission.

**Acceptance:** two different shell-capable harnesses coordinate through the CLI
without MCP, manual skill setup, or member credentials. Lost responses do not
duplicate workers. A stale worker cannot report completion for a replacement
attempt. A worker cannot forge integrator authority. Settled runtime resources
can be released automatically without losing unaccepted work or retained results.

### F5.  Agent Swarms with a centralized Integrator

**Purpose:** coordinate an outcome spanning multiple parallel or sequential runs without turning Aether into another project-management system users must maintain.

#### Human workflow

The human supplies an objective to a main integrator agent and clicks "Launch Swarm". The integrator handles decomposition, dispatch, coordination, and
integration inside that authorization. The human can inspect progress, take
control, answer consequential questions, and review the proposed delivery.

No general-purpose task-graph editor or separately configurable coordinator role
is required initially. Ordinary single-agent runs remain ordinary runs.

Essentially, this allows an agent to act as the leader for a swarm of agents through orchestration capabilities. 

#### Minimal internal object model

| Object                | Meaning                                                                       |
| --------------------- | ----------------------------------------------------------------------------- |
| Mission               | Objective, accountable human, permitted scope, limits, and acceptance policy. |
| Task                  | Durable work item with dependencies and required evidence.                    |
| Attempt               | One authorized execution of that task, backed by an Aether Run.               |
| Integrator assignment | Currently authorized coordinating run and its authority generation.           |
| Gate                  | Specific decision or evidence requirement.                                    |
| Submission            | Proposed result tied to an exact artifact revision.                           |

Do not rename Aether's existing Run to match Orca's different terminology.
Start with one workspace/repository per mission. Cross-repository missions are
not part of the initial scope.

#### Task progression

Present a short progression:

**Ready -> Working -> Review -> Done**

- Ready means the work is authorized and its required dependencies are satisfied.
- Working means a current attempt is executing it.
- Review means a result awaits assessment against its acceptance criteria.
- Done means the exact submitted revision has accepted evidence.

Show blockers alongside the task, with their owner and resolving action. Work
proposed outside the authorized scope remains a proposal, not a Ready task.
Record an explicit decision to abandon work without inventing a successful
outcome. Failed attempts may leave their task open for authorized retry.

Keep attempt IDs, authority generations, and detailed execution states
inspectable but out of the everyday task progression. Research and decision
tasks do not need a merged state. Code integration belongs to the mission's
integration result.

Dependencies refer to accepted, versioned outputs, not merely an agent process
exiting. Reject cyclic dependencies. Only the current attempt can submit an
authoritative current result.

An example mission retains the original ideation's objective:

```text
Mission: Add organization audit export
+-- Research protocol and storage constraints
+-- Accept the server/dashboard export contract
+-- Implement server export                 [worker]
+-- Implement dashboard download            [worker, independent]
+-- Review the authorization boundary
+-- Assemble changes and verify combined behavior
+-- Human delivery decision
```

The server and dashboard workers start after their shared contract is accepted.
The outline is an example, not a required sequence of roles for every mission.

#### Integrator authority

The integrator is a normal, observable Aether run with additional mission-scoped
permissions. One agent both orchestrates and integrates initially.

It can:

- dispatch approved work to sessions;
- create or refine tasks within an authorized scope;
- route questions and negotiate ownership;
- assess evidence under the mission's acceptance policy and request rework;
- cancel or retry within explicit policy;
- prepare an integration candidate.

It cannot:

- increase its own budget or authority;
- select an unauthorized account;
- approve a human-only gate;
- launch unlimited descendants;
- direct unrelated runs;
- mark its own assertions independently verified.

Workers do not spawn children initially. They propose subtasks to the integrator.
Hierarchical delegation is outside this implementation scope.

#### Integrator failure

If the integrator dies:

- workers may finish existing assignments and submit durable reports;
- new dispatches stop until an authorized coordinator resumes;
- an authorized replacement reconstructs state from the mission;
- the previous coordinator generation is fenced;
- no duplicate worker is launched merely because a response or heartbeat is missing.

Unknown liveness is an explicit observation, not an inferred failure. A quiet
agent or live terminal is not proof that its task is finished. Cancellation stops
execution; automatic release reclaims settled resources only after preserving
the work and evidence. Neither action silently abandons a task.

#### Integration contract

The integrator:

1. selects exact accepted worker revisions;
2. assembles them in an isolated integration checkout;
3. resolves conflicts;
4. obtains evidence for the combined candidate;
5. requests the human delivery decision;
6. lands or proposes that candidate through an authorized Git path.

A moving target branch invalidates assumptions. Compare the expected target
revision and re-evaluate the candidate when necessary. Changes to the candidate
or requested action invalidate stale approval. Individually passing worker
branches do not prove the combined change works.

Respect local-only versus mirrored workspace base ownership; never force-update
a mirrored base as an integration shortcut. External branch protections remain
necessary if workers hold credentials that permit bypassing Aether. A mission
alone does not restrict those credentials.

**Acceptance:** a worker reports success but required evidence is missing; its
task stays in Review, not Done. A retry supersedes an old attempt without
accepting late reports. An integrator restart recovers the same work without
duplicate dispatch. Combined verification failure prevents delivery. A target
branch change prevents reuse of stale integration approval.

### F6. Work scope and proactive coordination

**Purpose:** make intended ownership visible before conflicts occur, without
introducing a separate work-contract administration system.

Put scope directly in the task specification:

- expected paths/packages and semantic responsibility;
- shared API/schema interfaces and their accepted revisions;
- migrations and generated artifacts;
- shared tests;
- base and merge target;
- explicit exclusions.

The integrator establishes and revises this scope. Compare it against observed
changes and show intended overlap, observed overlap, and changes outside the
assigned scope in the existing conflict experience.

Workers can propose a split, scope change, or handoff through the coordination
CLI. The integrator resolves it within mission authority; humans handle choices
outside that authority. Preserve the decision and affected task revisions.

**Do not make every overlap block work.** A shared generated file and a
conflicting schema migration need different treatment. Different files do not
prove semantic independence either.

Remove advisory/negotiated/leased/escalated operating modes, separate scope
claims, and exclusive scope leases from the implementation scope. Task assignment
already identifies the current authoritative attempt; it does not require a
second lease system for files. There is no filesystem lock or automatic deletion
of work that exceeds its declaration.

The [Anthropic compiler experiment](https://www.anthropic.com/engineering/building-c-compiler)
supports prioritizing independently verifiable decomposition. It does not
establish that file locks, a particular board, or a fixed agent count is the
solution.

**Acceptance:** workers negotiate a shared interface before editing. The
integrator can revise their task scopes without a separate contract-management
workflow. Out-of-scope edits remain visible and require disposition rather than
being discarded or silently accepted.

### F7. Swarm templates

**Purpose:** repeat a proven collaboration pattern. A swarm template is a saved
swarm mission configuration, not a separate lifecycle or scheduling system.

A template instantiates:

- a mission;
- task specifications and necessary roles;
- shared interfaces, scope, and dependencies;
- the integrator assignment;
- account and harness choices;
- mission-wide concurrency and attempt limits;
- gates and evidence requirements.

Reuse Aether's existing template and scheduling concepts. No separate template
designer or independent workflow engine is required.

Start with one pattern:

> Contract definition -> independent implementers -> review -> integration ->
> combined verification -> human delivery decision.

Do not force that entire pipeline onto a one-file fix. Omit unnecessary roles;
a separate review agent is justified by the task's risk and acceptance criteria,
not by a mandatory roster. More agents is not a success criterion.

Incident, migration, and PR-review shapes remain possible saved workflows, not
additional runtime modes. Add them when the first template demonstrates reliable
settlement and recovery and a concrete workflow needs them.

**Acceptance:** the instantiated mission pins its template version. Editing the
template cannot mutate running work. Scheduled launches recheck current authority
and do not accumulate missed executions into an unexpected launch storm.

### F8. Shared decision records

**Purpose:** preserve inspectable engineering decisions, not unlimited
conversational memory or a competing source of project instructions.

**Defer durable workspace records until the coordination loop works.** Initially
retain decisions in mission discussions and evidence packets. Promote useful
ones into workspace records when later executions demonstrate a retrieval need.

A durable record contains:

- statement and scope;
- author, timestamp, and provenance;
- evidence;
- status: proposed, accepted, superseded;
- revision and supersession links.

Do not add confidence scoring, routine expiry/review-date administration, or
automatic package-entry retrieval to the initial design.

Rules for the later records:

- Agents can propose records; acceptance follows explicit authority.
- Repository guidance remains authoritative project instructions.
- Retrieved records cannot grant privileges or override platform policy.
- Conflicting accepted records are surfaced, not silently resolved by semantic
  similarity.
- Runs record exactly which revisions were supplied.
- Retrieval is bounded, at launch or through an explicit CLI query.

For example, an accepted package-scoped decision can state that the public API
remains v2-only and link to the design and compatibility evidence. A vector
database is not required to retrieve that record.

**Acceptance:** a reviewer can see which decision caused an assumption.
Superseding a record does not rewrite a previous run's history. Initial missions
remain useful without a workspace-memory feature.

### F9. Actionable flow and attention

**Purpose:** tell the team what needs a decision next.

Extend the existing attention board and approval inbox rather than creating a
separate action-inbox product. Show actionable work before analytics about it.

Each blocker needs:

- a structured reason;
- an owner or eligible decision-maker;
- when it began;
- what it prevents;
- the action that resolves it;
- links to supporting evidence or the actual error.

Useful initial reasons include human decision, dependency, credentials/environment,
verification, integration, and unavailable integrator. Distinguish a reported
concern from an enforced hold or a paused container.

Prefer concrete actions: **Answer**, **Review**, **Retry**, **Take control**.
Group related requests, deduplicate repeated notifications, and preserve
unresolved requests when everyone is offline. A mission should not generate one
human notification for every worker event.

Later metrics can cover decision latency, age of integration-ready work, rework,
failed attempts, time waiting for verification/integration, and measured cost
per accepted/integrated outcome with metering coverage. They are not prerequisites
for the attention experience.

Do not call duplicate work prevented merely because an overlap warning appeared.
Do not score developer productivity by agent count, tokens, or messages.

**Acceptance:** someone arriving after an overnight run can identify the next
actionable decision and its owner without reading every transcript or visiting
a second board.

---

## 6. Recommended implementation order

### Release A — Reliable shared operation

- F1: Run Room control, attributed send-or-refuse steering, and shared discussion.
- F2: automatic evidence summaries, retained objects, and immediate handoff.
- F1/F2: retained diff and transcript annotations, not browser capture or forks yet.
- F4: agent coordination CLI for status, messaging, questions, and reports, plus
  automatically discoverable worker instructions.
- F9: actionable questions and ownership in the existing attention experience.

This improves existing runs without requiring users to adopt missions. Preserve
the current trusted account-sharing boundary; do not imply stronger grants.

### Release B — Supervised agent teams

- F3: minimum mission-scoped authority, eligible controllers, and execution limits
  **before enabling worker spawning**.
- F5: minimal mission/task/attempt model.
- F4/F5: integrator skill, worker lifecycle CLI, and durable recovery.
- F6: task scope and proactive coordination in the existing conflict experience.
- F2/F5: exact-revision evidence and combined-candidate verification.
- F1/F5: human takeover and the final delivery gate.

**This is the first major multiplayer demonstration:**

> A human authorizes a mission. An integrator launches two workers using different
> harnesses. They negotiate a shared interface through Aether, submit retained
> evidence, and the integrator verifies the combined result before requesting the
> human delivery decision.

That scenario must include a disconnected client and a restarted integrator.
The integrator can act within approved scope without approval for every worker
step; expanded authority and final delivery still require human decisions.

### Release C — Reusable team operation

- F7: saved mission/swarm templates.
- F3: simpler sharing expiry/revocation controls and optional per-launch approval.
- F8: durable decision records only when retrieval needs are demonstrated.
- F9: flow analytics after the underlying states are reliable.
- Later portions of F1/F2: preview capture and explicit repository-state forks.

Do not use this release as an excuse to add a permission matrix, graph editor,
scope leases, or a new board. Each addition must improve one of the two everyday
workflows.

### Deferred directions — Separate design approval

Preserve these ideas from the original session without treating them as committed
implementation requirements:

- **Restricted execution:** separately scoped credentials and real action-path
  enforcement, before promising repository-only, no-push, or production limits.
- **Persistent named agents:** durable configuration, responsibility, and an
  accountable human reused across ordinary executions; not immortal processes
  or human impersonation identities.
- **Agent-proposed missions:** proposals generated inside an already authorized
  run and budget, awaiting human acceptance before launching new work or gaining
  privileges. No self-replenishing spending or approval loop.
- **Incident/release governance:** quorum approval and restricted production
  authority only where issued credentials cannot bypass those restrictions.
  An incident template alone does not provide production isolation.
- **Audit exports:** tamper-evident or externally retained packages where needed,
  with an explicit host-administrator threat model rather than an unsupported
  immutability claim.

Existing scheduling behavior must still recheck authority on every fire and skip
missed occurrences. A persistent definition does not create permanently valid
access or unlimited execution capacity.

---

## 7. Implementation boundaries

Keep the system structurally boring:

- **Scheduler:** processes, containers, pause/stop, automatic release, and recovery.
- **Coordination service:** run-authenticated messages and agent-facing operations.
- **Mission state:** tasks, attempts, assignments, gates, and submissions.
- **Permissions/grants:** current authority, including who can control delegated runs.
- **Git/evidence storage:** retained revisions and verification artifacts.
- **CLI, MCP, dashboard:** clients of the same contracts.

Reuse the event stream for delivery and projections, but do not derive
authoritative task state from free-form timeline messages. Critical state changes
and their durable audit records need a recoverable commit relationship.

Do not build:

- a second terminal supervisor;
- a generic distributed workflow language or task-graph editor;
- separate work-contract administration or semantic file locking;
- a second swarm scheduler or mandatory roster of agent roles;
- a vector database before decision-record retrieval is useful;
- production approval controls that credentials can bypass;
- autonomous retry loops without attempt and concurrency limits;
- manual resource-release chores that the server can safely handle.

### Verification before declaring a slice complete

Exercise the user workflows, not just the internal types:

1. **Two humans, one agent:** a teammate comments on a retained diff; the
   controller sends an attributed instruction; confirmed takeover fences the old
   connection; handoff transfers responsibility immediately with context attached.
2. **Mixed-harness mission:** a human authorizes bounded work; the integrator
   dispatches independent workers, answers CLI questions, assesses evidence,
   verifies the combined candidate, and requests delivery approval.
3. **Interrupted coordination:** a lost start response does not duplicate work;
   a replaced integrator recovers reports and questions; stale commands fail;
   human takeover cannot be bypassed.
4. **Unverified or unauthorized work:** missing evidence prevents acceptance;
   changing the delivery target invalidates approval; revoked account authority
   prevents new execution; preservation failure does not destroy recoverable work.

For each slice, resolve concrete protocol syntax, reconnect timing, supported
harness input handover, storage limits, and retention behavior against the current
source before implementation. These are implementation decisions, not additional
features or routine choices exposed to users. Update the affected operational
guides when behavior ships; this plan alone is not documentation of shipped APIs.

**The organizing center is the agent coordination CLI, version-matched skill,
and recoverable integrator loop.** Run Rooms give humans control, automatic
evidence makes results inspectable, grants bound authority, and missions give
that loop durable structure. Fewer visible concepts do not mean weaker guarantees.
