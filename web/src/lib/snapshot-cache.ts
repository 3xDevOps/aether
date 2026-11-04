import type { GatewayCapabilities, MemberList, Run, ServerInfo, Workspace } from '@/lib/types'
import type { RootStore } from '@/store'

// docs/privacy.md#what-the-app-stores-on-the-phone says what this holds.
const keyPrefix = 'aether.snapshot.v1:'
const maxBytes = 1_000_000

interface Saved {
  savedAt: number
  identity: string
  info: Omit<ServerInfo, 'disk' | 'diskError'>
  workspaces: Omit<Workspace, 'origin'>[]
  runs: Run[]
  members: MemberList
  capabilities: GatewayCapabilities | null
}

function key(): string {
  return keyPrefix + window.location.origin
}

const refused = new WeakSet<RootStore>()

/** Access was refused: the saved entry goes, and nothing is saved again
 * until a live load succeeds. */
export function refuseSnapshot(store: RootStore): void {
  refused.add(store)
  clearSnapshot()
}

/** A live load succeeded, so its data may be saved again. */
export function allowSnapshot(store: RootStore): void {
  refused.delete(store)
}

export function clearSnapshot(): void {
  try {
    window.localStorage.removeItem(key())
  } catch {
    // Storage that cannot be reached holds nothing to clear.
  }
}

/** Saves what a successful hydration just put in the store. */
export function saveSnapshot(store: RootStore): void {
  const s = store.getState()
  if (refused.has(store) || !s.info || !s.identityKey || !s.hydrated) return
  // A kill or close the server has not confirmed would read as done after a restart.
  if (Object.keys(s.deletingRuns).length > 0 || Object.values(s.runs).some((r) => r.stateChangedAtPredicted)) return
  // The Edge gateway listens on a fixed loopback port that another app can
  // squat and serve script that would read what is saved here.
  if (s.linkStatus?.edge_url) {
    clearSnapshot()
    return
  }
  const { disk: _disk, diskError: _diskError, ...rest } = s.info
  const { git_name: _n, git_email: _e, ...member } = rest.member
  const info = { ...rest, member }
  const members: MemberList = {
    members: Object.values(s.members).map(({ git_name: _name, git_email: _email, ...member }) => member),
    merged_into: s.mergedInto,
  }
  const saved: Saved = {
    savedAt: Date.now(),
    identity: s.identityKey,
    info,
    workspaces: Object.values(s.workspaces).map(({ origin: _origin, ...workspace }) => workspace),
    runs: Object.values(s.runs).map(({ toolActivity: _activity, stateChangedAt, stateChangedAtEstimated, stateChangedAtPredicted: _predicted, ...run }) => ({
      ...run,
      paused: s.pausedRuns[run.id] ?? run.paused,
      // An estimate is not the server's time; painting derives it again.
      status_changed_at: stateChangedAtEstimated ? undefined : stateChangedAt,
    })),
    members,
    capabilities: s.capabilities,
  }
  try {
    const text = JSON.stringify(saved)
    if (text.length > maxBytes) clearSnapshot()
    else window.localStorage.setItem(key(), text)
  } catch {
    // A full or blocked store only costs the instant paint next time.
  }
}

/**
 * Fills an empty store from the last snapshot so the dashboard paints at
 * once. `staleSince` stays set until a live hydration replaces it. Only a
 * cold start paints: a reconnect to another server must not show the last one.
 */
export function paintSnapshot(store: RootStore): void {
  if (store.getState().hydrated || store.getState().identityKey !== null) return
  let saved: Saved
  try {
    saved = JSON.parse(window.localStorage.getItem(key()) ?? 'null') as Saved
  } catch {
    return
  }
  // A damaged entry must not stop startup: drop it and load live.
  if (
    typeof saved?.identity !== 'string' ||
    !saved.info?.member ||
    !Array.isArray(saved.runs) ||
    !Array.isArray(saved.workspaces) ||
    !Array.isArray(saved.members?.members)
  ) {
    clearSnapshot()
    return
  }
  const s = store.getState()
  s.setIdentityKey(saved.identity)
  s.setInfo(saved.info as ServerInfo)
  s.setCapabilities(saved.capabilities ?? null)
  // Not setWorkspaces: a route to a workspace made after the save must stay put.
  const workspaces = Object.fromEntries(saved.workspaces.map((w) => [w.id, w as Workspace]))
  store.setState({
    workspaces,
    activeWorkspace: workspaces[s.activeWorkspace] ? s.activeWorkspace : Object.keys(workspaces).sort()[0] ?? '',
  })
  s.setMemberList(saved.members)
  // Display only: a live snapshot must replace these whatever their order.
  s.setRuns(saved.runs.map((run) => ({ ...run, status_seq: 0 })))
  s.seedPaused(Object.fromEntries(saved.runs.filter((r) => r.paused !== undefined).map((r) => [r.id, r.paused === true])))
  store.setState({ hydrated: true, staleSince: saved.savedAt })
}
