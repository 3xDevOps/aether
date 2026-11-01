import type { Tone } from '@/components/ui/status-dot'
import { stateLabel, stateTone, type PresentationState, type RunPresentation } from '@/lib/status'
import { isLaunch } from '@/store/session-rows'
import { workSummary } from '@/store/session-steps'
import { agentLabel, agentState, type AgentIndex, type AgentState, type Subagent } from '@/store/subagents'

/** A run's states, plus Not started for work nobody picked up and Not checked yet for a fact nobody read. */
export type WorkflowState = PresentationState | 'pending' | 'unknown'

export type WorkflowKind = 'run' | 'request' | 'agent' | 'workflow'

export interface WorkflowCounts {
  noun: 'agent'
  done: number
  total: number
  running?: number
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
  /** A run's branch, for its one-line git summary. */
  branch?: string
  children: WorkflowNode[]
}

export const workflowLabel: Record<WorkflowState, string> = { ...stateLabel, pending: 'Not started', unknown: 'Not checked yet' }
export const workflowTone: Record<WorkflowState, Tone> = { ...stateTone, pending: 'neutral', unknown: 'neutral' }

const settled = new Set<WorkflowState>(['completed', 'merged', 'closed'])

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
  if (counts.running) return `${counts.running} ${noun(counts.running)} running`
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
  shown: Pick<RunPresentation, 'state' | 'reason'>,
  label: string,
  needsYou: boolean,
): RunFacts {
  return {
    label,
    state: shown.state,
    reason: shown.reason,
    needsYou,
    startedAt: run.started_at ?? run.created_at,
    endedAt: run.finished_at ?? undefined,
    branch: run.branch || undefined,
  }
}

function detailOf(state: WorkflowState, reason: string | undefined): string | undefined {
  return reason && reason !== workflowLabel[state] ? reason : undefined
}

/** A child run as a workflow row, under the run that started it. */
export interface ChildRunFacts extends RunFacts {
  runId: string
  parentRunId: string
}

function childRunNodes(parentRunID: string, parentId: string, childRuns: ChildRunFacts[]): WorkflowNode[] {
  return childRuns.filter((child) => child.parentRunId === parentRunID).map((child) => {
    const id = `run:${child.runId}`
    return {
      id,
      parentId,
      kind: 'run',
      state: child.state,
      label: child.label,
      detail: detailOf(child.state, child.reason),
      startedAt: child.startedAt,
      endedAt: child.endedAt,
      runId: child.runId,
      branch: child.branch,
      children: childRunNodes(child.runId, id, childRuns),
    }
  })
}

const agentStates: Record<AgentState, { state: WorkflowState; detail?: string }> = {
  running: { state: 'working' },
  done: { state: 'completed' },
  failed: { state: 'failed' },
  stopped: { state: 'interrupted', detail: 'Stopped when its turn ended' },
  launched: { state: 'running', detail: 'Started in the background' },
  unknown: { state: 'unknown', detail: 'Its launch is not loaded' },
}

const agentNodes = new WeakMap<Subagent, Omit<WorkflowNode, 'parentId' | 'children'>>()

function agentNode(agent: Subagent): Omit<WorkflowNode, 'parentId' | 'children'> {
  let node = agentNodes.get(agent)
  if (!node) {
    const { state, detail } = agentStates[agentState(agent)]
    const tools = new Map<string, string>()
    for (const { tool_call: call } of agent.items) if (call && !isLaunch(call)) tools.set(call.id, call.tool_kind ?? 'other')
    node = {
      id: `agent:${agent.id}`,
      kind: agent.workflow ? 'workflow' : 'agent',
      state,
      label: agentLabel(agent),
      detail: [detail, workSummary([...tools.values()])].filter(Boolean).join(' · ') || undefined,
      startedAt: agent.startedAt,
      endedAt: state === 'working' || state === 'running' ? undefined : agent.settledAt,
      agentId: agent.id,
    }
    agentNodes.set(agent, node)
  }
  return node
}

function agentsIn(nodes: WorkflowNode[]): WorkflowNode[] {
  return nodes.flatMap((node) => (node.agentId ? [node, ...agentsIn(node.children)] : []))
}

/** Every subagent at any depth; a background one still going reads Running. */
function agentCounts(children: WorkflowNode[]): WorkflowCounts | undefined {
  const agents = agentsIn(children)
  if (agents.length === 0) return undefined
  return {
    noun: 'agent',
    total: agents.length,
    running: agents.filter((agent) => agent.state === 'working' || agent.state === 'running').length,
    done: agents.filter((agent) => agent.state === 'completed').length,
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
    nodes.push({ ...base, parentId, children })
  }
  return nodes
}

/** `childRuns` holds the run's descendants; a cycle among them is never reached from the run. */
export function deriveRunWorkflow(runID: string, run: RunFacts, agents: AgentIndex, childRuns: ChildRunFacts[] = []): WorkflowNode {
  const id = `run:${runID}`
  const seen = new Set<string>()
  const children = agentTree(agents, agents.roots, id, seen)
  // An agent whose launch the loaded history does not hold is in no list of children.
  const unplaced = Object.values(agents.agents)
    .filter((agent) => !seen.has(agent.id) && (!agent.parentID || !agents.agents[agent.parentID]))
    .sort((a, b) => (a.items[0]?.seq ?? 0) - (b.items[0]?.seq ?? 0))
    .map((agent) => agent.id)
  children.push(...agentTree(agents, unplaced, id, seen), ...childRunNodes(runID, id, childRuns))
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
