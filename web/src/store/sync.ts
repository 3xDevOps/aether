// Hydration and live updates: one HTTP fetch fills the store, then the event
// stream is the only thing that changes it.

import { toast } from 'sonner'
import { api, ApiError, takeRequestedRun, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { backoff, connectEvents, onWake } from '@/lib/stream'
import type {
  Event,
  GatewayCapabilities,
  GitBranchPayload,
  LinkStatus,
  OverlapPayload,
  RunArchivedPayload,
  RunDiffPayload,
  RunProtectedPayload,
  RunStatusPayload,
  RunTitlePayload,
  ServerUpdatePayload,
} from '@/lib/types'
import type { RootStore } from '@/store'
import { pausedFromTimeline } from '@/store/board'
import { serverUpdateApplying, type UnreachableKind } from '@/store/server'

/**
 * Names the hop that failed. The local gateway reports a dead transport as
 * 503 (protocol.CodeUnavailable) with the failing hop in the message:
 * "network unreachable: ..." when this machine never got off its own
 * network stack (DNS dead, no route), "server unreachable: ..." when it did
 * and aether-server did not answer. A fetch that never got an answer at all
 * (a TypeError from fetch) means the origin serving this page is gone, and
 * which origin that is decides what the user can do: the desktop gateway is
 * a process on this machine that can be restarted, while the server gateway
 * is across the tailnet, so its silence is a dead link or a dead host.
 * Anything else - a 401, a 500 the server produced - is neither.
 */
function classifyUnreachable(err: unknown, store: RootStore): UnreachableKind | null {
  if (err instanceof ApiError && err.status === 503) {
    if (err.message.includes('network unreachable')) return 'network'
    if (err.message.includes('server unreachable')) return 'server'
  }
  if (err instanceof TypeError) {
    // An unknown gateway is the desktop one: it is the only surface that can
    // fail before the descriptor is read, since the probe seeds it.
    return store.getState().capabilities?.gateway === 'server' ? 'tailnet' : 'gateway'
  }
  return null
}


/** Names why the gateway refused the capabilities probe. */
function refusalKind(err: ApiError, store: RootStore): UnreachableKind | null {
  if (err.status === 403) return 'refused'
  if (err.message.includes('tailnet identity unavailable')) return 'identity'
  return classifyUnreachable(err, store)
}

/**
 * Fills the store from the server. False means the fetch failed or its owner
 * was disposed. Direct callers may omit the signal; connect owns its lifetime.
 */
export async function hydrate(
  store: RootStore,
  client: Api = api,
  signal?: AbortSignal,
): Promise<boolean> {
  if (signal?.aborted) return false
  const s = store.getState()
  try {
    const [info, workspaces, members, runs, overlaps, capabilities] =
      await Promise.all([
        client.serverInfo(),
        client.workspaceListFull(),
        client.memberList(),
        client.runList(),
        // The conflict radar is a warning system, not a data source the app
        // needs: an unreachable one leaves the chips off, it does not fail the
        // hydration.
        client.runOverlaps().catch(() => []),
        // A legacy remote monitor does not serve the endpoint; null keeps the
        // client on its built-in assumptions.
        client.capabilities().catch(() => null),
      ])
    if (signal?.aborted) return false
    const origin = typeof window === 'undefined' ? '' : window.location.origin
    const incomingIdentity = `${origin}\u0000${info.tailnet_hostname ?? ''}\u0000${info.member.id}`
    const previousIdentity = store.getState().identityKey
    if (previousIdentity && previousIdentity !== incomingIdentity) {
      // Reconnects retain drafts; a different authenticated owner must not.
      store.getState().resetFiles()
    }
    if (!s.hydrated && capabilities?.local?.includes('workspace.selection')) {
      try {
        const saved = await client.localWorkspaceSelection()
        if (signal?.aborted) return false
        // A choice made while startup was fetching outranks the saved one.
        if (store.getState().activeWorkspace === s.activeWorkspace && saved.workspace_id) {
          s.setActiveWorkspace(saved.workspace_id)
        }
      } catch (err) {
        if (signal?.aborted) return false
        // Preferences are optional; report the gateway's error without
        // turning a successful server snapshot into a connection failure.
        toast.error(message(err))
      }
    }
    s.setIdentityKey(incomingIdentity)
    s.setInfo(info)
    s.setWorkspaces(workspaces)
    const active = store.getState().activeWorkspace
    s.setMembers(members)
    s.setRuns(runs.filter((run) => !store.getState().deletedWorkspaceIDs.has(run.workspace_id)))
    // The snapshot is authoritative for the paused badge; runs without the
    // wire field (a legacy gateway) stay unknown.
    s.seedPaused(
      Object.fromEntries(
        runs
          .filter((r) => r.paused !== undefined)
          .map((r) => [r.id, r.paused === true]),
      ),
    )
    s.setOverlaps(overlaps)
    if (
      active &&
      (capabilities === null || capabilities.methods.includes('mission.list'))
    ) {
      await client
        .missionList({ workspace_id: active, limit: 50 })
        .then((result) => {
          if (!signal?.aborted) s.setMissions(active, result.missions, result.next_cursor)
        })
        .catch(ignore)
    }
    if (signal?.aborted) return false
    s.setCapabilities(capabilities)
    // A deep link (`aether://run/<id>` from either shell) arrives as
    // `?run=<id>` and can only be acted on now that the runs are here. A
    // member is sent the runs they may see, so an id that is not among them
    // is not theirs or no longer exists: the board stays, rather than
    // mounting a run detail for something nothing can load. Before the
    // onboarding redirect below, which outranks it.
    const requested = takeRequestedRun()
    if (requested && store.getState().runs[requested]) {
      store.getState().navigate('terminal', { runId: requested })
    }
    // The status bar's link chip reads linkStatus, and nothing else
    // fetches it until the settings or onboarding view opens - so without
    // this, a linked machine launches looking unlinked and the chip points
    // at onboarding on every start. Local gateways only, and isolated: a
    // failed poll must not fail the hydration.
    if (store.getState().capabilities?.local?.includes('link.status')) {
      try {
        const linkStatus = await client.localLinkStatus()
        if (signal?.aborted) return false
        s.setLinkStatus(linkStatus)
        if (linkStatus.linked === true) s.setOnboarded(true)
      } catch {
        ignore()
      }
    }
    if (signal?.aborted) return false
    s.setHydrated(true)
    if (
      !store.getState().onboarded &&
      capabilities?.local?.includes('link.status') === true
    ) {
      store.setState({ route: { name: 'onboarding', params: {} } })
    }
    s.setUnreachable(null)
    return true
  } catch (err) {
    if (signal?.aborted) return false
    // A failed re-hydration keeps the data we already have; only the error
    // is new. Once the token is known dead, the recorded recovery hint is
    // more useful than this raw failure, so it stays.
    if (!store.getState().streamDead) {
      s.setUnreachable(classifyUnreachable(err, store))
      s.setHydrated(s.hydrated, err instanceof Error ? err.message : String(err))
    }
    return false
  }
}

/**
 * Refetches room history until a page reaches the cache boundary. A realtime
 * event only names the mutation, so one newest page is not enough when the
 * client missed a burst larger than that page during a disconnect.
 */
async function reconcileRoomHistory(
  store: RootStore,
  client: Api,
  workspaceID: string,
  runID: string,
  targetMessageID?: string,
): Promise<void> {
  const cached = store.getState().roomMessages[runID] ?? []
  const cachedIDs = new Set(cached.map((message) => message.id))
  const oldestCachedID = cached[0]?.id
  let before: string | undefined
  let append = cached.length === 0
  const visited = new Set<string>()

  while (true) {
    const page = await client.runRoomList({
      workspace_id: workspaceID,
      run_id: runID,
      ...(before === undefined ? {} : { before }),
      limit: 100,
    })
    store.getState().setRoomPage(runID, page.messages, page.next_before, append)
    const containsTarget = targetMessageID
      ? page.messages.some((message) => message.id === targetMessageID)
      : false
    const overlapsBoundary = oldestCachedID
      ? page.messages.some((message) => message.id === oldestCachedID)
      : page.messages.some((message) => cachedIDs.has(message.id))
    if (
      containsTarget ||
      overlapsBoundary ||
      !page.next_before ||
      visited.has(page.next_before)
    ) return
    visited.add(page.next_before)
    before = page.next_before
    append = true
  }
}

/**
 * Evidence events are summaries by design. Walk every missed page until the
 * existing cache boundary, so a burst larger than one page remains fillable.
 */
async function reconcileEvidenceHistory(
  store: RootStore,
  client: Api,
  workspaceID: string,
  runID: string,
): Promise<void> {
  const cached = store.getState().evidencePackets[runID] ?? []
  const cachedIDs = new Set(cached.map((packet) => packet.id))
  const oldestCachedID = cached[0]?.id
  let before: string | undefined
  let append = cached.length === 0
  const visited = new Set<string>()

  while (true) {
    const page = await client.runEvidenceList({
      workspace_id: workspaceID,
      run_id: runID,
      ...(before === undefined ? {} : { before }),
      limit: 100,
    })
    store.getState().setEvidencePage(runID, page.packets, page.next_before, append)
    const overlapsBoundary = oldestCachedID
      ? page.packets.some((packet) => packet.id === oldestCachedID)
      : page.packets.some((packet) => cachedIDs.has(packet.id))
    if (overlapsBoundary || !page.next_before || visited.has(page.next_before)) return
    visited.add(page.next_before)
    before = page.next_before
    append = true
  }
}

/**
 * Applies one event and reports whether it resolved. Await it, and await it in
 * sequence order: an event about a run the store has never seen has to fetch
 * that run first, and the cursor must never move past an event still waiting
 * on a fetch. False means the event could not be resolved at all, and only a
 * fresh snapshot repairs the store.
 *
 * Every state read happens after the awaits, never before them.
 */
export async function applyEvent(
  store: RootStore,
  ev: Event,
  client: Api = api,
): Promise<boolean> {
  if (ev.seq > 0 && ev.seq <= store.getState().lastSeq) {
    // Replay resumes strictly after the cursor, so an equal sequence is a
    // duplicate. One below it means the server's event log restarted - a
    // fresh or restored data dir numbers from scratch - and every event
    // would be dropped forever: forget the cursor and take a fresh snapshot,
    // exactly as a reconnect with no cursor does.
    if (ev.seq === store.getState().lastSeq) return true
    store.getState().resetSeq()
    return false
  }
  if (ev.type === 'workspace.deleted') {
    // Record deletion before any fetch: every list writer must reject older
    // snapshots, even while this event's own reconciliation is pending.
    store.getState().removeWorkspace(ev.workspace_id)
  }

  // Workspaces arrive only by fetch, so an event for one we do not know means
  // a teammate created it after we hydrated. Without this its runs would be
  // stored but rendered nowhere.
  if (ev.type !== 'workspace.deleted' && ev.workspace_id && !store.getState().workspaces[ev.workspace_id]) {
    await client
      .workspaceListFull()
      .then(store.getState().setWorkspaces)
      .catch(ignore)
  }

  // Members likewise: no member.* event exists, so an actor we have never
  // seen is a teammate who joined after we hydrated. Without the re-read
  // their name renders as a raw ID everywhere.
  if (ev.actor_id && !store.getState().members[ev.actor_id]) {
    await client.memberList().then(store.getState().setMembers).catch(ignore)
  }

  if (ev.type === 'mission.changed' && ev.workspace_id) {
    try {
      const runs = await client.runList({ workspace_id: ev.workspace_id })
      const state = store.getState()
      state.setRuns([
        ...Object.values(state.runs).filter((run) => run.workspace_id !== ev.workspace_id),
        ...runs,
      ])
    } catch (err) {
      store.getState().setUnreachable(classifyUnreachable(err, store))
      return false
    }
  }

  // Mission events are scoped projection hints. Never let an event from a
  // background workspace refresh the mission currently visible in this tab.
  if (ev.type.startsWith('mission.') && ev.workspace_id) {
    const state = store.getState()
    if (state.activeWorkspace === ev.workspace_id) {
      const current = state.route
      const missionID =
        current.name === 'missions' ? current.params.missionId : undefined
      let changedMissionID: string | undefined
      if (typeof ev.payload === 'object' && ev.payload !== null && 'mission_id' in ev.payload) {
        const candidate = ev.payload.mission_id
        if (typeof candidate === 'string') changedMissionID = candidate
      }
      const currentMission = missionID ? state.missions[missionID] : undefined
      if (
        missionID &&
        changedMissionID === missionID &&
        (!currentMission || currentMission.workspace_id === ev.workspace_id)
      ) {
        await client
          .missionShow(missionID)
          .then((result) => {
            if (result.mission.workspace_id !== ev.workspace_id) return
            store.getState().setMissionDetail({
              mission: result.mission,
              tasks: result.tasks,
              attempts: result.attempts ?? [],
              submissions: result.submissions ?? [],
              diagnostics: result.diagnostics ?? [],
              questions: result.questions ?? [],
              plan_reviews: result.plan_reviews ?? [],
            })
          })
          .catch(ignore)
      } else {
        await client
          .missionList({ workspace_id: ev.workspace_id, limit: 50 })
          .then((result) => {
            const state = store.getState()
            // Merged, not replaced, so older pages the reader loaded stay.
            // Everything newer than the stored cursor is still held, so it
            // still marks the next older page; a cursor read for another
            // workspace, or none, gives way to the fetched one.
            const loaded = state.missionListWorkspace === ev.workspace_id
            state.setMissions(
              ev.workspace_id,
              result.missions,
              loaded ? state.missionNextCursor ?? undefined : result.next_cursor,
              true,
            )
          })
          .catch(ignore)
      }
    }
  }

  switch (ev.type) {
    case 'workspace.deleted': {
      try {
        store.getState().setWorkspaces(await client.workspaceListFull())
      } catch (err) {
        store.getState().setUnreachable(classifyUnreachable(err, store))
        return false
      }
      break
    }
    case 'run.deleted':
      store.getState().removeRun(ev.run_id)
      break
    case 'run.status': {
      const p = ev.payload as RunStatusPayload
      if (!store.getState().runs[ev.run_id]) {
        // A run launched by someone else after we hydrated. Fetching it here,
        // before the event is applied and before the cursor moves, is what
        // keeps two transitions of a brand new run in order.
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          // A live delete publishes its final status before run.deleted, so
          // the local removal can make this status fetch return 404.
          if (!(err instanceof ApiError && err.status === 404)) {
            store.getState().setUnreachable(classifyUnreachable(err, store))
            return false
          }
        }
      }
      store.getState().applyRunStatus(ev.run_id, p.to, p.reason, ev.time)
      break
    }
    case 'run.title': {
      const p = ev.payload as RunTitlePayload
      if (!store.getState().runs[ev.run_id]) {
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          store.getState().setUnreachable(classifyUnreachable(err, store))
          return false
        }
      }
      store.getState().applyRunTitle(ev.run_id, p.title)
      break
    }
    case 'run.protected': {
      const p = ev.payload as RunProtectedPayload
      if (!store.getState().runs[ev.run_id]) {
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          store.getState().setUnreachable(classifyUnreachable(err, store))
          return false
        }
      }
      store.getState().applyRunProtected(ev.run_id, p.protected)
      break
    }
    case 'run.archived': {
      const p = ev.payload as RunArchivedPayload
      if (!store.getState().runs[ev.run_id]) {
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          store.getState().setUnreachable(classifyUnreachable(err, store))
          return false
        }
      }
      store.getState().applyRunArchived(ev.run_id, p.archived_at, p.deletes_at)
      break
    }
    case 'run.diff': {
      // A snapshot carries per-file stats and the two trees bounding the
      // interval it ended. It is the timeline entry the Diff tab lists, the
      // range that tab asks for, and the signal that the run's cumulative
      // patch text is behind.
      const p = ev.payload as RunDiffPayload
      store.getState().noteDiffSnapshot(ev.run_id, {
        time: ev.time,
        files: p.files ?? [],
        tree: p.tree,
        parentTree: p.parent_tree,
      })
      break
    }
    case 'server.update': {
      store.getState().applyServerUpdate(ev.payload as ServerUpdatePayload)
      break
    }
    case 'git.branch': {
      const p = ev.payload as GitBranchPayload
      if (!store.getState().runs[ev.run_id]) {
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          store.getState().setUnreachable(classifyUnreachable(err, store))
          return false
        }
      }
      if (p.commit) store.getState().applyLastCommit(ev.run_id, p.commit, ev.time)
      break
    }
    case 'run.overlap': {
      // The radar names the peer runs, not who owns them; the member comes
      // from the run the client already holds.
      const p = ev.payload as OverlapPayload
      const s = store.getState()
      s.applyOverlap(
        ev.run_id,
        (p.with ?? []).map((peer) => ({
          ...peer,
          member_id: s.runs[peer.run_id]?.member_id ?? '',
        })),
      )
      break
    }
    case 'workspace.timeline': {
      // A paused run still reads `running`, so the board's paused badge comes
      // from the pause and resume steering entries.
      const paused = pausedFromTimeline(ev.payload)
      if (paused !== null && ev.run_id) store.getState().setPaused(ev.run_id, paused)
      // A handoff publishes no run.status event, so the new owner arrives
      // with nothing else to carry it: re-read the run.
      const kind = (ev.payload as { kind?: string } | null)?.kind
      if (kind === 'handoff' && ev.run_id) {
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          store.getState().setUnreachable(classifyUnreachable(err, store))
          return false
        }
      }
      break
    }
    case 'workspace.room_message': {
      // Room event payloads intentionally contain no message body. A question
      // or reply changes the server-computed attention count even when the
      // room has never been opened; an open room still refreshes its durable
      // history below.
      const runID = ev.run_id
      const workspaceID = ev.workspace_id
      const payload = (ev.payload ?? {}) as { kind?: string; message_id?: string }
      if (
        runID &&
        (payload.kind === 'question' || payload.kind === 'reply')
      ) {
        try {
          store.getState().upsertRun(await client.runGet(runID))
        } catch (err) {
          // Without this snapshot the attention badge can remain stale. Leave
          // the event unresolved so the stream performs authoritative recovery.
          store.getState().setUnreachable(classifyUnreachable(err, store))
          return false
        }
      }
      if (runID && workspaceID && store.getState().roomMessages[runID]) {
        await reconcileRoomHistory(store, client, workspaceID, runID, payload.message_id)
          .then(() => store.getState().setRoomError(runID))
          .catch((err) => {
            store.getState().setRoomError(
              runID,
              err instanceof Error ? err.message : String(err),
            )
          })
      }
      break
    }
    case 'workspace.evidence_packet': {
      // Evidence events are summaries by design. Refetch every page of an
      // already visible list until the cache boundary, preserving lazy
      // loading for runs with no drawer open.
      const runID = ev.run_id
      const workspaceID = ev.workspace_id
      if (runID && workspaceID && store.getState().evidencePackets[runID]) {
        await reconcileEvidenceHistory(store, client, workspaceID, runID)
          .then(() => store.getState().setEvidenceError(runID))
          .catch((err) => {
            store.getState().setEvidenceError(
              runID,
              err instanceof Error ? err.message : String(err),
            )
          })
      }
      break
    }
  }
  store.getState().noteSeq(ev.seq)
  return true
}

