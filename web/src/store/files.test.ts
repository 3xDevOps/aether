import { describe, expect, it } from 'vitest'
import { createRootStore } from '@/store'
import { filesKey } from '@/store/files'

describe('files cache', () => {
  it('stores entries and invalidates every cached run value', () => {
    const store = createRootStore()
    const runKey = filesKey('ws-1', 'run-1', 'src')
    const baseKey = filesKey('ws-1', '', '')
    store.getState().setTree(runKey, {
      entries: [{ name: 'main.go', kind: 'file', size: 12 }],
    })
    store.getState().setDocument(runKey, {
      content: 'package main\n',
      truncated: false,
      binary: false,
      size: 13,
    })
    store.getState().setTree(baseKey, { entries: [] })

    expect(store.getState().trees[runKey]?.entries[0].name).toBe('main.go')
    expect(store.getState().documents[runKey]?.content).toBe('package main\n')
    store.getState().invalidateRun('run-1')
    expect(store.getState().trees[runKey]).toBeUndefined()
    expect(store.getState().documents[runKey]).toBeUndefined()
    expect(store.getState().trees[baseKey]?.entries).toEqual([])
  })

  it('invalidates a run file cache when a diff snapshot lands unmounted', () => {
    const store = createRootStore()
    const runKey = filesKey('ws-1', 'run-1', 'src')
    store.getState().setTree(runKey, { entries: [] })
    store.getState().setDocument(runKey, {
      content: 'stale\n',
      truncated: false,
      binary: false,
      size: 6,
    })
    store.getState().setFileDiff(runKey, { patch: 'stale', truncated: false })

    store.getState().noteDiffSnapshot('run-1', { time: '2026-09-03T00:00:00Z', files: [] })

    const state = store.getState()
    expect(state.diffs['run-1']?.revision).toBe(1)
    expect(state.trees[runKey]).toBeUndefined()
    expect(state.documents[runKey]).toBeUndefined()
    expect(state.fileDiffs[runKey]).toBeUndefined()
  })
})
