# Multiplayer collaboration: draft feature plan

- **Status:** Draft product and implementation-planning baseline, not a list of
  shipped capabilities. Proposed commands below are not available today.
- **Goal:** Let developers share control of long-running coding agents and
  delegate larger objectives to an agent that coordinates a team of workers.
- **Audience:** Developers and implementers who know Git, containers, and coding
  agents but have not participated in the design discussions.
- **Scope:** Enumerate the complete proposed feature set, its user-visible
  behavior, safety limits, dependencies, and acceptance criteria.
- **Not frozen:** Exact command flags, wire schemas, database layouts, harness
  integrations, retention limits, and delivery dates.

## Terms

### Runs, people, and interaction

- **Aether:** A self-hosted server, command-line interface (CLI), and dashboard
  for running coding agents in containers and sharing their work.
- **Workspace:** A repository and its shared settings, runs, history, and costs.
- **Run:** One agent execution with its own container, checkout, branch, and
  terminal transcript. It does not require a mission.
- **Harness:** The coding-agent CLI Aether launches, such as Claude Code or
  Codex. Harnesses differ in how they accept input and report activity.
- **Run Room / shared run:** The collaborative view of an existing run: people,
  control, conversation, and evidence. Not another execution object or workspace.
- **Presence:** Who is connected or watching. It conveys availability, not
  authority, progress, or proof that somebody read a message.
- **Run owner / workflow owner:** The human responsible for the run or mission
  and its notifications. Ownership can change without changing its account.
- **Controller / driver:** The human session or authorized integrator allowed
  to steer a run now. These names mean the same role, not two permissions.
- **Steering / injection:** Input intended to change what a live agent does.
  Injection submits an instruction; raw terminal typing is another input path.
- **Suggestion:** A proposed instruction awaiting the controller's decision.
  Ordinary discussion does not automatically become a suggestion or agent input.
- **Observation / question / directive:** Information / a request for an answer /
  an instruction to act. These describe intent, not required composer modes or
  automatic permission to interrupt execution.
- **Annotation / anchor:** A comment and its reference to an exact diff hunk,
  transcript range, or captured preview, rather than a moving screen position.
- **Handoff:** Immediate transfer of workflow ownership, with notification and
  available evidence. It is not an account transfer or model-context transfer.
- **Fork:** A new run from selected repository state and evidence under fresh
  authorization, not a clone of the original process or hidden model context.

### Teamwork and planning

- **Mission:** A durable objective containing tasks, dependencies, worker runs,
  decisions, and a proposed combined result.
- **Task:** A defined piece of work with scope and acceptance criteria.
- **Attempt / dispatch:** One authorized execution of a task, backed by a run.
  Dispatching assigns and starts that attempt. A retry creates a new attempt.
  Orca uses Dispatch as an object name; Aether need not add a second object.
- **Worker:** An agent run assigned a mission task.
- **Integrator / coordinator:** The agent run that organizes workers, assesses
  results, and assembles the combined change. One agent does both jobs initially.
- **Orchestration:** Assigning, starting, observing, and settling worker attempts
  and dependencies. The agent chooses strategy; the server enforces operations.
- **Dependency / task graph:** A prerequisite relationship between tasks.
  A directed acyclic graph (DAG) has no circular chain of prerequisites.
- **Work scope / work contract:** The task's declared responsibility: expected
  files, APIs, schemas, tests, exclusions, and merge target. It is advisory
  ownership, not a filesystem access-control rule.
- **Shared contract:** The agreed interface between cooperating tasks, such as
  an API response schema. Different from a task's work-scope declaration.
- **Conflict radar:** Aether's existing detector of overlapping file changes.
  It reports observed overlap, not certainty that two changes are incompatible.
- **Swarm / swarm template:** A team executing a mission / a reusable definition
  of its roles, tasks, dependencies, limits, and integration workflow.
- **Named agent:** A persistent configuration, responsibility, and accountable
  owner reused across executions; not a permanently running process.
- **Agent-proposed mission:** A proposed objective awaiting human authorization,
  not an automatically permitted new workload.
- **Decision record / engineering memory:** A scoped, versioned engineering
  statement with author, evidence, acceptance status, and possible supersession.
- **Flow intelligence:** Information about waiting and delivery bottlenecks,
  rather than a score based on how active agents appear.

### Authority and limits

- **Actor / operator:** The human, originating run, or server action that
  actually performs an operation. Automated actions must identify the run.
- **Authorizing human:** The person whose grant permits an automated operation;
  not necessarily its actor, workflow owner, or account owner.
- **Agent account / account owner:** The selected member's model login, native
  credentials, and persistent environment / the member who owns them.
- **Member home:** The persistent writable directory mounted into runs using
  an account. It can contain configuration, installed tools, and credentials.
- **Execution identity / configuration:** The image, environment, and credential
  references supplied to a container. This describes execution setup, not a
  new human identity or proof of every credential the process subsequently used.
- **Delegated execution / grant:** Authorized use of selected execution capacity /
  the recorded permission, recipient, scope, and limits that allow it.
- **Capability:** A specific operation the server permits, such as steering or
  starting a worker. A role name or skill text alone does not grant it.
- **Control lease / driver token:** The server's temporary exclusive assignment
  of interactive control. It is not a vendor login credential or a filesystem lock.
- **Assignment lease:** A temporary exclusive claim to a task's work scope,
  considered for later negotiation. Different from live interactive control.
- **Authority generation / epoch / fencing:** A version of a control or
  integrator assignment / rejecting commands from older versions. This prevents
  a replaced controller from regaining authority through a stale connection.
- **Admission budget:** A rule deciding whether new work may start, based on
  recorded spend and any reservations. It does not cap an in-flight request.
- **Metered / unmetered usage:** Usage with available accounting / usage without
  reliable accounting. Unmetered means unknown, not free.
- **Stop threshold / hard limit:** A reaction after observed usage crosses a
  threshold, possibly overshooting / a ceiling enforced where spend occurs.
