// The Link step's first choice: reach a server through an edge. Signing in
// runs the edge's device flow through the local gateway, which keeps the
// device token to itself; this screen only shows the code, waits for the
// gateway to report the sign-in, and then links a server the account
// reaches or claims a new one with the code `aether-server setup` printed.

import { useCallback, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Chip } from '@/components/ui/heroui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { Api } from '@/lib/api'
import { edgeHost, message } from '@/lib/format'
import type { EdgeAccount, EdgeLinkResult, EdgeLogin, EdgeServer } from '@/lib/types'

/** How often the screen asks the gateway whether the sign-in finished. */
const pollMs = 2000

const providerName: Record<string, string> = { github: 'GitHub', google: 'Google' }

type Phase =
  | { name: 'checking' }
  | { name: 'signed-out' }
  | { name: 'waiting'; login: EdgeLogin }
  | { name: 'signed-in'; edge: string; account: EdgeAccount }

const errorLine =
  'border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed'

export function EdgeSignIn({
  client,
  onLinked,
}: {
  client: Api
  onLinked: (result: EdgeLinkResult) => void
}) {
  const [phase, setPhase] = useState<Phase>({ name: 'checking' })
  const [error, setError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)

  const check = useCallback(async () => {
    setError(null)
    try {
      const status = await client.localEdgeStatus()
      if (status.login?.state === 'pending') {
        setPhase({ name: 'waiting', login: status.login })
        return
      }
      if (status.login?.state === 'failed' && status.login.error) setError(status.login.error)
      const signedIn = status.edges.find((e) => e.account)
      if (signedIn?.account) {
        setPhase({ name: 'signed-in', edge: signedIn.edge, account: signedIn.account })
        return
      }
      // A stored sign-in the gateway cannot read is fixed by signing in again.
      const unreadable = status.edges.find((e) => e.error)?.error
      if (unreadable) setError(unreadable)
      setPhase({ name: 'signed-out' })
    } catch (err) {
      setError(message(err))
      setPhase({ name: 'signed-out' })
    }
  }, [client])

  useEffect(() => {
    void check()
  }, [check])

  const waiting = phase.name === 'waiting'
  useEffect(() => {
    if (!waiting) return
    let live = true
    let timer: ReturnType<typeof setTimeout>
    const poll = () => {
      timer = setTimeout(() => {
        client.localEdgeStatus().then(
          (status) => {
            if (!live) return
            const login = status.login
            if (login?.state === 'signed_in' && login.account) {
              setError(null)
              setPhase({ name: 'signed-in', edge: login.edge, account: login.account })
            } else if (login?.state === 'failed') {
              setError(login.error ?? 'sign in failed')
              setPhase({ name: 'signed-out' })
            } else {
              poll()
            }
          },
          (err: unknown) => {
            if (!live) return
            // The gateway is local; keep waiting, and say what it answered.
            setError(message(err))
            poll()
          },
        )
      }, pollMs)
    }
    poll()
    return () => {
      live = false
      clearTimeout(timer)
    }
  }, [client, waiting])

  const signIn = async () => {
    setStarting(true)
    setError(null)
    // Opened before the await so a browser treats it as the click's own
    // window rather than a blocked popup. The desktop shell refuses blank
    // windows and opens the address in the system browser instead.
    const popup = window.open('about:blank', '_blank')
    if (popup) popup.opener = null
    try {
      const login = await client.localEdgeLogin()
      if (popup) popup.location.replace(login.verification_uri)
      else window.open(login.verification_uri, '_blank', 'noopener,noreferrer')
      setPhase({ name: 'waiting', login })
    } catch (err) {
      popup?.close()
      setError(message(err))
    } finally {
      setStarting(false)
    }
  }

  return (
    <section aria-label="Sign in" className="min-w-0 max-w-2xl space-y-3 text-sm">
      <div className="space-y-1">
        <h3 className="text-sm font-semibold">Sign in</h3>
        <p className="text-[13px] leading-5 text-muted-foreground">
          Reach your server through an edge relay, with no VPN, open port or address to set
          up. Sign in with GitHub or Google, then pick a server your account reaches or add a
          new one.
        </p>
      </div>
      {error && <p className={errorLine}>{error}</p>}
      {phase.name === 'signed-out' && (
        <Button size="sm" disabled={starting} onClick={() => void signIn()}>
          {starting ? 'Signing in...' : 'Sign in'}
        </Button>
      )}
      {phase.name === 'waiting' && (
        <div className="min-w-0 space-y-2 border-y border-border/70 py-3">
          <p>
            Open{' '}
            <a
              href={phase.login.verification_uri}
              target="_blank"
              rel="noopener noreferrer"
              className="break-all font-mono underline underline-offset-2"
            >
              {phase.login.verification_uri}
            </a>{' '}
            and confirm this code:
          </p>
          <p className="font-mono text-base font-semibold tracking-wider select-all">
            {phase.login.user_code}
          </p>
          <p role="status" className="text-muted-foreground">
            Waiting for you to confirm the code in the browser...
          </p>
        </div>
      )}
      {phase.name === 'signed-in' && (
        <ServerPicker
          client={client}
          edge={phase.edge}
          account={phase.account}
          onLinked={onLinked}
        />
      )}
    </section>
  )
}

