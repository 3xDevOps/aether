# Multiplayer collaboration draft plan

**Status:** Draft product direction, not an implementation announcement. The
initial UX decisions below are settled for this draft; command spelling,
storage schemas, and rollout details need review before implementation.

**Goal:** Let people work together on an agent run, or delegate a larger
objective to an integrator agent that coordinates workers and returns an
inspectable result. Keep everyday operation simpler than the underlying
coordination machinery.

## Product decisions

Expose two workflows, not a collection of new products:

1. **Work together on a run.** A run is one agent execution with its own
   container, repository checkout, branch, and transcript. Extend its existing
   view with visible control, conversation, and a useful handoff summary.
2. **Delegate a mission.** A mission is an objective spanning several tasks
   and runs. An integrator agent plans and supervises workers, resolves their
   coordination needs, and prepares the combined result for a human decision.

A workspace remains the repository and shared project context. Ordinary runs
remain usable without a mission. A solo user should not manage a control
queue, select a message category, or configure an orchestration graph.

The simplicity review established these defaults:

- `aether inject` sends or clearly refuses. It never silently creates a
  suggestion instead. Suggestions are an explicit alternative.
- The everyday composer is plain text. Observation, question, directive, and
  blocker are not mandatory categories users must learn before contributing.
- Handoff remains immediate for an authorized member, with notification and an
  automatic summary. The recipient does not have to accept before it happens.
- The integrator operates within an approved mission. Human approval is for
  expanded scope or authority and final delivery, not every worker transition.
- Evidence, task scope, templates, and attention routing support the two
  workflows; they do not require separate boards or setup flows.

## Existing foundations and limits

Reuse [team controls](../teams.md), [conflict coordination](../coordination.md),
the [run-authenticated bridge](../mcp-bridge.md), and the existing scheduler,
Git engine, event stream, approvals, templates, and attention states.

Current behavior sets limits on what the plan can promise:

- Several members can write to one run terminal. Exclusive interactive
  control requires server enforcement, not just a disabled dashboard button.
- Account sharing exposes the account owner's complete writable home and
  credentials. A narrow-looking launch selector does not restrict what those
  credentials can do inside a container. See [security](../security.md).
- Revocation stops future account use for launches and relaunches, not already
  running containers. Copied credentials need provider-side revocation.
- Workspace budgets block new launches based on recorded spend. Running work
  continues, and unmetered usage makes totals incomplete rather than zero.
- Existing coordination is run-authenticated and bounded; overlap authorizes
  peer messaging. Mission participants need explicit communication authority
  before they edit overlapping files.
