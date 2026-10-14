import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { SysboxStatus } from '@/lib/types'
import { useCapability, useIsAdmin } from '@/store/hooks'

export const SYSBOX_POLL_MS = 2000

const ROOT_COMMAND = 'sudo aether-server sysbox install'

/** The command to run on the server host when the server cannot install
 * itself because it does not run as root. */
export function rootCommand(status: SysboxStatus): string | undefined {
  return status.state === 'off' && status.reason?.includes(ROOT_COMMAND) ? ROOT_COMMAND : undefined
}

/** The admin-only Docker-inside-runs state. `status` stays null for a
 * non-admin and for a server without server.sysbox.status. */
export function useSysbox(client: Api = api) {
  const caps = useCapability()
  const admin = useIsAdmin()
  const enabled = admin && caps.hasMethod('server.sysbox.status')
  const [status, setStatus] = useState<SysboxStatus | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [attempted, setAttempted] = useState(false)
  const [justInstalled, setJustInstalled] = useState(false)
  const previous = useRef<SysboxStatus['state'] | null>(null)

  const apply = useCallback((next: SysboxStatus) => {
    if (previous.current === 'installing' && next.state === 'on') setJustInstalled(true)
    previous.current = next.state
    setStatus(next)
    setError(null)
  }, [])

  useEffect(() => {
    if (!enabled) return
    let live = true
    client.sysboxStatus().then(
      (next) => live && apply(next),
      (err) => live && setError(message(err)),
    )
    return () => {
      live = false
    }
  }, [enabled, client, apply])

  const installing = status?.state === 'installing'
  useEffect(() => {
    if (!enabled || !installing) return
    let live = true
    const timer = setInterval(() => {
      client.sysboxStatus().then(
        (next) => live && apply(next),
        (err) => live && setError(message(err)),
      )
    }, SYSBOX_POLL_MS)
    return () => {
      live = false
      clearInterval(timer)
    }
  }, [enabled, installing, client, apply])

  const install = useCallback(() => {
    setError(null)
    setAttempted(true)
    setJustInstalled(false)
    const before = previous.current
    previous.current = 'installing'
    client.sysboxInstall().then(apply, (err) => {
      previous.current = before
      setError(message(err))
    })
  }, [client, apply])

  return { status: enabled ? status : null, error, justInstalled, attempted, install }
}
