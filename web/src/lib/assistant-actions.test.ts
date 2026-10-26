import { aetherTool, assistantView, readSummary, type AssistantAction } from '@/lib/assistant-actions'
import type { AssistantActionRecord } from '@/lib/assistant-types'
import type { SessionRow } from '@/store/session-rows'
import { stepsBlock, type WorkItem } from '@/store/session-steps'

const at = '2026-10-26T10:00:00Z'

function block(...entries: Partial<WorkItem>[]): SessionRow {
  return stepsBlock('b1', entries.map((entry, i) => {
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
    expect(assistantView([block(stop, message)], waiting).actions.map((a) => [a.state, a.approval])).toEqual([['failed', undefined], ['approval', undefined]])

    const notice = (seq: number, action: AssistantActionRecord): SessionRow => ({ kind: 'event', id: `ev:${seq}`, at, text: action.summary })
    const record = (over: Partial<AssistantActionRecord>): AssistantActionRecord => ({ id: 'a1', method: 'run.inject', summary: 'Message', outcome: 'approved', created_at: at, ...over })
    const records = new Map([
      ['ev:7', record({ id: 'a0', method: 'run.kill', summary: 'Withdrawn: stop', outcome: 'withdrawn', error: 'the turn or session ended before the member answered', target: { run_id: 'r1' } })],
      ['ev:8', record({ id: 'a9', summary: 'Approved: message r9', approval: 'card', target: { run_id: 'r9' } })],
      ['ev:9', record({ summary: 'Auto-approved: message r2', outcome: 'auto_approved', approval: 'auto', target: { run_id: 'r2' } })],
      ['ev:10', record({ summary: 'Done: message r2', outcome: 'succeeded', approval: 'auto', target: { run_id: 'r2' } })],
    ])
    const view = assistantView([block(stop, message), ...[...records].map(([id, r]) => notice(Number(id.slice(3)), r))], waiting, records)
    expect(view.actions.map((a) => [a.state, a.approval, a.error])).toEqual([
      ['failed', undefined, 'the turn or session ended before the member answered'],
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
})