- Harness status reports are useful observations, not proof of task acceptance.
  [Steering delivery](../harnesses.md#steering-delivery) is not proof that an
  agent understood or followed an instruction.
- Diff intervals are not durable evidence merely because they have tree IDs.
  Their referenced Git objects must survive source-checkout cleanup.
- A declared `merged` outcome is not proof of integration. An approval record
  is not an execution barrier unless the actual action checks it.
- Workspace scope must not be presented as tenant isolation. The existing
  membership and visibility model remains the baseline.

## Workflow: work together on a run

### Visible, exclusive control

Show who is watching and who currently controls the run. Use **Take control**,
**Release control**, and **Send suggestion** rather than exposing lease or
queue terminology. Taking occupied control is an explicit, attributed action
for an authorized member, not an automatic side effect of typing. Taking free
control is automatic for an eligible interactive operation.

Internally, control belongs to one session or authorized integrator assignment
and has a generation that changes on transfer or revocation. The server rejects
stale writes on existing connections. Losing control does not stop the agent
from executing work it already accepted; losing presence is not a run failure.

Inventory all Aether-mediated mutation paths before implementation: terminal
input, injection, live-run file editing, writable run shells, overlays, and
forwarded control services. Shared input arbitration must cover the actual
writes, not just connection establishment. A shell is not read-only because it
was opened from an inspection view. Do not claim exclusive filesystem access:
the agent, previously started processes, and shared account homes can still
change state outside an interactive control transfer.

Pause and stop remain explicit operations with their own authorization. They
do not require the current controller's consent. Stop must not wait for a
summary, an agent acknowledgement, or an unavailable integrator.

### Injection and suggestions

Keep the existing command and make its result unambiguous:

```sh
aether inject <run> "Keep the v1 endpoint; add v2 alongside it."
```

An eligible caller with available control sends the instruction. A caller
without control receives a nonzero exit and an error naming the controller
and how to take control or submit a suggestion. The dashboard presents the
same distinction. Viewer access stays read-only.

Suggestions remain in the run conversation until the controller sends or
dismisses them. Discussion does not automatically enter the agent's input.
If a controller sends someone else's suggestion, preserve both identities:

```text
[Aether instruction #42]
Author: Dana
Sent by: Alex

Keep the v1 endpoint; add v2 alongside it.
[End instruction]
```

An edit records a new revision and its editor without replacing the original
attribution. The banner is context, not a new permission or system instruction.
Member names and bodies must not smuggle terminal control sequences into it.

Persist an instruction ID and delivery authorization before attempting input.
Raw typing and injection share arbitration. Do not append to or erase a
partially typed prompt. A generic terminal does not reliably expose its input
buffer; refuse with a clear explanation when safe delivery cannot be established
and require an explicit handover from interactive input. Harness-specific
support may make that handover smoother without changing the contract.

Report **sent**, **not sent**, or **delivery uncertain**, separately from the
agent's response. A successful transport write is the meaning of sent. A crash
or partial write can leave delivery uncertain; never retry it invisibly or
claim a refusal means no bytes arrived. Retries use the original instruction
identity and an explicit decision when delivery was uncertain.

### Handoff, summaries, and annotations

`aether handoff` keeps its immediate ownership and notification-routing
semantics. Attach an automatic summary of the objective, current repository
state, observed checks, unresolved questions, and next action. Build the factual
manifest without requiring another model call; an optional authored narrative
is supplementary. Missing evidence is visible and does not prevent reassignment.
Account selection and credential grants do not transfer with ownership.

Let collaborators comment on retained diff hunks and transcript ranges from
the run view. Store the original tree/path/range or transcript segment/offsets,
not just a moving branch name or rendered line number. Browser annotations
come later and must reference a captured state rather than a mutable URL alone.

## Workflow: delegate a mission

### One objective and one integrator

The human supplies the objective, accountable owner, permitted account and
harness choices, work scope, and resource limits. The integrator is an ordinary
Aether run with mission-scoped orchestration authority. It both supervises work
and assembles the result initially; do not require a separate coordinator role.

Start with one workspace/repository per mission. The integrator proposes or
refines tasks within approved scope, launches ready independent work together,
answers worker questions, resolves ownership conflicts, requests corrections,
and prepares a candidate for final human approval. Expanding the objective,
account access, or resource allowance requires a new human decision.

A mission view shows tasks, their assigned runs, blockers, and the proposed
result. Technical attempt IDs and authority generations remain available for
inspection and machine clients, not prerequisites for using the board.

### Tasks, attempts, and scope

A task is durable work; an attempt is one authorized execution backed by a run.
Retries create new attempts, not fresh ownership of the same old lifecycle ID.
Each task names its target, expected change, constraints, owned scope,
dependencies, and observable acceptance evidence.

Use a small task progression: proposed, ready, active, review, accepted, with
abandonment explicit. Blocking reasons and failed attempts are separate facts.
Research and decision tasks need not become merged. Dependencies refer to
accepted output revisions, not merely an exited process or a success message.

Task scope includes expected paths/packages, shared APIs or schemas, migrations,
generated files, tests, exclusions, and merge target. Compare declared scope
with observed edits and reuse the conflict radar. Overlap is advisory: workers
can propose a split or handoff, and the integrator resolves it. Do not introduce
filesystem locks or claim that different files guarantee independent work.

Only the integrator spawns workers initially. Workers propose subtasks instead
of recursively creating more agents. Enforce mission-wide concurrency and
attempt limits on the server, including retries. Account and launch authority
must be rechecked at execution time, not inherited indefinitely from a prompt.

### Agent CLI and skill

Give every participating agent a version-matched, run-local CLI and a short
coordination skill. Shell-capable harnesses must not need MCP to participate.
Use the existing socket authentication and binary-staging machinery; do not
put member SSH keys, dashboard tokens, or a Docker socket into workers.

A proposed namespace is `aether coord`; final command syntax is not settled.
The capability groups are:

| Available to | Operations |
| --- | --- |
| Participants | Inspect own assignment/capabilities; load the skill; send/read messages; ask/reply; propose work; submit an outcome with evidence. |
| Integrator | Inspect workers; dispatch approved tasks; request rework; cancel or retry within policy; release settled runtime resources. |

CLI and optional MCP tools call the same service. Machine output has a versioned
schema, stable errors, bounded reads, and cursor-based waiting. Mutations carry
idempotency identities; partial-start failures return the resources already
created so an uncertain response cannot produce a duplicate worker.

The server derives the caller from the run socket and checks its current task
attempt and mission role. IDs, environment variables, copied skill text, and
claims to be an integrator cannot create authority. Discoverable peers are
scoped to explicit mission/task relationships or existing authorized overlaps,
not an unrestricted deployment-wide agent directory.

The skill teaches two roles:

- **Worker:** read the assignment, stay in scope, check messages at natural
  checkpoints, ask the integrator through the CLI, and submit success or
  failure with real evidence. After submission, take no new work until assigned.
- **Integrator:** partition independently verifiable work, establish shared
  contracts before dispatch, start independent workers together, resolve
  questions, assess results, verify integration, and account for every worker.

Install a small discovery instruction at launch and load detailed guidance from
the staged CLI. Keep it out of repository changes and shared member-home edits.
Advertise server capabilities so an older live CLI cannot silently use changed
operations after an upgrade. A mission requiring coordination must fail clearly
if that capability is unavailable, rather than launch uncoordinated workers.

### Messages, control, and recovery

Messages are durably enqueued and delivered at least once, replaying until
acknowledged. Acknowledgement means the client consumed a delivery, not that
the model understood it. Processing must tolerate replay. Keep question and
reply identities stable across timeouts; no answer is not approval to proceed.
Wake-up notices are best-effort hints. Bounded waits replace busy polling.

An integrator may direct its workers only under current mission and control
authority. Human takeover suspends automatic steering of that worker until
control is explicitly returned. It does not silently stop the entire mission,
and a mailbox or alternate input path must not bypass the takeover. Already
accepted work and informational peer messages remain distinguishable from new
steering commands.

Persist tasks, reports, pending questions, and the current integrator assignment
outside agent context. If the integrator dies, workers can finish their assigned
work and submit results; no new dispatch occurs until an authorized integrator
resumes. Replacement changes the assignment generation so the old integrator
cannot act if it returns. Unknown liveness never authorizes an automatic
replacement worker. Cancellation and release are separate: release removes
settled runtime resources without discarding retained work or evidence.

### Evidence and integration

A worker submits an outcome and retained evidence for an exact repository
revision. The integrator evaluates the result against its acceptance criteria;
worker-reported success does not automatically satisfy them. Human review is
not mandatory for each task, but evidence validation is.

The integrator assembles exact accepted revisions in its own integration
checkout and checks the combined candidate before asking to deliver it. Bind
approval to the candidate and expected target revision. Further edits or target
movement require re-evaluation; never reuse a stale approval. Respect the
workspace's configured Git ownership: a mirrored base is not a branch the
integrator may force-update. Delivery uses the authorized upstream or local
workflow. External branch protection is still needed when mounted credentials
can bypass Aether.

Retain a factual evidence manifest with the objective, task/attempt, base and
candidate revisions, configuration references, observed commands and results,
open decisions, author, capture time, and event-log boundary. Label server
observations, harness reports, agent claims, and human decisions separately.
Do not claim to capture every command from a generic terminal transcript.

Referenced Git objects and transcript segments must survive source-checkout
cleanup. Capture exclusions, truncation, and unavailable evidence explicitly.
A live worktree capture is not an atomic environment snapshot; verify a frozen
candidate in a separate checkout. Summaries cannot upgrade a claim into proof.
Access and export follow the underlying permissions; logs and screenshots can
contain secrets. Retention needs bounded storage and explicit deletion policy.

Forking, when added, means a new run from retained repository state and selected
evidence under fresh authorization. It does not clone hidden model context,
credentials, browser sessions, or live processes.

## Authority and truthful limits

Keep the acting human/run/server, authorizing human, workflow owner, account
owner, and execution configuration distinct. Record agent actions as agent
actions, not as if the account owner typed them. Configuration references record
what was supplied; they do not make a mutable shared home immutable.

The initial mission authorization is small: approved scope, accounts/harnesses,
allowed orchestration operations, concurrency/attempt limits, and a final human
delivery gate. A skill never grants any of these permissions.

Steering a credential-bearing run can exercise its credentials. Therefore
mission/account-use authorization must constrain who can control its workers;
team-wide steering, handoff, or integrator replacement cannot silently broaden
it. Ownership alone is not a credential grant. Administrative emergency stop
is distinct from permission to use somebody else's account.

Keep native account sharing explicitly described as trusted whole-home access.
Do not introduce claims such as no-push, repository-only access, or hard dollar
ceilings that a mounted credential can bypass. Future restricted execution
needs a separately designed credential or action boundary. Admission budgets,
observed-spend stop thresholds with possible overshoot, and provider-enforced
hard limits are different controls. Concurrent admission must not spend the
same allowance twice; unmetered remains unknown, not free.

Keep critical state changes and their audit records recoverably consistent.
Never infer authoritative task state from free-form timeline prose. A local
append-only log is not immutable against the host administrator.

## Delivery sequence

1. **Shared runs:** exclusive interactive control, send-or-refuse injection,
   explicit suggestions, immediate handoff summaries, retained diff/transcript
   anchors, and the agent CLI/skill for bounded communication.
2. **Supervised missions:** task/attempt state, scoped automation authority,
   integrator-driven worker lifecycle, advisory scope, durable recovery, and
   evidence-backed integration. Ship these as one usable end-to-end workflow.
3. **Reuse and insight:** extend existing templates to instantiate proven
   mission shapes; add versioned decision records and richer waiting-time
   views only after real use identifies repeated work and decisions.

The first mission demonstration is a human authorizing an objective, an
integrator dispatching two different harnesses, workers negotiating a shared
interface through the CLI, and the integrator verifying their combined result.
It must still work after a disconnected client and an integrator restart.

Do not add a second terminal supervisor, a generic workflow language, or a
separate swarm scheduler. The existing server owns execution and recovery;
the integrator agent supplies strategy through enforced operations.

## Future directions, not initial commitments

| Direction | Preserve this idea; defer this complexity |
| --- | --- |
| Delegation grants | Explicit expiry, per-launch consent, and separate revoke-future-use versus revoke-and-stop. Recheck scheduled/relaunched work and require provider revocation for copied credentials. |
| Swarm templates | Save proven task shapes, roles, contracts, limits, and gates; pin versions at launch. Start with feature work before incident, migration, and PR-review variants. |
| Work leases | Consider temporary exclusive assignment and negotiated takeover after advisory scope proves insufficient. Expiry is not proof of process death or permission to discard work. |
| Shared decisions | Inspectable, scoped, versioned records with evidence and supersession. Agents propose; authorized policy accepts. Record what each run received; do not override repository instructions or privileges. |
| Flow intelligence | Build on the existing attention surface: reason, owner, age, blocking effect, and resolving action. Later measure decision/integration waiting and cost with metering coverage, not agent activity as productivity. |
| Browser annotations and forks | Capture stable preview evidence and fork repository state plus evidence, never promise portable hidden agent context. |
| Named agents and schedules | Durable configurations and responsibilities using existing scheduling, not immortal processes. Recheck authority on each fire; do not accumulate missed launches. |
| Agent-proposed missions | Proposals stay within existing execution budgets; agents cannot approve their own spending or privileges. |
| Incident and release policy | Restricted production credentials, quorum approval, and externally retained or tamper-evident audit packages require actual enforcement and a defined threat model. |

These ideas remain in the roadmap without adding nine independent user-facing
features. Their exact UX and priority should follow use of the two core workflows.

## Acceptance scenarios for implementation

- Two clients race for control; only one succeeds, and stale connections cannot
  continue mutating the run. A solo run remains frictionless.
- Injection never reports a suggestion as delivered, corrupts a partial prompt,
  or silently retries uncertain input. Sending an edited suggestion preserves
  author, sender, and revision attribution.
- Handoff changes responsibility immediately and supplies available evidence
  without waiting on an LLM or transferring credential authority.
- Two shell-capable harnesses coordinate through the CLI without MCP, member
  credentials, or the ability to impersonate another run.
- A lost worker-start response does not create a duplicate attempt. Late reports
  from superseded attempts cannot settle current work.
- Worker questions survive a timeout and restart. Human takeover cannot be
  bypassed by integrator messaging or another input surface.
- An integrator replacement recovers assignments and evidence while fencing
  the old integrator. Unknown liveness does not spawn a competing editor.
- A reported success with inadequate evidence remains unaccepted. Combined
  verification and delivery approval apply to the exact candidate and target.
- Retained results remain inspectable after runtime cleanup. Missing artifacts
  are unavailable, not verified; a future fork restores only declared contents.
- Expired or revoked authority between request and execution prevents new work.
  Concurrency and attempt limits apply across the whole mission.
- A teammate returning later can find the next required action, its owner, and
  its evidence without reading every transcript. Presence loss is not failure.

## Revisit before implementation

Specify exact CLI/wire contracts, control transfer and safe-input behavior for
each harness, retention limits, and the available acceptance-evidence sources.
Exercise the two user journeys before adding more knobs. Keep generic-terminal
limitations visible rather than hiding them behind inferred readiness.

The draft preserves the full direction, but does not authorize implementation
of every future feature or freeze a database schema. Later plans should link
back to these decisions and explicitly record changes to them.

## References

- [Aether team workflows](../teams.md), [security posture](../security.md),
  [coordination](../coordination.md), [bridge and status reporting](../mcp-bridge.md),
  and [harness capabilities](../harnesses.md).
- [Orca agent CLI and skill](https://github.com/araa47/orca) and its
  [integrator role](https://github.com/araa47/orca/blob/main/skills/sprint-team/references/integrator.md):
  agents operate worker management through a CLI and role instructions.
- [Orca structured orchestration](https://www.onorca.dev/docs/cli/orchestration)
  and [version-matched skills](https://www.onorca.dev/docs/cli/skills): separate
  tasks from attempts, retain messages, and reconstruct supervision after failure.
  These are design references, not dependencies or adopted runtime guarantees.
- [Anthropic's parallel compiler experiment](https://www.anthropic.com/engineering/building-c-compiler):
  independently verifiable decomposition matters more than the number of agents.