- **Revocation:** Withdrawal of authority. Denying future launches, stopping
  current runs, and invalidating copied vendor credentials are separate actions.
- **Gate:** A requirement checked before a specified action may proceed.
  - **Decision gate:** An authorized choice is required.
  - **Evidence gate:** Required observations must support acceptance.
  - **Human approval gate:** A human must authorize the exact action/revision.
  - **Quorum approval:** A policy requires multiple eligible human approvals.
- **Blocker / attention item:** A reason work needs a decision or intervention,
  with an owner and resolving action. It does not by itself pause a container.

### Evidence and delivery

- **Artifact / snapshot:** A retained output / captured repository or preview
  state. A Git tree captures file contents; it is not a running-environment clone.
- **Evidence packet / manifest:** Retained state and supporting observations /
  the structured inventory of their identities, sources, and capture boundaries.
- **Provenance:** Where a claim or artifact came from: actor, source run,
  revision, observation mechanism, and time where available.
- **Submission:** An attempt's proposed outcome and supporting evidence.
  Submitting success does not accept the work or approve delivery.
- **Acceptance criteria / accepted output:** The observable requirements for
  a task / the exact submitted revision judged to satisfy those requirements.
- **Integration candidate:** The exact combined revision assembled for
  verification and delivery, not merely a collection of passing worker branches.
- **Integration contract:** Which revisions may be combined, who assembles them,
  what must be verified, and how the result may reach its target.
- **Delivery:** Landing the approved result through the configured Git workflow.
  Production deployment requires separate authority where applicable.
- **Audit package:** Retained decisions, actors, evidence, and artifacts for an
  outcome. An append-only local log is not immutable against the host administrator.
- **Restricted execution:** Credentials, reachable services, and action paths
  actually enforce permitted behavior; a restrictive prompt is not sufficient.

### Agent interfaces and recovery

- **Coordination CLI / skill:** Commands for agent communication and authorized
  operations / instructions teaching when and how to use those commands.
- **Version-matched guidance:** Instructions bundled with the CLI that is
  actually running, so examples match its supported commands and capabilities.
- **MCP:** Model Context Protocol, an optional way for a harness to call tools.
  The proposed CLI supports shell-capable agents that do not use MCP.
- **PTY / coordination socket / bridge:** A pseudo-terminal carrying raw input
  and output / a run-local endpoint that identifies its caller / the component
  translating CLI or tool calls into server coordination operations.
- **Mailbox / durable enqueue:** Stored messages for a recipient / persisting
  a message before reporting that it was queued successfully.
- **Delivery acknowledgement:** Confirmation that the client consumed a batch,
  not that the model understood it. Unacknowledged batches may be replayed.
- **At-least-once delivery:** Messages may arrive again after interruption;
  processing must tolerate duplicates.
- **Cursor / idempotency identity / receipt:** A position for resuming reads /
  an identifier preventing a retried operation from duplicating its effect /
  the operation's result, including partial effects or uncertainty.
- **Heartbeat / liveness:** A signal that a participant is reachable / an
  observation of whether it is live, exited, or unknown. Neither proves progress.
- **Settlement:** Recording an attempt's final reported or observed outcome.
  It does not itself mean the task passed review or that an idle process exited.
- **Cancellation / release:** Stopping execution / reclaiming settled runtime
  resources while retaining the work and evidence required by policy.

## Two user workflows

1. **Work together on a run.**
   - Open an existing run and see who is watching or controlling it.
   - Send an instruction when in control, or explicitly suggest one.
   - Comment on a diff or terminal output.
   - Hand responsibility to another developer with a useful summary.
2. **Delegate a mission.**
   - Give an objective and bounded authority to an integrator agent.
   - Let it assign independent tasks to workers and answer their questions.
   - Inspect progress, take over a worker, or resolve a decision when needed.
   - Review evidence for the combined result before authorizing delivery.

## Design defaults

- Keep the two workflows above as the product surface. The numbered features
  below are implementation slices, not a requirement for 18 screens or boards.
- Keep solo runs simple: no mandatory mission, manual control queue, task graph,
  or message classification before interacting with an agent.
- Keep the everyday composer plain text. Do not require users to choose between
  observation, question, directive, and blocker for every message.
- Keep `aether inject` send-or-refuse. Never report an undelivered suggestion as
  a successful injection.
- Keep ownership handoff immediate; notify the recipient without requiring an
  acceptance handshake. Attach available evidence automatically.
- Let the integrator act within the approved mission. Require human decisions
  for expanded scope or authority and final delivery, not every worker step.
- Reuse the run view, attention board, templates, scheduler, event stream, Git
  engine, and coordination bridge. Do not add a second execution platform.
- Keep sophisticated controls inspectable, but out of the normal path until
  the user needs them.

## Existing behavior versus proposed changes

- **Already present:** Shared terminal attach, injection, ownership handoff,
  roles, account sharing, branches, transcripts, diff history, approvals,
  presence, attention states, templates, schedules, and conflict radar.
- **Already present:** A bounded run-to-run mailbox authenticated by a socket
  mounted into each run. MCP tools and harness status reporting use the bridge.
- **New:** Exclusive interactive control, explicit suggestions, retained evidence,
  mission/task/attempt state, an agent coordination CLI and skill, and an
  integrator with server-enforced orchestration authority.
- **Important limits to preserve:**
  - Native account sharing exposes the complete writable home and credentials;
    it is not restricted credential delegation.
  - Account revocation blocks later launches/relaunches, not existing containers.
  - Budgets currently restrict new admissions using recorded spend; running and
    unmetered usage prevent a hard-spend guarantee.
  - A terminal write does not prove that an agent understood or obeyed a message.
  - A declared merged outcome does not prove Git integration occurred.
  - An approval record is not enforcement unless the action checks it.
  - Workspace scoping is not a new tenant-isolation boundary.
