import { describe, expect, it } from 'vitest'
import { createRootStore } from '@/store'
import { filesKey, homeKey } from '@/store/files'

const document = {
  content: 'package main\n',
  truncated: false,
  binary: false,
  size: 13,
  revision: 'r1',
  writable: true,
}

describe('files cache', () => {
  it('stores entries and invalidates every cached run value', () => {
    const store = createRootStore()
    const runKey = filesKey('ws-1', 'run-1', 'src')
    const baseKey = filesKey('ws-1', '', '')
    store.getState().setTree(runKey, {
      entries: [{ name: 'main.go', kind: 'file', size: 12 }],
    })
    store.getState().setDocument(runKey, document)
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
      revision: 'stale',
      writable: true,
    })
    store.getState().setFileDiff(runKey, { patch: 'stale', truncated: false })

    store.getState().noteDiffSnapshot('run-1', { time: '2026-09-03T00:00:00Z', files: [] })

    const state = store.getState()
    expect(state.diffs['run-1']?.revision).toBe(1)
    expect(state.trees[runKey]).toBeUndefined()
    expect(state.documents[runKey]).toBeUndefined()
    expect(state.fileDiffs[runKey]).toBeUndefined()
  })

  it('keeps edits typed while a captured save is in flight', () => {
    const store = createRootStore()
    const key = filesKey('ws-1', 'run-1', 'settings.json')
    store.getState().setDocument(key, { ...document, content: '{"old":true}\n', revision: 'r1', size: 13 })
    store.getState().updateDraft(key, '{"old":false}\n')
    store.getState().updateDraft(key, '{"new":true}\n')

    store.getState().markDraftSaved(key, '{"old":false}\n', {
      content: '{"old":false}\n',
      truncated: false,
      binary: false,
      size: 14,
      revision: 'r2',
      writable: true,
    })

    const draft = store.getState().drafts[key]
    expect(draft?.content).toBe('{"new":true}\n')
    expect(draft?.baseContent).toBe('{"old":false}\n')
    expect(draft?.baseRevision).toBe('r2')
  })
  it('preserves an undo back to the original while that save is pending', () => {
    const store = createRootStore()
    const key = filesKey('ws-1', 'run-1', 'settings.json')
    store.getState().setDocument(key, { ...document, content: 'A\n', revision: 'r1', size: 2 })
    store.getState().updateDraft(key, 'B\n')
    store.getState().setDraft(key, { ...store.getState().drafts[key]!, content: 'B\n', saving: true })
    store.getState().updateDraft(key, 'A\n')
    store.getState().markDraftSaved(key, 'B\n', {
      content: 'B\n',
      truncated: false,
      binary: false,
      size: 2,
      revision: 'r2',
      writable: true,
    })

    const draft = store.getState().drafts[key]
    expect(draft?.content).toBe('A\n')
    expect(draft?.baseContent).toBe('B\n')
    expect(draft?.baseRevision).toBe('r2')
    expect(draft?.saving).toBe(false)
  })

  it('keeps dirty drafts when a live run cache is invalidated', () => {
    const store = createRootStore()
    const key = filesKey('ws-1', 'run-1', 'settings.json')
    store.getState().setDocument(key, document)
    store.getState().updateDraft(key, 'changed\n')
    store.getState().invalidateRun('run-1')

    expect(store.getState().drafts[key]?.content).toBe('changed\n')
  })

  it('carries a moved home folder and drops what is left of a deleted one', () => {
    const store = createRootStore()
    const s = store.getState
    const tab = (path: string) => ({ key: homeKey(path), kind: 'home' as const, path, label: 'Home' })
    const workspace = filesKey('ws-1', '', 'notes/a.md')
    for (const path of ['notes/a.md', 'notes/sub/b.md', 'notebook.md', 'archive/a.md']) {
      s().setDocument(homeKey(path), { ...document, content: path })
      s().openFileTab(tab(path))
    }
    s().setDocument(workspace, document)
    s().updateDraft(homeKey('notes/sub/b.md'), 'draft\n')
    s().setTree(homeKey('notes/sub'), { entries: [{ name: 'b.md', kind: 'file', size: 1 }] })
    s().setActiveFileKey(homeKey('notes/sub/b.md'))

    // archive/a.md is a leftover of a path deleted elsewhere: the move lands on it.
    s().moveHomePath('notes', 'archive')
    expect(s().fileTabs.map((open) => open.path)).toEqual(['archive/a.md', 'archive/sub/b.md', 'notebook.md'])
    expect(s().activeFileKey).toBe(homeKey('archive/sub/b.md'))
    expect(s().documents[homeKey('archive/a.md')]?.content).toBe('notes/a.md')
    expect(s().drafts[homeKey('archive/sub/b.md')]?.content).toBe('draft\n')
    expect(s().trees[homeKey('archive/sub')]?.entries).toHaveLength(1)
    expect(s().documents[homeKey('notes/a.md')]).toBeUndefined()
    expect(s().documents[workspace]).toBeDefined()

    s().dropHomePath('archive')
    expect(s().fileTabs.map((open) => open.path)).toEqual(['notebook.md'])
    expect(s().activeFileKey).toBe(homeKey('notebook.md'))
    expect(s().drafts).toEqual({})
    expect(Object.keys(s().documents).sort()).toEqual([workspace, homeKey('notebook.md')].sort())

    s().setTree(homeKey(''), { entries: [] })
    s().setTree(filesKey('ws-1', '', ''), { entries: [] })
    s().refreshHome()
    expect(Object.keys(s().trees)).toEqual([filesKey('ws-1', '', '')])
  })

  it('does not carry a request in flight to the new path', () => {
    const s = createRootStore().getState
    s().setTree(homeKey('notes'), { entries: [], loading: true })
    s().setDocument(homeKey('notes/a.md'), { content: '', truncated: false, binary: false, size: 0, revision: '', writable: false, loading: true })
    s().setDraft(homeKey('notes/b.md'), { content: 'draft\n', saving: true })

    s().moveHomePath('notes', 'archive')
    // Their answers arrive for the old keys, so each reader asks again.
    expect(s().trees).toEqual({})
    expect(s().documents).toEqual({})
    expect(s().drafts[homeKey('archive/b.md')]).toMatchObject({ content: 'draft\n', saving: false })
  })
})
