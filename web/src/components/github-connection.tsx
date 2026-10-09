import { useLayoutEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { GitHubOAuthResult } from '@/lib/types'
import { useStore } from '@/store'
import { useIsAdmin } from '@/store/hooks'

const verificationURL = 'https://github.com/login/device'
const pending = (state?: GitHubOAuthResult['state']) => state === 'starting' || state === 'pending' || state === 'finishing'

export function GitHubConnection({ client = api, onConnected, onDisconnected }: {
  client?: Api
  onConnected?: (login: string) => void
  onDisconnected?: () => void
}) {
  const admin = useIsAdmin()
  const [result, setResult] = useState<GitHubOAuthResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const callbacks = useRef({ onConnected, onDisconnected })
  const command = useRef<(action: 'start' | 'cancel' | 'status') => void>(() => {})
  const copyGeneration = useRef(0)
  useLayoutEffect(() => { callbacks.current = { onConnected, onDisconnected } })

  useLayoutEffect(() => {
    let version = 0
    let timer: ReturnType<typeof setTimeout> | undefined
    let current: GitHubOAuthResult | null = null
    let active = false
    let announced: string | null | undefined
    const stop = () => { version += 1; copyGeneration.current += 1; clearTimeout(timer); active = false }
    const request = async (action: 'start' | 'cancel' | 'status') => {
      if (active) return
      clearTimeout(timer)
      const attempt = ++version
      active = true
      setBusy(true)
      setError(null)
      try {
        const next = action === 'start' ? await client.githubOAuthStart()
          : action === 'cancel' && current?.session_id ? await client.githubOAuthCancel(current.session_id)
            : await client.githubOAuthStatus(current?.session_id)
        if (attempt !== version) return
        if (current?.user_code !== next.user_code || !pending(next.state)) {
          copyGeneration.current += 1
          setCopied(false)
        }
        current = next
        setResult(next)
        setError(next.error ?? null)
        const login = next.state === 'connected' ? next.login ?? next.connection?.login ?? '' : null
        if (announced !== login) {
          announced = login
          if (login !== null) callbacks.current.onConnected?.(login)
          else callbacks.current.onDisconnected?.()
        }
        if (attempt !== version) return
        if (pending(next.state)) timer = setTimeout(() => { void request('status') }, 2000)
      } catch (cause) {
        if (attempt === version) setError(message(cause))
      } finally {
        if (attempt === version) { active = false; setBusy(false) }
      }
    }
    const reset = () => {
      stop()
      current = null
      announced = undefined
      setResult(null)
      setError(null)
      setCopied(false)
      setBusy(false)
      if (useStore.getState().info?.member.role === 'admin') void request('status')
    }
    command.current = (action) => { if (useStore.getState().info?.member.role === 'admin') void request(action) }
    const unsubscribe = useStore.subscribe((state, previous) => {
      if (state.identityKey !== previous.identityKey || state.connectionEpoch !== previous.connectionEpoch || state.info?.member.id !== previous.info?.member.id || state.info?.member.role !== previous.info?.member.role) reset()
    })
    reset()
    return () => { stop(); unsubscribe(); command.current = () => {} }
  }, [client, admin])

  if (!admin) return null
  const waiting = pending(result?.state)
  return <section aria-label="GitHub connection" className="space-y-3 text-ui">
    <p className="text-ui-sm text-muted">Connect GitHub to choose repositories and let your runs publish changes.</p>
    <p role="status">{result?.state === 'connected' ? `Connected as ${result.login ?? result.connection?.login ?? 'GitHub member'}`
      : result?.state === 'finishing' ? 'Finishing GitHub setup…'
        : waiting ? 'Waiting for GitHub authorization…'
          : result?.state === 'cancelled' ? 'GitHub connection cancelled.'
            : result?.state === 'expired' ? 'GitHub authorization expired. Connect again to get a new code.'
              : result?.state === 'failed' ? 'GitHub connection failed.'
                : busy ? 'Checking GitHub connection…' : 'GitHub is not connected.'}</p>
    {waiting && result?.user_code && <div className="space-y-2">
      <p>Enter this one-time code on GitHub: <strong className="select-all font-code">{result.user_code}</strong></p>
      <p className="text-ui-sm text-muted">Authorize GitHub CLI on GitHub; Aether uses it to connect this server.</p>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" size="sm" onClick={() => {
          const version = copyGeneration.current
          window.open(verificationURL, '_blank', 'noopener,noreferrer')
          void navigator.clipboard?.writeText(result.user_code!).then(() => {
            if (version === copyGeneration.current) setCopied(true)
          }).catch((cause: unknown) => {
            if (version === copyGeneration.current) setError(`${message(cause)}. Select and copy the code above, then use Open GitHub.`)
          })
          if (!navigator.clipboard) setError('Clipboard unavailable. Select and copy the code above, then use Open GitHub.')
        }}>{copied ? 'Code copied — open GitHub' : 'Copy code and open GitHub'}</Button>
        <a href={verificationURL} target="_blank" rel="noopener noreferrer" className="text-accent underline">Open GitHub</a>
      </div>
      <p className="text-ui-sm text-muted">This page completes automatically after authorization. If no tab opened, use Open GitHub.</p>
    </div>}
    {error && <p role="alert" className="whitespace-pre-wrap break-words text-ui-sm text-state-failed">{error}</p>}
    <div className="flex flex-wrap gap-2">
      {waiting ? <Button type="button" size="sm" variant="secondary" disabled={busy || !result?.session_id} onClick={() => command.current('cancel')}>Cancel connection</Button>
        : result?.state !== 'connected' && <Button type="button" size="sm" disabled={busy} onClick={() => command.current('start')}>Connect GitHub</Button>}
      {(error || result?.state === 'connected') && <Button type="button" size="sm" variant="secondary" disabled={busy} onClick={() => command.current('status')}>Check connection</Button>}
    </div>
  </section>
}
