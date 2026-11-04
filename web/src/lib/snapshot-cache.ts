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

function read(): Saved | null {
  try {
    const raw = window.localStorage.getItem(key())
    return raw ? (JSON.parse(raw) as Saved) : null
  } catch {
    return null
  }
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
  if (!s.info || !s.identityKey || !s.hydrated) return
  // The Edge gateway listens on a fixed loopback port that another app can
  // squat, and the app turns the WebView's own storage off for that reason.
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
    runs: Object.values(s.runs).map(({ toolActivity: _activity, stateChangedAt: _at, stateChangedAtEstimated: _estimated, stateChangedAtPredicted: _predicted, ...run }) => run as Run),
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
  const saved = read()
  if (!saved || !Array.isArray(saved.runs) || !Array.isArray(saved.workspaces)) return
  const s = store.getState()
  s.setIdentityKey(saved.identity)
  s.setInfo(saved.info as ServerInfo)
  s.setCapabilities(saved.capabilities)
  s.setWorkspaces(saved.workspaces)
  s.setMemberList(saved.members)
  // Display only: a live snapshot must replace these whatever their order.
  s.setRuns(saved.runs.map((run) => ({ ...run, status_seq: 0 })))
  store.setState({ hydrated: true, staleSince: saved.savedAt })
}
