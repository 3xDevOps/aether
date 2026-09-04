// Hydration and live updates: one HTTP fetch fills the store, then the event
// stream is the only thing that changes it.

import { api, ApiError, type Api } from '@/lib/api'
import { backoff, connectEvents } from '@/lib/stream'
import type {
  EnvironmentBuildPayload,
  EnvironmentEditPayload,
  Event,
  GitBranchPayload,
  OverlapPayload,
  RunDiffPayload,
  RunStatusPayload,
  ServerUpdatePayload,
  RunTitlePayload,
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
 * (a TypeError from fetch) means the gateway origin itself is gone.
 * Anything else - a 401, a 500 the server produced - is neither.
 */
function classifyUnreachable(err: unknown): UnreachableKind | null {
  if (err instanceof ApiError && err.status === 503) {
    if (err.message.includes('network unreachable')) return 'network'
    if (err.message.includes('server unreachable')) return 'server'
  }
  if (err instanceof TypeError) return 'gateway'
  return null
}

/** Fills the store from the server. False means the server was unreachable. */
export async function hydrate(store: RootStore, client: Api = api): Promise<boolean> {
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
    s.setInfo(info)
    s.setWorkspaces(workspaces)
    // Every scoped surface reads activeWorkspace, so it must name a
    // workspace that exists: an unset one, or one deleted while we were
    // away, falls back to the first by id rather than leaving the app
    // pointed at nothing.
    // Read after the fetches: `s` is the pre-await snapshot.
    const active = store.getState().activeWorkspace
    if (!active || !workspaces.some((w) => w.id === active)) {
      const first = [...workspaces].sort((a, b) => a.id.localeCompare(b.id))[0]
      if (first) s.setActiveWorkspace(first.id)
    }
    s.setMembers(members)
    s.setRuns(runs)
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
    s.setCapabilities(capabilities)
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
    // A failed re-hydration keeps the data we already have; only the error
    // is new. Once the token is known dead, the recorded recovery hint is
    // more useful than this raw failure, so it stays.
    if (!store.getState().streamDead) {
      s.setUnreachable(classifyUnreachable(err))
      s.setHydrated(s.hydrated, err instanceof Error ? err.message : String(err))
    }
    return false
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

  // Workspaces arrive only by fetch, so an event for one we do not know means
  // a teammate created it after we hydrated. Without this its runs would be
  // stored but rendered nowhere.
  if (ev.workspace_id && !store.getState().workspaces[ev.workspace_id]) {
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

  switch (ev.type) {
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
            store.getState().setUnreachable(classifyUnreachable(err))
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
          store.getState().setUnreachable(classifyUnreachable(err))
          return false
        }
      }
      store.getState().applyRunTitle(ev.run_id, p.title)
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
    case 'git.branch': {
      const p = ev.payload as GitBranchPayload
      if (!store.getState().runs[ev.run_id]) {
        try {
          store.getState().upsertRun(await client.runGet(ev.run_id))
        } catch (err) {
          store.getState().setUnreachable(classifyUnreachable(err))
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
          store.getState().setUnreachable(classifyUnreachable(err))
          return false
        }
      }
      break
    }
    case 'environment.build': {
      // One moment of a workspace image build. The slice keeps only the
      // latest coarse state; the banner on the First-run step and the run
      // view is its reader.
      const p = ev.payload as EnvironmentBuildPayload
      if (ev.workspace_id) store.getState().applyEnvBuild(ev.workspace_id, p)
      break
    }
    case 'server.update': {
      // The server updating itself. It is published once per workspace,
      // so the same phase lands several times; the slice keeps the
      // furthest one. The banner and the status bar read it live, and
      // `restarting` is the last frame before the socket drops.
      store.getState().applyServerUpdate(ev.payload as ServerUpdatePayload)
      break
    }
    case 'environment.edit': {
      // One moment of a server-side edit run. The slice keeps the coarse
      // state plus a line window; the Environment panel is its reader.
      const p = ev.payload as EnvironmentEditPayload
      if (ev.workspace_id) store.getState().applyEnvEdit(ev.workspace_id, p)
      break
    }
  }
  store.getState().noteSeq(ev.seq)
  return true
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
  let disposed = false
  let hydrating = false
  let attempts = 0
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let subscribed = false
  const queue: Event[] = []
  let chain: Promise<void> = Promise.resolve()

  const drain = async () => {
    while (!disposed && !hydrating && queue.length > 0) {
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
    if (disposed || hydrating || store.getState().streamDead) return
    hydrating = true
    await chain // let an event that is mid-flight finish first
    const ok = await hydrate(store, client)
    hydrating = false
    if (disposed) return
    if (!ok) {
      retryTimer = setTimeout(() => void load(), backoff(attempts++))
      return
    }
    attempts = 0
    pump()
  }

  const stopStream = connectEvents({
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
      const s = store.getState()
      if (!subscribed || s.lastSeq === 0 || serverUpdateApplying(s.serverUpdateProgress)) {
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
    onDead: (reason) => {
      // The token died, not the server: the stream has stopped for good, and
      // only a fresh token brings it back. The flag is what lets the panes
      // say so instead of claiming a retry that will never come, and the
      // pending hydrate retry is cancelled so a later 401 cannot overwrite
      // the recovery hint.
      if (retryTimer) clearTimeout(retryTimer)
      store.getState().setStreamDead()
      store.getState().setHydrated(false, `${reason}; mint one with \`aether gui\``)
    },
    afterSeq: () => store.getState().lastSeq,
  })

  return () => {
    disposed = true
    if (retryTimer) clearTimeout(retryTimer)
    stopStream()
  }
}

// A workspace list we could not refresh leaves the store as it was; the next
// event for that workspace tries again.
function ignore(): void {}
