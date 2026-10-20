import type { Tone } from '@/components/ui/status-dot'
import { stateLabel, stateTone, type PresentationState, type RunPresentation } from '@/lib/status'
import type { CandidateState, DeliveryReceipt, DeliveryRequest, VerificationStatus } from '@/lib/integration-types'
import type { MissionAttempt, MissionAttemptState, MissionSubmission, MissionTask } from '@/lib/types'
import { missionTree, taskTitle } from '@/routes/missions/tree'
import type { MissionDetail } from '@/store/missions'
import { workSummary } from '@/store/session-rows'
import { agentLabel, agentState, type AgentIndex, type AgentState, type Subagent } from '@/store/subagents'

/** A run's states, plus Not started for work nobody picked up and Not checked yet for a fact nobody read. */
export type WorkflowState = PresentationState | 'pending' | 'unknown'

export type WorkflowKind = 'run' | 'request' | 'agent' | 'workflow' | 'integrator' | 'task' | 'attempt' | 'submission' | 'candidate'

export interface WorkflowCounts {
  noun: 'task' | 'subagent'
  done: number
  total: number
  running: number
}

export interface WorkflowNode {
  id: string
  parentId: string | null
  kind: WorkflowKind
  state: WorkflowState
  label: string
  detail?: string
  counts?: WorkflowCounts
  startedAt?: string
  endedAt?: string
  runId?: string
  /** The subagent the inspector opens for an agent or workflow row. */
  agentId?: string
  /** A worker's or run's branch, for its one-line git summary. */
  branch?: string
  children: WorkflowNode[]
}

export const workflowLabel: Record<WorkflowState, string> = { ...stateLabel, pending: 'Not started', unknown: 'Not checked yet' }
export const workflowTone: Record<WorkflowState, Tone> = { ...stateTone, pending: 'neutral', unknown: 'neutral' }

const settled = new Set<WorkflowState>(['completed', 'merged', 'closed'])

/** Completed subtrees fold to their summary line; everything else starts open. */
export function startsOpen(node: WorkflowNode): boolean {
  return !settled.has(node.state)
}

const asking = new WeakMap<WorkflowNode, WorkflowNode[]>()

/** The deepest descendants that need the viewer, the rows that lead to the request, which a folded parent still shows. */
export function needsYouBelow(node: WorkflowNode): WorkflowNode[] {
  let found = asking.get(node)
  if (!found) {
    found = node.children.flatMap((child) => {
      const deeper = needsYouBelow(child)
      return deeper.length > 0 || child.state !== 'needs-you' ? deeper : [child]
    })
    asking.set(node, found)
  }
  return found
}

export function countsText(counts: WorkflowCounts): string {
  const noun = (n: number) => `${counts.noun}${n === 1 ? '' : 's'}`
  if (counts.noun === 'task') return `${counts.done} of ${counts.total} ${noun(counts.total)} done`
  if (counts.running > 0) return `${counts.running} ${noun(counts.running)} running`
  return `${counts.done} of ${counts.total} ${noun(counts.total)} done`
}

/** The facts a run row shows, read from presentRun so every surface agrees. */
export interface RunFacts {
  label: string
  state: PresentationState
  reason: string
  needsYou: boolean
  startedAt?: string
  endedAt?: string
  branch?: string
}

export function runFacts(
  run: { task: string; title?: string; branch: string; created_at: string; started_at: string | null; finished_at: string | null },
  shown: Pick<RunPresentation, 'state' | 'reason' | 'needsYou'>,
  label: string,
): RunFacts {
  return {
    label,
    state: shown.state,
    reason: shown.reason,
    needsYou: shown.needsYou !== undefined,
    startedAt: run.started_at ?? run.created_at,
    endedAt: run.finished_at ?? undefined,
    branch: run.branch || undefined,
  }
}

function detailOf(state: WorkflowState, reason: string | undefined): string | undefined {
  return reason && reason !== workflowLabel[state] ? reason : undefined
}

const agentStates: Record<AgentState, { state: WorkflowState; detail?: string }> = {
  running: { state: 'working' },
  done: { state: 'completed' },
  failed: { state: 'failed' },
  stopped: { state: 'interrupted', detail: 'Stopped when its turn ended' },
  launched: { state: 'running', detail: 'Started in the background' },
  unknown: { state: 'unknown', detail: 'Its launch is not loaded' },
}

const agentNodes = new WeakMap<Subagent, Omit<WorkflowNode, 'parentId' | 'children' | 'counts'>>()

