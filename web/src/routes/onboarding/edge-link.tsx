// The Link step's first choice: reach a server through an edge. Signing in
// runs the edge's device flow through the local gateway, which keeps the
// device token to itself; this screen only shows the code, waits for the
// gateway to report the sign-in, and then links a server: by the id its
// admin gave, from the edge's list after showing what the link will pin,
// or by claiming a new one with the code `aether-server setup` printed.

import { useCallback, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Chip } from '@/components/ui/heroui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { Api } from '@/lib/api'
import { edgeHost, message, providerName } from '@/lib/format'
import type { EdgeAccount, EdgeLinkResult, EdgeLogin, EdgeServer } from '@/lib/types'

/** How often the screen asks the gateway whether the sign-in finished. */
const pollMs = 2000

type Phase =
  | { name: 'checking' }
  | { name: 'signed-out' }
  | { name: 'waiting'; login: EdgeLogin }
  | { name: 'signed-in'; edges: SignedInEdge[]; chosen: string | null }

type SignedInEdge = { edge: string; account: EdgeAccount }

// Pre-wrapped: a server's refusal lists the commands that fix it, one per line.
const errorLine =
  'min-w-0 whitespace-pre-wrap break-words border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed'

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
        // With more than one edge the person picks, as the CLI's --edge does.
        setPhase({ name: 'signed-in', edges: signedIn, chosen: signedIn.length === 1 ? signedIn[0].edge : null })
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

  const picked = phase.name === 'signed-in' ? phase.edges.find((e) => e.edge === phase.chosen) : undefined

  return (
    <section aria-label="Sign in" className="min-w-0 max-w-2xl space-y-3 text-sm">
      <div className="space-y-1">
        <h3 className="text-sm font-semibold">Sign in</h3>
        <p className="text-[13px] leading-5 text-muted-foreground">
          Reach your server through an edge relay, with no VPN, open port or address to set
          up. Sign in with GitHub or Google, then link a server by the id its admin gave you,
          pick one your account reaches, or add a new one.
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
          <p className="text-muted-foreground">
            <span className="font-mono">{edgeHost(phase.login.signin_origin)}</span> signs you in
            for the edge <span className="font-mono">{edgeHost(phase.login.edge)}</span>.
          </p>
          <p role="status" className="text-muted-foreground">
            Waiting for you to confirm the code in the browser...
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

/** The edges this machine is signed in to, when there is more than one. */
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
    <fieldset className="min-w-0 space-y-1.5">
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
            <span className="font-mono">{edgeHost(e.edge)}</span> as {accountName(e.account)}
          </span>
        </label>
      ))}
    </fieldset>
  )
}

const policyLine: Record<EdgeServer['access_policy'], string> = {
  account: 'Account access: signing in is enough',
  'approved-devices': 'Approved devices: a new device waits for approval',
}

/** The servers the signed-in account reaches, linking by id, and the claim
 * form. */
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
      <form
        aria-label="Link by server id"
        className="min-w-0 space-y-2"
        onSubmit={(e) => {
          e.preventDefault()
          void link(() => client.localEdgeLink(serverID.trim(), edge))
        }}
      >
        <Label className="block min-w-0 max-w-sm space-y-1">
          Server id from your admin
          <Input
            className="min-w-0 font-mono"
            autoComplete="off"
            value={serverID}
            disabled={busy}
            onChange={(e) => setServerID(e.target.value)}
          />
        </Label>
        <p className="text-[13px] leading-5 text-muted-foreground">
          The server&apos;s admin reads it from{' '}
          <span className="font-mono">aether-server edge status</span>. The link accepts only
          the server this id names.
        </p>
        <Button type="submit" size="sm" disabled={busy || !serverID.trim()}>
          Link by id
        </Button>
      </form>
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
                  <Chip color="default" variant="tertiary" size="sm">
                    <Chip.Label>{server.kind}</Chip.Label>
                  </Chip>
                </div>
                <p className="min-w-0 text-xs text-muted-foreground">
                  {policyLine[server.access_policy]}
                </p>
                <p className="min-w-0 break-all font-mono text-xs text-muted-foreground">
                  {server.id}
                </p>
              </div>
              <Button
                size="sm"
                variant="outline"
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
            prints a new one. Claiming makes{' '}
            <span className="font-medium">{accountName(account)}</span>, the account the edge
            reported when you signed in, the server&apos;s admin.
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

/**
 * What linking a server from the edge's list pins, shown before it does:
 * the id comes from the edge, so only comparing it with the admin's rules
 * out an edge that lists a false one.
 */
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
      className="min-w-0 space-y-2 border-l-2 border-border px-3 py-2"
    >
      <p className="font-medium">Link {server.name}?</p>
      <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">Server id</dt>
        <dd className="min-w-0 break-all font-mono">{server.id}</dd>
        <dt className="text-muted-foreground">Host key</dt>
        <dd className="min-w-0 break-all font-mono">
          {fingerprint ?? (error ? 'not read' : 'reading...')}
        </dd>
      </dl>
      {error && <p className={errorLine}>{error}</p>}
      <p className="text-[13px] leading-5 text-muted-foreground">
        This id comes from <span className="font-mono">{edgeHost(edge)}</span>. From now on this
        link accepts only this host key, directly and through the edge. An edge that lists a
        false id can send a first link to another server: compare the id with the one the
        server&apos;s admin gives you, or that{' '}
        <span className="font-mono">aether-server edge status</span> prints on the server.
      </p>
      <div className="flex min-w-0 flex-wrap gap-2">
        <Button size="sm" disabled={busy || fingerprint === null} onClick={onConfirm}>
          {busy ? 'Linking...' : 'Link and pin'}
        </Button>
        <Button size="sm" variant="outline" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </section>
  )
}
