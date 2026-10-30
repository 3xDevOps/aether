import { flattenWorkflow } from '@/components/workflow/flatten'
import type { SessionItem, SessionToolCall } from '@/lib/session-types'
import { presentRun, runLabel } from '@/lib/status'
import type { MissionAttempt, MissionSubmission, Run } from '@/lib/types'
import type { MissionDetail } from '@/store/missions'
import { toRecord } from '@/store/runs'
import { indexItems, noAgents } from '@/store/subagents'
import {
  countsText,
  deriveRunWorkflow,
  deriveSwarmWorkflow,
  needsYouBelow,
  runFacts,
  shareTree,
  type ChildRunFacts,
  type RunFacts,
  type WorkflowNode,
} from '@/store/workflow'
import { resetItems, tool } from '@/test/acp-stream'
import { mission, missionTask, missionTaskRevision, run, stateContext } from '@/test/fixtures'

beforeEach(resetItems)

const launch = (turn: number, id: string, status = 'in_progress', extra: Partial<SessionToolCall> = {}) =>
  tool(turn, id, 'other', `Agent ${id}`, status, { tool_name: 'Agent', subagent: { description: `Job ${id}`, type: 'Explore' }, ...extra })
const read = (turn: number, parent: string, id: string) => tool(turn, id, 'read', `Read ${id}`, 'completed', { parent_id: parent, tool_name: 'Read' })
const agentsOf = (items: SessionItem[]) => indexItems(noAgents, items).index

const facts = (over: Partial<Run> = {}) => {
  const record = toRecord(run(over))
  const shown = presentRun(record, stateContext({ runs: { [record.id]: record } }))
  return runFacts(record, shown, runLabel(record), shown.needsYou !== undefined)
}

function byID(node: WorkflowNode, id: string): WorkflowNode | undefined {
  if (node.id === id) return node
  for (const child of node.children) {
    const found = byID(child, id)
    if (found) return found
  }
  return undefined
}

describe('run workflow', () => {
  it('nests subagents under their launching agent and groups their tool calls', () => {
    const agents = agentsOf([launch(1, 'a1'), launch(1, 'a2', 'completed'), launch(1, 'b1', 'in_progress', { parent_id: 'a1' }), read(1, 'b1', 'r1'), read(1, 'b1', 'r2'), read(1, 'b1', 'r2'), launch(1, 'c1', 'in_progress', { parent_id: 'b1' })])
    const tree = deriveRunWorkflow('run_1', facts({ state: 'waiting-agents', status: 'running' }), agents)
    expect(tree.children.map((child) => child.id)).toEqual(['agent:a1', 'agent:a2'])
    const nested = byID(tree, 'agent:b1')!
    expect(nested).toMatchObject({ parentId: 'agent:a1', kind: 'agent', state: 'working', label: 'Job b1', detail: 'Read 2 files', agentId: 'b1' })
    expect(byID(tree, 'agent:a2')!.state).toBe('completed')
    expect(countsText(tree.counts!)).toBe('3 agents running')
    expect(byID(tree, 'agent:a1')!.counts).toBeUndefined()
    expect(tree.counts).toMatchObject({ total: 4, running: 3, done: 1 })
  })

  it('keeps a background agent that outlives its launch call running', () => {
    const agents = agentsOf([launch(1, 'w1', 'completed', { tool_name: 'Workflow', subagent: undefined, raw_input: { name: 'audit' } }), read(1, 'w1', 'r1')])
    const tree = deriveRunWorkflow('run_1', facts({ status: 'running', state: 'working' }), agents)
    expect(tree.children[0]).toMatchObject({ kind: 'workflow', state: 'working', endedAt: undefined })
  })

  it('keeps an agent whose launch is not loaded', () => {
    const agents = agentsOf([read(1, 'x1', 'r1')])
    const tree = deriveRunWorkflow('run_1', facts({ status: 'running', state: 'working' }), agents)
    expect(tree.children.map((child) => [child.id, child.state])).toEqual([['agent:x1', 'unknown']])
  })

  it('follows the run through working, waiting for agents, needs you, ready for review and completed', () => {
    const seen = [
      facts({ status: 'running', state: 'working' }),
      facts({ status: 'running', state: 'waiting-agents' }),
      facts({ status: 'needs-attention', state: 'needs-you', reason: 'agent asked a question' }),
      facts({ status: 'needs-attention', state: 'finished' }),
      facts({ status: 'completed', finished_at: '2026-08-14T10:30:00Z' }),
    ].map((run) => deriveRunWorkflow('run_1', run, noAgents))
    expect(seen.map((tree) => tree.state)).toEqual(['working', 'waiting-agents', 'needs-you', 'ready', 'completed'])
    expect(seen[2]!.children[0]).toMatchObject({ kind: 'request', state: 'needs-you', id: 'request:run_1' })
    expect(seen[4]!.endedAt).toBe('2026-08-14T10:30:00Z')
  })

  it('keeps every unchanged subtree when an unrelated agent moves', () => {
    const first = [launch(1, 'a1'), launch(1, 'b1', 'in_progress', { parent_id: 'a1' }), launch(1, 'a2')]
    const before = agentsOf(first)
    const run = facts({ status: 'running', state: 'working' })
    const prev = deriveRunWorkflow('run_1', run, before)
    const after = indexItems(before, [read(1, 'a2', 'r9')]).index
    const next = shareTree(prev, deriveRunWorkflow('run_1', run, after))
    expect(next).not.toBe(prev)
    expect(byID(next, 'agent:a1')).toBe(byID(prev, 'agent:a1'))
    expect(byID(next, 'agent:a2')).not.toBe(byID(prev, 'agent:a2'))
    expect(shareTree(next, deriveRunWorkflow('run_1', run, after))).toBe(next)
  })
})

