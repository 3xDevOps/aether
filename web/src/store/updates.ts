import { ApiError, type Api, type UpgradeRequired } from '@/lib/api'
import { message } from '@/lib/format'
import type {
  ServerUpdatePayload,
  UpdateApplyResult,
  UpdateBuildStatus,
  UpdateStatus,
} from '@/lib/types'
import type { RootState } from '@/store'
import type { SliceCreator } from '@/store/slice'

/** One target's place in docs/dashboard-frontend.md#update-prompts. */
export type UpdateTarget =
  | { state: 'unknown' }
  | { state: 'up_to_date'; version: string; checkedAt?: string }
  | { state: 'checking'; version?: string }
  // `unread`: the status read itself failed, not the release check behind it.
  | { state: 'check_failed'; error: string; checkedAt?: string; version?: string; unread?: boolean }
  | { state: 'available'; version: string; latest: string; notesUrl?: string }
  | ({ state: 'required' } & UpgradeRequired)
  | { state: 'scheduled'; latest: string; by: string }
  | { state: 'applying'; latest: string; step?: string }
  | { state: 'restarting'; latest: string }
  | { state: 'ready_to_restart'; latest: string }
  | { state: 'failed'; latest: string; error: string }
  | { state: 'rolled_back'; latest: string; error: string }

export type ClientApply =
  | { name: 'idle' }
  | { name: 'applying' }
  | { name: 'done'; result: UpdateApplyResult }
  // `version` is the release the click was for; a check naming another
  // one starts over.
  | { name: 'failed'; detail: string; version: string }
  // The member closed the administrator dialog, or macOS refused the
  // password: nothing was downloaded into place.
  | { name: 'cancelled'; detail: string; version: string }
  // The click's fresh check found this machine already on the newest
  // release; the version it was made about.
  | { name: 'current'; version: string }

/** A server whose update phases stop arriving for this long has failed. */
export const SERVER_UPDATE_TIMEOUT_MS = 10 * 60 * 1000

export interface UpdatesSlice {
  /** The last -32005 / 426 from any call; the dashboard is gated while set. */
  updateRequired: UpgradeRequired | null
  setUpdateRequired: (required: UpgradeRequired | null) => void
  clientChecking: boolean
  /** The last update.check failure, verbatim; the last good answer stays. */
  clientCheckError: string | null
  serverChecking: boolean
  clientApply: ClientApply
  clientBuild: UpdateBuildStatus | null
  /** The last version announced through the desktop shell's notification. */
  notifiedUpdate: string
  setNotifiedUpdate: (version: string) => void
}

export const createUpdatesSlice: SliceCreator<UpdatesSlice> = (set) => ({
  updateRequired: null,
  setUpdateRequired: (updateRequired) => set({ updateRequired }),
  clientChecking: false,
  clientCheckError: null,
  serverChecking: false,
  clientApply: { name: 'idle' },
  clientBuild: null,
  notifiedUpdate: '',
  setNotifiedUpdate: (notifiedUpdate) => set({ notifiedUpdate }),
})

function gatedBy(s: Pick<RootState, 'updateRequired'>, update: UpdateStatus | null): boolean {
  return s.updateRequired !== null || update?.client_update_required === true
}

/** The release update.apply would install: a gated gateway names the server's. */
function installTarget(update: UpdateStatus | null, gated: boolean): string {
  return (gated && update?.target_version) || update?.cli.latest || ''
}

type Store = {
  getState: () => RootState
  setState: (partial: Partial<RootState>) => void
}

// Only the newest read of each kind writes, so a click supersedes reads in
// flight; `pending` counts because reads overlap.
type Reads = WeakMap<Store, { issued: number; pending: number }>
const clientReads: Reads = new WeakMap()
const statusReads: Reads = new WeakMap()

function counter(reads: Reads, store: Store) {
  let c = reads.get(store)
  if (!c) {
    c = { issued: 0, pending: 0 }
    reads.set(store, c)
  }
  return c
}

export function clientCheckPending(store: Store): boolean {
  return counter(clientReads, store).pending > 0
}

export function serverStatusPending(store: Store): boolean {
  return counter(statusReads, store).pending > 0
}

