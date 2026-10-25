import { useCallback, useEffect, useState } from 'react'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { SysboxStatus } from '@/lib/types'
import { useStore } from '@/store'
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
  const [attempt, setAttempt] = useState(0)

  const apply = useCallback((next: SysboxStatus) => {
    const { sysboxState, setSysboxState } = useStore.getState()
    if (sysboxState === 'installing' && next.state === 'on') setJustInstalled(true)
    setSysboxState(next.state)
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
  }, [enabled, client, apply, attempt])

  const read = useCallback(() => {
    setError(null)
    setAttempt((n) => n + 1)
  }, [])

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
    const { sysboxState: before, setSysboxState } = useStore.getState()
    setSysboxState('installing')
    client.sysboxInstall().then(apply, (installErr) => {
      setSysboxState(before)
      client.sysboxStatus().then(apply, () => setError(message(installErr)))
    })
  }, [client, apply])

  return { status: enabled ? status : null, error, justInstalled, attempted, install, read }
}
