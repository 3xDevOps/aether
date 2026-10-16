import { describe, expect, it, vi } from 'vitest'
import { fakeApi, run } from '@/test/fixtures'
import { createRootStore } from '@/store'
import { readCheckpoints } from '@/store/diff'

const snapshot = (time: string, tree: string) => ({
  time,
  files: [{ path: 'a.go', additions: 3, deletions: 1 }],
  tree,
})

describe('checkpoints', () => {
  it('keep the numbers a menu row shows, not the file list', () => {
    const store = createRootStore()
    store.getState().noteDiffSnapshot('run-1', { ...snapshot('2026-08-14T10:00:00Z', 'tree1'), head: 'h1', headSubject: 'fix it' })

    expect(store.getState().diffs['run-1'].checkpoints).toEqual([
      { time: '2026-08-14T10:00:00Z', tree: 'tree1', head: 'h1', headSubject: 'fix it', files: 1, additions: 3, deletions: 1 },
    ])
  })

  it('merge history with live snapshots newest first, one entry per tree', async () => {
    const store = createRootStore()
    const client = fakeApi()
    const entry = (seq: number, time: string, tree: string) => ({
      seq,
      time,
      type: 'run.diff',
      payload: { files: [], tree },
    })
    vi.mocked(client.workspaceTimeline).mockResolvedValue({
      events: [entry(1, '2026-08-14T10:00:00Z', 'tree1'), entry(2, '2026-08-14T10:01:00Z', 'tree2')] as never,
      next_seq: 3,
      more: false,
    })
    store.getState().noteDiffSnapshot('run-1', snapshot('2026-08-14T10:01:00Z', 'tree2'))
    store.getState().noteDiffSnapshot('run-1', snapshot('2026-08-14T10:02:00Z', 'tree3'))

    await readCheckpoints(store, client, run({ id: 'run-1' }))

    expect(store.getState().diffs['run-1'].checkpoints.map((c) => c.tree)).toEqual(['tree3', 'tree2', 'tree1'])
  })

  it('keep the newest checkpoint for a tree the run returned to', () => {
    const store = createRootStore()
    store.getState().noteDiffSnapshot('run-1', snapshot('2026-08-14T10:00:00Z', 'treeA'))
    store.getState().noteDiffSnapshot('run-1', snapshot('2026-08-14T10:01:00Z', 'treeB'))
    store.getState().noteDiffSnapshot('run-1', { ...snapshot('2026-08-14T10:02:00Z', 'treeA'), head: 'h2', headSubject: 'revert it' })

    expect(store.getState().diffs['run-1'].checkpoints.map((c) => [c.tree, c.time, c.headSubject])).toEqual([
      ['treeA', '2026-08-14T10:02:00Z', 'revert it'],
      ['treeB', '2026-08-14T10:01:00Z', undefined],
    ])
  })

  it('page older history only while the timeline says there is more', async () => {
    const store = createRootStore()
    const client = fakeApi()
    vi.mocked(client.workspaceTimeline).mockResolvedValue({ events: [], next_seq: 1, more: false })

    await readCheckpoints(store, client, run({ id: 'run-1' }))
    await readCheckpoints(store, client, run({ id: 'run-1' }), true)

    expect(client.workspaceTimeline).toHaveBeenCalledTimes(1)
  })
})