/** update.check; `refresh` skips the gateway's cached answer. */
export async function checkClient(store: Store, client: Api, refresh = false): Promise<UpdateStatus> {
  const c = counter(clientReads, store)
  const id = ++c.issued
  c.pending += 1
  if (refresh) store.setState({ clientChecking: true })
  try {
    const status = await client.localUpdateCheck(refresh)
    if (id === c.issued) {
      // A failed lookup keeps the last release it found; the server half is fresh.
      const previous = store.getState().update
      const next = status.cli_error && previous ? { ...status, cli: previous.cli } : status
      store.getState().setUpdate(next)
      store.setState({ clientCheckError: status.cli_error || null })
      const s = store.getState()
      const apply = s.clientApply
      const target = installTarget(next, gatedBy(s, next))
      if ((apply.name === 'failed' || apply.name === 'cancelled') && target && target !== apply.version) {
        store.setState({ clientApply: { name: 'idle' } })
      }
    }
    return status
  } catch (err) {
    if (id === c.issued) store.setState({ clientCheckError: message(err) })
    throw err
  } finally {
    c.pending -= 1
    if (id === c.issued) store.setState({ clientChecking: false })
  }
}

export async function readServerStatus(store: Store, client: Api, refresh = false): Promise<void> {
  const c = counter(statusReads, store)
  const id = ++c.issued
  c.pending += 1
  if (refresh) store.setState({ serverChecking: true })
  try {
    const status = await client.serverUpdateStatus(refresh)
    if (id === c.issued) store.getState().setServerUpdate(status)
  } catch (err) {
    if (id === c.issued) store.getState().setServerUpdateFailed(message(err))
  } finally {
    c.pending -= 1
    if (id === c.issued && store.getState().serverChecking) store.setState({ serverChecking: false })
  }
}

/** The member asked: both reads skip their caches, and failures are shown. */
export async function checkForUpdates(
  store: Store,
  client: Api,
  serves: { client: boolean; server: boolean },
): Promise<void> {
  await Promise.all([
    serves.client ? checkClient(store, client, true).catch(() => {}) : undefined,
    serves.server ? readServerStatus(store, client, true) : undefined,
  ])
}

/**
 * update.apply after a fresh check, then update.status polls while the
 * desktop-app rebuild it may start is running.
 */
export async function applyClientUpdate(store: Store, client: Api): Promise<void> {
  store.setState({ clientApply: { name: 'applying' }, clientBuild: null })
  let result: UpdateApplyResult
  let version = ''
  try {
    const fresh = await checkClient(store, client, true)
    const required = store.getState().updateRequired
    const gated = gatedBy(store.getState(), fresh)
    version = installTarget(fresh, gated)
    if (fresh.cli_error) {
      store.setState({ clientApply: { name: 'failed', detail: fresh.cli_error, version } })
      return
    }
    // A gated gateway installs the server's release, not the latest, and
    // says which in target_version; an older gateway omits the field.
    if (gated) {
      if (fresh.target_version === '') {
        const serverVersion = fresh.server_version || required?.server_version || ''
        const minimum = fresh.min_client_version || required?.min_client_version || serverVersion
        store.setState({
          clientApply: {
            name: 'failed',
            version,
            detail: `update.check: no published release matches the server: it runs ${serverVersion} and accepts clients from ${minimum}, and target_version is empty`,
          },
        })
        return
      }
    } else if (!fresh.cli.update_available) {
      store.setState({ clientApply: { name: 'current', version: fresh.cli.version } })
      return
    }
    store.getState().setInstallingUpdate(true)
    result = await client.localUpdateApply()
    // Only `restarting` says the gateway is going away on purpose; an
    // unsupervised gateway stays, and the flag is never cleared.
    if (result.restarting) store.getState().setGatewayRestarting(true)
    store.setState({ clientApply: { name: 'done', result } })
  } catch (err) {
    // 403 is the gateway's "administrator access was not granted"; a
    // bearer-token refusal is 401.
    const detail = message(err)
    store.setState({
      clientApply:
        err instanceof ApiError && err.status === 403
          ? { name: 'cancelled', detail, version }
          : { name: 'failed', detail, version },
    })
    return
  } finally {
    store.getState().setInstallingUpdate(false)
  }
  while (result.rebuilding) {
    await new Promise((resolve) => setTimeout(resolve, 1000))
    try {
      const build = await client.localUpdateStatus()
      store.setState({ clientBuild: build })
      if (build.phase === 'done' || build.phase === 'error') return
    } catch {
      // A failed poll is transient; the next tick retries.
    }
  }
}

/** What the gate offers in place of a button that could not work. */
export function manualCommand(update: UpdateStatus | null, required?: UpgradeRequired | null): string {
  if (update?.install_method === 'manual') return 'sudo aether update'
  return required?.command || 'aether update'
}

