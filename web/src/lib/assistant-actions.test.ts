import { aetherTool, assistantView, readSummary, type AssistantAction } from '@/lib/assistant-actions'
import type { AssistantActionRecord } from '@/lib/assistant-types'
import type { SessionRow } from '@/store/session-rows'
import { stepsBlock, type WorkItem } from '@/store/session-steps'

const at = '2026-10-26T10:00:00Z'

function block(...entries: Partial<WorkItem>[]): SessionRow {
  return blockOf('b1', ...entries)
}

function blockOf(id: string, ...entries: Partial<WorkItem>[]): SessionRow {
  return stepsBlock(id, entries.map((entry, i) => {
    const full: WorkItem = { id: `1:t${i}`, tool: 'other', label: '', status: 'done', at, ...entry }
    return { kind: 'tool' as const, id: full.id, at, entry: full }
  }), at)
}

describe('aetherTool', () => {
  it.each([
    ['mcp__aether__aether_launch_run', 'aether_launch_run'],
    ['aether.aether_pr_merge', 'aether_pr_merge'],
    ['aether_get_run', 'aether_get_run'],
    ['mcp__tracker__get', undefined],
    ['Bash', undefined],
  ])('reads %s as %s', (name, tool) => expect(aetherTool(name)).toBe(tool))
})

