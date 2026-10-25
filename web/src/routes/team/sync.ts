// Team read models are read whole on connect, reconnect or wake; between those,
// `applyEvent` in `store/sync.ts` keeps them current.

import { dequal } from 'dequal'
import { useEffect, useRef } from 'react'
import { api, type Api } from '@/lib/api'
import { onWake } from '@/lib/stream'
import type { DiskUsage, TimelineQuery } from '@/lib/types'
import { useStore, type RootState, type RootStore } from '@/store'
import type { FeedFilters } from '@/store/timeline'

/** Presence expires after 45s server-side; a third of it keeps us online. */
const heartbeatMs = 15_000
/** A wake within this long of the last full read does not read again. */
const minGapMs = 2500
/**
 * Both gateways speak HTTP/1.1, so an unanswered disk read holds one of the
 * browser's six connections to the host. A current server answers within 5 s.
 */
const diskTimeoutMs = 10_000
/** How far back from the log head the feed opens, in event sequence. */
const feedWindow = 500
const feedPage = 200
/** One read stops here, so a long log cannot be walked in a single click. */
const maxPages = 5
export const pageBudget = maxPages * feedPage

/**
 * Presence is keyed on (member, workspace), so this is the only workspace a
 * heartbeat may claim: beating every one would show the user online where they never looked.
 */
export function focusedWorkspace(state: RootState): string {
  const { params } = state.route
  if (params.workspaceId) return params.workspaceId
  if (params.runId) return state.runs[params.runId]?.workspace_id ?? ''
  return state.activeWorkspace
}

/**
 * Reads every workspace: the Team line's worst budget state and the queue
 * count are whole-deployment claims no subset can answer.
 */
export async function refreshTeam(store: RootStore, client: Api = api): Promise<void> {
  await Promise.all([
    client.presenceRoster().then(store.getState().setPresence).catch(ignore),
    readDisk(client),
    ...Object.keys(store.getState().workspaces).map((id) =>
      client.budgetGet(id).then(store.getState().setBudget).catch(ignore),
    ),
  ])
}

export async function heartbeat(store: RootStore, client: Api = api): Promise<void> {
  const workspaceID = focusedWorkspace(store.getState())
  if (!workspaceID) return
  await client.presenceHeartbeat(workspaceID).catch(ignore)
}

let diskRead: Promise<void> | undefined

/** One read at a time: a reconnect or wake joins the heartbeat's read instead of adding one. */
function readDisk(client: Api): Promise<void> {
  if (diskRead) return diskRead
  const abort = new AbortController()
  const deadline = setTimeout(() => abort.abort(new Error('Disk usage request timed out after 10 seconds')), diskTimeoutMs)
  diskRead = client
    .disk(abort.signal)
    .then(rememberDisk)
    .catch(rememberDiskError)
    .finally(() => {
      clearTimeout(deadline)
      diskRead = undefined
    })
  return diskRead
}

/** Writes only a real change, so a quiet server does not re-render the shell every refresh. */
function rememberDisk(disk: DiskUsage): void {
  const s = useStore.getState()
  if (!s.info || (!s.info.diskError && dequal(s.info.disk, disk))) return
  s.setInfo({ ...s.info, disk, diskError: undefined })
}

function rememberDiskError(error: unknown): void {
  const s = useStore.getState()
  const diskError = error instanceof Error ? error.message : String(error)
  if (!s.info || s.info.diskError === diskError) return
  s.setInfo({ ...s.info, disk: undefined, diskError })
}

/** Reads in full on every reconnect: events missed while away are not all replayed. */
export function useTeamRefresh(client: Api = api): void {
  const route = useStore((s) => s.route)
  const offline = useStore((s) => s.connection !== 'live')
  const hydrated = useStore((s) => s.hydrated)
  const workspaceIDs = useStore((s) => Object.keys(s.workspaces).sort().join(','))
  const lastRun = useRef(0)

  useEffect(() => {
    // Hydration lists the workspaces, after the stream is live, so a read
    // before it is repeated. Losing the connection is no reason to read;
    // getting it back is.
    if (!hydrated || (offline && lastRun.current > 0)) return
    lastRun.current = Date.now()
    void refreshTeam(useStore, client)
  }, [hydrated, offline, workspaceIDs, client])

  useEffect(() => {
    void heartbeat(useStore, client)
    const timer = setInterval(() => {
      void heartbeat(useStore, client)
      // Disk usage has no event; the heartbeat's pace is plenty for a gauge.
      void readDisk(client)
    }, heartbeatMs)
    return () => clearInterval(timer)
  }, [route, client])

  // A backgrounded tab freezes the timer above, so presence has expired by wake.
  // The full read keeps a floor so app flipping or a flapping link cannot
  // multiply requests; the heartbeat always goes.
  useEffect(
    () =>
      onWake(() => {
        const now = Date.now()
        if (now - lastRun.current >= minGapMs) {
          lastRun.current = now
          void refreshTeam(useStore, client)
        }
        void heartbeat(useStore, client)
      }),
    [client],
  )
}

