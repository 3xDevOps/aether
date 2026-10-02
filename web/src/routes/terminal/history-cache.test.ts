import { IDBFactory, IDBKeyRange, IDBObjectStore } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TerminalHistoryParams, TerminalHistoryResult } from '@/lib/types'
import { getHistoryCache, type HistoryScope, type SavedHistoryView } from '@/routes/terminal/history-cache'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

const indexedDB = new IDBFactory()
let testIdentity = 0
let scope: HistoryScope

function archive(total: number) {
  const lines = Array.from({ length: total }, (_, index) => ({
    cursor: `row:${index}`,
    time: index,
    text: index % 5 === 0 ? 'repeated output' : `recorded line ${index}`,
  }))
  const terminalHistory = vi.fn(async (params: TerminalHistoryParams): Promise<TerminalHistoryResult> => {
    const end = params.before ? Number(params.before.split(':')[1]) : lines.length
    const start = Math.max(0, end - (params.limit ?? 200))
    return { lines: lines.slice(start, end), next_cursor: start ? `before:${start}` : undefined, has_more: start > 0 }
  })
  return { lines, terminalHistory }
}

function savedView(row = -197): SavedHistoryView {
  return {
    anchor: { row, offset: 7.25, left: 123.5 },
    screen: {
      rows: ['<span style="color: #ff0000;">current &lt;screen&gt;</span>', 'wide screen content'],
      cols: 160,
      viewportY: 11,
      baseY: 20,
      cellWidth: 7.2,
      cellHeight: 15.6,
      fontFamily: 'monospace',
      fontSize: 12,
      letterSpacing: 0,
    },
  }
}

