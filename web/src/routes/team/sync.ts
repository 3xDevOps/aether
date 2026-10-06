// The team surfaces are read models the gateway already serves: the approval
// inbox, the presence roster, budgets, and workspace history. They are read
// whole when the app connects, reconnects or wakes; between those, the events
// that change them update the store directly (`applyEvent` in
// `store/sync.ts`).

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
  const id = s.startInboxRead()
  const results = await Promise.all(
    Object.keys(s.workspaces).map((wsp) =>
      client
        .approvalList(wsp, s.showDecided)
        .then((list) => {
          const now = store.getState()
          // An approval event applied meanwhile is newer than this answer.
          if (now.inboxRequest === id && now.inboxEvents[wsp] === s.inboxEvents[wsp]) {
            now.setInbox(wsp, list)
          }
          return null
        })
        .catch(message),
    ),
  )
  if (store.getState().inboxRequest !== id) return
  store.getState().setInboxError(results.find((r) => r !== null) ?? null)
}

/** Tells the server we are here, in the workspace we are actually in. */
export async function heartbeat(store: RootStore, client: Api = api): Promise<void> {
  const workspaceID = focusedWorkspace(store.getState())
  if (!workspaceID) return
  await client.presenceHeartbeat(workspaceID).catch(ignore)
}

/**
 * Every field the gauge reads. The totals move the bar and the components
 * fill the tooltip, so comparing the totals alone would pin a stale
 * breakdown - a server that starts reporting a new component while its
 * totals sit still would never reach the tooltip.
 */
function sameDisk(a: DiskUsage | undefined, b: DiskUsage): boolean {
  return (
    a !== undefined &&
    a.used_bytes === b.used_bytes &&
    a.total_bytes === b.total_bytes &&
    a.free_bytes === b.free_bytes &&
    a.worktree_bytes === b.worktree_bytes &&
    a.transcript_bytes === b.transcript_bytes &&
    a.database_bytes === b.database_bytes &&
    a.repo_bytes === b.repo_bytes
  )
}

/**
 * Disk usage rides on `server.info` even though it arrives on its own
 * route: the status bar's gauge is the client's one reader of it, and the
 * shared `server.info` result cannot carry it. Only a real change is
 * written, so a quiet server does not re-render the shell every refresh.
 */
function rememberDisk(disk: DiskUsage): void {
  const s = useStore.getState()
  if (!s.info || sameDisk(s.info.disk, disk)) return
  s.setInfo({ ...s.info, disk })
}

/**
 * Keeps the team reads current for as long as the status bar is mounted: a
 * full read on mount and on every reconnect - events missed while away are
 * not all replayed - and when the set of reads changes, plus the heartbeat
 * on its own interval.
 */
export function useTeamRefresh(client: Api = api): void {
  const route = useStore((s) => s.route)
  const offline = useStore((s) => s.connection !== 'live')
  const showDecided = useStore((s) => s.showDecided)
  const workspaceIDs = useStore((s) => Object.keys(s.workspaces).sort().join(','))
  const lastRun = useRef(0)

  useEffect(() => {
    // Losing the connection is no reason to read; getting it back is.
    if (offline && lastRun.current > 0) return
    lastRun.current = Date.now()
    void refreshTeam(useStore, client)
  }, [offline, showDecided, workspaceIDs, client])

  useEffect(() => {
    void heartbeat(useStore, client)
    const timer = setInterval(() => {
      void heartbeat(useStore, client)
      // Disk usage has no event; the heartbeat's pace is plenty for a gauge.
      void client.disk().then(rememberDisk).catch(ignore)
    }, heartbeatMs)
    return () => clearInterval(timer)
  }, [route, client])

  // A backgrounded tab freezes the timer above, so a phone comes back with
  // presence already expired server-side (the TTL is 45s) and an inbox that
  // may have gained an approval while its socket was down. Flipping between
  // two apps, or a cellular link flapping `online`, must not become 2 + 2N
  // requests a time, so the full read keeps a floor; the heartbeat, one small
  // request and the reason the wake exists, always goes.
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
 * Marks a feed view mounted while `active`, so `applyEvent` adds the live
 * events its filters select, and reads what the stream may have skipped when
 * the connection comes back. A read that failed or stopped on its page
 * budget leaves a gap the live tail cannot close, so the next event applied
 * reads it again. The view opens the feed itself.
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