/** What the capabilities probe before the stream found, or null for
 * "nothing special: open the stream". */
type Probe =
  | { unlinked: { capabilities: GatewayCapabilities; status: LinkStatus } }
  | { rejected: string }
  | { refused: ApiError }

/**
 * Reads the capabilities descriptor before anything else, because two
 * answers change what the app does next: a local gateway with no server
 * configured goes to onboarding, and a 401 means the gateway rejected the
 * credential. The 401 matters most on a phone, where the token lives in
 * per-tab session storage: without this the WebSocket upgrade would be
 * rejected the same way, the socket would retry forever, and the app would
 * blame an unreachable server. A 403 or 503 is the gateway refusing this
 * caller outright - a tagged tailnet node, a WhoIs outage - whose reason
 * only an HTTP body carries, so it is recorded before the stream's own
 * failure can only say "unreachable".
 */
async function probeGateway(
  store: RootStore,
  client: Api,
  signal: AbortSignal,
): Promise<Probe | null> {
  let capabilities: GatewayCapabilities
  try {
    capabilities = await client.capabilities()
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return { rejected: err.message }
    if (err instanceof ApiError && (err.status === 403 || err.status === 503)) return { refused: err }
    return null
  }
  if (signal.aborted) return null
  // Hydration writes the same descriptor, but a hydration that never
  // succeeds writes nothing - and classifying its failure needs to know
  // which gateway serves the page.
  store.getState().setCapabilities(capabilities)
  try {
    if (!capabilities.local?.includes('link.status')) return null
    const status = await client.localLinkStatus()
    return status.server_configured ? null : { unlinked: { capabilities, status } }
  } catch {
    return null
  }
}