- **Current guides:** [Teams](../teams.md), [security](../security.md),
  [coordination](../coordination.md), [bridge](../mcp-bridge.md), and
  [harnesses](../harnesses.md) remain the operational documentation.

## Feature index

1. [Shared presence and control](#1-shared-presence-and-control)
2. [Instruction injection and suggestions](#2-instruction-injection-and-suggestions)
3. [Diff, terminal, and preview annotations](#3-diff-terminal-and-preview-annotations)
4. [Retained evidence packets](#4-retained-evidence-packets)
5. [Ownership handoff and run forks](#5-ownership-handoff-and-run-forks)
6. [Delegated execution and account authority](#6-delegated-execution-and-account-authority)
7. [Missions, tasks, and dependencies](#7-missions-tasks-and-dependencies)
8. [Agent coordination CLI](#8-agent-coordination-cli)
9. [Coordination skill and role instructions](#9-coordination-skill-and-role-instructions)
10. [Integrator orchestration and recovery](#10-integrator-orchestration-and-recovery)
11. [Work scope and conflict negotiation](#11-work-scope-and-conflict-negotiation)
12. [Verification, integration, and delivery](#12-verification-integration-and-delivery)
13. [Reusable mission and swarm templates](#13-reusable-mission-and-swarm-templates)
14. [Shared engineering decision records](#14-shared-engineering-decision-records)
15. [Actionable attention and flow intelligence](#15-actionable-attention-and-flow-intelligence)
16. [Persistent named agents and schedules](#16-persistent-named-agents-and-schedules)
17. [Agent-proposed missions](#17-agent-proposed-missions)
18. [Incident and release governance](#18-incident-and-release-governance)

## Shared-run features

### 1. Shared presence and control

- **Purpose:** Make it obvious who is operating a run and prevent competing
  human inputs from silently racing into the same agent.
- **Stage:** Initial shared-run delivery; integrator control joins with missions.
- **User capabilities:**
  - See connected viewers and the current controller in the run view.
  - Take free control automatically through an eligible interactive operation.
  - Explicitly take or release occupied control when authorized.
  - Continue watching after losing control; viewer access remains read-only.
  - Pause or stop under the existing authorization rules without controller consent.
- **Control rules:**
  - One current controller per run, tied to a session or integrator assignment.
  - Record control transfers and their actors in the timeline.
  - Use an internal authority generation: after transfer, stale commands and
    existing connections cannot continue writing under the previous generation.
  - Expire disconnected control under a documented reconnect policy; do not
    equate presence loss with run failure or silently return contested control.
  - Human takeover suspends integrator steering for that worker until explicitly
    returned. It does not stop unrelated workers or already accepted agent work.
- **Enforcement inventory:**
  - Agent-terminal input and injected instructions.
  - Live-run file writes and active overlays.
  - Writable run-shell input.
  - Forwarded services that expose control or mutation paths.
  - Authorization at the actual operation, not only when a connection opens.
- **Limits:**
  - Taking control grants no additional account permissions.
  - Existing processes, the running agent, and shared homes can still change
    state. This is not an exclusive filesystem lock or a rollback mechanism.
  - Emergency stop must not wait for an agent, summary, or integrator response.
- **Acceptance:**
  - Two simultaneous control claims produce one winner.
  - A previous controller cannot bypass transfer through an already-open channel.
  - A solo user can interact without a separate control-request ceremony.

### 2. Instruction injection and suggestions

- **Purpose:** Distinguish instructions actually sent to the agent from team
  discussion and proposals awaiting a controller's decision.
- **Stage:** Initial shared-run delivery; depends on feature 1.
- **User capabilities:**
  - Use the existing `aether inject <run> "<instruction>"` command.
  - Send directly when authorized and control is available.
  - Receive a nonzero exit and an actionable error when control is unavailable.
  - Submit a suggestion explicitly instead; the controller can send or dismiss it.
  - Discuss a suggestion without automatically adding discussion to agent input.
- **Attribution:**
  - Retain the original author, sending controller, edits, and editor identities.
  - Show the submitted revision in the timeline; do not rewrite earlier messages.
  - Sanitize terminal control sequences in member names and message content.
  - Keep identity labels as context, not privileged system instructions.
- **Example:** Dana suggests preserving an old endpoint; Alex sends the proposal.

  ```text
  [Aether instruction #42]
  Author: Dana
  Sent by: Alex

  Keep the v1 endpoint; add v2 alongside it.
  [End instruction]
  ```

- **Delivery rules:**
  - Persist an instruction identity and authorization before attempting input.
  - Serialize injection with raw terminal input; never erase or append into a
    partially typed prompt without an explicit handover.
  - Do not pretend a generic terminal reveals its editor buffer. If safe input
    cannot be established, explain the limitation and require explicit handover.
  - Report `sent`, `not sent`, or `delivery uncertain`, separately from replies.
  - `Sent` means the input transport accepted the complete instruction.
  - A partial write or crash can be uncertain; do not claim no bytes arrived.
  - Never retry uncertain input invisibly. Preserve its identity and require an
    explicit retry decision. Suggestions are not an implicit fallback.
- **Limits:**
  - An injected question does not prove the agent will answer before working.
  - A message saying stop is not an execution barrier; use Pause or Stop.
- **Acceptance:**
  - A non-controller cannot get a success response for an undelivered proposal.
  - An edited suggestion retains both Dana's original and Alex's revision.
  - A crash during delivery cannot silently duplicate a consequential instruction.

### 3. Diff, terminal, and preview annotations

- **Purpose:** Let a teammate point to the exact evidence behind a comment.
- **Stage:** Diff and terminal anchors initially; browser captures later.
- **User capabilities:**
  - Select a diff hunk or terminal range and attach a comment.
  - Turn that comment into an explicit suggestion to the controller.
  - Open the original referenced content even after the current view changes.
  - Later, annotate a captured preview image or browser state.
- **Required anchors:**
  - Diff: retained tree or snapshot, file path, old/new side, and range.
  - Terminal: retained transcript segment and offsets, not rendered row numbers.
  - Browser: captured state, capture time, and associated run/repository revision.
- **Limits:**
  - A moving branch, URL, or screen position alone is not a durable anchor.
  - An annotation does not automatically steer or interrupt the agent.
  - Retention and access follow feature 4; unavailable evidence is labeled.
- **Acceptance:** A comment still opens the original hunk after later edits,
  or explicitly reports its evidence unavailable rather than pointing elsewhere.

### 4. Retained evidence packets

- **Purpose:** Make a run's state and claimed results inspectable without reading
  every terminal line or trusting an agent's prose summary.
- **Stage:** Basic summaries and retention initially; task/integration evidence
  expands with missions. Used by features 3, 5, 7, and 12.
- **Packet contents:**
  - Objective and current recorded plan.
  - Run, task, and attempt identities where applicable.
  - Base commit, submitted commit or retained tree, and changed files.
  - Included patch intervals and explicitly excluded or untracked material.
  - Supplied image/configuration references, not credential values.
  - Observed commands, results, and supporting output or external check links.
  - Unresolved assumptions, failures, security concerns, and questions.
  - Pending approvals, relevant decisions, and peer messages.
  - Next recommended action, its author, and its basis.
  - Capture time, creator, and the event-log boundary covered.
- **Evidence rules:**
  - Separate server observations, harness reports, agent claims, human decisions,
    and imported external results.
  - Build the factual manifest without requiring an LLM call; narrative is optional.
  - Do not claim a complete command history for terminal-only harnesses.
  - Retain referenced Git objects and transcript segments independently of run
    checkout cleanup, including Git objects reached through alternates.
  - Bind accepted evidence to immutable revisions, not moving branches.
  - Label missing, truncated, expired, or inaccessible material.
  - A live capture is not an atomic environment snapshot. Verify frozen candidate
    contents in a separate checkout when consistency is required.
- **Access and storage:**
  - Follow underlying permissions; export is explicit, not automatic public sharing.
  - Treat logs, screenshots, and untracked files as potentially secret-bearing.
  - Define storage limits and deletion policy before implementation; retained
    evidence is not a promise of unlimited permanent storage.
- **Acceptance:** A reviewer can inspect retained work after the source checkout
  is removed, and cannot mistake an unsupported agent claim for observed proof.

### 5. Ownership handoff and run forks

- **Purpose:** Transfer responsibility or explore a new direction without losing
  the state needed to understand the work.
- **Stage:** Immediate handoff summaries initially; repository-state forks later.
- **Handoff behavior:**
  - Preserve `aether handoff <run> <member>` as an immediate authorized transfer.
  - Notify the recipient and attach the available feature 4 summary.
  - Keep unresolved questions and approvals visible to the new owner.
  - Record the outgoing owner, incoming owner, and actor performing the transfer.
  - Do not require recipient acceptance or block on summary generation.
  - Do not change account selection or confer missing credential authority.
- **Future fork behavior:**
  - Select a retained repository revision and relevant evidence.
  - Create a new run with a new objective and newly authorized execution configuration.
  - Preserve a link to the source run and snapshot.
  - State which repository contents and artifacts were restored or excluded.
- **Limits:**
  - A fork does not clone hidden model context, credentials, browser sessions,
    live processes, or an entire mutable environment.
  - Ownership is not proof of permission to relaunch under another member's account.
- **Acceptance:** Handoff succeeds immediately with unavailable evidence labeled;
  a future fork restores the declared repository contents without inheriting secrets.

## Mission and agent-team features

### 6. Delegated execution and account authority

- **Purpose:** Let someone authorize work using selected execution capacity while
  preserving who acted, who owns the work, and whose credentials are involved.
- **Stage:** Bounded mission authority before worker spawning; richer grants and
  restricted credentials later. Trusted native account sharing remains explicit.
- **Identity record:**
  - Actor: authenticated human, originating run, or server operation.
  - Authorizing human: who allowed an automated operation.
  - Workflow owner: who is responsible and receives notifications.
  - Account owner: whose native login/home and quota are selected.
  - Execution configuration: supplied image and configuration references.
- **Initial mission authorization:**
  - Objective and workspace/repository scope.
  - Permitted accounts and harnesses.
  - Allowed orchestration operations and who may control delegated workers.
  - Mission-wide concurrency, attempt, and retry allowances.
  - A final human delivery decision.
- **Future grant controls:**
  - Explicit grantee, workspace/harness selectors, expiry, and revocation.
  - Separate launch, steer, approve, and orchestration authority.
  - Optional per-launch consent bound to the exact launch specification.
  - Audit grant creation, use, expiration, and revocation.
  - Separate revoke-future-use from revoke-and-stop-affected-runs.
- **Mandatory enforcement:**
  - Check current authority at execution for launch, relaunch, scheduling, retry,
    handoff-related account use, and integrator replacement.
  - Restrict controllers of credential-bearing workers; launch restrictions
    are ineffective if any teammate can steer those credentials afterward.
  - Distinguish admin emergency-stop authority from permission to use an account.
  - Record agents as actors; never imply the account owner typed their commands.
- **Credential and budget limits:**
  - Whole-home sharing exposes all credentials reachable there. Workspace and
    harness launch selectors are admission rules, not credential isolation.
  - No-push, repository-only, or production restrictions require an actual
    credential/action boundary; do not offer misleading enforcement switches.
  - Stopping a run cannot recall credentials copied elsewhere; report required
    provider-side revocation and any failure to stop affected execution.
  - Distinguish admission budgets, metered stop thresholds with possible
    overshoot, and provider-enforced hard limits. Unmetered is unknown, not free.
  - Concurrent admissions cannot independently spend the same reserved allowance.
- **Acceptance:** Revocation between approval and provisioning denies the launch;
  a third teammate cannot gain account-use authority by taking control of its run.

### 7. Missions, tasks, and dependencies

- **Purpose:** Track an objective across multiple executions without making Git
  branches the only record of ownership, progress, and completion.
- **Stage:** Supervised missions; depends on evidence and bounded authority.
- **Mission fields:**
  - Objective, accountable human, workspace, and expected outcome.
  - Current integrator assignment and approved execution limits.
  - Tasks, dependencies, open decisions, and proposed delivery candidate.
- **Task fields:**
  - Target and expected change.
  - Human or agent assignment, constraints, and do-not-touch scope.
  - Dependencies and the accepted output revision needed to proceed.
  - Required acceptance evidence and current submission.
  - Attempt/run links, relevant branch/artifact, and blocking reason if any.
- **Lifecycle:**
  - **Proposed:** Defined work not yet admitted to the approved plan.
  - **Ready:** Approved work whose required dependencies are satisfied.
  - **Active:** A current attempt is performing the assignment.
  - **Review:** A result has been submitted and awaits evidence assessment.
  - **Accepted:** The exact submitted revision satisfies the task's criteria.
  - **Abandoned:** An explicit decision not to pursue the task further.
  - A failed attempt can leave the task open for authorized retry.
  - A blocker records why work cannot proceed; it is not automatically a process pause.
  - Research and decision tasks need acceptance but not a merged status.
  - Only the active attempt can submit an authoritative current result.
  - Reject cyclic dependencies; do not release dependents on process exit alone.
- **Example mission: Add organization audit export.**
  1. Research existing protocol and storage constraints.
  2. Accept the server/dashboard export contract.
  3. Implement server export and dashboard download as independent tasks.
  4. Review the authorization boundary against the submitted revisions.
  5. Assemble the accepted changes and verify the combined behavior.
  6. Present the candidate and evidence for human delivery approval.
- **Scope limits:**
  - One workspace/repository per mission initially.
  - Simple runs need no mission or graph.
  - The UI shows tasks, assigned runs, blockers, and results; technical IDs stay
    inspectable without becoming mandatory user input.
- **Acceptance:** A successful worker exit with missing required evidence leaves
  its task unaccepted and its evidence-dependent tasks blocked.

### 8. Agent coordination CLI

- **Purpose:** Give agents a direct, documented way to communicate and perform
  authorized coordination without needing a human's credentials or MCP support.
- **Stage:** Messaging with shared runs; task and worker lifecycle with missions.
- **Delivery:**
  - Stage a version-matched CLI in each participating container.
  - Authenticate through its run-local socket using the existing bridge pattern.
  - Derive caller identity and current role server-side; no caller-supplied
    sender ID, environment variable, or copied token can impersonate another run.
  - Expose the same service through optional MCP tools, not a second implementation.
- **Proposed namespace:** `aether coord`; names below describe the intended
  command surface, not a frozen CLI or commands available today.
  - `status --json`: own identity, assignment, capabilities, and coordinator.
  - `skill`: load version-matched worker or integrator instructions.
  - `task show` / `task propose`: inspect assigned work or propose additional work.
  - `send`: message an authorized peer or coordinator.
  - `inbox --wait` / `inbox --ack`: receive bounded batches and acknowledge delivery.
  - `ask` / `reply`: open and answer a durable, correlated question.
  - `report`: submit a success/failure outcome and evidence references.
  - `worker start` / `worker list` / `worker inspect`: integrator dispatch and inspection.
  - `worker cancel` / `worker retry` / `worker release`: distinct lifecycle actions.
- **Messages:**
  - Authorize peers through explicit mission/task relationships or existing
    overlap rules; workers must communicate before they touch the same file.
  - Keep discovery scoped; no unrestricted deployment-wide agent directory.
  - Durable enqueue is not proof of recipient attention.
  - Deliver at least once, replaying until acknowledged; tolerate duplicate processing.
  - Acknowledgement means client consumption, not model understanding.
  - Keep the same question identity after a timeout; no answer grants no permission.
  - Use bounded waits and cursor reads rather than busy polling.
  - Treat wake-up notices as best-effort hints, not the authoritative message.
  - Bound message size, inbox depth, peer reach, and request rates on the server.
- **Machine contract:**
  - Versioned JSON, stable error codes, meaningful exit statuses, bounded output.
  - File/stdin input for long specifications instead of fragile shell quoting.
  - Idempotent mutation identities and receipts listing partial-start resources.
  - Reject unsupported required capabilities instead of silently degrading missions.
  - No human SSH key, dashboard token, Docker socket, or unrestricted control RPC.
- **Acceptance:** Two shell-capable harnesses exchange questions and results without
  MCP; a lost worker-start response does not create a second worker on retry.

### 9. Coordination skill and role instructions

- **Purpose:** Teach agents when and how to use feature 8, while keeping security
  and durable state in the server rather than in prompts.
- **Stage:** Worker messaging instructions initially; integrator instructions with missions.
- **Skill delivery:**
  - Supply a small discovery instruction at launch.
  - Load role-specific guidance from the staged, version-matched CLI.
  - Keep runtime instructions out of repository edits and shared member-home writes.
  - Advertise capabilities across upgrades; do not instruct old clients to invent flags.
- **Worker instructions:**
  - Read the actual assignment, task, attempt, scope, and acceptance requirements.
  - Check messages at natural checkpoints, including before submitting a result.
  - Ask the integrator through the CLI instead of an inaccessible local question UI.
  - Propose subtasks or scope changes; do not self-authorize extra workers.
  - Submit success or failure explicitly with real evidence and unresolved risks.
  - After submission, take no new work until assigned; a report is not self-acceptance.
- **Integrator instructions:**
  - Establish shared contracts before dispatching independent work together.
  - Route questions and scope conflicts; request rework when evidence is inadequate.
  - Match reports to current attempts, and reconstruct state after interruption.
  - Verify the combined candidate before requesting final delivery approval.
  - Account for every worker, unresolved decision, and retained artifact.
- **Limits:**
  - Instructions do not grant permissions or guarantee model compliance.
  - Repository content and peer messages cannot promote themselves into runtime policy.
  - Native subagents inside a run are not automatically separate Aether workers
    with independently authenticated identities.
- **Acceptance:** A freshly launched supported worker can discover its assignment,
  ask a question, and report an outcome without manual coordination setup.

### 10. Integrator orchestration and recovery

- **Purpose:** Make the integrator an actual agent-operated coordinator, not merely
  the last worker asked to merge whatever other agents happened to produce.
- **Stage:** Supervised missions; depends on features 6 through 9.
- **Integrator capabilities within approved scope:**
  - Decompose work, refine task specifications, and dispatch ready tasks.
  - Select allowed harnesses/accounts and enforce parallelism through the server.
  - Answer questions, accept evidence, request corrections, and resolve assignments.
  - Cancel or retry under the mission policy and inspect retained results.
  - Prepare integration work and a final report naming outcomes and open blockers.
- **Limits:**
  - One integrator both coordinates and integrates initially; no mandatory extra role.
  - Workers propose subtasks rather than recursively spawning descendants.
  - No self-increase of budget, account access, privilege, or approved objective.
  - No human-only approvals, unrelated-run control, or unrestricted cleanup.
  - Human takeover fences new steering of the affected worker across input paths;
    informational messages and already accepted work are not new steering authority.
- **Recovery rules:**
  - Persist tasks, attempts, reports, questions, and integrator assignment outside
    model context. A terminal title or transcript is not lifecycle authority.
  - If the integrator stops, workers may finish existing assignments and report;
    new dispatch waits for an authorized coordinator to resume.
  - Replacement advances the assignment generation so the old coordinator's
    commands fail if it returns.
  - Distinguish live, exited, and unknown observations; silence is not proof of death.
  - Do not start a competing attempt solely because a heartbeat or response is missing.
  - Cancellation stops execution; release reclaims settled runtime resources.
    Neither should silently discard retained work and evidence.
  - Make current ownership and retained-resource decisions visible after recovery.
- **Acceptance:** Restart the integrator while workers are active; its replacement
  receives the same assignments/results, cannot duplicate a worker, and rejects
  commands from the previous coordinator generation.

### 11. Work scope and conflict negotiation

- **Purpose:** Prevent accidental duplicate work and make overlap resolvable before
  agents independently change the same interface or migration.
- **Stage:** Advisory scope with missions; stronger assignment leases later.
- **Task scope declaration:**
  - Expected files/packages and semantic responsibility.
  - Public API/schema ownership and shared contract revisions.
  - Migrations, generated artifacts, and tests owned by the task.
  - Excluded areas, base revision, merge target, claimant, and help-needed state.
- **Coordination behavior:**
  - Show intended overlap separately from observed file overlap.
  - Surface edits outside declared scope without discarding them.
  - Allow peer proposals for scope splits, handoffs, and shared-contract changes.
  - Let the integrator resolve within mission authority; escalate broader choices.
  - Preserve the existing conflict radar as advisory telemetry.
- **Escalation path:**
  - Advisory: warn and show evidence.
  - Negotiated: record an agreed split or handoff.
  - Later leased assignment: temporary exclusive claim to a narrow task scope.
  - Human decision: choose takeover, rescoping, rebasing, or working in one shared run.
- **Limits:**
  - Different files do not prove semantic independence; the same file does not
    always mean incompatible work.
  - No filesystem locks initially. Assignment exclusivity is not write prevention.
  - Lease expiry is not evidence of process death or permission to delete work.
- **Acceptance:** Two workers negotiate API ownership before an overlapping edit;
  an out-of-scope change stays visible and requires a disposition rather than being lost.

### 12. Verification, integration, and delivery

- **Purpose:** Turn worker activity into a reviewable combined result, without
  treating success messages or individually passing branches as final proof.
- **Stage:** Supervised missions; depends on evidence, tasks, and integrator recovery.
- **Distinct facts:**
  - The agent reported success.
  - A command exited successfully.
  - Required evidence was accepted for an exact revision.
  - The accepted combined candidate was delivered to its target.
- **Integration workflow:**
  - Select exact accepted worker revisions and record their provenance.
  - Assemble them in an isolated integration checkout.
  - Resolve conflicts and inspect behavior shared between worker changes.
  - Verify the combined candidate against required tests, review, and other evidence.
  - Request a human delivery decision tied to candidate and expected target revision.
  - Execute only the approved action through the configured Git workflow.
- **Gates:**
  - Do not require human approval of every routine task transition.
  - Reject inadequate evidence; the integrator may request rework within its scope.
  - Changes to the candidate, action, or target invalidate stale approval.
  - A recorded approval must be checked on the actual execution path.
  - Keep rejection and the next needed action visible, not just a generic blocked label.
- **Git limits:**
  - Respect local-only versus mirrored workspace base ownership.
  - Never force-update a mirrored base as an integration shortcut.
  - Branch/PR links supplement exact revisions; they do not replace them.
  - External branch protection remains necessary if container credentials can
    bypass Aether's delivery path.
- **Acceptance:** Individually accepted worker changes fail combined verification;
  delivery remains unavailable. A later target update cannot reuse the old approval.

## Reuse and later operation

### 13. Reusable mission and swarm templates

- **Purpose:** Repeat a proven team structure without making users manually build
  the same task graph each time. A swarm is a mission launched from such a template.
- **Stage:** Later, after the first supervised mission workflow is reliable.
- **Template contents:**
  - Objective parameters, task specifications, roles, and dependencies.
  - Shared work contracts, acceptance evidence, and decision gates.
  - Integrator assignment, allowed harness/account choices, and resource limits.
  - Branch/base selection and the integration contract.
- **Initial template:** Contract definition, independent implementers, review,
  integration, combined verification, and final human delivery decision.
- **Additional shapes to consider:**
  - Feature: mapper, contract proposer, implementers, reviewer, integrator.
  - Incident: reproducer, investigator, mitigator, verifier, incident summary.
  - PR review: behavior/security/test-gap review, author follow-up, human decision.
  - Migration: inventory, compatibility analysis, subsystem changes, rollback
    verification, and integration.
- **Rules:**
  - Extend existing templates and schedules; do not create another scheduler.
  - Pin a template revision at launch; later edits do not mutate active missions.
  - Omit unnecessary roles for small work; more agents is not a success criterion.
  - Recheck authority and source/base rules for scheduled launches.
  - Do not accumulate missed schedule occurrences into a catch-up launch storm.
- **Acceptance:** Launching a saved shape creates a coherent bounded mission;
  editing its template afterward does not change the running assignments.

### 14. Shared engineering decision records

- **Purpose:** Give later agents inspectable project decisions instead of hidden,
  unbounded conversational memory or an unexplained vector-search result.
- **Stage:** Later; depends on evidence and demonstrated retrieval needs.
- **Record fields:**
  - Statement, workspace/package/mission/run scope, author, and timestamp.
  - Evidence links such as a PR, check result, design note, or incident.
  - Status: proposed, accepted, or superseded.
  - Revision, supersession link, and optional expiry/review date.
- **Capabilities and rules:**
  - Agents propose records; authorized policy determines acceptance.
  - Retrieve bounded, relevant records at launch or through an explicit CLI query.
  - Record exactly which revisions a run received and why they were selected.
  - Surface contradictory accepted decisions instead of silently choosing one.
  - Supersede stale facts without rewriting previous run history.
  - Keep repository instructions authoritative; records grant no extra privileges.
  - Do not promise automatic package-entry retrieval without a supported observation hook.
- **Example:** An accepted package-scoped decision says the public API remains
  v2-only and links to its design and compatibility evidence.
- **Acceptance:** A reviewer can trace an agent's assumption to the specific record
  revision; superseding it changes future retrieval, not historical attribution.

### 15. Actionable attention and flow intelligence

- **Purpose:** Show the next decision that unblocks work, not just how many agents
  are active or how much text they produce.
- **Stage:** Basic action routing with shared runs and missions; analytics later.
- **Every actionable blocker includes:**
  - Reason: human decision, dependency, approval, test, environment, credentials,
    merge/integration, or unavailable coordinator.
  - Owner or eligible decision-maker, start time, and affected work.
  - The exact action or evidence needed to resolve it.
  - Links to the relevant run, question, candidate, or error.
- **User behavior:**
  - Build on the existing attention view and approval inbox rather than adding a board.
  - Deduplicate notifications and keep unresolved requests when everyone is offline.
  - Distinguish a reported concern from an enforced hold or an actual paused container.
  - Surface ready-to-integrate work with no available integrator.
- **Later measurements:**
  - Time waiting for a human decision, dependency, verification, or integration.
  - Rework, repeated failures, and observed overlap/merge burden.
  - Measured cost per accepted/integrated outcome with metering coverage shown.
  - Potential duplicate work and recurring failure signatures with supporting evidence.
- **Limits:**
  - No productivity scores based on tokens, message counts, or active agent counts.
  - A warning appearing is not evidence that duplicate work was prevented.
  - An absent heartbeat is not proof of failure or progress stopping.
- **Acceptance:** A teammate returning after an overnight mission can identify the
  next needed action and its owner without reading every worker transcript.

### 16. Persistent named agents and schedules

- **Purpose:** Reuse an identifiable agent configuration and responsibility across
  executions without keeping one process alive indefinitely.
- **Stage:** Later; depends on proven missions, authority checks, and scheduling.
- **Named-agent definition:**
  - Name, accountable human, intended responsibility, and permitted scope.
  - Harness/configuration and allowed account selection.
  - Relevant accepted decision records and approved scheduling rules.
- **Behavior:**
  - Launch ordinary inspectable runs or missions from the definition.
  - Keep execution histories separate from the persistent definition.
  - Recheck current authority and configuration on every scheduled fire.
  - Preserve existing missed-occurrence and source-refresh policies.
- **Limits:**
  - A persistent name is not an immortal process, human impersonation identity,
    unlimited budget, or indefinitely valid credential grant.
  - Memory remains scoped and versioned under feature 14.
- **Acceptance:** Removing the authorizing access prevents the next scheduled
  launch even though the named-agent definition still exists.

### 17. Agent-proposed missions

- **Purpose:** Let agents identify useful follow-up objectives without giving them
  permission to generate and approve their own workload.
- **Stage:** Later, after human-authorized missions and limits are reliable.
- **Proposal contents:**
  - Objective, justification, evidence, expected scope, and acceptance criteria.
  - Proposed task split, account/harness needs, and estimated resource use.
  - Risks and decisions that need a human owner.
- **Behavior:**
  - Store the proposal without launching its workers.
  - Allow a human to accept, edit, or reject it.
  - Acceptance creates an explicit authorized mission; edits remain attributable.
- **Limits:**
  - Generating the proposal uses an already authorized run and its existing budget.
  - Agents cannot approve their own spending, account access, or expanded privileges.
  - No self-replenishing loop of proposals and automatic launches.
- **Acceptance:** A valid proposal remains non-executing until human authorization;
  rejection creates no worker runs or new credential access.

### 18. Incident and release governance

- **Purpose:** Support consequential operations only when their authority and
  approval boundaries can actually be enforced.
- **Stage:** Later; requires a separately designed restricted-execution boundary.
- **Potential capabilities:**
  - Restricted production credentials and permitted action scopes.
  - Quorum approval for explicitly identified release or incident actions.
  - Approval bound to the action, candidate, environment, and expiry.
  - Revocation/stop procedures with explicit residual-access reporting.
  - A retained audit package containing decisions, artifacts, checks, and actors.
- **Enforcement requirements:**
  - A container must not receive credentials that bypass the claimed restriction.
  - Count eligible approvers according to policy; an agent cannot approve for a human.
  - Validate the authorization on the actual action path, not merely in the inbox.
  - Distinguish local append-only history from tamper-evident or externally retained
    evidence; define the host-administrator threat model before claiming immutability.
- **Limits:** An incident template from feature 13 is not, by itself, production
  isolation, deployment approval enforcement, or compliance evidence.
- **Acceptance:** A changed deployment candidate requires fresh approval, and
  rejected or insufficient approvals cannot be bypassed using issued credentials.

## Delivery sequence and dependency checklist

1. **Shared runs: one complete collaboration workflow.**
   - Features 1 and 2: visible control, send-or-refuse injection, explicit suggestions.
   - Features 3 through 5: diff/terminal annotations, retained factual summaries,
     immediate handoff; exclude browser capture and forks initially.
   - Features 8 and 9: bounded messaging CLI and discoverable worker instructions.
   - Feature 15: actionable questions and ownership in the existing attention surface.
   - Preserve the current trusted account-sharing boundary; do not imply stronger grants.
2. **Supervised missions: one complete agent-team workflow.**
   - Feature 6: scoped orchestration authority before enabling worker spawning.
   - Feature 7: durable missions, tasks, attempts, and dependencies.
   - Features 8 through 11: lifecycle CLI, integrator skill, recovery, work scope.
   - Features 4 and 12: exact-revision evidence and combined-candidate verification.
   - Human takeover, final delivery gates, and mission-wide limits must work together.
3. **Reuse and insight: follow demonstrated needs.**
   - Features 13 through 15: templates, decision records, and flow measurements.
   - Later portions of features 3, 5, 6, and 11: browser anchors, forks, richer
     delegation grants, and assignment leases.
4. **Autonomous and restricted operation: separate design approval.**
   - Features 16 through 18: named agents, agent-proposed missions, release governance.
   - Enforcing credentials and hard external spending limits are prerequisites
     for those claims, not features implied by a mission or skill.

## End-to-end examples to verify

1. **Two humans, one agent.**
   - Alex controls a run; Dana opens it read-only and comments on a diff.
   - Dana explicitly suggests an instruction; Alex edits and sends it.
   - Dana takes control through an authorized transfer; Alex's old connection
     can still observe but cannot write.
   - Alex hands ownership to Dana; the account stays unchanged and a summary appears.
2. **Mixed-harness mission.**
   - A human authorizes an export feature with a bounded number of workers.
   - The integrator dispatches server and dashboard tasks to different harnesses.
   - Workers establish a shared contract through CLI ask/reply before editing.
   - The integrator evaluates their evidence and verifies the combined candidate.
   - A human approves delivery for that candidate and the expected target revision.
3. **Interrupted coordination.**
   - A worker-start response is lost; retrying the request returns the same attempt.
   - The integrator restarts while workers continue their assigned work.
   - A replacement recovers reports and questions; stale coordinator writes fail.
   - Human takeover of a worker prevents new integrator steering from bypassing it.
4. **Unverified or unauthorized work.**
   - A worker reports success without required evidence; its task remains unaccepted.
   - A target update invalidates the existing delivery approval.
   - Expired account authority prevents the next launch or retry.
   - An unavailable evidence object is labeled missing, never counted as a passed check.

## Implementation boundaries

- **Scheduler/runtime:** Own containers, process lifecycle, pause/stop, and recovery.
- **Coordination service:** Own run-authenticated messages and authorized agent operations.
- **Mission state:** Own tasks, attempts, assignments, gates, and submissions.
- **Permissions/account policy:** Check current authority at consequential operations.
- **Git/evidence storage:** Retain exact revisions and supporting artifacts.
- **CLI, MCP, dashboard:** Use the same contracts and expose equivalent outcomes.
- **Event/audit layer:** Keep authoritative state changes and audit records
  recoverably consistent; free-form timeline text is not the task database.
- **Do not add:** Another terminal supervisor, a generic workflow language,
  universal file locks, a second swarm scheduler, or a hidden memory database.

## Decisions required before coding

- **Control:** Exact lease/reconnect timing, same-member multi-client behavior,
  and supported safe-input handover for each harness.
- **Protocol:** Final CLI syntax, message schemas, version negotiation, error
  receipts, idempotency rules, and required versus optional capabilities.
- **Evidence:** Available observation sources, snapshot consistency, retention
  limits, sensitive-output handling, exports, and deletion behavior.
- **Authority:** Grant evaluation, account-control restrictions, reservation
  accounting, and which approval/action boundaries can truly be enforced.
- **Acceptance:** Which evidence the integrator may accept, which actions require
  human approval, and how changes invalidate prior decisions.
- **Rollout:** Surface existing limitations explicitly, keep ordinary runs usable,
  and validate the two workflows before adding more configuration.
- **Future features:** Require a later implementation plan; inclusion in this
  inventory is not authorization to build every feature at once.

## References and design influences

- **Aether:** [Team workflows](../teams.md), [security posture](../security.md),
  [coordination](../coordination.md), [bridge/status reporting](../mcp-bridge.md),
  and [harness capabilities](../harnesses.md).
- **Agent-facing CLI and skills:** [araa47/orca](https://github.com/araa47/orca)
  and its [integrator role](https://github.com/araa47/orca/blob/main/skills/sprint-team/references/integrator.md).
  - Borrow: Agents use a CLI and role instructions to manage workers.
  - Do not copy: A tmux supervisor or merging to the delivery target before
    validating the combined candidate.
- **Structured coordination:** [Orca orchestration](https://www.onorca.dev/docs/cli/orchestration)
  and [version-matched skills](https://www.onorca.dev/docs/cli/skills).
  - Borrow: Separate tasks from attempts, durable ask/reply, current-attempt
    authority, inspectable results, and recovery independent of model context.
  - Do not copy: Runtime-global reset authority or worker-reported success as
    automatic proof that Aether's acceptance evidence is satisfied.
- **Parallel decomposition:** [Anthropic's compiler experiment](https://www.anthropic.com/engineering/building-c-compiler).
  - Lesson: Independently verifiable work matters more than agent count.
- **Dependency policy:** These are design references, not new runtime dependencies
  or claims that Aether already implements their behavior.