function attempt(over: Partial<MissionAttempt>): MissionAttempt {
  return {
    id: 'att_1', mission_id: 'mission_1', task_id: 'task_1', task_revision: 1, number: 1, dispatch_key: 'k',
    harness: 'claude', mode: 'acp', state: 'running', run_id: 'run_w1', authority_generation: 1,
    integrator_generation: 1, created_at: '2026-08-14T10:03:00Z', reserved_at: '2026-08-14T10:03:00Z', ...over,
  }
}

function submission(over: Partial<MissionSubmission>): MissionSubmission {
  return {
    id: 'sub_1', mission_id: 'mission_1', task_id: 'task_1', task_revision: 1, attempt_id: 'att_1',
    ref: { workspace_id: 'ws', run_id: 'run_w1', evidence_ref: 'e', retained_revision: 'abc' },
    evidence: [], state: 'proposed', proposed_by_run_id: 'run_w1', integrator_generation: 1, created_at: '2026-08-14T10:10:00Z', ...over,
  }
}

const tasks = [
  missionTask(),
  missionTask({ id: 'task_2', revision: missionTaskRevision({ task_id: 'task_2', title: 'Second' }), status: 'ready', dependencies: [{ task_id: 'task_2', revision: 1, depends_on_task_id: 'task_1', depends_on_revision: 1 }] }),
]

function swarm(over: Partial<MissionDetail> = {}, runs: Record<string, RunFacts> = {}) {
  const detail: MissionDetail = {
    mission: mission({ current_integrator_run_id: 'run_int' }),
    tasks,
    attempts: [attempt({})],
    submissions: [],
    diagnostics: [],
    questions: [],
    ...over,
  }
  return deriveSwarmWorkflow({ detail, runs, integratorLabel: 'Integrator' })
}

