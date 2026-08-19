// The team surfaces are read models the gateway already serves: the approval
// inbox, the presence roster, budgets, and session history. None of them has
// a push channel of its own, so they refresh when the event cursor moves -
// every event the store applies advances `lastSeq`, and that is the signal
// that something a teammate did may have changed one of these reads.

import { useEffect, useRef } from 'react'
import { api, type Api } from '@/lib/api'
import { runState } from '@/lib/status'
import type { DiskUsage, RunStatus, TimelineQuery } from '@/lib/types'
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
 * The sessions a background refresh covers. Every read here is per session
 * on the wire and `session.list` returns every session that ever existed -
 * there is no closed state to thin it - so the set has to be bounded by
 * what is live: the sessions with a run still going, one still waiting on
 * an approval - a request outlives its run, and the decision may land
 * anywhere - plus whatever the centre view is showing. The inbox view asks
 * about the rest on demand.
 */
export function refreshSessions(state: RootState): string[] {
  const ids = new Set<string>()
  for (const run of Object.values(state.runs)) {
    if (isLive(run.status)) ids.add(run.session_id)
  }
  for (const [id, list] of Object.entries(state.inbox)) {
    if (list.some((a) => a.decision === 'requested')) ids.add(id)
  }
  const focused = focusedSession(state)
  if (focused) ids.add(focused)
  return [...ids]
}

function isLive(status: RunStatus): boolean {
  const state = runState(status)
  return state !== 'done' && state !== 'failed'
}

/**
 * The session the centre view is showing, if any. Presence is keyed on
 * (member, session), so this is the only session a heartbeat may claim:
 * beating every session would report the user online in sessions they have
 * never opened, to teammates who are working in them. An attach lives
 * inside this view, so it needs no separate account.
 */
export function focusedSession(state: RootState): string {
  const { params } = state.route
  if (params.sessionId) return params.sessionId
  if (params.runId) return state.runs[params.runId]?.session_id ?? ''
  return ''
}

/** Re-reads every team surface. Failures leave the last good data in place. */
export async function refreshTeam(store: RootStore, client: Api = api): Promise<void> {
  const s = store.getState()
  const sessions = refreshSessions(s)
  await Promise.all([
    client.presenceRoster().then(store.getState().setPresence).catch(ignore),
    client.disk().then(rememberDisk).catch(ignore),
    ...sessions.map((id) =>
      client
        .approvalList(id, s.showDecided)
        .then((list) => store.getState().setInbox(id, list))
        .catch(ignore),
    ),
    ...sessions.map((id) =>
      client.budgetGet(id).then(store.getState().setBudget).catch(ignore),
    ),
  ])
}

/**
 * Every session's inbox. The queue is shared and a request against a run
 * that has since finished still needs deciding, so the view that shows the
 * whole queue reads wider than the background refresh does - once, when it
 * opens, rather than every time the cursor moves.
 */
export async function refreshInbox(store: RootStore, client: Api = api): Promise<void> {
  const s = store.getState()
  await Promise.all(
    Object.keys(s.sessions).map((id) =>
      client
        .approvalList(id, s.showDecided)
        .then((list) => store.getState().setInbox(id, list))
        .catch(ignore),
    ),
  )
}

/**
 * The wide read: every session, not just the live ones. Two of these
 * surfaces answer a whole-deployment question - the status bar claims the
 * worst budget state any session is in, and the queue count claims the
 * whole queue - and a session does not stop being over its cap or holding
 * a pending request when its last run finishes. The bounded set cannot
 * answer that, so this runs when the session list changes and never on the
 * recurring path, where a per-session fan-out is what has to stay out.
 */
export async function refreshAllSessions(
  store: RootStore,
  client: Api = api,
): Promise<void> {
  const sessions = Object.keys(store.getState().sessions)
  await Promise.all([
    refreshInbox(store, client),
    ...sessions.map((id) =>
      client.budgetGet(id).then(store.getState().setBudget).catch(ignore),
    ),
  ])
}

/** Tells the server we are here, in the session we are actually in. */
export async function heartbeat(store: RootStore, client: Api = api): Promise<void> {
  const session = focusedSession(store.getState())
  if (!session) return
  await client.presenceHeartbeat(session).catch(ignore)
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
 * one refresh per burst of events, a heartbeat on its own interval, and
 * the wide read whenever the set of sessions changes - which is when the
 * shell first hydrates, and thereafter only when somebody creates or
 * removes a session. Events never trigger it.
 */
export function useTeamRefresh(client: Api = api): void {
  const runs = useStore((s) => s.runs)
  const route = useStore((s) => s.route)
  const lastSeq = useStore((s) => s.lastSeq)
  const showDecided = useStore((s) => s.showDecided)
  const sessionIDs = useStore((s) => Object.keys(s.sessions).sort().join(','))
  const lastRun = useRef(0)

  useEffect(() => {
    if (!sessionIDs) return
    void refreshAllSessions(useStore, client)
  }, [sessionIDs, showDecided, client])

  useEffect(() => {
    const wait = Math.max(0, minGapMs - (Date.now() - lastRun.current))
    const timer = setTimeout(() => {
      lastRun.current = Date.now()
      void refreshTeam(useStore, client)
    }, wait)
    return () => clearTimeout(timer)
  }, [runs, route, lastSeq, showDecided, client])

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
  if (!feedFilters.sessionID) return
  beginFeed()
  const id = store.getState().feedRequest
  let head = 0
  try {
    const probe = await client.sessionTimeline(
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
  if (!s.feedFilters.sessionID) return
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
      const got = await client.sessionTimeline(query(filters, cursor, feedPage))
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
    session_id: f.sessionID,
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