describe('assistantView', () => {
  it("prefers the server's action record, matched by method and run, over the tool call's status", () => {
    const stop = { id: '1:t0', name: 'mcp__aether__aether_stop_run', status: 'failed' as const, input: { run_id: 'r1' }, result: 'run.kill was not approved: the turn or session ended before the member answered' }
    const message = { id: '1:t1', name: 'mcp__aether__aether_message_run', status: 'running' as const, input: { run_id: 'r2' } }
    const waiting = [{ id: 'c', method: 'run.inject', summary: '', params: {}, created_at: at, expires_at: at }]
    expect(assistantView([block(stop, message)], waiting).actions.map((a) => [a.state, a.approval])).toEqual([['withdrawn', undefined], ['approval', undefined]])

    const notice = (seq: number, action: AssistantActionRecord): SessionRow => ({ kind: 'event', id: `ev:${seq}`, at, text: action.summary })
    const record = (over: Partial<AssistantActionRecord>): AssistantActionRecord => ({ id: 'a1', method: 'run.inject', summary: 'Message', target: {}, outcome: 'approved', created_at: at, ...over })
    const records = new Map([
      ['ev:7', record({ id: 'a0', method: 'run.kill', summary: 'Withdrawn: stop', outcome: 'withdrawn', error: 'the turn or session ended before the member answered', target: { run_id: 'r1' } })],
      ['ev:8', record({ id: 'a9', summary: 'Approved: message r9', approval: 'card', target: { run_id: 'r9' } })],
      ['ev:9', record({ summary: 'Auto-approved: message r2', outcome: 'auto_approved', approval: 'auto', target: { run_id: 'r2' } })],
      ['ev:10', record({ summary: 'Done: message r2', outcome: 'succeeded', approval: 'auto', target: { run_id: 'r2' } })],
    ])
    const view = assistantView([block(stop, message), ...[...records].map(([id, r]) => notice(Number(id.slice(3)), r))], waiting, { records, calls: new Map() })
    expect(view.actions.map((a) => [a.state, a.approval, a.error])).toEqual([
      ['withdrawn', undefined, 'the turn or session ended before the member answered'],
      ['done', 'auto-approved', undefined],
    ])
    expect(view.rows.map((row) => row.id)).toEqual(['ev:8'])
  })

  it('keeps a block that holds no Aether call as it was, and drops one that held only calls', () => {
    const plain = block({ name: 'Bash', tool: 'execute' })
    expect(assistantView([plain], []).rows).toEqual([plain])
    const view = assistantView([{ kind: 'user', id: 'u1', at, body: 'go' }, block({ name: 'aether_get_run' })], [])
    expect(view.rows.map((row) => row.id)).toEqual(['u1'])
    expect([...view.groups.keys()]).toEqual(['u1'])
    expect(view.actions).toEqual([])
  })

  it('names a read tool added later from its own name', () => {
    const reads = [{ tool: 'aether_list_tickets', state: 'done' }, { tool: 'aether_list_tickets', state: 'running' }] as AssistantAction[]
    expect(readSummary(reads)).toBe('Reading 2 tickets')
  })

  it('keeps each card where its call ran in the block', () => {
    const read: WorkItem = { id: '1:r', tool: 'other', label: '', status: 'done', at, name: 'aether_get_run' }
    const bash: WorkItem = { id: '1:x', tool: 'execute', label: 'Ran npm test', status: 'done', at, name: 'Bash' }
    const b = stepsBlock('b1', [
      { kind: 'tool', id: read.id, at, entry: read },
      { kind: 'narration', id: 'n1', at, text: 'It is stuck on CI.', streaming: false },
      { kind: 'tool', id: bash.id, at, entry: bash },
    ], at)
    const view = assistantView([{ kind: 'user', id: 'u1', at, body: 'why?' }, b], [])
    expect(view.rows.map((row) => row.id)).toEqual(['u1', 'b1:1:r'])
    expect(view.groups.get('u1')?.map((g) => g.id)).toEqual(['reads:1:r'])
  })

  it("gives an older server's approval note only to the call its words name", () => {
    const message = (id: string, run: string): Partial<WorkItem> => ({ id, name: 'aether_message_run', status: 'running', input: { run_id: run } })
    const note = (seq: number, title: string): SessionRow => ({ kind: 'event', id: `ev:${seq}`, at, text: title, detail: 'run.inject passed after you approved its card.' })
    const waiting = [{ id: 'c', method: 'run.inject', summary: '', params: {}, created_at: at, expires_at: at }]
    const view = assistantView([block(message('1:a', 'run-a'), message('1:b', 'run-b')), note(1, 'Approved: Message a run'), note(2, 'Approved: Message run run-a')], waiting)
    expect(view.actions.map((a) => [a.input.run_id, a.state, a.approval])).toEqual([['run-a', 'running', 'approved'], ['run-b', 'approval', undefined]])
    expect(view.rows.map((row) => row.id)).toEqual(['ev:1'])
  })

  describe('action records', () => {
    const record = (over: Partial<AssistantActionRecord>): AssistantActionRecord => ({ id: 'a1', method: 'run.inject', summary: 'Message', target: {}, outcome: 'approved', approval: 'card', created_at: at, ...over })
    const notices = (from: number, ...list: AssistantActionRecord[]) => {
      const records = new Map(list.map((r, i) => [`ev:${from + i}`, r]))
      const rows = [...records].map(([id, r]): SessionRow => ({ kind: 'event', id, at, text: r.summary }))
      return { records, rows }
    }

    it('gives identical calls of one block their records in the order they ran', () => {
      const message = (id: string, status: WorkItem['status'], result?: string): Partial<WorkItem> => ({ id, name: 'aether_message_run', status, input: { run_id: 'r1', message: 'go' }, result })
      const { records, rows } = notices(1,
        record({ id: 'a1', target: { run_id: 'r1' } }),
        record({ id: 'a2', target: { run_id: 'r1' } }),
        record({ id: 'a1', target: { run_id: 'r1' }, outcome: 'failed', error: 'run.inject: run r1 is not running' }),
        record({ id: 'a2', target: { run_id: 'r1' }, outcome: 'succeeded' }),
      )
      const view = assistantView([block(message('1:t0', 'failed', 'run.inject: run r1 is not running'), message('1:t1', 'done')), ...rows], [], { records, calls: new Map() })
      expect(view.actions.map((a) => [a.id, a.state, a.error])).toEqual([
        ['1:t0', 'failed', 'run.inject: run r1 is not running'],
        ['1:t1', 'done', undefined],
      ])
      expect(view.rows).toEqual([])
    })

    it('gives a launch record, which names no run, to the newest launch on its workspace past an unrecorded older one', () => {
      const launch = (id: string, workspace: string): Partial<WorkItem> => ({ id, name: 'aether_launch_run', status: 'done', input: { workspace_id: workspace, task: 'x' } })
      const { records, rows } = notices(5,
        record({ method: 'run.launch', target: { workspace_id: 'ws1' } }),
        record({ method: 'run.launch', target: { workspace_id: 'ws1' }, outcome: 'succeeded', result: { workspace_id: 'ws1', run_id: 'run_new' } }),
      )
      const view = assistantView([blockOf('b1', launch('1:old', 'ws1')), blockOf('b2', launch('2:other', 'ws2'), launch('2:new', 'ws1')), ...rows], [], { records, calls: new Map() })
      expect(view.actions.map((a) => [a.id, a.approval, a.target?.id])).toEqual([
        ['1:old', undefined, 'ws1'],
        ['2:other', undefined, 'ws2'],
        ['2:new', 'approved', 'run_new'],
      ])
      expect(view.rows).toEqual([])
    })

    it('says an approved call the turn ended under did not finish, and keeps how it was approved', () => {
      const stop = { id: '1:t0', name: 'aether_stop_run', status: 'done' as const, input: { run_id: 'r1' } }
      const save = { id: '1:t1', name: 'aether_save_environment', status: 'done' as const }
      const call = (id: string) => ({ id, title: '', status: 'in_progress' })
      const { records, rows } = notices(1,
        record({ method: 'run.kill', target: { run_id: 'r1' } }),
        record({ id: 'a2', method: 'env.save', outcome: 'auto_approved', approval: 'auto' }),
      )
      const view = assistantView([block(stop, save), ...rows], [], { records, calls: new Map([['1:t0', call('t0')], ['1:t1', call('t1')]]) })
      expect(view.actions.map((a) => [a.state, a.approval])).toEqual([['unfinished', 'approved'], ['unfinished', 'auto-approved']])
    })
  })
})
