import { useEffect, useRef, useState } from 'react'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { type Api, ApiError } from '@/lib/api'
import { message } from '@/lib/format'
import type { EdgeAccount, EdgeLogin, ServerEdge } from '@/lib/types'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { loadEdge } from '@/store/edge'
import { useCapability, useIsAdmin } from '@/store/hooks'

const pollMs = 2000

function connection(edge: ServerEdge): string {
  if (!edge.connected) return 'not connected'
  return edge.claimed ? 'connected' : 'the edge has not recorded this server'
}

export function RemoteAccessSection({ client }: { client: Api }) {
  const edge = useStore((s) => s.edge)
  const admin = useIsAdmin()
  const caps = useCapability()
  if (!edge) return null
  const connectable = admin && edge.edge_url !== '' && edge.owner === null
  const local = caps.hasLocal('edge.status') && caps.hasLocal('edge.login') && caps.hasLocal('edge.claim')
  return (
    <SettingsSection title="Remote access">
      <SettingRow label="Edge" help={edge.edge_url || 'off'} />
      <SettingRow label="Server id">
        <CopyableCommand command={edge.server_id} />
      </SettingRow>
      <SettingRow label="Access policy" help={edge.access_policy} />
      <SettingRow label="Owner" help={edge.owner ? `${edge.owner.login} (${edge.owner.provider})` : 'none'}>
        {connectable && (local
          ? <ConnectAccount client={client} edgeURL={edge.edge_url} serverID={edge.server_id} />
          : (
            <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm text-muted">
              <p>Run aether edge connect from a linked terminal</p>
              <CopyableCommand command="aether edge connect" />
            </div>
          ))}
      </SettingRow>
      {edge.edge_url && <SettingRow label="Connection" help={connection(edge)} />}
    </SettingsSection>
  )
}

type Phase =
  | { name: 'idle' }
  | { name: 'signing-in'; login: EdgeLogin }
  | { name: 'connecting'; account: EdgeAccount }

function ConnectAccount({ client, edgeURL, serverID }: { client: Api; edgeURL: string; serverID: string }) {
  const [phase, setPhase] = useState<Phase>({ name: 'idle' })
  const [error, setError] = useState<string | null>(null)
  const [known, setKnown] = useState<EdgeAccount | null>(null)
  const live = useRef(true)
  const busy = useRef(false)
  useEffect(() => {
    live.current = true
    return () => {
      live.current = false
    }
  }, [])
  useEffect(() => {
    client.localEdgeStatus().then(
      (status) => {
        if (live.current) setKnown(status.edges.find((e) => e.edge === edgeURL)?.account ?? null)
      },
      (err: unknown) => {
        if (live.current) setError(message(err))
      },
    )
  }, [client, edgeURL])

  const signedIn = async (): Promise<EdgeAccount> => {
    const status = await client.localEdgeStatus()
    const account = status.edges.find((e) => e.edge === edgeURL)?.account
    return account ?? signIn()
  }

  const signIn = async (): Promise<EdgeAccount> => {
    const login = await client.localEdgeLogin(edgeURL)
    setPhase({ name: 'signing-in', login })
    for (;;) {
      await new Promise((resolve) => setTimeout(resolve, pollMs))
      if (!live.current) throw new Error('left Settings before the sign-in finished')
      const now = (await client.localEdgeStatus()).login
      if (now?.edge !== login.edge || now.user_code !== login.user_code) throw new Error('Sign-in was replaced by another window. Try again.')
      if (now.state === 'signed_in' && now.account) return now.account
      if (now.state === 'failed') throw new Error(now.error ?? 'sign in failed')
    }
  }

  const claim = async () => {
    const { code } = await client.serverEdgeClaimCode()
    // The gateway is shared, so another tab may have relinked it since.
    if (code.split('-')[0] !== serverID) throw new Error('The gateway now reaches another server; reload and try again.')
    await client.localEdgeClaim(code, edgeURL)
  }

  const connect = async () => {
    if (busy.current) return
    busy.current = true
    setError(null)
    try {
      const account = await signedIn()
      if (live.current) setKnown(account)
      setPhase({ name: 'connecting', account })
      try {
        await claim()
      } catch (err) {
        const refusal = err instanceof ApiError ? err.data?.refusal : undefined
        if (refusal?.kind !== 'token-revoked' && refusal?.kind !== 'not-signed-in') throw err
        const fresh = await signIn()
        if (live.current) setKnown(fresh)
        setPhase({ name: 'connecting', account: fresh })
        await claim()
      }
      await loadEdge(useStore, client)
    } catch (err) {
      if (live.current) setError(message(err))
    } finally {
      busy.current = false
      if (live.current) setPhase({ name: 'idle' })
    }
  }

  return (
    <div className="flex min-w-0 flex-col items-start gap-2 text-ui-sm">
      {phase.name === 'idle' && (
        <Button size="sm" onClick={() => void connect()}>
          {known ? `Connect my GitHub account (${known.login ?? known.subject})` : 'Connect my GitHub account'}
        </Button>
      )}
      {phase.name === 'signing-in' && (
        <p>
          Open{' '}
          <a href={phase.login.verification_uri} target="_blank" rel="noopener noreferrer" className="break-all font-code text-accent underline underline-offset-2">
            {phase.login.verification_uri}
          </a>{' '}
          and confirm the code <span className="font-code select-all">{phase.login.user_code}</span>.
        </p>
      )}
      {phase.name === 'connecting' && (
        <p role="status">Connecting as {phase.account.login ?? phase.account.subject} ({phase.account.provider})</p>
      )}
      {error && <Callout tone="failed" role="alert" className="self-stretch whitespace-pre-wrap">{error}</Callout>}
    </div>
  )
}