/**
 * Subscribes, hydrates and follows the event stream for as long as the app is
 * mounted. Returns a disposer.
 *
 * Three orderings matter.
 *
 * The subscription comes first: hydration starts only once the server has
 * acknowledged it, so a change between the snapshot and the subscription
 * cannot fall in the gap. Events that arrive while the snapshot is in flight
 * wait in the queue and are applied after it, so an older snapshot never
 * overwrites a newer event.
 *
 * Events are then applied one at a time, in sequence order, each one fully
 * resolved before the next begins. That is what keeps the single global
 * cursor honest: it can never move past an event still waiting on a fetch.
 *
 * And a reconnect with no cursor cannot replay, so the client re-fetches
 * instead of subscribing live and missing the outage.
 */
export function connect(store: RootStore, client: Api = api): () => void {
  const lifecycle = new AbortController()
  const { signal } = lifecycle
  let hydrating = false
  let attempts = 0
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let subscribed = false
  const queue: Event[] = []
  let chain: Promise<void> = Promise.resolve()
  let stopStream: () => void = () => {}
  let selectionWrite = Promise.resolve()
  const stopSelection = store.subscribe((state, previous) => {
    if (!state.hydrated || !state.capabilities?.local?.includes('workspace.selection')) return
    if (state.activeWorkspace === previous.activeWorkspace && previous.hydrated) return
    const workspace = state.activeWorkspace
    selectionWrite = selectionWrite
      .then(() => client.localWorkspaceSelection(workspace))
      .then(() => {})
      .catch((err: unknown) => {
        if (!signal.aborted) {
          store.getState().setHydrated(true, err instanceof Error ? err.message : String(err))
        }
      })
  })

  const drain = async () => {
    while (!signal.aborted && !hydrating && queue.length > 0) {
      const ev = queue.shift() as Event
      if (await applyEvent(store, ev, client)) continue
      // The event named something we could not fetch. A fresh snapshot is the
      // repair; the rest of the queue waits for it.
      void load()
      return
    }
  }

  const pump = () => {
    chain = chain.then(drain).catch(ignore)
  }

  const load = async () => {
    if (signal.aborted || hydrating || store.getState().streamDead) return
    hydrating = true
    await chain // let an event that is mid-flight finish first
    const ok = await hydrate(store, client, signal)
    hydrating = false
    if (signal.aborted) return
    if (!ok) {
      retryTimer = setTimeout(() => {
        retryTimer = null
        void load()
      }, backoff(attempts++))
      return
    }
    attempts = 0
    pump()
  }

  const startStream = () => {
    stopStream = connectEvents({
      onEvent: (ev) => {
        queue.push(ev)
        pump()
      },
      onState: (state) => {
        store.getState().setConnection(state)
        if (
          state === 'offline' &&
          !store.getState().hydrated &&
          !store.getState().hydrationError
        ) {
          // The stream cannot even be established and nothing has been fetched:
          // say so, rather than animating skeletons forever. An error already
          // recorded - a 401 hydration, a dead token - is more precise than
          // this one, so it stays.
          store.getState().setHydrated(false, 'the server is unreachable')
        }
        if (state !== 'live') return
        // The subscription is installed. Hydrate behind it on the first connect,
        // and again on a reconnect that has no cursor to replay from - or one
        // that came while the server was replacing its own binaries, because
        // that is a server that may have just re-executed on a new version.
        // Only a fresh server.info says it did, and the update banner and the
        // notice in the status bar both end on that answer.
        // Tailnet reconnects can change member even when replay is possible.
        const s = store.getState()
        if (!subscribed || s.lastSeq === 0 || serverUpdateApplying(s.serverUpdateProgress) || s.capabilities?.gateway !== 'local') {
          void load()
        }
        subscribed = true
      },
      onUnreachable: (kind, detail) => {
        // The gateway answered and named the failing hop: either this
        // machine's own network, or the SSH tunnel to aether-server. Either
        // way the gateway itself is fine.
        const s = store.getState()
        s.setUnreachable(kind)
        // A refused subscribe never goes live, so hydration never runs and
        // nothing else will ever record what happened. Keep an error already
        // recorded: a dead token is more precise than a dead hop.
        if (!s.hydrated && !s.streamDead) s.setHydrated(false, detail)
      },
      afterSeq: () => store.getState().lastSeq,
    })
  }

  // The hydration retry is the third timer a frozen tab stops, and the only
  // one the reopened sockets cannot restart: a re-hydration that failed after
  // the first good one leaves a cursor to replay from, so the stream goes
  // live again without re-fetching. Without this the store would show stale
  // data for the rest of a wait that also caps at 30 seconds. A wake with no
  // retry pending re-fetches nothing.
  const stopWake = onWake(() => {
    if (signal.aborted || !retryTimer) return
    clearTimeout(retryTimer)
    retryTimer = null
    attempts = 0
    void load()
  })

  void probeGateway(store, client, signal).then((probe) => {
    if (signal.aborted) return
    if (probe && 'rejected' in probe) {
      // Every reconnect would carry the same rejected credential, so the
      // stream is never opened. The flag is what makes the panes and the
      // error page say the link expired instead of claiming a retry that
      // never comes, and the gateway's own message is kept verbatim.
      store.getState().setStreamDead()
      store.getState().setConnection('offline')
      store.getState().setHydrated(false, probe.rejected)
      return
    }
    if (probe && 'refused' in probe) {
      // The gateway said why; the handshake status never reaches this code,
      // so the stream keeps retrying and this stays the reason. A 403 is
      // the gateway turning this device away, and a 503 naming the tailnet
      // identity is its own daemon not answering; neither is a dead hop.
      const { refused } = probe
      store.getState().setUnreachable(refusalKind(refused, store))
      // The client prefixes its own request path; the gateway's words are
      // what the page shows.
      store.getState().setHydrated(false, refused.message.replace(/^[^\s:]+: /, ''))
    } else if (probe) {
      // No server to connect to yet: the onboarding wizard links first.
      store.getState().setCapabilities(probe.unlinked.capabilities)
      store.getState().setLinkStatus(probe.unlinked.status)
      store.getState().setConnection('offline')
      store.getState().setHydrated(true)
      store.getState().setUnreachable(null)
      store.setState({ route: { name: 'onboarding', params: {} } })
      return
    }
    startStream()
  })

  return () => {
    lifecycle.abort()
    stopWake()
    stopSelection()
    if (retryTimer) clearTimeout(retryTimer)
    stopStream()
  }
}

// A workspace list we could not refresh leaves the store as it was; the next
// event for that workspace tries again.
function ignore(): void {}