function accountName(account: EdgeAccount): string {
  const name = account.login ?? account.email ?? account.name ?? account.subject
  const provider = providerName[account.provider] ?? account.provider
  return `${name} (${provider})`
}

/** The servers the signed-in account reaches, and the claim form. */
function ServerPicker({
  client,
  edge,
  account,
  onLinked,
}: {
  client: Api
  edge: string
  account: EdgeAccount
  onLinked: (result: EdgeLinkResult) => void
}) {
  const [servers, setServers] = useState<EdgeServer[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [adding, setAdding] = useState(false)
  const [code, setCode] = useState('')

  const load = useCallback(async () => {
    setError(null)
    try {
      setServers((await client.localEdgeServers(edge)).servers)
    } catch (err) {
      setError(message(err))
    }
  }, [client, edge])

  useEffect(() => {
    void load()
  }, [load])

  const link = async (run: () => Promise<EdgeLinkResult>) => {
    setBusy(true)
    setError(null)
    try {
      onLinked(await run())
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-w-0 space-y-3">
      <p>
        Signed in to <span className="font-mono">{edgeHost(edge)}</span> as{' '}
        <span className="font-medium">{accountName(account)}</span>.
      </p>
      {error && (
        <div className="flex min-w-0 flex-wrap items-center gap-3">
          <p className={errorLine}>{error}</p>
          {servers === null && (
            <Button size="sm" variant="outline" onClick={() => void load()}>
              Retry
            </Button>
          )}
        </div>
      )}
      {servers?.length === 0 && (
        <p className="text-muted-foreground">
          Your account reaches no servers yet. Add one with its claim code, or ask a
          server&apos;s admin to invite {account.login ?? account.email ?? 'your account'}.
        </p>
      )}
      {servers && servers.length > 0 && (
        <ul aria-label="Your servers" className="min-w-0 border-y border-border/70">
          {servers.map((server) => (
            <li
              key={server.id}
              className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 border-b border-border/70 py-2 last:border-b-0"
            >
              <div className="min-w-0 space-y-0.5">
                <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                  <span className="min-w-0 break-words font-medium">{server.name}</span>
                  <Chip color={server.online ? 'success' : 'default'} variant="soft" size="sm">
                    <Chip.Label>{server.online ? 'online' : 'offline'}</Chip.Label>
                  </Chip>
                  <Chip color="default" variant="tertiary" size="sm">
                    <Chip.Label>{server.role}</Chip.Label>
                  </Chip>
                </div>
                <p className="min-w-0 break-all font-mono text-xs text-muted-foreground">
                  {server.id}
                </p>
              </div>
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                aria-label={`Link ${server.name}`}
                onClick={() =>
                  void link(async () => ({
                    ...(await client.localEdgeLink(server.id, edge)),
                    server_name: server.name,
                  }))
                }
              >
                Link
              </Button>
            </li>
          ))}
        </ul>
      )}
      {adding ? (
        <form
          aria-label="Add a server"
          className="min-w-0 space-y-2"
          onSubmit={(e) => {
            e.preventDefault()
            void link(() => client.localEdgeClaim(code.trim(), edge))
          }}
        >
          <Label className="block min-w-0 max-w-sm space-y-1">
            Claim code
            <Input
              className="min-w-0 font-mono"
              autoComplete="off"
              value={code}
              disabled={busy}
              onChange={(e) => setCode(e.target.value)}
            />
          </Label>
          <p className="text-[13px] leading-5 text-muted-foreground">
            <span className="font-mono">aether-server setup</span> printed it on the server,
            valid for 30 minutes. <span className="font-mono">sudo aether-server edge claim-code</span>{' '}
            prints a new one. Claiming makes your account the server&apos;s admin.
          </p>
          <Button type="submit" size="sm" disabled={busy || !code.trim()}>
            {busy ? 'Claiming...' : 'Claim and link'}
          </Button>
        </form>
      ) : (
        <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
          Add a server
        </Button>
      )}
    </div>
  )
}
