import type { GatewayCapabilities, MemberList, Run, ServerInfo, Workspace } from '@/lib/types'
import type { RootStore } from '@/store'

// docs/privacy.md#the-dashboards-saved-snapshot says what this holds.
const keyPrefix = 'aether.snapshot.v1:'
const maxBytes = 1_000_000

interface Saved {
  savedAt: number
  identity: string
  info: Omit<ServerInfo, 'disk' | 'diskError'>
  workspaces: Workspace[]
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
  const { disk: _disk, diskError: _diskError, ...info } = s.info
  const members: MemberList = {
    members: Object.values(s.members).map(({ git_name: _name, git_email: _email, ...member }) => member),
    merged_into: s.mergedInto,
  }
  const saved: Saved = {
    savedAt: Date.now(),
    identity: s.identityKey,
    info,
    workspaces: Object.values(s.workspaces),
    runs: Object.values(s.runs),
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
 * once. `staleSince` stays set until a live hydration replaces it.
 */
export function paintSnapshot(store: RootStore): void {
  if (store.getState().hydrated) return
  const saved = read()
  if (!saved || !Array.isArray(saved.runs) || !Array.isArray(saved.workspaces)) return
  const s = store.getState()
  s.setIdentityKey(saved.identity)
  s.setInfo(saved.info as ServerInfo)
  s.setCapabilities(saved.capabilities)
  s.setWorkspaces(saved.workspaces)
  s.setMemberList(saved.members)
  s.setRuns(saved.runs)
  store.setState({ hydrated: true, staleSince: saved.savedAt })
}
