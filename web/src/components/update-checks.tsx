import { useEffect, useRef } from 'react'
import { desktopBridge } from '@/components/shell/window-bar'
import { api, onUpgradeRequired, type Api } from '@/lib/api'
import { bareVersion } from '@/lib/format'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { checkClient, checkForUpdates, clientCheckPending, clientTarget, readServerStatus, serverTarget } from '@/store/updates'

export const RECHECK_MS = 30 * 60 * 1000

/** ±10%, so a fleet of dashboards opened together does not check together. */
function jittered(): number {
  return RECHECK_MS * (0.9 + Math.random() * 0.2)
}

/**
 * Feeds the update state: update.check and server.update_status on mount,
 * on a jittered half-hour timer, on focus and visibility, and the status
 * again after every reconnect. Mounted by App, outside the shell, so it
 * keeps running behind the required-update gate.
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
    const read = () => {
      if (document.visibilityState === 'hidden' && !background) return
      // The prompt must keep naming the release being installed.
      if (serves && !clientCheckPending(useStore) && !useStore.getState().installingUpdate) {
        void checkClient(useStore, client).catch(() => {})
      }
      if (reads && useStore.getState().info) void readServerStatus(useStore, client)
    }
    read()
    let timer: ReturnType<typeof setTimeout>
    const tick = () => {
      read()
      timer = setTimeout(tick, jittered())
    }
    timer = setTimeout(tick, jittered())
    window.addEventListener('focus', read)
    document.addEventListener('visibilitychange', read)
    return () => {
      clearTimeout(timer)
      window.removeEventListener('focus', read)
      document.removeEventListener('visibilitychange', read)
    }
  }, [serves, reads, client, desktop])

  // A self-updating server re-executes and drops the socket; the read on
  // reconnect is what ends the progress.
  useEffect(() => {
    if (!reads || !serverVersion || connection !== 'live') return
    void readServerStatus(useStore, client)
  }, [reads, serverVersion, connection, client])

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
