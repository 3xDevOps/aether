import type { FrozenTerminal } from '@/components/terminal-presentation'
import { api, type Api } from '@/lib/api'
import type { TerminalHistoryLine } from '@/lib/types'
import { useStore, type RootState } from '@/store'

export interface HistoryScope {
  identityKey: string | null
  epoch: number
  runID: string
  createdAt: string
}

export interface HistoryAnchor {
  row: number
  offset: number
  left: number
}

export interface SavedHistoryView {
  screen: FrozenTerminal
  anchor: HistoryAnchor
}

interface HistorySnapshot {
  count: number
  hasMore: boolean
  loading: boolean
  error: string | null
  revision: number
}

export interface HistoryCache {
  readView(): Promise<SavedHistoryView | null>
  saveView(view: SavedHistoryView | null): Promise<void>
  snapshot(): HistorySnapshot
  subscribe(listener: () => void): () => void
  loadOlder(): Promise<void>
  resetArchive(): Promise<void>
  readRows(start: number, end: number): Promise<Array<{ index: number; line: TerminalHistoryLine }>>
  cancel(): void
}

interface Header {
  key: string
  scope: HistoryScope
  count: number
  hasMore: boolean
  nextCursor?: string
  continuationError?: string
}

interface PageRange {
  id: [string, number]
  start: number
  end: number
  before?: string
}

interface StoredPage {
  id: [string, number]
  lines: TerminalHistoryLine[]
}

const pageSize = 200
const residentPageLimit = 8
const caches = new Map<string, RunHistoryCache>()
const resident = new Map<string, TerminalHistoryLine[]>()
let databasePromise: Promise<IDBDatabase> | undefined
let storageTail: Promise<unknown> = Promise.resolve()
let subscribed = false

const scopeKey = (scope: HistoryScope) =>
  JSON.stringify([scope.identityKey, scope.epoch, scope.runID, scope.createdAt])
const pageKey = (key: string, id: number) => JSON.stringify([key, id])
const pageKeys = (key: string) => IDBKeyRange.bound([key, 0], [key, Number.MAX_SAFE_INTEGER])
const message = (cause: unknown) => cause instanceof Error ? cause.message : String(cause)

function queueStorage<T>(operation: () => Promise<T>): Promise<T> {
  const result = storageTail.then(operation)
  storageTail = result.catch(() => undefined)
  return result
}

function database(): Promise<IDBDatabase | null> {
  if (typeof indexedDB === 'undefined') return Promise.resolve(null)
  if (!databasePromise) {
    const { promise, resolve, reject } = Promise.withResolvers<IDBDatabase>()
    const request = indexedDB.open('aether-terminal-history', 1)
    let blocked = false
    request.onupgradeneeded = () => {
      const db = request.result
      db.createObjectStore('histories', { keyPath: 'key' })
      db.createObjectStore('ranges', { keyPath: 'id' })
      db.createObjectStore('pages', { keyPath: 'id' })
      db.createObjectStore('views', { keyPath: 'key' })
    }
    request.onsuccess = () => {
      const db = request.result
      if (blocked) {
        db.close()
        return
      }
      db.onversionchange = () => {
        db.close()
        databasePromise = undefined
      }
      resolve(db)
    }
    request.onerror = () => reject(request.error ?? new Error('Could not open terminal history storage.'))
    request.onblocked = () => {
      blocked = true
      reject(new Error('Terminal history storage is blocked by another browser tab.'))
    }
    databasePromise = promise.catch((cause: unknown) => {
      databasePromise = undefined
      throw cause
    })
  }
  return databasePromise
}

function transactionDone(transaction: IDBTransaction): Promise<void> {
  const { promise, resolve, reject } = Promise.withResolvers<void>()
  transaction.oncomplete = () => resolve()
  transaction.onabort = () => reject(transaction.error ?? new Error('Terminal history storage transaction aborted.'))
  transaction.onerror = (event) => reject((event.target as IDBRequest).error ?? transaction.error ?? new Error('Terminal history storage transaction failed.'))
  return promise
}

async function writeTransaction(db: IDBDatabase, stores: string[], write: (transaction: IDBTransaction) => void) {
  const transaction = db.transaction(stores, 'readwrite')
  const done = transactionDone(transaction)
  try {
    write(transaction)
  } catch (cause) {
    transaction.abort()
    await done.catch(() => undefined)
    throw cause
  }
  await done
}

function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  const { promise, resolve, reject } = Promise.withResolvers<T>()
  request.onsuccess = () => resolve(request.result)
  request.onerror = () => reject(request.error ?? new Error('Could not read terminal history storage.'))
  return promise
}

