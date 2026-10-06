import { useCallback, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { edgeHost, linkTarget, message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { EdgeLinkResult, LinkApplyResult, LinkStatus } from '@/lib/types'
import { EdgeSignIn } from '@/routes/onboarding/edge-link'
import { GitIdentityForm } from '@/routes/onboarding/git-identity'
import { Step } from '@/routes/onboarding/layout'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'

export function ConnectStep({ client, caps, onNext }: { client: Api; caps: Capability; onNext: () => void }) {
  const setLinkStatus = useStore((s) => s.setLinkStatus)
  const [status, setStatus] = useState<LinkStatus | null>(null)
  const [statusError, setStatusError] = useState<string | null>(null)
  const [linkError, setLinkError] = useState<string | null>(null)
  const [address, setAddress] = useState('')
  const [invite, setInvite] = useState('')
  const [name, setName] = useState('')
  const [linking, setLinking] = useState(false)
  const [success, setSuccess] = useState<LinkApplyResult | null>(null)
  const [edgeLinked, setEdgeLinked] = useState<EdgeLinkResult | null>(null)

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
    void check()
    const onFocus = () => void check()
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [check])

  const link = async () => {
    setLinking(true)
    setLinkError(null)
    try {
      const next = await client.localLinkApply({
        addr: address.trim(),
        ...(invite.trim() ? { invite: invite.trim() } : {}),
        ...(name.trim() ? { name: name.trim() } : {}),
      })
      setSuccess(next)
      useStore.getState().reconnect()
      await check()
    } catch (err) {
      setLinkError(message(err))
    } finally {
      setLinking(false)
    }
  }

  const linkedThroughEdge = async (result: EdgeLinkResult) => {
    setEdgeLinked(result)
    useStore.getState().reconnect()
    await check()
  }

  const loading = useDelayed(status === null && statusError === null)
  const connected = success !== null || edgeLinked !== null || status?.server_configured === true
  const choosing = status !== null && !connected

  const summary = success ? (
    <>
      Linked to <Code>{success.addr}</Code> as {success.member.display_name} ({success.member.role}).
      {success.key_generated && <> Created SSH key <Code>{success.key_generated}</Code>.</>}
    </>
  ) : edgeLinked ? (
    <>
      Linked to <Code>{edgeLinked.server_name ?? edgeLinked.server_id}</Code> through <Code>{edgeHost(edgeLinked.edge)}</Code> as{' '}
      {edgeLinked.member.display_name} ({edgeLinked.member.role}).
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
      lead="Aether runs your agents on a server you host. Link this computer to it once."
      actions={connected && <Button onClick={onNext}>Continue</Button>}
    >
      {loading && <div className="h-20"><Skeleton className="size-full" /></div>}
      {statusError && (
        <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={() => void check()}>Retry</Button>}>
          {statusError}
        </Callout>
      )}
      {connected && <Callout tone="done" title="Connected">{summary}</Callout>}
      {choosing && <EdgeSignIn client={client} onLinked={(result) => void linkedThroughEdge(result)} />}
      {choosing && (
        <Collapsible>
          <CollapsibleTrigger>
            <span className="font-medium">Link by address</span>
            <span className="min-w-0 truncate text-muted">A server on your tailnet, or one this computer reaches over SSH</span>
          </CollapsibleTrigger>
          <CollapsibleContent className="pt-2 pl-5">
            <form
              className="flex min-w-0 flex-col gap-3"
              aria-label="Link server"
              onSubmit={(e) => {
                e.preventDefault()
                void link()
              }}
            >
              <div className="grid min-w-0 gap-3 sm:grid-cols-2">
                <FormField label="Server address">
                  <Input required placeholder="server-host:2222" value={address} disabled={linking} onChange={(e) => setAddress(e.target.value)} />
                </FormField>
                <FormField label="Invite code" help="Leave empty on a fresh server, where the first to link becomes the admin, or on a tailnet server.">
                  <Input value={invite} disabled={linking} onChange={(e) => setInvite(e.target.value)} />
                </FormField>
              </div>
              <FormField label="Your name" className="max-w-sm">
                <Input value={name} disabled={linking} onChange={(e) => setName(e.target.value)} />
              </FormField>
              {linkError && <Callout tone="failed" role="alert" className="whitespace-pre-wrap">{linkError}</Callout>}
              <div>
                <Button type="submit" size="sm" disabled={linking || !address.trim()}>
                  {linking ? 'Linking…' : 'Link'}
                </Button>
              </div>
            </form>
          </CollapsibleContent>
        </Collapsible>
      )}
      {connected && caps.hasMethod('member.git') && (
        <section aria-labelledby="connect-git-identity" className="flex flex-col gap-2 border-t border-seam pt-4">
          <h3 id="connect-git-identity" className="text-ui font-medium text-text">Git identity</h3>
          <GitIdentityForm client={client} caps={caps} />
        </section>
      )}
    </Step>
  )
}
