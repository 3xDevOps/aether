// The team surfaces are read models the gateway already serves: the approval
// inbox, the presence roster, budgets, and workspace history. None of them has
// a push channel of its own, so they refresh when the event cursor moves -
// every event the store applies advances `lastSeq`, and that is the signal
// that something a teammate did may have changed one of these reads.

import { useEffect, useRef } from 'react'
import { api, type Api } from '@/lib/api'
import type { DiskUsage, TimelineQuery } from '@/lib/types'
import { useStore, type RootState, type RootStore } from '@/store'
import type { FeedFilters } from '@/store/timeline'

/** Presence expires after 90s server-side; a third of it keeps us online. */
const heartbeatMs = 30_000
/**
 * Bursts of events coalesce into one refresh at most this often. Most
 * events change none of these reads - a diff snapshot moves the cursor
 * just as an approval does - so the floor is what keeps a chatty run from
 * turning into a request per event.
 */
const minGapMs = 2500
/** How far back from the log head the feed opens, in event sequence. */
const feedWindow = 500
const feedPage = 200
/** One read stops here, so a long log cannot be walked in a single click. */
const maxPages = 5
/** What that budget works out to, for the notice the view shows. */
export const pageBudget = maxPages * feedPage

/**
 * The workspace the centre view is showing, falling back to the active one.
 * Presence is keyed on (member, workspace), so this is the only workspace a
 * heartbeat may claim: beating every workspace would report the user online
 * in workspaces they have never opened, to teammates who are working in
 * them. An attach lives inside this view, so it needs no separate account.
 */
export function focusedWorkspace(state: RootState): string {
  const { params } = state.route
  if (params.workspaceId) return params.workspaceId
  if (params.runId) return state.runs[params.runId]?.workspace_id ?? ''
  return state.activeWorkspace
}

/**
 * Re-reads every team surface, for every workspace. A workspace is a repo
 * plus its environment plan, so a deployment has a handful and they outlive
 * every run in them; both readouts fed from here ask a whole-deployment
 * question - the status bar claims the worst budget state anywhere, and the
 * queue count claims the whole queue - which no subset can answer. Failures
 * leave the last good data in place.
 */
export async function refreshTeam(store: RootStore, client: Api = api): Promise<void> {
  await Promise.all([
    client.presenceRoster().then(store.getState().setPresence).catch(ignore),
    client.disk().then(rememberDisk).catch(ignore),
    refreshInbox(store, client),
    ...Object.keys(store.getState().workspaces).map((id) =>
      client.budgetGet(id).then(store.getState().setBudget).catch(ignore),
    ),
  ])
}

/**
 * Every workspace's inbox. The queue is shared and a request against a run
 * that has since finished still needs deciding, so this reads every
 * workspace rather than only the ones with something running.
 */
export async function refreshInbox(store: RootStore, client: Api = api): Promise<void> {
  const s = store.getState()
  await Promise.all(
    Object.keys(s.workspaces).map((id) =>
      client
        .approvalList(id, s.showDecided)
        .then((list) => store.getState().setInbox(id, list))
        .catch(ignore),
    ),
  )
}

/** Tells the server we are here, in the workspace we are actually in. */
export async function heartbeat(store: RootStore, client: Api = api): Promise<void> {
  const workspaceID = focusedWorkspace(store.getState())
  if (!workspaceID) return
  await client.presenceHeartbeat(workspaceID).catch(ignore)
}

/**
 * Disk usage rides on `server.info` even though it arrives on its own
 * route: the status bar's gauge is the client's one reader of it, and the
 * shared `server.info` result cannot carry it. Only a real change is
 * written, so a quiet server does not re-render the shell every refresh.
 */
function rememberDisk(disk: DiskUsage): void {
  const s = useStore.getState()
  if (
    !s.info ||
    (s.info.disk?.used_bytes === disk.used_bytes &&
      s.info.disk?.total_bytes === disk.total_bytes)
  ) {
    return
  }
  s.setInfo({ ...s.info, disk })
}

/**
 * Keeps the team reads current for as long as the status bar is mounted:
 * one refresh per burst of events, and a heartbeat on its own interval.
 */
export function useTeamRefresh(client: Api = api): void {
  const runs = useStore((s) => s.runs)
  const route = useStore((s) => s.route)
  const lastSeq = useStore((s) => s.lastSeq)
  const showDecided = useStore((s) => s.showDecided)
  const workspaceIDs = useStore((s) => Object.keys(s.workspaces).sort().join(','))
  const lastRun = useRef(0)

  useEffect(() => {
    const wait = Math.max(0, minGapMs - (Date.now() - lastRun.current))
    const timer = setTimeout(() => {
      lastRun.current = Date.now()
      void refreshTeam(useStore, client)
    }, wait)
    return () => clearTimeout(timer)
  }, [runs, route, lastSeq, showDecided, workspaceIDs, client])

  useEffect(() => {
    void heartbeat(useStore, client)
    const timer = setInterval(() => void heartbeat(useStore, client), heartbeatMs)
    return () => clearInterval(timer)
  }, [route, client])
}

/**
 * Opens the feed on the most recent history. The reader pages forward
 * only, so the window is found by asking for a page past the end: the
 * answer carries the log head, and the window starts `feedWindow` before
 * it.
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
 * Widens the window backwards. It reads the new stretch only, up to where
 * the old window began, and keeps everything already loaded: re-reading
 * the whole window would spend the page budget on history the feed
 * already has and lose the newest end of it, which is the end the reader
 * came for.
 */
export async function olderFeed(store: RootStore, client: Api = api): Promise<void> {
  const until = store.getState().feedFloor
  if (until === 0) return
  store.getState().beginFeed()
  const id = store.getState().feedRequest
  const floor = Math.max(0, until - feedWindow)
  store.getState().extendFeed(floor, floor > 0)
  if (await read(store, client, floor, until, id)) return
  // The stretch never fully loaded. Putting the floor back lets the next
  // click retry it, instead of walking past a gap the feed would then
  // silently skip forever.
  if (store.getState().feedRequest === id) store.getState().extendFeed(until, true)
}

/** Reads whatever the feed has not seen yet, from its cursor forward. */
export async function drain(store: RootStore, client: Api = api): Promise<void> {
  const s = store.getState()
  if (!s.feedFilters.workspaceID) return
  s.setFeedLoading(true)
  await read(store, client, s.feedCursor, 0, s.feedRequest)
}

/**
 * Pages history into the feed from `after`, stopping at `until` - zero
 * means the log head. Every iteration re-checks the request stamp, so a
 * read the user has already moved on from writes nothing. False means the
 * read failed partway with the stamp still current.
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

// A read that fails leaves the surface showing what it had; the next event
// tries again.
function ignore(): void {}
