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
})
