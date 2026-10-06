// The local gateway runs the edge device flow and keeps the device token;
// this screen never sees it.

import { useCallback, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { StatusDot } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { edgeHost, message, providerName } from '@/lib/format'
import type { EdgeAccount, EdgeLinkResult, EdgeLogin, EdgeServer } from '@/lib/types'

const pollMs = 2000

type Phase =
  | { name: 'checking' }
  | { name: 'signed-out' }
  | { name: 'waiting'; login: EdgeLogin }
  | { name: 'signed-in'; edges: SignedInEdge[]; chosen: string | null }

type SignedInEdge = { edge: string; account: EdgeAccount }

// Pre-wrapped: a server's refusal lists the commands that fix it, one per line.
function ErrorLine({ children }: { children: string }) {
  return <Callout tone="failed" role="alert" className="self-stretch whitespace-pre-wrap">{children}</Callout>
}

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
      const signedIn = status.edges.flatMap((e) => (e.account ? [{ edge: e.edge, account: e.account }] : []))
      if (signedIn.length > 0) {
        setPhase({ name: 'signed-in', edges: signedIn, chosen: signedIn.length === 1 ? signedIn[0].edge : null })
        return
      }
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
              setPhase({
                name: 'signed-in',
                edges: [{ edge: login.edge, account: login.account }],
                chosen: login.edge,
              })
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
    // Opened before the await so it is not blocked as a popup. The desktop
    // shell refuses blank windows and opens the address in the system browser.
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

  const picked = phase.name === 'signed-in' ? phase.edges.find((e) => e.edge === phase.chosen) : undefined

  return (
    <section aria-label="Sign in" className="flex min-w-0 flex-col items-start gap-3 text-ui">
      <p className="text-text">
        Sign in with GitHub to reach your server through an edge relay: no VPN, open port or address to set up.
      </p>
      {error && <ErrorLine>{error}</ErrorLine>}
      {phase.name === 'signed-out' && (
        <Button size="sm" disabled={starting} onClick={() => void signIn()}>
          {starting ? 'Signing in…' : 'Sign in'}
        </Button>
      )}
      {phase.name === 'waiting' && (
        <div className="flex min-w-0 flex-col gap-2">
          <p>
            Open{' '}
            <a
              href={phase.login.verification_uri}
              target="_blank"
              rel="noopener noreferrer"
              className="break-all font-code text-accent underline underline-offset-2"
            >
              {phase.login.verification_uri}
            </a>{' '}
            and confirm this code:
          </p>
          <p className="font-code text-title tracking-wider select-all">
            {phase.login.user_code}
          </p>
          <p className="text-muted">
            <span className="font-code">{edgeHost(phase.login.signin_origin)}</span> signs you in
            for the edge <span className="font-code">{edgeHost(phase.login.edge)}</span>.
          </p>
          <p role="status" className="text-muted">
            Waiting for you to confirm the code in the browser…
          </p>
        </div>
      )}
      {phase.name === 'signed-in' && (
        <EdgeChoice
          edges={phase.edges}
          chosen={phase.chosen}
          onChoose={(edge) => setPhase({ ...phase, chosen: edge })}
        />
      )}
      {picked && (
        <ServerPicker
          key={picked.edge}
          client={client}
          edge={picked.edge}
          account={picked.account}
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

function EdgeChoice({
  edges,
  chosen,
  onChoose,
}: {
  edges: SignedInEdge[]
  chosen: string | null
  onChoose: (edge: string) => void
}) {
  if (edges.length < 2) return null
  return (
    <fieldset className="flex min-w-0 flex-col gap-1.5">
      <legend className="mb-1.5">
        You are signed in to {edges.length} edges. Link through:
      </legend>
      {edges.map((e) => (
        <label key={e.edge} className="flex min-w-0 items-center gap-2">
          <input
            type="radio"
            name="edge"
            checked={chosen === e.edge}
            onChange={() => onChoose(e.edge)}
          />
          <span className="min-w-0 break-all">
            <span className="font-code">{edgeHost(e.edge)}</span> as {accountName(e.account)}
          </span>
        </label>
      ))}
    </fieldset>
  )
}

const policyLine: Record<EdgeServer['access_policy'], string> = {
  account: 'signing in is enough',
  'approved-devices': 'a new device waits for approval',
}

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
  const [code, setCode] = useState('')
  const [serverID, setServerID] = useState('')
  const [confirming, setConfirming] = useState<EdgeServer | null>(null)

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
    <div className="flex min-w-0 flex-col gap-3 self-stretch">
      <p>
        Signed in to <span className="font-code">{edgeHost(edge)}</span> as{' '}
        <span className="font-medium">{accountName(account)}</span>.
      </p>
      {error && (
        <div className="flex min-w-0 flex-col items-start gap-2">
          <ErrorLine>{error}</ErrorLine>
          {servers === null && (
            <Button size="sm" variant="secondary" onClick={() => void load()}>
              Retry
            </Button>
          )}
        </div>
      )}
      {servers?.length === 0 && (
        <p className="text-muted">
          Your account reaches no servers yet. Add one with its claim code under Other ways to link, or ask a
          server&apos;s admin to invite {account.login ?? account.email ?? 'your account'}.
        </p>
      )}
      {servers && servers.length > 0 && (
        <ul aria-label="Your servers" className="flex min-w-0 flex-col divide-y divide-seam rounded-panel border border-seam">
          {servers.map((server) => (
            <li key={server.id} className="flex min-w-0 items-center gap-3 px-3 py-2">
              <StatusDot tone={server.online ? 'done' : 'neutral'} />
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate font-medium">{server.name}</span>
                <span className="truncate text-ui-sm text-muted">
                  {server.online ? 'Online' : 'Offline'} · {server.role} · {policyLine[server.access_policy]}
                </span>
              </span>
              <Button
                size="sm"
                variant="secondary"
                disabled={busy}
                aria-label={`Link ${server.name}`}
                onClick={() => setConfirming(server)}
              >
                Link
              </Button>
            </li>
          ))}
        </ul>
      )}
      {confirming && (
        <ConfirmPin
          key={confirming.id}
          client={client}
          edge={edge}
          server={confirming}
          busy={busy}
          onCancel={() => setConfirming(null)}
          onConfirm={() =>
            void link(async () => ({
              ...(await client.localEdgeLink(confirming.id, edge)),
              server_name: confirming.name,
            }))
          }
        />
      )}
      <Collapsible>
        <CollapsibleTrigger>Other ways to link</CollapsibleTrigger>
        <CollapsibleContent className="flex flex-col gap-4 pt-2 pl-5">
          <form
            aria-label="Link by server id"
            className="flex min-w-0 max-w-sm flex-col items-start gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              void link(() => client.localEdgeLink(serverID.trim(), edge))
            }}
          >
            <FormField
              className="self-stretch"
              label="Server id from your admin"
              help={<>The admin reads it from <span className="font-code">aether-server edge status</span>. The link accepts only the server this id names.</>}
            >
              <Input className="font-code" autoComplete="off" value={serverID} disabled={busy} onChange={(e) => setServerID(e.target.value)} />
            </FormField>
            <Button type="submit" size="sm" variant="secondary" disabled={busy || !serverID.trim()}>
              Link by id
            </Button>
          </form>
          <form
            aria-label="Add a server"
            className="flex min-w-0 max-w-sm flex-col items-start gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              void link(() => client.localEdgeClaim(code.trim(), edge))
            }}
          >
            <FormField
              className="self-stretch"
              label="Claim code"
              help={<><span className="font-code">aether-server setup</span> printed it, valid for 30 minutes; <span className="font-code">sudo aether-server edge claim-code</span> prints a new one. Claiming makes {accountName(account)} the server&apos;s admin.</>}
            >
              <Input className="font-code" autoComplete="off" value={code} disabled={busy} onChange={(e) => setCode(e.target.value)} />
            </FormField>
            <Button type="submit" size="sm" variant="secondary" disabled={busy || !code.trim()}>
              {busy ? 'Claiming…' : 'Claim and link'}
            </Button>
          </form>
        </CollapsibleContent>
      </Collapsible>
    </div>
  )
}

