import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import { useCallback, useEffect, useId, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { parseClaim, parseJoin } from '@/lib/claim-link'
import { edgeHost, linkTarget, message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import { shellQuote } from '@/lib/shell'
import type { EdgeLinkResult, LinkApplyResult, LinkStatus } from '@/lib/types'
import { forgetHandedLink, handedLink, onHandedLink } from '@/lib/url-state'
import { cn, focusRing } from '@/lib/utils'
import { EdgeSignIn, JoinLink, LinkBeforeSignIn, LinkByCommand } from '@/routes/onboarding/edge-link'
import { GitIdentityForm } from '@/routes/onboarding/git-identity'
import { Step } from '@/routes/onboarding/layout'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'

type Route = 'tailscale' | 'relay' | 'ssh'

const routes: { value: Route; title: string; detail: string }[] = [
  { value: 'tailscale', title: 'Tailscale', detail: 'This computer and the server are on the same tailnet.' },
  { value: 'relay', title: 'Aether relay', detail: 'No Tailscale or open port: sign in with GitHub.' },
  { value: 'ssh', title: 'SSH address', detail: 'A host and port this computer reaches directly.' },
]

function RouteCard({ value, title, detail }: { value: Route; title: string; detail: string }) {
  const id = useId()
  return (
    <RadioGroupPrimitive.Item
      value={value}
      aria-labelledby={`${id}-title`}
      aria-describedby={`${id}-detail`}
      className={cn(
        focusRing,
        'group flex min-w-0 cursor-pointer flex-col gap-1 rounded-panel border border-seam p-3 text-left transition-colors duration-100 max-sm:p-2 motion-reduce:transition-none',
        'hover:bg-hover data-[state=checked]:border-accent data-[state=checked]:bg-hover',
      )}
    >
      <span className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="grid size-4 shrink-0 place-items-center rounded-full border border-control bg-canvas group-data-[state=checked]:border-accent">
          <span className="size-2 rounded-full bg-accent opacity-0 group-data-[state=checked]:opacity-100" />
        </span>
        <span id={`${id}-title`} className="text-ui font-medium text-text">{title}</span>
        {value === 'tailscale' && <Badge tone="review">Recommended</Badge>}
      </span>
      <span id={`${id}-detail`} className="text-ui-sm text-muted max-sm:hidden">{detail}</span>
    </RadioGroupPrimitive.Item>
  )
}

export function ConnectStep({ client, caps, another, onNext }: { client: Api; caps: Capability; another: boolean; onNext: () => void }) {
  const setLinkStatus = useStore((s) => s.setLinkStatus)
  const [status, setStatus] = useState<LinkStatus | null>(null)
  const [statusError, setStatusError] = useState<string | null>(null)
  const [linkError, setLinkError] = useState<string | null>(null)
  const [address, setAddress] = useState('')
  const [invite, setInvite] = useState('')
  const [name, setName] = useState('')
  const [linking, setLinking] = useState(false)
  const [signedOut, setSignedOut] = useState(false)
  const [success, setSuccess] = useState<LinkApplyResult | null>(null)
  const [edgeLinked, setEdgeLinked] = useState<EdgeLinkResult | null>(null)
  const [handed, setHanded] = useState(handedLink)
  const [picked, setPicked] = useState<Route | null>(null)
  const routesID = useId()
  const local = caps.hasLocal('link.status')

  useEffect(
    () =>
      onHandedLink((next) => {
        setHanded(next)
        setEdgeLinked(null)
        setPicked(null)
      }),
    [],
  )

  const check = useCallback(async () => {
    setStatusError(null)
    try {
      const next = await client.localLinkStatus()
      setStatus(next)
      setLinkStatus(next)
    } catch (err) {
      setStatusError(message(err))
    }
  }, [client, setLinkStatus])

  useEffect(() => {
    if (!local) return
    void check()
    const onFocus = () => void check()
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [check, local])

  const link = async (ssh: boolean) => {
    const pasted = address.trim()
    const kind = parseJoin(pasted) ? 'join' : parseClaim(pasted) ? 'claim' : null
    if (kind) {
      setHanded({ kind, link: pasted })
      setPicked('relay')
      return
    }
    setLinking(true)
    setLinkError(null)
    try {
      // A pasted dashboard address names the host; its port is the HTTPS one.
      const next = await client.localLinkApply({
        addr: !ssh && /^https?:\/\//.test(pasted) ? new URL(pasted).hostname : pasted,
        ...(ssh && invite.trim() ? { invite: invite.trim() } : {}),
        ...(ssh && name.trim() ? { name: name.trim() } : {}),
      })
      setSuccess(next)
      // A pending member can read nothing yet, so hydrating would replace
      // this step with an error; Continue reconnects once approved.
      if (!next.member.pending) useStore.getState().reconnect()
      await check()
    } catch (err) {
      setLinkError(message(err))
    } finally {
      setLinking(false)
    }
  }

  const linkedThroughEdge = async (result: EdgeLinkResult) => {
    forgetHandedLink()
    setEdgeLinked(result)
    if (!result.member.pending) useStore.getState().reconnect()
    await check()
  }

  const loading = useDelayed(local && status === null && statusError === null)
  const connected = success !== null || edgeLinked !== null || status?.server_configured === true
  const choosing = status !== null && (!connected || (another && success === null && edgeLinked === null))
  const handedClaim = handed?.kind === 'claim' && !edgeLinked && caps.hasLocal('edge.claim')
  const joining = handed?.kind === 'join' && !edgeLinked
  const claim = handed?.kind === 'claim' ? parseClaim(handed.link) : null
  const route = picked ?? (handed || (another && status?.edge_url) ? 'relay' : 'tailscale')
  const relay = choosing ? route === 'relay' : handedClaim || joining

  const member = success?.member ?? edgeLinked?.member
  const pending = member?.pending === true
  const summary = success ? (
    <>
      Linked to <Code>{success.addr}</Code> as {success.member.display_name} ({success.member.role}{pending && ', pending'}).
      {success.key_generated && <> Created SSH key <Code>{success.key_generated}</Code>.</>}
    </>
  ) : edgeLinked ? (
    <>
      Linked to <Code>{edgeLinked.server_name ?? edgeLinked.server_id}</Code> through <Code>{edgeHost(edgeLinked.edge)}</Code> as{' '}
      {edgeLinked.member.display_name} ({edgeLinked.member.role}{pending && ', pending'}).
    </>
  ) : status ? (
    <>
      Linked to <Code>{linkTarget(status)}</Code> as {status.user}
      {status.linked && status.repo ? <>, with <Code>{status.repo}</Code></> : null}.
    </>
  ) : null

  return (
    <Step
      label="Connect"
      title="Connect to your server"
      lead="Aether runs agents on a server: yours, or one you were invited to. Link this computer to it once."
      actions={(connected || !local) && (
        <Button
          onClick={() => {
            if (pending) useStore.getState().reconnect()
            onNext()
          }}
        >
          Continue
        </Button>
      )}
    >
      {loading && <div className="h-20"><Skeleton className="size-full" /></div>}
      {statusError && (
        <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={() => void check()}>Retry</Button>}>
          {statusError}
        </Callout>
      )}
      {connected && (
        <Callout tone={pending ? 'needs-you' : 'done'} title={pending ? 'Waiting for an admin to approve you' : 'Connected'}>
          {summary}
          {pending && member && (
            <> An admin approves you from <strong>Needs you</strong>, or runs <Code>aether member approve {member.id}</Code>; then click Continue.</>
          )}
        </Callout>
      )}
      {choosing && (
        <div className="flex min-w-0 flex-col gap-2">
          <h3 id={routesID} className="text-ui font-medium text-text">How does this computer reach your server?</h3>
          <RadioGroupPrimitive.Root
            aria-labelledby={routesID}
            value={route}
            onValueChange={(next) => {
              setPicked(next as Route)
              setLinkError(null)
            }}
            className="grid min-w-0 grid-cols-3 gap-2"
          >
            {routes.map((r) => <RouteCard key={r.value} {...r} />)}
          </RadioGroupPrimitive.Root>
          {/* A phone keeps the three choices on one row, so the field below stays above the keyboard. */}
          <p className="text-ui-sm text-muted sm:hidden">{routes.find((r) => r.value === route)?.detail}</p>
        </div>
      )}
      {choosing && route !== 'relay' && (
        <form
          className="flex min-w-0 flex-col gap-3"
          aria-label="Link server"
          onSubmit={(e) => {
            e.preventDefault()
            void link(route === 'ssh')
          }}
        >
          {route === 'tailscale' ? (
            <FormField
              label="Server's tailnet name"
              className="max-w-sm"
              help={<>Your admin sent it, or <Code>aether-server setup</Code> printed it at the end, such as <Code>my-server.tail1234.ts.net</Code>.</>}
            >
              <Input required placeholder="my-server" value={address} disabled={linking} onChange={(e) => setAddress(e.target.value)} />
            </FormField>
          ) : (
            <>
              <div className="grid min-w-0 gap-3 sm:grid-cols-2">
                <FormField label="Server address">
                  <Input required placeholder="server-host:2222" value={address} disabled={linking} onChange={(e) => setAddress(e.target.value)} />
                </FormField>
                <FormField label="Invite code" help="From an admin. Leave it empty on a new server: the first to link becomes its admin.">
                  <Input value={invite} disabled={linking} onChange={(e) => setInvite(e.target.value)} />
                </FormField>
              </div>
              <FormField label="Your name" className="max-w-sm" help="Shown to others when you join with an invite code.">
                <Input value={name} disabled={linking} onChange={(e) => setName(e.target.value)} />
              </FormField>
            </>
          )}
          {linkError && (
            <Callout tone="failed" role="alert" className="whitespace-pre-wrap">
              {linkError}
              {route === 'tailscale' && (
                <p className="text-ui-sm text-muted">
                  Check that <Code>tailscale status</Code> lists the server on this computer, and that it runs{' '}
                  <Code>aether-server</Code> (<Code>sudo systemctl status aether-server</Code>).
                </p>
              )}
            </Callout>
          )}
          <div>
            <Button type="submit" size="sm" disabled={linking || !address.trim()}>
              {linking ? 'Linking…' : 'Link'}
            </Button>
          </div>
          {route === 'tailscale' && (
            <p className="text-ui-sm text-muted">
              No tailnet? Choose Aether relay, or install{' '}
              <a href="https://tailscale.com/download" target="_blank" rel="noopener noreferrer" className="text-accent underline underline-offset-2">
                Tailscale
              </a>{' '}
              on both machines and run <Code>sudo aether-server setup</Code> again on the server.
            </p>
          )}
        </form>
      )}
      {relay && joining && (
        <JoinLink key={handed.link} client={client} caps={caps} link={handed.link} onLinked={(result) => void linkedThroughEdge(result)} />
      )}
      {handed?.kind === 'claim' && claim && !caps.hasLocal('edge.claim') && (
        <LinkByCommand
          link={handed.link}
          command={`aether link --claim ${shellQuote(claim.code)}${claim.edge ? ` --edge ${shellQuote(claim.edge)}` : ''}`}
        />
      )}
      {(relay || choosing) && !joining && (
        // Hidden rather than unmounted, so a link in flight keeps its busy
        // state while another card is shown.
        <div hidden={!relay}>
          <EdgeSignIn
            client={client}
            claim={handed?.kind === 'claim' ? handed.link : undefined}
            onHand={setHanded}
            onLinked={(result) => void linkedThroughEdge(result)}
            onSignedOut={setSignedOut}
          />
        </div>
      )}
      {relay && !joining && signedOut && handed?.kind !== 'claim' && <LinkBeforeSignIn onHand={setHanded} />}
      {connected && caps.hasMethod('member.git') && (
        <section aria-labelledby="connect-git-identity" className="flex flex-col gap-2 border-t border-seam pt-4">
          <div className="flex items-baseline gap-2">
            <h3 id="connect-git-identity" className="text-ui font-medium text-text">Git identity</h3>
            <span className="text-ui-sm text-muted">Optional</span>
          </div>
          <GitIdentityForm client={client} caps={caps} />
        </section>
      )}
    </Step>
  )
}
