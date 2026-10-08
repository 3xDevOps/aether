import { toast } from 'sonner'
import { api, ApiError, type Api } from '@/lib/api'
import { edgeHost, errorSentence } from '@/lib/format'
import { backoff, connectEvents, onWake } from '@/lib/stream'
import { redirectRoute } from '@/lib/url-state'
import type {
  CoordMessageAckedPayload,
  CoordMessagePayload,
  Event,
  GatewayCapabilities,
  GitBranchPayload,
  LinkStatus,
  OverlapPayload,
  RunArchivedPayload,
  RunControllerPayload,
  RunDiffPayload,
  RunInputPayload,
  RunProtectedPayload,
  RunRetentionPayload,
  RunStatusPayload,
  RunTitlePayload,
  ServerUpdatePayload,
} from '@/lib/types'
import type { RootStore } from '@/store'
import type { AgentPayload } from '@/store/activity'
import { applyApprovalEvent } from '@/store/approvals'
import { batchNotifications } from '@/store/batch'
import { coalesce } from '@/store/coalesce'
import { pausedFromTimeline } from '@/store/board'
import { scheduleBudgetRead, type BudgetPayload } from '@/store/cost'
import { inMessageScope, loadMessagePage } from '@/store/messages'
import { watchOutcomeSeen } from '@/store/outcome-seen'
import { serverUpdateApplying, type UnreachableKind } from '@/store/server'

/**
 * The local gateway reports a dead transport as 503 with the failing hop in the
 * message ("network unreachable: ..." or "server unreachable: ..."). A fetch
 * TypeError means the origin serving this page is gone.
 */
function classifyUnreachable(err: unknown, store: RootStore): UnreachableKind | null {
  if (err instanceof ApiError && err.status === 503) {
    if (err.message.includes('network unreachable')) return 'network'
    if (err.message.includes('server unreachable')) return edgeHop(err.message, store) ?? 'server'
  }
  if (err instanceof TypeError) {
    // An unknown gateway is the desktop one: it is the only surface that can
    // fail before the descriptor is read, since the probe seeds it.
    return store.getState().capabilities?.gateway === 'server' ? 'tailnet' : 'gateway'
  }
  return null
}

/** Null when the link is not through an edge or the error matches none of these. */
function edgeHop(detail: string, store: RootStore): UnreachableKind | null {
  const edge = store.getState().linkStatus?.edge_url
  if (!edge) return null
  if (detail.includes('server is not connected to the edge')) return 'edge-server'
  if (detail.includes('device token revoked') || detail.includes('not signed in')) {
    return 'signed-out'
  }
  if (detail.includes('was revoked on this server')) return 'device-revoked'
  if (detail.includes('is waiting for approval')) return 'device-pending'
  // The edge's refusal and the server's SSH banner word it the same way.
  if (detail.includes('not a member of this server')) return 'not-member'
  if (detail.includes(`${edgeHost(edge)} refused: `)) return 'edge-refused'
  if (detail.includes(`${edgeHost(edge)}: `)) return 'edge'
  return null
}


function refusalKind(err: ApiError, store: RootStore): UnreachableKind | null {
  if (err.status === 403) return 'refused'
  if (err.message.includes('tailnet identity unavailable')) return 'identity'
  return classifyUnreachable(err, store)
}

/** False means the fetch failed or its owner was disposed. */
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
        // An unreachable conflict radar leaves the chips off; it must not fail hydration.
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
        toast.error(errorSentence(err))
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
    // Nothing else fetches linkStatus before settings or onboarding opens, so
    // without this a linked machine launches looking unlinked. Must not fail hydration.
    if (store.getState().capabilities?.local?.includes('link.status')) {
      try {
        const linkStatus = await client.localLinkStatus()
        if (signal?.aborted) return false
        s.setLinkStatus(linkStatus)
        if (!s.hydrated && linkStatus.linked === true && !s.onboardingWorkspace) s.setOnboarded(true)
      } catch {
        ignore()
      }
    }
    if (signal?.aborted) return false
    s.setHydrated(true)
    const current = store.getState()
    const linked = current.route.params.runId
    const unknownLink = !s.hydrated && current.route === s.route && !!linked && !current.runs[linked]
    if (
      !s.hydrated &&
      (s.route.name === 'board' || unknownLink) &&
      current.route === s.route &&
      !current.onboarded &&
      (capabilities?.local?.includes('link.status') === true ||
        ((!current.onboardingWorkspace ||
          workspaces.length === 0 ||
          !!current.workspaces[current.onboardingWorkspace]) &&
          (capabilities?.methods.includes('*') ||
            (capabilities?.methods.includes('member.git') && capabilities.methods.includes('agent.list')))))
    ) {
      redirectRoute(store, { name: 'onboarding', params: {} })
    }
    else if (unknownLink) redirectRoute(store, { name: 'board', params: {} })
    s.setUnreachable(null)
    return true
  } catch (err) {
    if (signal?.aborted) return false
    // A failed re-hydration keeps existing data. Once the token is known dead,
    // its recorded recovery hint beats this raw failure.
    if (!store.getState().streamDead) {
      s.setUnreachable(classifyUnreachable(err, store))
      s.setHydrated(s.hydrated, err instanceof Error ? err.message : String(err))
    }
    return false
  }
}

