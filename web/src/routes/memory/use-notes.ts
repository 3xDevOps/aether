import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { MemoryItem } from '@/lib/types'
import { listLimit, reads, type Filter, type Listing } from '@/routes/memory/model'
import { useStore } from '@/store'

const METHOD_NOT_FOUND = -32601
/** An agent writing many notes at once gets one re-read, this long after its last write. */
const changeDelay = 250

export type Load =
  | { kind: 'loading' }
  | ({ kind: 'ready'; words: string; filter: Filter } & Listing)
  | { kind: 'failed'; message: string }
  | { kind: 'unsupported' }

export type Brief = { kind: 'loading' } | { kind: 'ready'; text: string } | { kind: 'failed'; message: string }

async function readPair(client: Api, workspaceID: string, words: string, filter: Filter): Promise<Listing> {
  const params = reads(workspaceID, words, filter)
  const results = await Promise.all(params.map((p) => client.memorySearch(p)))
  const filled = (pinned: boolean) => params.some((p, i) => Boolean(p.pinned) === pinned && results[i].items.length >= listLimit)
  return { items: results.flatMap((result) => result.items), pinsFull: filled(true), full: filled(false) }
}

const patched = (items: MemoryItem[], item: MemoryItem) => items.map((old) => (old.id === item.id ? item : old))

/**
 * The list for the filter and words, the unfiltered All pair behind the tab
 * counts, and the brief, re-read after each `memory.change` for the
 * workspace and whenever the connection comes back live. Answers to an older
 * request are dropped.
 */
export function useNotes(client: Api, workspaceID: string, words: string, filter: Filter) {
  const connection = useStore((s) => s.connection)
  const [load, setLoad] = useState<Load>({ kind: 'loading' })
  const [all, setAll] = useState<Listing | null>(null)
  const [brief, setBrief] = useState<Brief>({ kind: 'loading' })
  const requests = useRef({ list: 0, all: 0, brief: 0 })
  // The All pair is the list itself on the All filter with no words, so it is read once.
  const listIsAll = filter === 'all' && !words
  const covered = useRef(listIsAll)
  covered.current = listIsAll

  const readList = useCallback(async () => {
    const id = ++requests.current.list
    // The All pair keeps its own request id, so its counts land even when the viewer has moved on.
    const allID = filter === 'all' && !words ? ++requests.current.all : 0
    try {
      const listing = await readPair(client, workspaceID, words, filter)
      if (allID && allID === requests.current.all) setAll(listing)
      if (id !== requests.current.list) return
      setLoad({ kind: 'ready', words, filter, ...listing })
    } catch (error) {
      if (id !== requests.current.list) return
      // A 404 with a code is the server's own answer, such as an unknown workspace.
      const unsupported = error instanceof ApiError && (error.code === METHOD_NOT_FOUND || (error.status === 404 && error.code === undefined))
      setLoad(unsupported ? { kind: 'unsupported' } : { kind: 'failed', message: message(error) })
    }
  }, [client, workspaceID, words, filter])

  const readAll = useCallback(async () => {
    const id = ++requests.current.all
    try {
      const listing = await readPair(client, workspaceID, '', 'all')
      if (id === requests.current.all) setAll(listing)
    } catch {
      // The list read reports the same failure; the counts only go stale.
    }
  }, [client, workspaceID])

  const readBrief = useCallback(async () => {
    const id = ++requests.current.brief
    try {
      const result = await client.memoryBrief(workspaceID)
      if (id === requests.current.brief) setBrief({ kind: 'ready', text: result.text })
    } catch (error) {
      if (id === requests.current.brief) setBrief({ kind: 'failed', message: message(error) })
    }
  }, [client, workspaceID])

  const refreshNow = useCallback(() => {
    void readList()
    void readBrief()
    if (!covered.current) void readAll()
  }, [readList, readBrief, readAll])
  // Callers hold `refresh` across an await, so it always reads the view on screen now.
  const latest = useRef(refreshNow)
  latest.current = refreshNow
  const refresh = useCallback(() => latest.current(), [])

  // A new search or filter keeps the list on screen until its answer lands; another workspace's notes do not.
  useEffect(() => {
    setLoad({ kind: 'loading' })
    setAll(null)
    setBrief({ kind: 'loading' })
  }, [workspaceID])

  useEffect(() => {
    if (connection === 'live' && workspaceID) void readList()
  }, [connection, workspaceID, readList])

  useEffect(() => {
    if (connection !== 'live' || !workspaceID) return
    void readBrief()
    if (!covered.current) void readAll()
  }, [connection, workspaceID, readBrief, readAll])

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const stop = useStore.subscribe((s, previous) => {
      if (s.memoryChanges[workspaceID] === previous.memoryChanges[workspaceID]) return
      clearTimeout(timer)
      timer = setTimeout(refresh, changeDelay)
    })
    return () => {
      stop()
      clearTimeout(timer)
    }
  }, [workspaceID, refresh])

  /** Shows a note's new state at once, ahead of the re-read that confirms it. */
  const patch = useCallback((item: MemoryItem) => {
    setLoad((current) => (current.kind === 'ready' ? { ...current, items: patched(current.items, item) } : current))
    setAll((current) => current && { ...current, items: patched(current.items, item) })
  }, [])

  return { load, all, brief, readList, readBrief, refresh, patch, waiting: load.kind === 'loading' && connection !== 'live' }
}
