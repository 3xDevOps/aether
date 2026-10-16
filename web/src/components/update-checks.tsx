import { useEffect, useRef } from 'react'
import { desktopBridge } from '@/components/shell/window-bar'
import { api, onUpgradeRequired, type Api } from '@/lib/api'
import { bareVersion } from '@/lib/format'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { serverInFlight } from '@/store/server'
import {
  checkClient,
  checkForUpdates,
  clientCheckPending,
  clientTarget,
  readServerStatus,
  serverStatusPending,
  serverTarget,
} from '@/store/updates'

export const RECHECK_MS = 30 * 60 * 1000
/** Focus and visibility read at most this often. */
export const FOCUS_READ_MS = 60 * 1000
/** While a reconnect finds an update in flight, the status is re-read this often, this many times. */
export const IN_FLIGHT_REREAD_MS = 3000
const IN_FLIGHT_REREADS = 20
// sessionStorage, so a gate that a fresh check contradicts reloads once per
// client version rather than on every load.
const GATE_RELOAD_KEY = 'aether.update-gate-reloaded'

/** ±10%, so a fleet of dashboards opened together does not check together. */
function jittered(): number {
  return RECHECK_MS * (0.9 + Math.random() * 0.2)
}

/**
 * Feeds the update state: update.check and server.update_status on mount,
 * on a jittered half-hour timer, on focus and visibility at most once a
 * minute, and the status again after every reconnect. Mounted by App,
 * outside the shell, so it keeps running behind the required-update gate.
 */
export function UpdateChecks({ client = api }: { client?: Api }) {
  const caps = useCapability()
  const serves = caps.hasLocal('update.check')
  const reads = caps.hasMethod('server.update_status')
  const gateway = useStore((s) => s.capabilities?.gateway)
  const serverVersion = useStore((s) => s.info?.server_version)
  const connection = useStore((s) => s.connection)
  const desktop = desktopBridge()

  useEffect(() => {
    onUpgradeRequired((required) => useStore.getState().setUpdateRequired(required))
  }, [])

  useEffect(() => {
    if (!serves && !reads) return
    // The desktop shell notifies from the background, so its reads run hidden too.
    const background = typeof desktop?.notifyUpdate === 'function'
    let lastRead = 0
    const read = () => {
      if (document.visibilityState === 'hidden' && !background) return
      lastRead = Date.now()
      // The prompt must keep naming the release being installed.
      if (serves && !clientCheckPending(useStore) && !useStore.getState().installingUpdate) {
        void checkClient(useStore, client).catch(() => {})
      }
      const s = useStore.getState()
      // Offline, the read could only fail; the reconnect reads it.
      if (reads && s.info && s.connection === 'live' && !serverStatusPending(useStore)) void readServerStatus(useStore, client)
    }
    const onFocus = () => {
      if (Date.now() - lastRead >= FOCUS_READ_MS) read()
    }
    read()
    let timer: ReturnType<typeof setTimeout>
    const tick = () => {
      read()
      timer = setTimeout(tick, jittered())
    }
    timer = setTimeout(tick, jittered())
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onFocus)
    return () => {
      clearTimeout(timer)
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onFocus)
    }
  }, [serves, reads, client, desktop])

  // A self-updating server re-executes and drops the socket; the read on
  // reconnect is what ends the progress. A server that comes back still
  // verifying publishes applied on a feed this tab may miss, so an
  // in-flight answer is re-read for a minute.
  useEffect(() => {
    if (!reads || !serverVersion || connection !== 'live') return
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const reread = async (left: number) => {
      await readServerStatus(useStore, client)
      if (cancelled || left === 0 || !serverInFlight(useStore.getState().serverUpdate?.phase)) return
      timer = setTimeout(() => void reread(left - 1), IN_FLIGHT_REREAD_MS)
    }
    void reread(IN_FLIGHT_REREADS)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [reads, serverVersion, connection, client])

  // A refusal gates until something says otherwise. A later check that
  // reached the server and found this client accepted ends the gate; the
  // stream stopped for good when it was refused, so only a reload restarts it.
  useEffect(
    () =>
      useStore.subscribe((s, previous) => {
        const update = s.update
        if (update === previous.update || !s.updateRequired || !update) return
        if (update.client_update_required !== false || !update.server_version || update.server_error) return
        if (sessionStorage.getItem(GATE_RELOAD_KEY) === update.cli.version) return
        sessionStorage.setItem(GATE_RELOAD_KEY, update.cli.version)
        window.location.reload()
      }),
    [],
  )

  // A dashboard served by the server itself is the server's own build, so
  // a server that came back on another version reloads into it.
  const seen = useRef(serverVersion)
  useEffect(() => {
    const before = seen.current
    seen.current = serverVersion
    if (gateway === 'server' && before && serverVersion && before !== serverVersion) window.location.reload()
  }, [gateway, serverVersion])

  useEffect(() => {
    if (!desktop?.onCheckForUpdates) return
    return desktop.onCheckForUpdates(() => {
      useStore.getState().clearDismissedUpdates()
      useStore.getState().setUpdatesOpen(true)
      void checkForUpdates(useStore, client, { client: serves, server: reads })
    })
  }, [desktop, client, serves, reads])

  useEffect(() => {
    if (typeof desktop?.notifyUpdate !== 'function') return
    const announce = () => {
      // Only while the window is in the background: in front, the sidebar line is the notice.
      if (document.hasFocus()) return
      const s = useStore.getState()
      const server = serverTarget(s, Date.now())
      const cli = clientTarget(s)
      const admin = s.info?.member.role === 'admin'
      const offer =
        admin && server.state === 'available'
          ? { version: server.latest, title: `Server ${bareVersion(server.latest)} is available`, body: `The server runs ${server.version}. Open Aether to update it.` }
          : cli.state === 'available' && !s.update?.cli.packaged
            ? { version: cli.latest, title: `Aether ${bareVersion(cli.latest)} is available`, body: `This computer runs ${cli.version}. Open Aether to update.` }
            : null
      if (!offer || s.notifiedUpdate === offer.version) return
      s.setNotifiedUpdate(offer.version)
      desktop.notifyUpdate?.(offer)
    }
    announce()
    return useStore.subscribe((s, previous) => {
      if (s.update !== previous.update || s.serverUpdate !== previous.serverUpdate) announce()
    })
  }, [desktop])

  return null
}