beforeEach(() => {
  vi.stubGlobal('indexedDB', indexedDB)
  vi.stubGlobal('IDBKeyRange', IDBKeyRange)
  const record = toRecord(run())
  scope = { identityKey: `history-member:${++testIdentity}`, epoch: 0, runID: record.id, createdAt: record.created_at }
  useStore.setState({ identityKey: scope.identityKey, terminalCacheEpoch: 0, runs: { [record.id]: record }, hydrated: true })
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('retained terminal history', () => {
  it('reaches the archive beginning and revisits older disk pages without moving newer row anchors', async () => {
    const client = archive(2601)
    const cache = getHistoryCache(scope, client)
    await cache.loadOlder()
    const anchor = await cache.readRows(-197, -196)
    const view = savedView()
    await cache.saveView(view)
    client.lines.push({ cursor: 'new live row', time: 9999, text: 'written while browsing older output' })

    while (cache.snapshot().hasMore) await cache.loadOlder()

    expect(cache.snapshot().count).toBe(2601)
    expect(await cache.readRows(-2601, -2598)).toEqual(client.lines.slice(0, 3).map((line, index) => ({ index: index - 2601, line })))
    expect(await cache.readRows(-197, -196)).toEqual(anchor)
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[2600] }])
    expect(await cache.readView()).toEqual(view)
    expect(client.terminalHistory).toHaveBeenCalledTimes(14)
  })

  it('recovers all pages and the frozen pixel viewport in a fresh cache module', async () => {
    const client = archive(1801)
    const cache = getHistoryCache(scope, client)
    while (cache.snapshot().hasMore) await cache.loadOlder()
    const view = savedView(-1750)
    await cache.saveView(view)
    cache.cancel()
    // A static import would reuse resident state instead of exercising a browser reload.

    vi.resetModules()
    const { useStore: restoredStore } = await import('@/store')
    restoredStore.setState({ identityKey: scope.identityKey, terminalCacheEpoch: scope.epoch, runs: {}, hydrated: false })
    const { getHistoryCache: restoreCache } = await import('@/routes/terminal/history-cache')
    const network = { terminalHistory: vi.fn().mockRejectedValue(new Error('offline')) }
    const restored = restoreCache(scope, network)

    expect(await restored.readView()).toEqual(view)
    expect(await restored.readRows(-1801, -1800)).toEqual([{ index: -1801, line: client.lines[0] }])
    expect(await restored.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[1800] }])
    expect(network.terminalHistory).not.toHaveBeenCalled()
  })

  it('retries a failed boundary restore without overwriting saved pages, view, or continuation', async () => {
    const client = archive(2201)
    const cache = getHistoryCache(scope, client)
    for (let page = 0; page < 10; page++) await cache.loadOlder()
    const view = savedView(-1977)
    await cache.saveView(view)
    client.terminalHistory.mockClear()

    // A static import would reuse the hydrated instance instead of reading persisted boundary pages.
    vi.resetModules()
    const { useStore: restoredStore } = await import('@/store')
    restoredStore.setState({ identityKey: scope.identityKey, terminalCacheEpoch: scope.epoch, runs: {}, hydrated: false })
    const { getHistoryCache: restoreCache } = await import('@/routes/terminal/history-cache')
    const get = IDBObjectStore.prototype.get
    let unavailable = true
    vi.spyOn(IDBObjectStore.prototype, 'get').mockImplementation(function (this: IDBObjectStore, key) {
      if (this.name === 'pages' && unavailable) throw new DOMException('boundary page temporarily locked', 'UnknownError')
      return get.call(this, key)
    })
    const restored = restoreCache(scope, client)

    await expect(restored.readView()).rejects.toThrow('boundary page temporarily locked')
    expect(restored.snapshot().error).toContain('boundary page temporarily locked')
    expect(restored.snapshot().count).toBe(0)
    // Neither a route-disposal save nor a new reading episode may reinterpret
    // unread persisted data as a fresh empty archive.
    await restored.saveView(null)
    await expect(restored.resetArchive()).rejects.toThrow('boundary page temporarily locked')
    await restored.loadOlder()
    expect(client.terminalHistory).not.toHaveBeenCalled()

    unavailable = false
    expect(await restored.readRows(-2000, -1997)).toEqual(client.lines.slice(201, 204).map((line, index) => ({ index: index - 2000, line })))
    expect(restored.snapshot()).toMatchObject({ count: 2000, hasMore: true, error: null })
    expect(await restored.readView()).toEqual(view)
    expect(await restored.readRows(view.anchor.row, view.anchor.row + 1)).toEqual([{ index: view.anchor.row, line: client.lines[224] }])
    expect(await restored.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[2200] }])

    await restored.loadOlder()
    expect(client.terminalHistory.mock.calls[0][0].before).toBe('before:201')
    expect(restored.snapshot().count).toBe(2200)
    expect(await restored.readRows(-2200, -2197)).toEqual(client.lines.slice(1, 4).map((line, index) => ({ index: index - 2200, line })))
    expect(await restored.readRows(-2002, -1997)).toEqual(client.lines.slice(199, 204).map((line, index) => ({ index: index - 2002, line })))
    expect(await restored.readView()).toEqual(view)
  })

  it('retries saved-view and evicted-page reads independently after reaching the archive beginning', async () => {
    const client = archive(1801)
    const cache = getHistoryCache(scope, client)
    while (cache.snapshot().hasMore) await cache.loadOlder()
    const view = savedView(-1)
    await cache.saveView(view)
    client.terminalHistory.mockClear()
    const get = IDBObjectStore.prototype.get
    let failPage = true
    let failView = true
    vi.spyOn(IDBObjectStore.prototype, 'get').mockImplementation(function (this: IDBObjectStore, key) {
      if (this.name === 'pages' && failPage) throw new DOMException('evicted page temporarily locked', 'UnknownError')
      if (this.name === 'views' && failView) throw new DOMException('saved view temporarily locked', 'UnknownError')
      return get.call(this, key)
    })

    await expect(cache.readRows(-1, 0)).rejects.toThrow('evicted page temporarily locked')
    await expect(cache.readView()).rejects.toThrow('saved view temporarily locked')
    expect(cache.snapshot().hasMore).toBe(false)
    await cache.loadOlder()
    expect(client.terminalHistory).not.toHaveBeenCalled()
    failView = false
    expect(await cache.readView()).toEqual(view)
    expect(cache.snapshot().error).toContain('evicted page temporarily locked')
    failPage = false
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[1800] }])
    expect(cache.snapshot()).toMatchObject({ count: 1801, hasMore: false, error: null })
    expect(client.terminalHistory).not.toHaveBeenCalled()
  })

  it('starts a new reading episode at appended output without changing the saved pinned view', async () => {
    const client = archive(201)
    const cache = getHistoryCache(scope, client)
    await cache.loadOlder()
    const view = savedView()
    await cache.saveView(view)
    const newest = { cursor: 'after-live', time: 9999, text: 'new output after returning live' }
    client.lines.push(newest)
    await cache.resetArchive()
    expect(await cache.readRows(-1, 0)).toEqual([])
    expect(await cache.readView()).toEqual(view)
    await cache.loadOlder()
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line: newest }])
    while (cache.snapshot().hasMore) await cache.loadOlder()
    expect(await cache.readRows(-202, -201)).toEqual([{ index: -202, line: client.lines[0] }])
  })

  it('retries an aborted archive reset without reviving dismissed history after reload', async () => {
    const client = archive(401)
    const cache = getHistoryCache(scope, client)
    await cache.loadOlder()
    const view = savedView()
    await cache.saveView(view)
    const remove = IDBObjectStore.prototype.delete
    let unavailable = true
    vi.spyOn(IDBObjectStore.prototype, 'delete').mockImplementation(function (this: IDBObjectStore, key) {
      if (this.name === 'ranges' && unavailable) throw new DOMException('archive reset temporarily locked', 'UnknownError')
      return remove.call(this, key)
    })

    await expect(cache.resetArchive()).rejects.toThrow('archive reset temporarily locked')
    expect(cache.snapshot()).toMatchObject({ count: 200, hasMore: true })
    expect(cache.snapshot().error).toContain('archive reset temporarily locked')
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[400] }])
    expect(await cache.readView()).toEqual(view)

    unavailable = false
    const newest = { cursor: 'after-failed-reset', time: 9999, text: 'output after returning live' }
    client.lines.push(newest)
    await Promise.all([cache.resetArchive(), cache.saveView(null)])
    expect(cache.snapshot().error).toBeNull()
    // A static import would reuse the cache instead of exercising a reload.

    vi.resetModules()
    const { useStore: restoredStore } = await import('@/store')
    restoredStore.setState({ identityKey: scope.identityKey, terminalCacheEpoch: scope.epoch, runs: {}, hydrated: false })
    const { getHistoryCache: restoreCache } = await import('@/routes/terminal/history-cache')
    const restored = restoreCache(scope, client)
    expect(await restored.readView()).toBeNull()
    expect(await restored.readRows(-1, 0)).toEqual([])
    await restored.loadOlder()
    expect(await restored.readRows(-1, 0)).toEqual([{ index: -1, line: newest }])
  })

  it('keeps equal text with different cursors and follows short or empty progressing pages', async () => {
    const a = { cursor: 'a', time: 1, text: 'same text' }
    const b = { cursor: 'b', time: 2, text: 'same text' }
    const c = { cursor: 'c', time: 3, text: 'same text' }
    const terminalHistory = vi.fn()
      .mockResolvedValueOnce({ lines: [c, c], next_cursor: 'one', has_more: true })
      .mockResolvedValueOnce({ lines: [], next_cursor: 'two', has_more: true })
      .mockResolvedValueOnce({ lines: [b, c], next_cursor: 'three', has_more: true })
      .mockResolvedValueOnce({ lines: [a, b], has_more: false })
    const cache = getHistoryCache(scope, { terminalHistory })
    while (cache.snapshot().hasMore) await cache.loadOlder()
    expect(await cache.readRows(-3, 0)).toEqual([
      { index: -3, line: a }, { index: -2, line: b }, { index: -1, line: c },
    ])
    expect(terminalHistory.mock.calls.map(([params]) => params.before)).toEqual([undefined, 'one', 'two', 'three'])
  })

  it('deduplicates the oldest overlap across short pages and cold restore after resident eviction', async () => {
    const client = archive(2201)
    const cache = getHistoryCache(scope, client)
    for (let page = 0; page < 10; page++) await cache.loadOlder()
    client.terminalHistory.mockResolvedValueOnce({
      lines: client.lines.slice(199, 202), next_cursor: 'short', has_more: true,
    })
    await cache.loadOlder()
    client.terminalHistory.mockResolvedValueOnce({ lines: [], next_cursor: 'empty', has_more: true })
    await cache.loadOlder()
    const view = savedView(-2001)
    await cache.saveView(view)

    // Static imports reuse resident state instead of exercising a browser reload.
    vi.resetModules()
    const { useStore: restoredStore } = await import('@/store')
    restoredStore.setState({ identityKey: scope.identityKey, terminalCacheEpoch: scope.epoch, runs: {}, hydrated: false })
    const { getHistoryCache: restoreCache } = await import('@/routes/terminal/history-cache')
    const terminalHistory = vi.fn().mockResolvedValue({
      lines: client.lines.slice(198, 203), has_more: false,
    })
    const restored = restoreCache(scope, { terminalHistory })
    await restored.loadOlder()

    expect(terminalHistory.mock.calls[0][0].before).toBe('empty')
    expect(restored.snapshot().count).toBe(2003)
    expect(await restored.readRows(-2003, -1997)).toEqual(client.lines.slice(198, 204).map((line, index) => ({ index: index - 2003, line })))
    expect(await restored.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[2200] }])
    expect(await restored.readView()).toEqual(view)
  })

  it.each([
    ['unchanged', 'two'],
    ['cyclic', 'one'],
    ['missing', undefined],
  ])('stops a %s nonempty continuation without retrying or discarding saved rows', async (_kind, nextCursor) => {
    const line = { cursor: 'same-row', text: 'retained output', time: 0 }
    const terminalHistory = vi.fn()
      .mockResolvedValueOnce({ lines: [line], next_cursor: 'one', has_more: true })
      .mockResolvedValueOnce({ lines: [], next_cursor: 'two', has_more: true })
      .mockResolvedValueOnce({ lines: [line], next_cursor: nextCursor, has_more: true })
    const cache = getHistoryCache(scope, { terminalHistory })
    await cache.loadOlder()
    await cache.loadOlder()
    await cache.loadOlder()
    const error = cache.snapshot().error
    expect(error).toMatch(/continuation cursor/i)
    expect(cache.snapshot().hasMore).toBe(true)
    await cache.loadOlder()
    expect(terminalHistory).toHaveBeenCalledTimes(3)
    expect(cache.snapshot().error).toBe(error)
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line }])

    // Reload the module so the blocked continuation must come from disk.
    vi.resetModules()
    const { useStore: restoredStore } = await import('@/store')
    restoredStore.setState({ identityKey: scope.identityKey, terminalCacheEpoch: scope.epoch, runs: {}, hydrated: false })
    const { getHistoryCache: restoreCache } = await import('@/routes/terminal/history-cache')
    const restored = restoreCache(scope, { terminalHistory })
    await restored.loadOlder()
    expect(terminalHistory).toHaveBeenCalledTimes(3)
    expect(restored.snapshot().error).toBe(error)

    terminalHistory.mockResolvedValueOnce({ lines: [line], has_more: false })
    await restored.resetArchive()
    await restored.loadOlder()
    expect(terminalHistory).toHaveBeenCalledTimes(4)
    expect(restored.snapshot().error).toBeNull()
    expect(await restored.readRows(-1, 0)).toEqual([{ index: -1, line }])
  })

  it('paces timeout continuations without calling them errors and cancels an inactive retry', async () => {
    const terminalHistory = vi.fn()
      .mockResolvedValueOnce({ lines: [], next_cursor: 'cutoff', has_more: true })
      .mockResolvedValueOnce({ lines: [], next_cursor: 'cutoff', has_more: true })
      .mockResolvedValueOnce({ lines: [{ cursor: 'old', text: 'oldest output', time: 0 }], has_more: false })
    const cache = getHistoryCache(scope, { terminalHistory })
    await cache.loadOlder()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    await cache.loadOlder()
    expect(cache.snapshot().error).toBeNull()
    expect(cache.snapshot().hasMore).toBe(true)
    const waiting = cache.loadOlder()
    await vi.advanceTimersByTimeAsync(249)
    expect(terminalHistory).toHaveBeenCalledTimes(2)
    cache.cancel()
    await waiting
    await vi.advanceTimersByTimeAsync(1000)
    expect(terminalHistory).toHaveBeenCalledTimes(2)
    vi.useRealTimers()
    await cache.loadOlder()
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line: { cursor: 'old', text: 'oldest output', time: 0 } }])
    expect(terminalHistory.mock.calls[2][0].before).toBe('cutoff')
  })

  it('isolates late cancelled responses and permits a fresh request', async () => {
    const late = Promise.withResolvers<TerminalHistoryResult>()
    const terminalHistory = vi.fn()
      .mockImplementationOnce((_params: TerminalHistoryParams, _signal: AbortSignal) => late.promise)
      .mockResolvedValueOnce({ lines: [{ cursor: 'fresh', time: 2, text: 'fresh response' }], has_more: false })
    const cache = getHistoryCache(scope, { terminalHistory })
    const first = cache.loadOlder()
    await vi.waitFor(() => expect(terminalHistory).toHaveBeenCalledTimes(1))
    const observed: boolean[] = []
    const unsubscribe = cache.subscribe(() => observed.push(cache.snapshot().loading))
    cache.cancel()
    cache.cancel()
    expect(terminalHistory.mock.calls[0][1].aborted).toBe(true)
    expect(observed).toEqual([false])
    unsubscribe()
    await cache.loadOlder()
    late.resolve({ lines: [{ cursor: 'late', time: 1, text: 'must not replace current output' }], has_more: false })
    await first
    expect(await cache.readRows(-2, 0)).toEqual([{ index: -1, line: { cursor: 'fresh', time: 2, text: 'fresh response' } }])
  })

  it('commits an accepted page even if its subscriber immediately leaves the route', async () => {
    const client = archive(2001)
    const cache = getHistoryCache(scope, client)
    const unsubscribe = cache.subscribe(() => {
      if (cache.snapshot().count === 200) cache.cancel()
    })
    await cache.loadOlder()
    unsubscribe()
    while (cache.snapshot().hasMore) await cache.loadOlder()
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[2000] }])
  })

  it('retains session history and the viewport without IndexedDB', async () => {
    vi.stubGlobal('indexedDB', undefined)
    const client = archive(1801)
    const cache = getHistoryCache(scope, client)
    while (cache.snapshot().hasMore) await cache.loadOlder()
    const view = savedView(-1800)
    await cache.saveView(view)
    cache.cancel()
    const returning = getHistoryCache(scope, client)
    expect(await returning.readRows(-1801, -1800)).toEqual([{ index: -1801, line: client.lines[0] }])
    expect(await returning.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[1800] }])
    expect(await returning.readView()).toEqual(view)
    expect(returning.snapshot().error).toBeNull()
  })

  it('keeps readable disk pages and the new saved view after a write failure', async () => {
    const client = archive(2201)
    const cache = getHistoryCache(scope, client)
    for (let page = 0; page < 9; page++) await cache.loadOlder()
    await cache.saveView(savedView())
    const put = IDBObjectStore.prototype.put
    vi.spyOn(IDBObjectStore.prototype, 'put').mockImplementation(function (this: IDBObjectStore, value, key) {
      if (this.name === 'pages') throw new DOMException('history quota full', 'QuotaExceededError')
      return put.call(this, value, key)
    })
    await cache.loadOlder()
    expect(cache.snapshot().error).toContain('history quota full')
    const view = savedView(-1999)
    await cache.saveView(view)
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[2200] }])
    expect(await cache.readRows(-2000, -1999)).toEqual([{ index: -2000, line: client.lines[201] }])
    expect(await cache.readView()).toEqual(view)
    while (cache.snapshot().hasMore) await cache.loadOlder()
    expect(await cache.readRows(-2201, -2200)).toEqual([{ index: -2201, line: client.lines[0] }])
  })

  it('preserves the real RPC error and retries without losing retained rows', async () => {
    const client = archive(201)
    const cache = getHistoryCache(scope, client)
    await cache.loadOlder()
    client.terminalHistory.mockRejectedValueOnce(new Error('SSH backend: connection reset by peer'))
    await cache.loadOlder()
    expect(cache.snapshot().error).toBe('SSH backend: connection reset by peer')
    expect(await cache.readRows(-1, 0)).toEqual([{ index: -1, line: client.lines[200] }])
    await cache.loadOlder()
    expect(await cache.readRows(-201, -200)).toEqual([{ index: -201, line: client.lines[0] }])
    expect(cache.snapshot().error).toBeNull()
  })

  it('leaves storage alone during run status churn but removes replaced run history', async () => {
    const cache = getHistoryCache(scope, archive(1))
    await cache.loadOlder()
    const view = savedView(-1)
    await cache.saveView(view)
    const openCursor = vi.spyOn(IDBObjectStore.prototype, 'openCursor')
    const record = useStore.getState().runs[scope.runID]
    for (let update = 0; update < 10; update++) {
      useStore.setState({ runs: { [record.id]: { ...record, status: update % 2 ? 'running' : 'completed' } } })
    }
    expect(await cache.readView()).toEqual(view)
    expect(openCursor).not.toHaveBeenCalled()

    const replacement = { ...record, created_at: '2026-09-23T00:00:00Z' }
    useStore.setState({ runs: { [record.id]: replacement } })
    await cache.saveView(view)
    const replaced = getHistoryCache({ ...scope, createdAt: replacement.created_at })
    expect(await replaced.readView()).toBeNull()
    expect(await cache.readRows(-1, 0)).toEqual([])
    expect(openCursor).toHaveBeenCalled()

    useStore.setState({ runs: { [record.id]: record } })
    expect(await getHistoryCache(scope).readView()).toBeNull()
  })

  it('fences identity changes, epochs and authoritative deletion, including stale disposal saves', async () => {
    const client = archive(1)
    const cache = getHistoryCache(scope, client)
    await cache.loadOlder()
    await cache.saveView(savedView(-1))
    useStore.setState({ identityKey: 'another member' })
    await cache.saveView(savedView(-1))
    expect(await cache.readRows(-1, 0)).toEqual([])
    expect(await cache.readView()).toBeNull()

    useStore.setState({ identityKey: scope.identityKey })
    const returning = getHistoryCache(scope, client)
    expect(await returning.readView()).toBeNull()
    expect(await returning.readRows(-1, 0)).toEqual([])
    await returning.loadOlder()
    await returning.saveView(savedView(-1))
    useStore.getState().resetSeq()
    expect(await returning.readView()).toBeNull()

    const nextScope = { ...scope, epoch: useStore.getState().terminalCacheEpoch }
    const next = getHistoryCache(nextScope, client)
    await next.loadOlder()
    await next.saveView(savedView(-1))
    useStore.getState().removeRun(scope.runID)
    expect(await next.readView()).toBeNull()
    expect(await next.readRows(-1, 0)).toEqual([])
    useStore.getState().upsertRun(run())
    expect(await getHistoryCache(nextScope, client).readView()).toBeNull()
  })
})
