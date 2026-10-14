import type { SessionItem, SessionToolCall } from '@/lib/session-types'
import {
  activityLabel, agentLabel, agentLoaded, agentPrompt, agentReport, agentRows, agentState, agentTurn, agentType, ancestry,
  indexItems, localItem, noAgents, replaceAgentItem, siblings, trimAgents, turnExtras, withAgentRows,
} from '@/store/subagents'
import { rowsOfTurn } from '@/store/session-rows'
import { item, resetItems, say, tool } from '@/test/acp-stream'

beforeEach(resetItems)

const launch = (turn: number, id: string, status = 'in_progress', extra: Partial<SessionToolCall> = {}) =>
  tool(turn, id, 'other', `Agent ${id}`, status, { tool_name: 'Agent', subagent: { description: `Job ${id}`, type: 'Explore' }, ...extra })
const read = (turn: number, parent: string, id: string, status = 'completed') =>
  tool(turn, id, 'read', `Read ${id}`, status, { parent_id: parent, tool_name: 'Read' })
const index = (items: SessionItem[], older = false, from = noAgents) => indexItems(from, items, older)

describe('indexing subagents', () => {
  it('routes owned items to their agent and leaves the main agent to the turns', () => {
    const items = [item('turn_start', 1), launch(1, 'a1'), read(1, 'a1', 'r1'), say(1, 'assistant', 'hi'), item('plan', 1, { plan: [], plan_parent_id: 'a1' }),
      item('thought', 1, { message: { role: 'thought', message_id: 'th', parent_id: 'a1', text: 'hm' } })]
    const { index: idx, main } = index(items)
    expect(main.map((it) => it.seq)).toEqual([1, 2, 4])
    expect(idx.agents.a1!.items.map((it) => it.seq)).toEqual([3, 5, 6])
    expect(idx.agents.a1!.tools).toBe(1)
    expect(idx.agents.a1!.latest?.seq).toBe(6)
    expect(idx.roots).toEqual(['a1'])
  })

  it('chains nested subagents and keeps concurrent ones as siblings', () => {
    const { index: idx } = index([launch(1, 'a1'), launch(1, 'a2'), launch(1, 'b1', 'in_progress', { parent_id: 'a1' }), read(1, 'b1', 'r1')])
    expect(idx.roots).toEqual(['a1', 'a2'])
    expect(idx.agents.a1!.children).toEqual(['b1'])
    expect(idx.agents.b1).toMatchObject({ parentID: 'a1', tools: 1 })
    expect(idx.agents.a1!.items.map((it) => it.seq)).toEqual([3])
    expect(ancestry(idx, 'b1').map((a) => a.id)).toEqual(['a1', 'b1'])
    expect(siblings(idx, idx.agents.b1!).map((id) => id)).toEqual(['b1'])
    expect(siblings(idx, idx.agents.a2!)).toEqual(['a1', 'a2'])
  })

  it('derives each state from the call, the turn and the workflow flag', () => {
    const run = (items: SessionItem[]) => Object.values(index(items).index.agents).map(agentState)
    expect(run([launch(1, 'a')])).toEqual(['running'])
    expect(run([launch(1, 'a'), launch(1, 'a', 'completed')])).toEqual(['done'])
    expect(run([launch(1, 'a', 'failed')])).toEqual(['failed'])
    expect(run([launch(1, 'a'), item('turn_end', 1)])).toEqual(['stopped'])
    expect(run([launch(1, 'w', 'completed', { tool_name: 'Workflow', subagent: undefined })])).toEqual(['launched'])
    expect(run([read(1, 'ghost', 'r1')])).toEqual(['unknown'])
  })

  it('takes a background task state over the call status and settles when the task ends', () => {
    const workflow = (status: string, task: NonNullable<SessionToolCall['task']>['status']) =>
      launch(1, 'w', status, { tool_name: 'Workflow', subagent: undefined, task: { id: 't1', type: 'local_workflow', status: task },
        content: [{ type: 'text', text: 'Workflow audit is running in the background.' }] })
    let idx = index([workflow('in_progress', 'running'), workflow('completed', 'running')]).index
    expect(agentState(idx.agents.w!)).toBe('running')
    expect(idx.agents.w!.settledAt).toBeUndefined()
    const ended = workflow('completed', 'completed')
    idx = index([ended], false, idx).index
    expect(agentState(idx.agents.w!)).toBe('done')
    expect(idx.agents.w!.settledAt).toBe(ended.time)
    expect(agentReport(idx.agents.w!)).toBeUndefined()
    expect(agentState(trimAgents(idx, ended.seq + 1).agents.w!)).toBe('done')
    expect(agentState(index([workflow('completed', 'stopped')]).index.agents.w!)).toBe('stopped')
    expect(agentState(index([workflow('completed', 'failed')]).index.agents.w!)).toBe('failed')
  })

  it('routes a parent-less late snapshot by the call it first saw', () => {
    const first = read(1, 'a1', 'r1', 'in_progress')
    const late = tool(2, 'r1', 'read', 'Read r1', 'completed')
    const { index: idx, main } = index([launch(1, 'a1'), first, item('turn_end', 1), item('turn_start', 2), late])
    expect(main).not.toContain(late)
    expect(idx.agents.a1!.items).toEqual([first, late])
    expect(idx.agents.a1!.tools).toBe(1)
  })

  it('takes the latest call, the first launch and the first settle across snapshots', () => {
    const { index: idx } = index([launch(1, 'a1'), launch(1, 'a1', 'completed'), launch(1, 'a1', 'completed', { title: 'last' })])
    const agent = idx.agents.a1!
    expect(agent).toMatchObject({ launchSeq: 1, callSeq: 3, startedAt: expect.any(String), turn: 1 })
    expect(agent.call?.title).toBe('last')
    expect(agent.settledAt).toBe(new Date(Date.parse('2026-10-06T10:00:00Z') + 2000).toISOString())
  })

  it('discovers launches out of order from older pages', () => {
    const l1 = launch(1, 'a1')
    const c1 = read(1, 'a1', 'r1')
    const l2 = launch(1, 'a2')
    const done1 = launch(1, 'a1', 'completed')
    const c2 = read(1, 'a2', 'r2')
    const live = index([done1, c2])
    expect(live.index.agents.a1).toMatchObject({ launchSeq: 4, parentID: '' })
    const paged = index([l1, c1, l2], true, live.index)
    const { agents, roots } = paged.index
    expect(roots).toEqual(['a1', 'a2'])
    expect(agents.a1).toMatchObject({ launchSeq: 1, callSeq: 4, startedAt: l1.time })
    expect(agents.a1!.settledAt).toBe(done1.time)
    expect(agents.a1!.items.map((it) => it.seq)).toEqual([2])
    expect(agents.a2!.items.map((it) => it.seq)).toEqual([5])
    expect(agents.a2!.tools).toBe(1)
  })

  it('links children discovered before their launch, then fixes the parent when it arrives', () => {
    const outer = launch(1, 'a1')
    const inner = launch(1, 'b1', 'in_progress', { parent_id: 'a1' })
    const first = index([read(2, 'b1', 'r1')])
    expect(first.index.agents.b1).toMatchObject({ parentID: '' })
    expect(first.index.agents.b1!.launchSeq).toBeUndefined()
    expect(first.index.roots).toEqual([])
    const second = index([outer, inner], true, first.index).index
    expect(second.agents.b1).toMatchObject({ parentID: 'a1', launchSeq: 2 })
    expect(second.agents.a1!.children).toEqual(['b1'])
    expect(second.roots).toEqual(['a1'])
    expect(agentLoaded(second.agents.b1!, 1)).toBe(true)
    expect(agentLoaded(second.agents.b1!, 3)).toBe(false)
  })

  it('reports a snapshot first seen in a later turn that an older page moves home', () => {
    const early = tool(1, 'x', 'execute', 'make', 'in_progress')
    const late = tool(3, 'x', 'execute', 'make', 'completed')
    const live = index([late])
    expect(live.home.size).toBe(0)
    const paged = index([early], true, live.index)
    expect(paged.rehomed).toEqual([{ id: 'x', from: 3, to: 1 }])
  })

  it('reorders siblings when an older page reveals earlier launches', () => {
    const a = launch(1, 'a')
    const b = launch(1, 'b')
    const live = index([launch(1, 'b', 'completed'), launch(1, 'a', 'completed')])
    expect(live.index.roots).toEqual(['b', 'a'])
    expect(index([a, b], true, live.index).index.roots).toEqual(['a', 'b'])
  })

  it('closes agents of a turn when it ends, including a turn end already seen', () => {
    const end = item('turn_end', 1)
    const a = launch(1, 'a')
    const live = index([a, end])
    expect(live.index.agents.a!.turnEndedAt).toBe(end.time)
    const older = index([launch(1, 'b')], true, index([end]).index)
    expect(older.index.agents.b!.turnEndedAt).toBe(end.time)
  })

  it('trims old items, replaces a whole item and reads the call back', () => {
    const items = [launch(1, 'a1', 'completed', { content: [{ type: 'text', text: 'one' }, { type: 'text', text: 'two' }], raw_input: { prompt: 'go' } }), read(1, 'a1', 'r1'), read(1, 'a1', 'r2')]
    const { index: idx } = index(items)
    const trimmed = trimAgents(idx, 3)
    expect(trimmed.agents.a1!.items.map((it) => it.seq)).toEqual([3])
    expect(trimAgents(trimmed, 3)).toBe(trimmed)
    expect(trimmed.agents.a1!.launchSeq).toBe(1)
    expect(trimmed.agents.a1!.call).toMatchObject({ status: 'completed' })
    expect(trimmed.agents.a1!.call?.content).toBeUndefined()
    expect(trimmed.agents.a1!.callTruncated).toBe(true)
    expect(agentState(trimmed.agents.a1!)).toBe('done')
    expect(index([items[0]!], true, trimmed).index.agents.a1!.call?.content).toHaveLength(2)

    const whole = { ...items[0]!, tool_call: { ...items[0]!.tool_call!, title: 'whole' } }
    const replaced = replaceAgentItem(idx, whole)
    expect(replaced.agents.a1!.call?.title).toBe('whole')
    const wholeChild = { ...items[1]!, tool_call: { ...items[1]!.tool_call!, title: 'whole child' } }
    expect(replaceAgentItem(idx, wholeChild).agents.a1!.items[0]!.tool_call?.title).toBe('whole child')
    expect(replaceAgentItem(idx, say(1, 'assistant', 'main'))).toBe(idx)
  })

  it('describes an agent from its call', () => {
    const { index: idx } = index([
      launch(1, 'a', 'completed', { raw_input: { prompt: 'dig' }, content: [{ type: 'text', text: ' one ' }, { type: 'text', text: 'two' }] }),
      launch(1, 'w', 'completed', { tool_name: 'Workflow', subagent: undefined, title: 'flow', raw_input: { name: 'Audit', script: 'run()' } }),
      launch(1, 'f', 'failed', { subagent: undefined, raw_input: 'not an object' }),
    ])
    const { a, w, f } = idx.agents
    expect([agentLabel(a!), agentType(a!), agentPrompt(a!), agentReport(a!)]).toEqual(['Job a', 'Explore', 'dig', 'one \n\ntwo'])
    expect([agentLabel(w!), agentType(w!), agentPrompt(w!), agentReport(w!)]).toEqual(['Audit', 'Workflow', 'run()', undefined])
    expect([agentLabel(f!), agentPrompt(f!)]).toEqual(['Agent f', undefined])
  })

  it('labels activity and memoizes the agent turn per agent object', () => {
    expect(activityLabel(launch(1, 'n'))).toBe('Started Job n')
    expect(activityLabel(read(1, 'a', 'r', 'in_progress'))).toBe('Reading r')
    expect(activityLabel(read(1, 'a', 'r'))).toBe('Read r')
    expect(activityLabel(say(1, 'assistant', 'first\nsecond line\n\n'))).toBe('second line')
    expect(activityLabel(say(1, 'assistant', ' \n'))).toBeUndefined()
    expect(activityLabel(item('thought', 1, { message: { role: 'thought', message_id: 't', text: 'x' } }))).toBe('Thinking')
    expect(activityLabel(item('plan', 1, { plan: [] }))).toBe('Updated the plan')
    const agent = index([launch(1, 'a'), read(1, 'a', 'r')]).index.agents.a!
    expect(agentTurn(agent)).toBe(agentTurn(agent))
    expect(agentTurn(agent)).toMatchObject({ turn: 1, closed: false })
    expect(agentTurn({ ...agent })).not.toBe(agentTurn(agent))
  })

  it('keeps the shared empty index empty and returns the same index when nothing changes', () => {
    const used = index([launch(1, 'a'), item('turn_end', 1)])
    expect(used.index).not.toBe(noAgents)
    expect(noAgents.agents).toEqual({})
    expect(noAgents.roots).toEqual([])
    expect(noAgents.calls.size).toBe(0)
    expect(noAgents.endedTurns.size).toBe(0)
    expect(index([say(1, 'assistant', 'x')]).index).toBe(noAgents)
    expect(index([]).index).toBe(noAgents)
    expect(index([say(1, 'assistant', 'x')], false, used.index).index).toBe(used.index)
    const again = index([read(1, 'a', 'r1')], false, used.index)
    expect(again.index.agents).not.toBe(used.index.agents)
    expect(again.index.roots).toBe(used.index.roots)
  })

  it('marks an agent opened and loaded only once an older page reaches its opening snapshot', () => {
    const opening = launch(1, 'a1')
    const live = index([read(1, 'a1', 'r1'), read(1, 'a1', 'r2'), launch(1, 'a1', 'completed')])
    expect(live.index.agents.a1).toMatchObject({ opened: false, startedAt: undefined })
    expect(agentLoaded(live.index.agents.a1!, 1)).toBe(false)
    const paged = index([opening], true, live.index).index.agents.a1!
    expect(paged).toMatchObject({ opened: true, startedAt: opening.time })
    expect(agentLoaded(paged, 1)).toBe(true)
  })

  it('stops an agent whose turn ended mid-call until an own item arrives after the turn end', () => {
    const state = (items: SessionItem[]) => agentState(index(items).index.agents.a1!)
    expect(state([launch(1, 'a1'), item('turn_end', 1)])).toBe('stopped')
    expect(state([launch(1, 'a1'), read(1, 'a1', 'r1'), item('turn_end', 1)])).toBe('stopped')
    expect(state([launch(1, 'a1'), item('turn_end', 1), read(1, 'a1', 'r1')])).toBe('running')
  })

  it('keeps a background Agent launched after its ack, and running once it acts again', () => {
    const raw_input = { run_in_background: true, prompt: 'x' }
    const started = launch(1, 'a1', 'in_progress', { raw_input })
    const ack = launch(1, 'a1', 'completed', { raw_input, content: [{ type: 'text', text: 'Async agent launched successfully.' }] })
    const quiet = index([started, ack]).index.agents.a1!
    expect(agentState(quiet)).toBe('launched')
    expect(agentReport(quiet)).toBeUndefined()
    expect(agentState(index([started, ack, read(1, 'a1', 'r1')]).index.agents.a1!)).toBe('running')
  })

  it('shows a subagent whose launch row was trimmed at its first held item', () => {
    const items = [item('turn_start', 1), launch(1, 'a1'), launch(1, 'n1', 'in_progress', { parent_id: 'a1' }), read(1, 'n1', 'r0'), read(1, 'a1', 'r1'), read(1, 'a1', 'r2')]
    const { index: idx } = index(items)
    expect(turnExtras(idx, 0).get(1)?.orphans).toBeUndefined()
    const trimmed = trimAgents(idx, 5)
    expect(turnExtras(trimmed, 5).get(1)?.orphans).toEqual({ ids: ['a1'], at: items[4]!.time })
    expect(agentState(trimmed.agents.a1!)).toBe('running')
  })

  it('keeps one card when a later launch snapshot outlives the trim, and shows nested work whose launch was trimmed', () => {
    const items = [item('turn_start', 1), launch(1, 'a1'), launch(1, 'n1', 'in_progress', { parent_id: 'a1' }), read(1, 'a1', 'r1'), read(1, 'n1', 'r2'),
      launch(1, 'a1', 'completed')]
    const trimmed = trimAgents(index(items).index, 4)
    expect(turnExtras(trimmed, 4).get(1)?.orphans).toEqual({ ids: ['n1'], at: items[4]!.time })
  })

  it('reads a cut background launch as launched once its whole item arrives', () => {
    const cut = launch(1, 'a1', 'completed', { content: [{ type: 'text', text: 'Async agent launched successfully.' }] })
    const { index: idx } = index([{ ...cut, truncated: true }])
    expect(agentState(idx.agents.a1!)).toBe('done')
    const whole = { ...cut, truncated: true, tool_call: { ...cut.tool_call!, raw_input: { run_in_background: true } } }
    const replaced = replaceAgentItem(idx, whole).agents.a1!
    expect(agentState(replaced)).toBe('launched')
    expect(replaced.callTruncated).toBe(false)
  })

  it('moves an orphan card up when an older page brings an earlier item', () => {
    const later = [item('turn_start', 1), say(1, 'user', 'go')]
    const early = read(1, 'ghost', 'r1')
    const late = read(1, 'ghost', 'r2')
    const { index: idx, main } = index([...later, late])
    const turn = { turn: 1, items: main, closed: true }
    const before = withAgentRows(rowsOfTurn(turn), 1, turnExtras(idx, late.seq))
    expect(before.map((row) => row.kind)).toEqual(['user', 'subagents'])
    const older = { ...early, seq: 0, time: later[0]!.time }
    const after = withAgentRows(rowsOfTurn(turn), 1, turnExtras(index([older], true, idx).index, 0))
    expect(after.map((row) => row.kind)).toEqual(['subagents', 'user'])
  })

  it('counts a replay starting at a background ack as not opened', () => {
    const ack = launch(1, 'w', 'completed', { tool_name: 'Workflow', subagent: undefined, task: { id: 't1', status: 'running' } })
    const agent = index([ack]).index.agents.w!
    expect(agent.opened).toBe(false)
    expect(agent.startedAt).toBeUndefined()
    expect(agentLoaded(agent, 0)).toBe(false)
  })

  it('orders activity against a settle or turn end by seq when their times tie', () => {
    const raw_input = { run_in_background: true }
    const ack = launch(1, 'a1', 'completed', { raw_input })
    const child = { ...read(1, 'a1', 'r1'), time: ack.time }
    expect(agentState(index([launch(1, 'a1', 'in_progress', { raw_input }), ack, child]).index.agents.a1!)).toBe('running')
    const end = item('turn_end', 1)
    const busy = { ...read(1, 'b1', 'r2'), time: end.time }
    expect(agentState(index([launch(1, 'b1'), end, busy]).index.agents.b1!)).toBe('running')
    const before = read(1, 'c1', 'r3')
    expect(agentState(index([launch(1, 'c1'), before, { ...item('turn_end', 1), time: before.time }]).index.agents.c1!)).toBe('stopped')
  })

  it('collects a subagent whose launch is not loaded as an orphan until an older page delivers it', () => {
    const main = [item('turn_start', 1), say(1, 'user', 'go'), read(1, 'ghost', 'r1'), say(1, 'assistant', 'done'), item('turn_end', 1, { stop_reason: 'end_turn' })]
    const { index: idx, main: held } = index(main)
    expect(turnExtras(idx, 0).get(1)?.orphans?.ids).toEqual(['ghost'])
    const rows = withAgentRows(rowsOfTurn({ turn: 1, items: held, closed: true }), 1, turnExtras(idx, 0))
    expect(rows.map((row) => row.kind)).toEqual(['user', 'subagents', 'assistant', 'finished'])
    expect(rows[1]).toMatchObject({ id: 'orphans:1', ids: ['ghost'] })
    expect(turnExtras(index([launch(1, 'ghost')], true, idx).index, 0).get(1)?.orphans).toBeUndefined()
  })

  it('prefixes a subagent row ids so they never collide with the main turn', () => {
    const edit = tool(1, 'e1', 'edit', 'Edit a.go', 'completed', { parent_id: 'a1', diffs: [{ path: 'a.go', patch: '+x\n' }] })
    const agent = index([item('turn_start', 1), launch(1, 'a1'), edit]).index.agents.a1!
    const rows = rowsOfTurn(agentTurn(agent))
    const own = agentRows(agent, rows).map((row) => row.id)
    expect(own.every((id) => id.startsWith('agent:a1/'))).toBe(true)
    expect(agentRows(agent, rows)).toBe(agentRows(agent, rows))
  })

  it('keeps a trimmed agent running or done with its label', () => {
    const items = [launch(1, 'r', 'in_progress'), read(1, 'r', 'r1'), launch(1, 'd', 'completed', { content: [{ type: 'text', text: 'ok' }] }), read(1, 'd', 'r2')]
    const trimmed = trimAgents(index(items).index, 100).agents
    expect(trimmed.r).toMatchObject({ latest: undefined, callTruncated: true })
    expect(trimmed.r!.activeSeq).toBeDefined()
    expect([agentState(trimmed.r!), agentState(trimmed.d!)]).toEqual(['running', 'done'])
    expect([agentLabel(trimmed.r!), agentLabel(trimmed.d!)]).toEqual(['Job r', 'Job d'])
    expect(trimmed.d!.callTruncated).toBe(true)
  })

  it('treats a launch id reused in a later turn as a new subagent', () => {
    const first = launch(1, 'a1', 'completed')
    const items = [item('turn_start', 1), launch(1, 'a1'), first, item('turn_end', 1), item('turn_start', 2)]
    const again = launch(2, 'a1')
    const kid = read(2, 'a1', 'r1')
    const { index: idx, main } = index([...items, again, kid])
    const key = `a1@${again.seq}`
    expect(Object.keys(idx.agents).sort()).toEqual(['a1', key])
    expect(idx.agents.a1!.items).toEqual([])
    expect(idx.agents[key]!.items).toEqual([kid])
    expect(main.find((it) => it.seq === again.seq)?.tool_call?.id).toBe(key)
    expect(idx.roots).toEqual(['a1', key])
    const later = launch(2, 'a1', 'completed')
    expect(localItem(idx, later).tool_call?.id).toBe(key)
    expect(localItem(idx, first).tool_call?.id).toBe('a1')
  })
})