function agentNode(agent: Subagent): Omit<WorkflowNode, 'parentId' | 'children' | 'counts'> {
  let node = agentNodes.get(agent)
  if (!node) {
    const { state, detail } = agentStates[agentState(agent)]
    const tools = agent.items.flatMap((item) => (item.kind === 'tool_call' && item.tool_call ? [item.tool_call.tool_kind ?? 'other'] : []))
    node = {
      id: `agent:${agent.id}`,
      kind: agent.workflow ? 'workflow' : 'agent',
      state,
      label: agentLabel(agent),
      detail: [detail, workSummary(tools)].filter(Boolean).join(' · ') || undefined,
      startedAt: agent.startedAt,
      endedAt: agent.settledAt,
      agentId: agent.id,
    }
    agentNodes.set(agent, node)
  }
  return node
}

function agentCounts(children: WorkflowNode[]): WorkflowCounts | undefined {
  const agents = children.filter((child) => child.kind === 'agent' || child.kind === 'workflow')
  if (agents.length === 0) return undefined
  return {
    noun: 'subagent',
    total: agents.length,
    running: agents.filter((child) => child.state === 'working').length,
    done: agents.filter((child) => child.state === 'completed').length,
  }
}

function agentTree(index: AgentIndex, ids: string[], parentId: string, seen: Set<string>): WorkflowNode[] {
  const nodes: WorkflowNode[] = []
  for (const id of ids) {
    const agent = index.agents[id]
    if (!agent || seen.has(id)) continue
    seen.add(id)
    const base = agentNode(agent)
    const children = agentTree(index, agent.children, base.id, seen)
    nodes.push({ ...base, parentId, counts: agentCounts(children), children })
  }
  return nodes
}

/** A run with its subagents and nested workflows; a Needs you row leads when the run asks the viewer. */
export function deriveRunWorkflow(runID: string, run: RunFacts, agents: AgentIndex): WorkflowNode {
  const id = `run:${runID}`
  const children = agentTree(agents, agents.roots, id, new Set())
  if (run.needsYou) {
    children.unshift({ id: `request:${runID}`, parentId: id, kind: 'request', state: 'needs-you', label: run.reason, runId: runID, children: [] })
  }
  return {
    id,
    parentId: null,
    kind: 'run',
    state: run.state,
    label: run.label,
    detail: detailOf(run.state, run.reason),
    counts: agentCounts(children),
    startedAt: run.startedAt,
    endedAt: run.endedAt,
    runId: runID,
    children,
  }
}

/** The integration facts a candidate row shows: integration.list, with integration.show's verifications when read. */
export interface CandidateFacts {
  candidate_id: string
  state: CandidateState
  candidate_revision?: string
  target_ref: string
  conflicts?: string[]
  verifications?: { candidate_revision: string; status: VerificationStatus; exit_code?: number; error?: string; created_at: string; finished_at?: string }[]
  delivery_request?: Pick<DeliveryRequest, 'state'>
  delivery_receipt?: Pick<DeliveryReceipt, 'result' | 'created_at'>
  created_at: string
  error?: string
}

const attemptStates: Record<MissionAttemptState, WorkflowState> = {
  reserved: 'working',
  launching: 'working',
  running: 'working',
  unknown: 'unknown',
  submitted: 'ready',
  completed: 'completed',
  failed: 'failed',
  cancelled: 'closed',
  superseded: 'closed',
  abandoned: 'closed',
}

const attemptWord: Record<MissionAttemptState, string> = {
  reserved: 'Reserved',
  launching: 'Launching',
  running: 'Running',
  unknown: 'State unknown',
  submitted: 'Submitted',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
  superseded: 'Superseded',
  abandoned: 'Abandoned',
}

function submissionNode(submission: MissionSubmission, task: MissionTask, parentId: string): WorkflowNode {
  const earlier = submission.task_revision !== task.current_revision ? ` for revision ${submission.task_revision}` : ''
  const shown: Record<MissionSubmission['state'], { state: WorkflowState; label: string }> = {
    proposed: { state: 'waiting-agents', label: 'Submitted, waiting for the integrator' },
    accepted: { state: 'completed', label: 'Accepted' },
    rejected: { state: 'failed', label: 'Rejected' },
    superseded: { state: 'closed', label: 'Superseded' },
    abandoned: { state: 'closed', label: 'Abandoned' },
  }
  const { state, label } = shown[submission.state]
  const violations = submission.scope_violations ?? []
  return {
    id: `submission:${submission.id}`,
    parentId,
    kind: 'submission',
    state,
    label: `${label}${earlier}`,
    detail: violations.length > 0 ? `Out of scope: ${violations.join(', ')}` : undefined,
    startedAt: submission.created_at,
    endedAt: submission.decided_at ?? undefined,
    runId: submission.ref.run_id,
    children: [],
  }
}