/**
 * A read that failed or stopped on its page budget leaves a gap the live tail
 * cannot close, so the next event applied reads it again.
 */
export function useLiveFeed(active: boolean, client: Api = api): void {
  const holdFeed = useStore((s) => s.holdFeed)
  const live = useStore((s) => s.connection === 'live')
  const wasLive = useRef(live)

  useEffect(() => (active ? holdFeed() : undefined), [active, holdFeed])

  useEffect(() => {
    if (!active) return
    return useStore.subscribe((s, prev) => {
      if (s.lastSeq === prev.lastSeq || s.feedLoading) return
      if (!s.feedError && !s.feedTruncated) return
      // Nothing loaded means the opening probe may never have found the
      // head; draining from zero would page the log from its start.
      void (s.feed.length === 0 ? openFeed(useStore, client) : drain(useStore, client))
    })
  }, [active, client])

  useEffect(() => {
    const reconnected = live && !wasLive.current
    wasLive.current = live
    if (reconnected && active && !useStore.getState().feedLoading) void drain(useStore, client)
  }, [live, active, client])
}

/**
 * The reader pages forward only, so a probe past the end finds the log head
 * and the window starts `feedWindow` before it.
 */
export async function openFeed(store: RootStore, client: Api = api): Promise<void> {
  const { feedFilters, beginFeed } = store.getState()
  if (!feedFilters.workspaceID) return
  beginFeed()
  const id = store.getState().feedRequest
  let head = 0
  try {
    const probe = await client.workspaceTimeline(
      query(feedFilters, Number.MAX_SAFE_INTEGER, 1),
    )
    head = probe.next_seq
  } catch (err) {
    if (store.getState().feedRequest === id) {
      store.getState().setFeedLoading(false, message(err))
    }
    return
  }
  if (store.getState().feedRequest !== id) return
  const floor = Math.max(0, head - feedWindow)
  store.getState().resetFeed(floor, floor > 0)
  await read(store, client, floor, 0, id)
}

/**
 * Reads only the new stretch: re-reading the whole window would spend the
 * page budget on loaded history and lose its newest end.
 */
export async function olderFeed(store: RootStore, client: Api = api): Promise<void> {
  const until = store.getState().feedFloor
  if (until === 0) return
  store.getState().beginFeed()
  const id = store.getState().feedRequest
  const floor = Math.max(0, until - feedWindow)
  store.getState().extendFeed(floor, floor > 0)
  if (await read(store, client, floor, until, id)) return
  // Restoring the floor lets the next click retry instead of skipping the gap forever.
  if (store.getState().feedRequest === id) store.getState().extendFeed(until, true)
}

export async function drain(store: RootStore, client: Api = api): Promise<void> {
  const s = store.getState()
  if (!s.feedFilters.workspaceID) return
  s.setFeedLoading(true)
  await read(store, client, s.feedCursor, 0, s.feedRequest)
}

/**
 * `until` zero means the log head. Every page re-checks the request stamp, so
 * a superseded read writes nothing. False means it failed with the stamp current.
 */
async function read(
  store: RootStore,
  client: Api,
  after: number,
  until: number,
  id: number,
): Promise<boolean> {
  const filters = store.getState().feedFilters
  let cursor = after
  try {
    for (let page = 0; page < maxPages; page++) {
      if (store.getState().feedRequest !== id) return true
      const got = await client.workspaceTimeline(query(filters, cursor, feedPage))
      if (store.getState().feedRequest !== id) return true
      store.getState().appendFeed(got.events, got.next_seq)
      cursor = got.next_seq
      if (!got.more || (until > 0 && cursor >= until)) {
        store.getState().setFeedLoading(false)
        // Read through to the head, nothing past the cursor is missing.
        if (until === 0) store.getState().setFeedTruncated(false)
        return true
      }
    }
    store.getState().setFeedLoading(false)
    store.getState().setFeedTruncated(true)
    return true
  } catch (err) {
    if (store.getState().feedRequest === id) {
      store.getState().setFeedLoading(false, message(err))
      return false
    }
    return true
  }
}

function query(f: FeedFilters, afterSeq: number, limit: number): TimelineQuery {
  return {
    workspace_id: f.workspaceID,
    run_id: f.runID || undefined,
    member_id: f.memberID || undefined,
    types: f.type ? [f.type] : undefined,
    after_seq: afterSeq,
    limit,
  }
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// A read that fails leaves the surface showing what it had; the next
// heartbeat, reconnect or wake reads it again.
function ignore(): void {}
