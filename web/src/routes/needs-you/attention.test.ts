import { describe, expect, it } from 'vitest'
import type { AttentionItem } from '@/lib/types'
import { byAttention } from '@/routes/needs-you/attention'
import type { RunTree, SidebarGroup } from '@/store/selectors'

const tree = (id: string) => ({ run: { id }, children: [] }) as unknown as RunTree
const item = (run_id: string, kind: AttentionItem['kind']): AttentionItem =>
  ({ kind, run_id, workspace_id: 'ws', title: run_id, harness: 'claude', reason: '' })

describe('byAttention', () => {
  it('counts and orders Needs you as the server does, and moves a finished session to review', () => {
    const groups: SidebarGroup[] = [
      { key: 'needs-you', label: 'Needs you', runs: [tree('failed'), tree('done'), tree('asks')], count: 3 },
      { key: 'working', label: 'Working', runs: [tree('busy')], count: 1 },
    ]
    const regrouped = byAttention(groups, [item('failed', 'failed'), item('done', 'ready'), item('asks', 'permission')])
    expect(regrouped.map((group) => [group.key, group.count, group.runs.map((row) => row.run.id)])).toEqual([
      ['needs-you', 2, ['asks', 'failed']],
      ['review', 1, ['done']],
      ['working', 1, ['busy']],
    ])
  })

  it('leaves the groups alone until the list loads', () => {
    const groups: SidebarGroup[] = [{ key: 'needs-you', label: 'Needs you', runs: [tree('a')], count: 1 }]
    expect(byAttention(groups, null)).toBe(groups)
  })
})