function attemptNode(attempt: MissionAttempt, name: string, note: string | undefined, submissions: MissionSubmission[], task: MissionTask, run: RunFacts | undefined, parentId: string): WorkflowNode {
  const id = `attempt:${attempt.id}`
  const live = run && !['cancelled', 'superseded', 'abandoned'].includes(attempt.state)
  const state: WorkflowState = live ? run.state : attemptStates[attempt.state]
  const reason = live ? detailOf(run.state, run.reason) : undefined
  return {
    id,
    parentId,
    kind: 'attempt',
    state,
    label: name,
    detail: [attemptWord[attempt.state], reason, attempt.last_error, note].filter(Boolean).join(' · '),
    startedAt: attempt.started_at ?? undefined,
    endedAt: attempt.finished_at ?? run?.endedAt,
    runId: attempt.run_id || undefined,
    branch: run?.branch,
    children: submissions.filter((item) => item.attempt_id === attempt.id).map((item) => submissionNode(item, task, id)),
  }
}

function taskState(task: MissionTask, latest: WorkflowNode | undefined, waitingOn: string[], accepted: boolean): { state: WorkflowState; detail?: string } {
  if (accepted || task.status === 'done') return { state: 'completed' }
  switch (task.status) {
    case 'abandoned':
      return { state: 'closed', detail: 'Abandoned' }
    case 'proposed':
      return { state: 'pending', detail: 'Proposed, not accepted yet' }
    case 'blocked': {
      const blocker = task.blockers?.[0]
      return { state: 'waiting', detail: blocker ? `Blocked on ${blocker.kind}: ${blocker.action}` : 'Blocked' }
    }
    case 'review':
      return { state: 'waiting-agents', detail: 'Waiting for the integrator to review' }
  }
  if (latest && ['ready', 'completed', 'merged'].includes(latest.state)) return { state: 'waiting-agents', detail: 'Worker finished, waiting for the integrator' }
  if (latest && latest.state !== 'closed') return { state: latest.state, detail: latest.detail }
  if (waitingOn.length > 0) return { state: 'waiting', detail: `Waiting on ${waitingOn.join(', ')}` }
  return { state: 'pending', detail: 'No worker yet' }
}

function verificationState(candidate: CandidateFacts): { state: WorkflowState; detail: string } {
  const checks = (candidate.verifications ?? []).filter((item) => item.candidate_revision === candidate.candidate_revision)
  const latest = checks.at(-1)
  if (!latest) return { state: 'unknown', detail: candidate.verifications ? 'Not verified yet' : 'Verification not checked yet' }
  switch (latest.status) {
    case 'running':
      return { state: 'waiting-ci', detail: 'Verification running' }
    case 'passed':
      return { state: 'ready', detail: 'Verification passed' }
    default: {
      const why = latest.error || (latest.exit_code !== undefined ? `exit ${latest.exit_code}` : latest.status.replace('_', ' '))
      return { state: 'failed', detail: `Verification failed: ${why}` }
    }
  }
}

function candidateNode(candidate: CandidateFacts, parentId: string): WorkflowNode {
  const base = {
    id: `candidate:${candidate.candidate_id}`,
    parentId,
    kind: 'candidate' as const,
    label: `Candidate for ${candidate.target_ref}`,
    startedAt: candidate.created_at,
    children: [],
  }
  if (candidate.error) return { ...base, state: 'failed', detail: candidate.error }
  const receipt = candidate.delivery_receipt
  if (receipt) return { ...base, state: 'completed', detail: receipt.result === 'proposed' ? 'Delivered as a proposal' : 'Delivered', endedAt: receipt.created_at }
  switch (candidate.state) {
    case 'preparing':
      return { ...base, state: 'working', detail: 'Preparing' }
    case 'conflicted': {
      const n = candidate.conflicts?.length ?? 0
      return { ...base, state: 'waiting-agents', detail: n > 0 ? `${n} ${n === 1 ? 'conflict' : 'conflicts'}: ${candidate.conflicts!.join(', ')}` : 'Conflicted' }
    }
    case 'frozen': {
      const request = candidate.delivery_request?.state
      if (request === 'pending' || request === 'approved' || request === 'delivering') return { ...base, state: 'waiting-agents', detail: `Delivery ${request}` }
      if (request === 'denied') return { ...base, state: 'failed', detail: 'Delivery denied' }
      return { ...base, ...verificationState(candidate) }
    }
    default:
      return { ...base, state: 'closed', detail: candidate.state.charAt(0).toUpperCase() + candidate.state.slice(1) }
  }
}