describe('swarm workflow', () => {
  it('links task to attempt to submission', () => {
    const tree = swarm({ submissions: [submission({ state: 'accepted', decided_at: '2026-08-14T10:12:00Z' })] })
    expect(tree.children.map((child) => child.id)).toEqual(['task:task_1', 'task:task_2'])
    const task = byID(tree, 'task:task_1')!
    expect(task.state).toBe('completed')
    expect(task.children[0]).toMatchObject({ id: 'attempt:att_1', parentId: 'task:task_1', runId: 'run_w1' })
    expect(task.children[0]!.children[0]).toMatchObject({ id: 'submission:sub_1', parentId: 'attempt:att_1', state: 'completed', label: 'Accepted' })
    expect(countsText(tree.counts!)).toBe('1 of 2 tasks done')
    expect(byID(tree, 'task:task_2')).toMatchObject({ state: 'pending', detail: 'No worker yet · Depends on rewrite the guest checkout flow' })
    expect(byID(swarm(), 'task:task_2')).toMatchObject({ state: 'waiting', detail: 'Waiting on rewrite the guest checkout flow' })
  })

  it('counts a task done only for an accepted submission of its current revision', () => {
    const stale = swarm({ tasks: [missionTask({ current_revision: 2 })], submissions: [submission({ state: 'accepted', task_revision: 1 })] })
    expect(stale.counts).toMatchObject({ done: 0, total: 1 })
    expect(stale.children[0]!.children[0]!.children[0]!.label).toBe('Accepted for revision 1')
  })

  it('reads a task from its current revision and names the prerequisites that block it', () => {
    const revised = swarm({
      tasks: [missionTask({ current_revision: 2, status: 'ready' }), { ...tasks[1]!, status: 'blocked', blockers: [{ kind: 'dependency', action: 'wait' }] }],
      attempts: [attempt({ state: 'completed', task_revision: 1 })],
    })
    expect(byID(revised, 'task:task_1')).toMatchObject({ state: 'pending', detail: 'No worker yet' })
    expect(byID(revised, 'task:task_2')).toMatchObject({ state: 'waiting', detail: 'Waiting on rewrite the guest checkout flow' })
  })

  it('surfaces a worker that needs you through its folded task', () => {
    const asking = facts({ id: 'run_w1', status: 'needs-attention', state: 'needs-you', reason: 'approve a command' })
    const tree = swarm({}, { run_w1: asking })
    const task = byID(tree, 'task:task_1')!
    expect(task.state).toBe('needs-you')
    expect(needsYouBelow(tree).map((node) => node.id)).toEqual(['attempt:att_1'])
    const rows = flattenWorkflow(tree, new Map([['task:task_1', false]]))
    const surfaced = rows.find((row) => row.surfaced)
    expect(surfaced).toMatchObject({ key: 'task:task_1>attempt:att_1', level: 3, parentKey: 'task:task_1' })
  })

  it('keeps the other tasks when one worker changes state', () => {
    const working = facts({ id: 'run_w1', status: 'running', state: 'working' })
    const detail = { attempts: [attempt({}), attempt({ id: 'att_2', task_id: 'task_2', run_id: 'run_w2' })] }
    const prev = swarm(detail, { run_w1: working, run_w2: working })
    const next = shareTree(prev, swarm(detail, { run_w1: working, run_w2: facts({ id: 'run_w2', status: 'running', state: 'waiting-ci' }) }))
    expect(byID(next, 'task:task_1')).toBe(byID(prev, 'task:task_1'))
    expect(byID(next, 'task:task_2')!.state).toBe('waiting-ci')
  })
})

describe('child runs', () => {
  const childFacts = (runId: string, parentRunId: string, over: Partial<Run> = {}): ChildRunFacts =>
    ({ ...facts({ id: runId, child_purpose: `review ${runId}`, ...over }), runId, parentRunId })

  it('nests child runs under the run that started them, at any depth', () => {
    const tree = deriveRunWorkflow('run_1', facts(), noAgents, [childFacts('c1', 'run_1'), childFacts('c2', 'c1', { status: 'needs-attention', state: 'needs-you' })])
    expect(byID(tree, 'run:c1')).toMatchObject({ parentId: 'run:run_1', kind: 'run', label: 'review c1', runId: 'c1' })
    expect(byID(tree, 'run:c2')).toMatchObject({ parentId: 'run:c1', state: 'needs-you' })
    expect(needsYouBelow(tree).map((node) => node.id)).toEqual(['run:c2'])
  })

  it('nests a worker\'s child runs under its attempt and the integrator\'s under the root', () => {
    const detail: MissionDetail = { mission: mission({ current_integrator_run_id: 'run_int' }), tasks, attempts: [attempt({})], submissions: [], diagnostics: [], questions: [] }
    const tree = deriveSwarmWorkflow({
      detail, runs: {}, integratorLabel: 'Integrator',
      childRuns: [childFacts('wc', 'run_w1'), childFacts('ic', 'run_int')],
    })
    expect(byID(tree, 'run:wc')?.parentId).toBe('attempt:att_1')
    expect(byID(tree, 'run:ic')?.parentId).toBe('integrator:mission_1')
  })
})

describe('folding', () => {
  it('folds completed subtrees and caps the depth with a nested row', () => {
    const chain = [launch(1, 'a1'), launch(1, 'a2', 'in_progress', { parent_id: 'a1' }), launch(1, 'a3', 'in_progress', { parent_id: 'a2' }), launch(1, 'a4', 'in_progress', { parent_id: 'a3' }), launch(1, 'a5', 'in_progress', { parent_id: 'a4' }), launch(1, 'done', 'completed'), launch(1, 'inner', 'completed', { parent_id: 'done' })]
    const tree = deriveRunWorkflow('run_1', facts({ status: 'running', state: 'working' }), agentsOf(chain))
    const rows = flattenWorkflow(tree, new Map())
    expect(rows.map((row) => row.key)).toEqual(['run:run_1', 'agent:a1', 'agent:a2', 'agent:a3', 'nested:agent:a3', 'agent:done'])
    expect(rows[4]).toMatchObject({ nested: 2, level: 5 })
    expect(rows.find((row) => row.key === 'agent:done')!.expanded).toBe(false)
  })
})