/** The id comes from the edge, so only comparing it with the admin's rules
 * out an edge that lists a false one. */
function ConfirmPin({
  client,
  edge,
  server,
  busy,
  onCancel,
  onConfirm,
}: {
  client: Api
  edge: string
  server: EdgeServer
  busy: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const [fingerprint, setFingerprint] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    client.localEdgeHostKey(server.id, edge).then(
      (key) => {
        if (live) setFingerprint(key.fingerprint)
      },
      (err: unknown) => {
        if (live) setError(message(err))
      },
    )
    return () => {
      live = false
    }
  }, [client, edge, server.id])

  return (
    <section
      aria-label="Confirm server"
      className="flex min-w-0 flex-col gap-2 rounded-panel border border-seam p-3"
    >
      <p className="font-medium">Link {server.name}?</p>
      <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm">
        <dt className="text-muted">Server id</dt>
        <dd className="min-w-0 break-all font-code">{server.id}</dd>
        <dt className="text-muted">Host key</dt>
        <dd className="min-w-0 break-all font-code">
          {fingerprint ?? (error ? 'not read' : 'reading…')}
        </dd>
      </dl>
      {error && <ErrorLine>{error}</ErrorLine>}
      <p className="text-ui-sm text-muted">
        From now on this link accepts only this host key, directly and through the edge. An edge that lists a false
        id can send a first link to another server: compare the id with the one{' '}
        <span className="font-code">aether-server edge status</span> prints on the server.
      </p>
      <div className="flex min-w-0 flex-wrap gap-2">
        <Button size="sm" disabled={busy || fingerprint === null} onClick={onConfirm}>
          {busy ? 'Linking…' : 'Link and pin'}
        </Button>
        <Button size="sm" variant="secondary" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </section>
  )
}