/** A realtime event only names the mutation, so a missed burst larger than one
 * page needs every page back to the cache boundary. */
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

/** Evidence events are summaries, so walk every missed page to the cache boundary. */
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
 * Await in sequence order: the cursor must never move past an event still
 * waiting on a fetch. False means only a fresh snapshot repairs the store.
 * Every state read happens after the awaits, never before them.
 */
export async function applyEvent(
  store: RootStore,
  ev: Event,
  client: Api = api,
): Promise<boolean> {
  if (ev.seq > 0 && ev.seq <= store.getState().lastSeq) {
    // Replay resumes strictly after the cursor, so equal is a duplicate. Below
    // it means the server's event log restarted (fresh or restored data dir):
    // forget the cursor and take a fresh snapshot.
    if (ev.seq === store.getState().lastSeq) return true
    store.getState().resetSeq()
    return false
  }
  if (ev.type === 'workspace.deleted') {
    // Record deletion before any fetch: every list writer must reject older
    // snapshots, even while this event's own reconciliation is pending.
    store.getState().removeWorkspace(ev.workspace_id)
  }

  // No workspace.created event exists; without this a new workspace's runs
  // would be stored but rendered nowhere.
  if (ev.type !== 'workspace.deleted' && ev.workspace_id && !store.getState().workspaces[ev.workspace_id]) {
    await client
      .workspaceListFull()
      .then(store.getState().setWorkspaces)
      .catch(ignore)
  }

  // A new teammate has no event of their own; without this they render as a raw ID.
  if (ev.actor_id && !store.getState().members[ev.actor_id]) {
    await client.memberList().then(store.getState().setMembers).catch(ignore)
  }

  if (ev.type === 'mission.changed' && (ev.payload as { deleted?: boolean } | null)?.deleted) {
    const missionID = (ev.payload as { mission_id: string }).mission_id
    const state = store.getState()
    state.removeMission(missionID)
    if (state.route.name === 'missions' && state.route.params.missionId === missionID) state.navigate('missions')
    return true
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
            })
          })
          .catch(ignore)
      } else {
        await client
          .missionList({ workspace_id: ev.workspace_id, limit: 50 })
          .then((result) => {
            const state = store.getState()
            // Merged, not replaced, so loaded older pages stay and the stored
            // cursor still marks the next older page for this workspace.
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
    case 'member.changed': {
      const p = ev.payload as { member_id: string; display_name: string }
      await client.memberList().then(store.getState().setMembers).catch(ignore)
      const info = store.getState().info
      if (info && info.member.id === p.member_id) {
        store.getState().setInfo({ ...info, member: { ...info.member, display_name: p.display_name } })
      }
      break
    }
    case 'run.status': {
      const p = ev.payload as RunStatusPayload
      if (!store.getState().runs[ev.run_id]) {
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
      store.getState().applyRunStatus(ev.run_id, p.to, p.reason, ev.time, p.outcome_unseen)
      break
    }
    case 'run.retention': {
      if (!store.getState().runs[ev.run_id]) {
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          if (!(err instanceof ApiError && err.status === 404)) {
            store.getState().setUnreachable(classifyUnreachable(err, store))
            return false
          }
        }
      }
      const current = store.getState().runs[ev.run_id]
      const p = ev.payload as RunRetentionPayload
      if (current) store.getState().upsertRun({
        ...current,
        container_retained_until: p.container_retained_until,
        cleanup_pending: p.cleanup_pending,
        cleanup_error: p.cleanup_error,
      })
      break
    }
    case 'run.outcome_seen':
      store.getState().applyOutcomeSeen(ev.run_id)
      break
    case 'run.input': {
      if (!store.getState().runs[ev.run_id]) {
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          if (!(err instanceof ApiError && err.status === 404)) {
            store.getState().setUnreachable(classifyUnreachable(err, store))
            return false
          }
        }
      }
      const p = ev.payload as RunInputPayload
      store.getState().applyRunInput(ev.run_id, p.pending_inputs)
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
    case 'run.mode': {
      try {
        store.getState().upsertRun(await client.runGet(ev.run_id))
      } catch (err) {
        if (!(err instanceof ApiError && err.status === 404)) {
          store.getState().setUnreachable(classifyUnreachable(err, store))
          return false
        }
      }
      break
    }
    case 'run.controller':
      // A run this client has not loaded arrives with the holder in its snapshot.
      store.getState().applyRunController(ev.run_id, (ev.payload as RunControllerPayload).member_id)
      break
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
      store.getState().applyRunArchived(ev.run_id, p.archived_at)
      break
    }
    case 'run.diff': {
      // Also signals that the run's cumulative patch text is behind.
      const p = ev.payload as RunDiffPayload
      store.getState().noteDiffSnapshot(ev.run_id, {
        time: ev.time,
        files: p.files ?? [],
        tree: p.tree,
        parentTree: p.parent_tree,
        historyGap: p.history_gap,
        snapshotError: p.snapshot_error,
      })
      break
    }
    case 'workspace.approval':
      applyApprovalEvent(store, client, ev)
      break
    case 'workspace.budget':
      if (ev.workspace_id) store.getState().applyBudgetEvent(ev.workspace_id, ev.payload as BudgetPayload)
      break
    case 'run.cost': {
      // Budget events fire only on thresholds and edits; spend moves with every
      // result. The read does not hold up the events behind it.
      if (ev.workspace_id) scheduleBudgetRead(store, client, ev.workspace_id)
      break
    }
    case 'workspace.presence':
      // The payload names one transition, not the roster it changes.
      coalesce(store, 'presence', () =>
        client.presenceRoster().then(store.getState().setPresence).catch(ignore),
      )
      break
    case 'run.agent':
      // Activity is a hint for the state line: an event about a run this
      // client has not loaded is not worth a fetch.
      store.getState().applyAgentEvent(ev.run_id, (ev.payload ?? {}) as AgentPayload, ev.time)
      break
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
      // Payloads carry no message body, yet a question or reply changes the
      // server-computed attention count even for a room never opened.
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
          // Left unresolved so the stream recovers; else the attention badge stays stale.
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
    case 'coord.message': {
      // The event carries no body: every loaded list the message belongs to
      // re-reads its newest page.
      const p = ev.payload as CoordMessagePayload
      const scopes = Object.values(store.getState().messageLists)
        .map((list) => list.scope)
        .filter((scope) => inMessageScope(scope, p))
      await Promise.all(scopes.map((scope) => loadMessagePage(store, client, scope)))
      refreshUnacked(store, client, p.to_run_id)
      break
    }
    case 'coord.message.acked': {
      const p = ev.payload as CoordMessageAckedPayload
      store.getState().applyMessageAcked(p.message_id, p.acked_at)
      refreshUnacked(store, client, p.to_run_id)
      break
    }
    case 'workspace.evidence_packet': {
      // Only lists already loaded; a run whose Captures were never opened stays lazy.
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
  store.getState().appendLiveEvent(ev)
  store.getState().appendSessionEvent(ev)
  store.getState().noteSeq(ev.seq)
  return true
}

/**
 * Off the event queue, and only the unread fields: a full snapshot landing late
 * would undo run events applied after it was read. A failed read is dropped.
 */
function refreshUnacked(store: RootStore, client: Api, runID: string): void {
  if (!store.getState().runs[runID]) return
  coalesce(store, `unacked:${runID}`, () =>
    client
      .runGet(runID)
      .then((run) => store.getState().applyUnackedMessages(runID, run))
      .catch(ignore),
  )
}

/** Null means "nothing special: open the stream". */
type Probe =
  | { unlinked: { capabilities: GatewayCapabilities; status: LinkStatus } }
  | { rejected: string }
  | { refused: ApiError }

/**
 * Runs before the stream: a rejected WebSocket upgrade carries no body, so a
 * 401 would retry forever blaming an unreachable server, and a 403/503's
 * reason would be lost.
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
  // Classifying a hydration failure needs to know which gateway serves the page.
  store.getState().setCapabilities(capabilities)
  try {
    if (!capabilities.local?.includes('link.status')) return null
    const status = await client.localLinkStatus()
    if (signal.aborted) return null
    // Classifying a failure needs to know whether the link runs through an edge.
    store.getState().setLinkStatus(status)
    return status.server_configured ? null : { unlinked: { capabilities, status } }
  } catch {
    return null
  }
}

/**
 * Ordering invariants: hydration starts only after the subscription is acked,
 * and events queued meanwhile apply after the snapshot. Events apply one at a
 * time in sequence order. A reconnect with no cursor re-fetches.
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
    // The gateway keys the selection by the server's answer, so an unlinked
    // gateway, hydrated only for onboarding, has nowhere to save it.
    if (!state.hydrated || !state.info || !state.capabilities?.local?.includes('workspace.selection')) return
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

  const stopOutcomeSeen = watchOutcomeSeen(store, client)

  const missionRefreshes = new Set<string>()
  let refreshingMissions = false
  let missionGeneration = 0

  const refreshMissionRuns = () => {
    if (refreshingMissions || hydrating || signal.aborted || missionRefreshes.size === 0) return
    refreshingMissions = true
    const generation = missionGeneration
    void (async () => {
      while (missionRefreshes.size > 0 && !hydrating && !signal.aborted && generation === missionGeneration) {
        const workspaceID = missionRefreshes.values().next().value as string
        missionRefreshes.delete(workspaceID)
        try {
          const listed = await client.runList({ workspace_id: workspaceID })
          if (signal.aborted || generation !== missionGeneration) return
          store.setState((state) => {
            let runs = state.runs
            for (const run of listed) {
              const current = runs[run.id]
              if (!current || current.workspace_id !== workspaceID) continue
              if (
                current.mission_id === run.mission_id &&
                current.mission_role === run.mission_role &&
                current.integrator_run_id === run.integrator_run_id
              ) continue
              if (runs === state.runs) runs = { ...runs }
              // Status/title/delete events may have landed during this fetch.
              runs[run.id] = {
                ...current,
                mission_id: run.mission_id,
                mission_role: run.mission_role,
                integrator_run_id: run.integrator_run_id,
              }
            }
            return { runs }
          })
        } catch (err) {
          if (signal.aborted || generation !== missionGeneration) return
          store.getState().setUnreachable(classifyUnreachable(err, store))
          void load()
          return
        }
      }
    })().finally(() => {
      if (generation !== missionGeneration) return
      refreshingMissions = false
      refreshMissionRuns()
    })
  }

  // Listeners hear one change per drained burst rather than one per write.
  const drain = () => batchNotifications(store, async () => {
    while (!signal.aborted && !hydrating && queue.length > 0) {
      const ev = queue.shift() as Event
      if (await applyEvent(store, ev, client)) {
        if (ev.type === 'mission.changed' && ev.workspace_id) {
          missionRefreshes.add(ev.workspace_id)
          refreshMissionRuns()
        }
        continue
      }
      // The event named something we could not fetch. A fresh snapshot is the
      // repair; the rest of the queue waits for it.
      void load()
      return
    }
  })

  const pump = () => {
    chain = chain.then(drain).catch(ignore)
  }

  const load = async () => {
    if (signal.aborted || hydrating || store.getState().streamDead) return
    hydrating = true
    // Full hydration supersedes pending relationship snapshots, not vice versa.
    missionGeneration++
    refreshingMissions = false
    missionRefreshes.clear()
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
    refreshMissionRuns()
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
          // Say so rather than animate skeletons forever; an error already
          // recorded is more precise, so it stays.
          store.getState().setHydrated(false, 'the server is unreachable')
        }
        if (state !== 'live') return
        // Re-hydrate when there is no cursor, when the server may have just
        // re-executed on a new version (only a fresh server.info says so), or
        // on a tailnet reconnect, which can change member even with replay.
        const s = store.getState()
        if (!subscribed || s.lastSeq === 0 || serverUpdateApplying(s.serverUpdateProgress) || s.capabilities?.gateway !== 'local') {
          void load()
        }
        subscribed = true
      },
      onUnreachable: (kind, detail) => {
        const s = store.getState()
        s.setUnreachable(kind === 'server' ? edgeHop(detail, store) ?? kind : kind)
        // A refused subscribe never goes live, so nothing else records this.
        // A dead token already recorded is more precise than a dead hop.
        if (!s.hydrated && !s.streamDead) s.setHydrated(false, detail)
      },
      afterSeq: () => store.getState().lastSeq,
    })
  }

  // A frozen tab stops the hydration retry timer, and reopened sockets cannot
  // restart it: with a cursor to replay from they go live without re-fetching.
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
      // stream is never opened.
      store.getState().setStreamDead()
      store.getState().setConnection('offline')
      store.getState().setHydrated(false, probe.rejected)
      return
    }
    if (probe && 'refused' in probe) {
      // The handshake status never reaches the stream code, so this stays the
      // reason while the stream keeps retrying.
      const { refused } = probe
      store.getState().setUnreachable(refusalKind(refused, store))
      // The client prefixes its own request path; the gateway's words are
      // what the page shows.
      store.getState().setHydrated(false, refused.message.replace(/^[^\s:]+: /, ''))
    } else if (probe) {
      store.getState().setCapabilities(probe.unlinked.capabilities)
      store.getState().setLinkStatus(probe.unlinked.status)
      store.getState().setConnection('offline')
      store.getState().setHydrated(true)
      store.getState().setUnreachable(null)
      redirectRoute(store, { name: 'onboarding', params: {} })
      return
    }
    startStream()
  })

  return () => {
    lifecycle.abort()
    stopWake()
    stopSelection()
    stopOutcomeSeen()
    if (retryTimer) clearTimeout(retryTimer)
    stopStream()
  }
}

// A workspace list we could not refresh leaves the store as it was; the next
// event for that workspace tries again.
function ignore(): void {}