function remember(key: string, id: number, lines: TerminalHistoryLine[]) {
  const entry = pageKey(key, id)
  resident.delete(entry)
  resident.set(entry, lines)
  while (resident.size > residentPageLimit) resident.delete(resident.keys().next().value!)
}

function allowed(scope: HistoryScope, state = useStore.getState()): boolean {
  if (scope.identityKey === null || scope.identityKey !== state.identityKey || scope.epoch !== state.terminalCacheEpoch) return false
  const run = state.runs[scope.runID]
  return run ? run.created_at === scope.createdAt : !state.hydrated
}

function synchronize(state: RootState, previous?: RootState) {
  const removed = new Set<string>()
  if (previous && previous.runs !== state.runs) {
    for (const run of Object.values(previous.runs)) {
      if (!state.runs[run.id]) removed.add(JSON.stringify([run.id, run.created_at]))
    }
  }
  const retain = (scope: HistoryScope) => {
    // The first empty snapshot is not an authoritative list of deleted runs.
    if (state.identityKey === null && !previous) return true
    return allowed(scope, state) && !removed.has(JSON.stringify([scope.runID, scope.createdAt]))
  }
  for (const [key, cache] of caches) {
    if (retain(cache.scope)) continue
    cache.invalidate()
    caches.delete(key)
  }
  void queueStorage(async () => {
    const db = await database()
    if (!db) return
    const transaction = db.transaction(['histories', 'ranges', 'pages', 'views'], 'readwrite')
    const done = transactionDone(transaction)
    const request = transaction.objectStore('histories').openCursor()
    request.onsuccess = () => {
      const cursor = request.result
      if (!cursor) return
      const header = cursor.value as Header
      if (!retain(header.scope)) {
        cursor.delete()
        transaction.objectStore('ranges').delete(pageKeys(header.key))
        transaction.objectStore('pages').delete(pageKeys(header.key))
        transaction.objectStore('views').delete(header.key)
      }
      cursor.continue()
    }
    await done
  }).catch((cause: unknown) => {
    for (const cache of caches.values()) cache.storageError(cause)
  })
}

function membershipChanged(state: RootState, previous: RootState): boolean {
  if (state.runs === previous.runs) return false
  const runs = Object.keys(state.runs)
  return runs.length !== Object.keys(previous.runs).length
    || runs.some((id) => state.runs[id].created_at !== previous.runs[id]?.created_at)
}

function initializeSubscription() {
  if (subscribed) return
  subscribed = true
  useStore.subscribe((state, previous) => {
    if (state.identityKey !== previous.identityKey || state.terminalCacheEpoch !== previous.terminalCacheEpoch || state.hydrated !== previous.hydrated || membershipChanged(state, previous)) {
      synchronize(state, previous)
    }
  })
  synchronize(useStore.getState())
}

class RunHistoryCache implements HistoryCache {
  private state: HistorySnapshot = { count: 0, hasMore: true, loading: false, error: null, revision: 0 }
  private listeners = new Set<() => void>()
  private ranges: PageRange[] = []
  private boundaryCursors: string[] = []
  private continuations = new Set<string>()
  private memoryPages = new Map<number, TerminalHistoryLine[]>()
  private memoryView: SavedHistoryView | null = null
  private hasMemoryView = false
  private memoryOnly = false
  private invalid = false
  private nextCursor: string | undefined
  private continuationError: string | undefined
  private controller: AbortController | null = null
  private pending: Promise<void> | null = null
  private sequence = 0
  private archiveGeneration = 0
  private resetting: Promise<void> | null = null
  private retryDelay = 0
  private retryAt = 0
  private initialized: Promise<void> | null = null
  private storageReadStarted = false
  private storageErrors = new Map<string, string>()

  constructor(readonly scope: HistoryScope, private key: string, private client: Pick<Api, 'terminalHistory'>) {
    this.invalid = !allowed(scope)
    if (this.invalid) this.state.hasMore = false
    void this.ensureInitialized()
  }

  private current() {
    return !this.invalid && allowed(this.scope)
  }

  private patch(change: Partial<HistorySnapshot>) {
    if (Object.entries(change).every(([key, value]) => this.state[key as keyof HistorySnapshot] === value)) return
    this.state = { ...this.state, ...change }
    for (const listener of this.listeners) listener()
  }