export function clientTarget(s: RootState): UpdateTarget {
  const update = s.update
  const apply = s.clientApply
  const latest = (apply.name === 'done' && apply.result.version) || installTarget(update, gatedBy(s, update))
  if (apply.name === 'applying') return { state: 'applying', latest }
  if (apply.name === 'failed') return { state: 'failed', latest, error: apply.detail }
  if (apply.name === 'done') {
    if (apply.result.rebuilding && s.clientBuild?.phase === 'error') {
      return { state: 'failed', latest, error: s.clientBuild.error ?? 'the rebuild failed' }
    }
    if (apply.result.rebuilding && s.clientBuild?.phase !== 'done') {
      return { state: 'applying', latest, step: s.clientBuild?.phase }
    }
    return apply.result.restarting ? { state: 'restarting', latest } : { state: 'ready_to_restart', latest }
  }
  if (s.updateRequired) return { state: 'required', ...s.updateRequired }
  if (update?.client_update_required) {
    const serverVersion = update.server_version
    return {
      state: 'required',
      message: `this aether is ${update.cli.version} and the server runs ${serverVersion}; update this client first, then retry`,
      client_version: update.cli.version,
      server_version: serverVersion,
      min_client_version: update.min_client_version ?? '',
      command: 'aether update',
    }
  }
  if (s.clientChecking) return { state: 'checking', version: update?.cli.version }
  if (update?.cli.update_available && update.cli.latest) {
    return { state: 'available', version: update.cli.version, latest: update.cli.latest, notesUrl: update.cli.release_url }
  }
  if (s.clientCheckError) {
    return { state: 'check_failed', error: s.clientCheckError, checkedAt: update?.cli.checked_at, version: update?.cli.version }
  }
  if (update) return { state: 'up_to_date', version: update.cli.version, checkedAt: update.cli.checked_at }
  return { state: 'unknown' }
}

function lastError(s: RootState): string {
  return s.serverUpdate?.error || s.serverUpdate?.last?.detail || s.serverUpdateError || ''
}

export function serverTarget(s: RootState, now: number): UpdateTarget {
  const status = s.serverUpdate
  const progress: ServerUpdatePayload | null = s.serverUpdateProgress
  const running = status?.server_version || s.info?.server_version || s.update?.server_version || ''
  const latest = status?.latest_version || status?.latest || s.update?.cli.latest || ''
  const target = progress?.version || status?.to_version || latest
  // The server compares with selfupdate.Behind itself; its answer stands.
  const isBehind = status ? status.update_available : (s.update?.server_behind ?? false)
  switch (progress?.phase) {
    case 'scheduled':
      return { state: 'scheduled', latest: target, by: progress.actor_id ?? '' }
    case 'preflight':
    case 'applying':
    case 'restarting':
    case 'verifying': {
      if (now - s.serverUpdateProgressAt > SERVER_UPDATE_TIMEOUT_MS) {
        const known = lastError(s)
        return {
          state: 'failed',
          latest: target,
          error: `The server did not finish updating to ${target} within 10 minutes.${known ? ` Last error: ${known}` : ''}`,
        }
      }
      if (progress.phase === 'restarting' || progress.phase === 'verifying') return { state: 'restarting', latest: target }
      return { state: 'applying', latest: target, step: progress.phase }
    }
    case 'failed':
      return { state: 'failed', latest: target, error: progress.detail || lastError(s) }
    case 'rolled_back':
      return { state: 'rolled_back', latest: target, error: progress.detail || lastError(s) }
  }
  // A cancel's own answer outranks the snapshot read before it.
  if (status?.pending && progress?.phase !== 'cancelled') {
    return { state: 'scheduled', latest: status.pending.version, by: status.pending.requested_by }
  }
  if (isBehind && status?.phase === 'failed') return { state: 'failed', latest: target, error: lastError(s) }
  if (isBehind && status?.phase === 'rolled_back') return { state: 'rolled_back', latest: target, error: lastError(s) }
  if (s.serverChecking) return { state: 'checking', version: running }
  if (isBehind && latest) return { state: 'available', version: running, latest, notesUrl: status?.notes_url }
  if (s.serverUpdateError) {
    return { state: 'check_failed', error: s.serverUpdateError, checkedAt: status?.checked_at, version: running, unread: true }
  }
  if (status?.check_error) {
    return { state: 'check_failed', error: status.check_error, checkedAt: status.checked_at, version: running }
  }
  // Only a release check that answered can say there is nothing newer.
  if (status && (status.latest_version || status.latest)) return { state: 'up_to_date', version: running, checkedAt: status.checked_at }
  return { state: 'unknown' }
}