export interface SwarmInput {
  detail: MissionDetail
  runs: Record<string, RunFacts>
  candidates: CandidateFacts[]
  integratorLabel: string
  workerLabel?: (attempt: MissionAttempt, name: string) => string
}

/** The integrator, its tasks, their workers, the workers' submissions, then the integration candidates. */
export function deriveSwarmWorkflow({ detail, runs, candidates, integratorLabel, workerLabel = (_, name) => name }: SwarmInput): WorkflowNode {
  const { mission } = detail
  const rootID = `integrator:${mission.id}`
  const runID = mission.current_integrator_run_id
  const integrator = runID ? runs[runID] : undefined
  const titles = new Map(detail.tasks.map((task) => [task.id, taskTitle(task)]))
  const accepted = new Set(
    detail.tasks
      .filter((task) => detail.submissions.some((item) => item.task_id === task.id && item.state === 'accepted' && item.task_revision === task.current_revision))
      .map((task) => task.id),
  )
  const unfinished = new Set(detail.tasks.filter((task) => task.status !== 'done' && !accepted.has(task.id)).map((task) => task.id))
  let done = 0
  let running = 0
  const tasks = missionTree(detail).map((entry): WorkflowNode => {
    const { task } = entry
    const id = `task:${task.id}`
    const attempts = entry.attempts.map((item) =>
      attemptNode(item.attempt, workerLabel(item.attempt, item.name), item.earlierIntegrator ? 'from a replaced integrator' : undefined, detail.submissions, task, runs[item.attempt.run_id], id),
    )
    const waitingOn = (task.dependencies ?? []).filter((item) => unfinished.has(item.depends_on_task_id)).map((item) => titles.get(item.depends_on_task_id) ?? item.depends_on_task_id)
    const { state, detail: why } = taskState(task, attempts[0], waitingOn, accepted.has(task.id))
    if (accepted.has(task.id)) done++
    if (state === 'working') running++
    const depends = entry.dependsOn.length > 0 ? `Depends on ${entry.dependsOn.map(taskTitle).join(', ')}` : undefined
    return {
      id,
      parentId: rootID,
      kind: 'task',
      state,
      label: entry.title,
      detail: [why, state === 'waiting' ? undefined : depends].filter(Boolean).join(' · ') || undefined,
      startedAt: attempts.at(-1)?.startedAt,
      endedAt: state === 'completed' ? attempts[0]?.endedAt : undefined,
      children: attempts,
    }
  })
  const children = [...tasks, ...candidates.map((candidate) => candidateNode(candidate, rootID))]
  const state: WorkflowState = integrator?.state ?? (mission.phase === 'completed' ? 'completed' : mission.phase === 'cancelled' ? 'closed' : 'unknown')
  return {
    id: rootID,
    parentId: null,
    kind: 'integrator',
    state,
    label: integratorLabel,
    detail: integrator ? detailOf(integrator.state, integrator.reason) : runID ? 'Run not loaded' : 'No integrator run',
    counts: tasks.length > 0 ? { noun: 'task', done, total: tasks.length, running } : undefined,
    startedAt: integrator?.startedAt,
    endedAt: integrator?.endedAt,
    runId: runID || undefined,
    children,
  }
}

function sameCounts(a: WorkflowCounts | undefined, b: WorkflowCounts | undefined): boolean {
  return a === b || (!!a && !!b && a.noun === b.noun && a.done === b.done && a.total === b.total && a.running === b.running)
}

const fields = ['parentId', 'kind', 'state', 'label', 'detail', 'startedAt', 'endedAt', 'runId', 'agentId', 'branch'] as const

function index(node: WorkflowNode | undefined, into = new Map<string, WorkflowNode>()): Map<string, WorkflowNode> {
  if (!node) return into
  into.set(node.id, node)
  for (const child of node.children) index(child, into)
  return into
}

/**
 * The next tree with every unchanged subtree replaced by the previous one's object, so a memoised
 * row skips re-rendering. Idempotent: sharing a tree with itself returns it.
 */
export function shareTree(prev: WorkflowNode | undefined, next: WorkflowNode): WorkflowNode {
  if (!prev || prev === next) return next
  const old = index(prev)
  const share = (node: WorkflowNode): WorkflowNode => {
    const children = node.children.map(share)
    const before = old.get(node.id)
    if (
      before &&
      fields.every((key) => before[key] === node[key]) &&
      sameCounts(before.counts, node.counts) &&
      before.children.length === children.length &&
      before.children.every((child, i) => child === children[i])
    ) {
      return before
    }
    return children.every((child, i) => child === node.children[i]) ? node : { ...node, children }
  }
  return share(next)
}