  snapshot = () => this.state

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }

  storageError(cause: unknown, source = 'write') {
    if (!this.current()) return
    const error = `Terminal history storage: ${message(cause)}`
    this.storageErrors.delete(source)
    this.storageErrors.set(source, error)
    this.patch({ error })
  }

  private storageErrorMessage() {
    let latest: string | undefined
    for (const error of this.storageErrors.values()) latest = error
    return latest
  }

  private clearStorageError(source: string) {
    const error = this.storageErrors.get(source)
    this.storageErrors.delete(source)
    if (error && this.state.error === error) {
      this.patch({ error: this.storageErrorMessage() ?? this.continuationError ?? null })
    }
  }

  private ensureInitialized(): Promise<void> {
    if (!this.initialized) {
      const operation = this.initialize()
      this.initialized = operation
      // Construction starts hydration without a caller to handle rejection.
      // Keep the failure observable to readers while allowing the next call to retry.
      void operation.catch(() => {
        if (this.initialized === operation) this.initialized = null
      })
    }
    return this.initialized
  }

  private async initialize() {
    if (!this.current()) return
    const generation = this.archiveGeneration
    try {
      await storageTail
      if (!this.current()) return
      const db = await database()
      if (!this.current()) return
      if (!db) {
        if (this.storageReadStarted) throw new Error('Previously saved terminal history is unavailable because IndexedDB is unavailable.')
        this.memoryOnly = true
        return
      }
      this.storageReadStarted = true
      const transaction = db.transaction(['histories', 'ranges'], 'readonly')
      const done = transactionDone(transaction)
      // A synchronously failing request must not leave the transaction rejection unhandled.
      void done.catch(() => undefined)
      const headerRequest = transaction.objectStore('histories').get(this.key)
      const rangesRequest = transaction.objectStore('ranges').getAll(pageKeys(this.key))
      const [header, ranges] = await Promise.all([
        requestResult<Header | undefined>(headerRequest),
        requestResult<PageRange[]>(rangesRequest),
        done,
      ])
      if (!this.current() || generation !== this.archiveGeneration) return
      const continuations = new Set<string>()
      const boundaryCursors: string[] = []
      if (header) {
        for (const range of ranges) {
          if (range.before) continuations.add(range.before)
        }
        // Stage the complete boundary before publishing any restored metadata.
        // Only the oldest page-sized boundary can overlap a monotonic before page.
        for (let i = ranges.length - 1; i >= 0 && boundaryCursors.length < pageSize; i--) {
          if (ranges[i].start === ranges[i].end) continue
          const lines = await this.readPage(ranges[i], false)
          if (!this.current() || generation !== this.archiveGeneration) return
          for (let j = 0; j < lines.length && boundaryCursors.length < pageSize; j++) boundaryCursors.push(lines[j].cursor)
        }
        this.ranges = ranges
        this.continuations = continuations
        this.boundaryCursors = boundaryCursors
        this.nextCursor = header.nextCursor
        this.continuationError = header.continuationError
      }
      this.storageErrors.delete('initialize')
      this.patch({
        count: header?.count ?? 0,
        hasMore: header?.hasMore ?? true,
        error: this.storageErrorMessage() ?? this.continuationError ?? null,
        revision: this.state.revision + 1,
      })
    } catch (cause) {
      if (!this.current() || generation !== this.archiveGeneration) return
      this.storageError(cause, 'initialize')
      throw cause
    }
  }

  private header(): Header {
    return { key: this.key, scope: this.scope, count: this.state.count, hasMore: this.state.hasMore, nextCursor: this.nextCursor, continuationError: this.continuationError }
  }

  readView = async (): Promise<SavedHistoryView | null> => {
    await this.ensureInitialized()
    if (!this.current()) return null
    await storageTail
    if (!this.current()) return null
    if (this.hasMemoryView) {
      this.clearStorageError('view')
      return this.memoryView
    }
    try {
      const db = await database()
      if (!db) {
        if (this.storageReadStarted) throw new Error('Previously saved terminal history is unavailable because IndexedDB is unavailable.')
        return null
      }
      const transaction = db.transaction('views', 'readonly')
      const [view] = await Promise.all([
        requestResult<{ key: string; view: SavedHistoryView } | undefined>(transaction.objectStore('views').get(this.key)),
        transactionDone(transaction),
      ])
      if (!this.current()) return null
      this.clearStorageError('view')
      return view?.view ?? null
    } catch (cause) {
      if (!this.current()) return null
      this.storageError(cause, 'view')
      throw cause
    }
  }

  saveView = async (view: SavedHistoryView | null): Promise<void> => {
    try {
      await this.ensureInitialized()
    } catch {
      // An unread archive is not an empty archive, including during disposal.
      return
    }
    if (!this.current()) return
    // Keep the new view until its transaction commits, including route disposal.
    this.memoryView = view
    this.hasMemoryView = true
    const header = this.header()
    await queueStorage(async () => {
      if (!this.current() || this.memoryOnly) return
      try {
        const db = await database()
        if (!db) {
          this.memoryOnly = true
          return
        }
        if (!this.current()) return
        await writeTransaction(db, ['histories', 'views'], (transaction) => {
          transaction.objectStore('histories').put(header)
          if (view) transaction.objectStore('views').put({ key: this.key, view })
          else transaction.objectStore('views').delete(this.key)
        })
        if (this.memoryView === view) {
          this.memoryView = null
          this.hasMemoryView = false
        }
      } catch (cause) {
        this.storageError(cause)
      }
    })
  }

  loadOlder = (): Promise<void> => {
    if (this.pending) return this.pending
    if (!this.current() || !this.state.hasMore || this.continuationError) return Promise.resolve()
    const sequence = ++this.sequence
    const controller = new AbortController()
    this.controller = controller
    this.patch({ loading: true, error: this.storageErrorMessage() ?? null })
    const operation = this.load(sequence, controller)
    this.pending = operation
    return operation
  }

  resetArchive = (): Promise<void> => {
    if (!this.resetting) this.resetting = this.reset()
    return this.resetting
  }

  private async reset() {
    try {
      await this.ensureInitialized()
      if (!this.current()) return
      this.cancel()
      this.clearArchive()
      this.patch({ count: 0, hasMore: true, error: null, revision: this.state.revision + 1 })
      const header = this.header()
      await queueStorage(async () => {
        if (!this.current() || this.memoryOnly) return
        try {
          const db = await database()
          if (!db) {
            this.memoryOnly = true
            return
          }
          if (!this.current()) return
          await writeTransaction(db, ['histories', 'ranges', 'pages'], (transaction) => {
            transaction.objectStore('histories').put(header)
            transaction.objectStore('ranges').delete(pageKeys(this.key))
            transaction.objectStore('pages').delete(pageKeys(this.key))
          })
        } catch (cause) {
          this.memoryOnly = true
          this.storageError(cause)
        }
      })
    } finally {
      this.resetting = null
    }
  }

  private clearArchive() {
    this.archiveGeneration++
    for (const range of this.ranges) resident.delete(pageKey(this.key, range.id[1]))
    this.memoryPages.clear()
    this.ranges = []
    this.boundaryCursors = []
    this.continuations.clear()
    this.nextCursor = undefined
    this.continuationError = undefined
    this.retryDelay = 0
    this.retryAt = 0
  }

  private async load(sequence: number, controller: AbortController) {
    try {
      await this.ensureInitialized()
      await this.resetting
      if (!this.current() || controller.signal.aborted || !this.state.hasMore || this.continuationError) return
      const delay = this.retryAt - Date.now()
      if (delay > 0) {
        const { promise, resolve } = Promise.withResolvers<void>()
        const finish = () => {
          clearTimeout(timer)
          controller.signal.removeEventListener('abort', finish)
          resolve()
        }
        const timer = setTimeout(finish, delay)
        controller.signal.addEventListener('abort', finish, { once: true })
        await promise
      }
      if (!this.current() || controller.signal.aborted) return
      const before = this.nextCursor
      const page = await this.client.terminalHistory({ run_id: this.scope.runID, limit: pageSize, ...(before ? { before } : {}) }, controller.signal)
      if (!this.current() || controller.signal.aborted || sequence !== this.sequence) return
      const nextCursor = page.next_cursor || undefined
      const stalled = page.lines.length === 0 && page.has_more && nextCursor === before
      const cycle = nextCursor !== undefined && this.continuations.has(nextCursor)
      if (page.has_more && !stalled && (nextCursor === undefined || nextCursor === before || cycle)) {
        this.continuationError = cycle
          ? 'Terminal history returned a previously consumed continuation cursor.'
          : 'Terminal history returned a non-advancing continuation cursor.'
        this.patch({ error: this.continuationError })
        await this.persistPage()
        return
      }
      const seen = new Set(this.boundaryCursors)
      const lines: TerminalHistoryLine[] = []
      for (const line of page.lines) {
        if (seen.has(line.cursor)) continue
        seen.add(line.cursor)
        lines.push(line)
      }
      if (lines.length) this.boundaryCursors = lines.map((line) => line.cursor).concat(this.boundaryCursors).slice(0, pageSize)
      this.retryDelay = stalled ? Math.min(this.retryDelay ? this.retryDelay * 2 : 250, 4000) : 0
      this.retryAt = Date.now() + this.retryDelay
      const range: PageRange = {
        id: [this.key, this.ranges.length],
        start: -this.state.count - lines.length,
        end: -this.state.count,
        before,
      }
      if (before && !stalled) this.continuations.add(before)
      this.nextCursor = nextCursor
      // Empty timeout continuations carry no new metadata or text to retain.
      if (!stalled) this.ranges.push(range)
      if (lines.length) {
        this.memoryPages.set(range.id[1], lines)
        remember(this.key, range.id[1], lines)
      }
      this.patch({
        count: this.state.count + lines.length,
        hasMore: page.has_more,
        error: this.state.error,
        revision: this.state.revision + 1,
      })
      if (!stalled) await this.persistPage(range, lines)
    } catch (cause) {
      if (this.current() && !controller.signal.aborted && sequence === this.sequence) {
        this.patch({ error: this.storageErrorMessage() ?? message(cause) })
      }
    } finally {
      if (sequence === this.sequence) {
        this.controller = null
        this.pending = null
        this.patch({ loading: false })
      }
    }
  }

  private async persistPage(range?: PageRange, lines: TerminalHistoryLine[] = []) {
    const header = this.header()
    await queueStorage(async () => {
      if (!this.current() || this.memoryOnly) return
      try {
        const db = await database()
        if (!db) {
          this.memoryOnly = true
          return
        }
        if (!this.current()) return
        await writeTransaction(db, ['histories', 'ranges', 'pages'], (transaction) => {
          transaction.objectStore('histories').put(header)
          if (range) {
            transaction.objectStore('ranges').put(range)
            if (lines.length) transaction.objectStore('pages').put({ id: range.id, lines } satisfies StoredPage)
          }
        })
        if (range) this.memoryPages.delete(range.id[1])
      } catch (cause) {
        // Previously committed pages remain on disk and can still be read.
        this.memoryOnly = true
        this.storageError(cause)
      }
    })
  }

  private async readPage(range: PageRange, retain = true): Promise<TerminalHistoryLine[]> {
    const generation = this.archiveGeneration
    const id = range.id[1]
    const memory = this.memoryPages.get(id) ?? resident.get(pageKey(this.key, id))
    if (memory) {
      remember(this.key, id, memory)
      return memory
    }
    const db = await database()
    if (!db) throw new Error('Previously saved terminal history is unavailable because IndexedDB is unavailable.')
    const transaction = db.transaction('pages', 'readonly')
    const [page] = await Promise.all([
      requestResult<StoredPage | undefined>(transaction.objectStore('pages').get(range.id)),
      transactionDone(transaction),
    ])
    if (!page) throw new Error('A previously saved terminal history page is missing from browser storage.')
    if (retain && this.current() && generation === this.archiveGeneration) remember(this.key, id, page.lines)
    return page.lines
  }

  readRows = async (start: number, end: number): Promise<Array<{ index: number; line: TerminalHistoryLine }>> => {
    await this.ensureInitialized()
    if (!this.current()) return []
    const generation = this.archiveGeneration
    const from = Math.max(-this.state.count, Math.floor(start))
    const to = Math.min(0, Math.ceil(end))
    const result: Array<{ index: number; line: TerminalHistoryLine }> = []
    try {
      let low = 0
      let high = this.ranges.length
      while (low < high) {
        const middle = (low + high) >>> 1
        if (this.ranges[middle].end > from) low = middle + 1
        else high = middle
      }
      for (let i = low - 1; i >= 0; i--) {
        const range = this.ranges[i]
        if (range.start >= to) break
        if (range.start === range.end) continue
        const lines = await this.readPage(range)
        if (!this.current() || generation !== this.archiveGeneration) return []
        for (let index = Math.max(from, range.start); index < Math.min(to, range.end); index++) {
          result.push({ index, line: lines[index - range.start] })
        }
      }
      this.clearStorageError('rows')
      return result
    } catch (cause) {
      if (!this.current() || generation !== this.archiveGeneration) return []
      this.storageError(cause, 'rows')
      throw cause
    }
  }

  cancel = () => {
    this.sequence++
    this.controller?.abort()
    this.controller = null
    this.pending = null
    this.patch({ loading: false })
  }

  invalidate() {
    this.invalid = true
    this.cancel()
    this.clearArchive()
    this.memoryView = null
    this.hasMemoryView = false
    this.storageErrors.clear()
    this.patch({ count: 0, hasMore: false, error: null, revision: this.state.revision + 1 })
  }
}

export function getHistoryCache(scope: HistoryScope, client: Pick<Api, 'terminalHistory'> = api): HistoryCache {
  initializeSubscription()
  const key = scopeKey(scope)
  let cache = caches.get(key)
  if (!cache) {
    cache = new RunHistoryCache({ ...scope }, key, client)
    caches.set(key, cache)
  }
  return cache
}
