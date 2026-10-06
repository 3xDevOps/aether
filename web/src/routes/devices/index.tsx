import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import type { Tone } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { message, providerName } from '@/lib/format'
import type { Device, DeviceLookup } from '@/lib/types'
import { useStore } from '@/store'
import { useIsAdmin } from '@/store/hooks'

const status: Record<Device['status'], { tone: Tone; label: string }> = {
  registered: { tone: 'needs-you', label: 'Registered' },
  pending: { tone: 'needs-you', label: 'Pending' },
  approved: { tone: 'done', label: 'Approved' },
  revoked: { tone: 'neutral', label: 'Revoked' },
}

const provider = (device: Device) => providerName[device.provider] ?? device.provider

/** Approval needs the code the device shows on its own screen; no row carries it. */
export function DevicesPanel({ client }: { client: Api }) {
  const members = useStore((s) => s.members)
  const isAdmin = useIsAdmin()
  const [devices, setDevices] = useState<Device[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [code, setCode] = useState('')
  const [looking, setLooking] = useState(false)
  const [confirming, setConfirming] = useState<{ code: string; found: DeviceLookup } | null>(null)
  const [revoking, setRevoking] = useState<Device | null>(null)

  useEffect(() => {
    let live = true
    client.memberDeviceList().then(
      (list) => live && setDevices(list),
      (err: unknown) => live && setError(message(err)),
    )
    return () => {
      live = false
    }
  }, [client])

  const refetch = () => client.memberDeviceList().then(setDevices, (err: unknown) => setError(message(err)))

  const lookup = async () => {
    setLooking(true)
    setError(null)
    try {
      const trimmed = code.trim()
      setConfirming({ code: trimmed, found: await client.memberDeviceLookup(trimmed) })
    } catch (err) {
      setError(message(err))
    } finally {
      setLooking(false)
    }
  }

  const owner = (device: Device) =>
    device.invitation_id ? `invitation ${device.invitation_id}` : (members[device.member_id]?.display_name ?? device.member_id)

  const approve = confirming && (() => {
    const { device } = confirming.found
    const admits = confirming.found.member_id
      ? `${confirming.found.display_name ?? confirming.found.member_id} (${confirming.found.role})`
      : `a new member (${confirming.found.role})`
    return (
      <Confirm
        title={`Approve ${device.label} as ${admits}?`}
        description={<>
          It signed in as {device.account} on {provider(device)}
          {device.invitation_id ? `, and approving accepts invitation ${device.invitation_id}` : ''}. Whoever holds this device gets that member&apos;s access.
        </>}
        action="Approve"
        onConfirm={() => client.memberDeviceApprove(confirming.code, device.id)}
        onDone={() => {
          toast.success(`${device.label} approved`)
          setCode('')
          void refetch()
        }}
        onClose={() => setConfirming(null)}
      >
        <p className="break-all font-code text-ui-sm text-muted">{device.fingerprint}</p>
      </Confirm>
    )
  })()

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div className="flex min-w-0 flex-col gap-1">
        <p className="text-ui text-muted">
          A device is a computer running aether that reaches this server through an edge. Approve one only with the code shown on its own screen.
        </p>
        <Collapsible>
          <CollapsibleTrigger><span className="text-muted">Learn more</span></CollapsibleTrigger>
          <CollapsibleContent className="flex flex-col gap-2 pt-1 pl-4">
            <p className="text-ui-sm text-muted">
              When signing in is enough for this server, a new device is registered on its first connection. When the server admits approved
              devices only, a new device waits, and a registered one is refused, until the member from a device they already use, an admin, or{' '}
              <Code>sudo aether-server device approve</Code> on the server approves it.
            </p>
            <p className="text-ui-sm text-muted">
              A device nobody is holding is someone else signed in with the member&apos;s account: revoke it. Before approving, check the member
              and role the code admits: whoever holds the device gets that member&apos;s access.
            </p>
          </CollapsibleContent>
        </Collapsible>
      </div>

      <form
        aria-label="Approve a device"
        className="flex min-w-0 flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          void lookup()
        }}
      >
        <FormField label="Approval code" className="min-w-0 flex-[1_1_12rem] sm:max-w-xs">
          <Input className="font-code" value={code} placeholder="ABCD-EFGH" autoComplete="off" disabled={looking} onChange={(e) => setCode(e.target.value)} />
        </FormField>
        <Button type="submit" disabled={looking || !code.trim()}>Review</Button>
      </form>

      {error && <Callout tone="failed" role="alert">{error}</Callout>}

      <section aria-label="Device list" className="flex min-w-0 flex-col gap-2">
        <SectionLabel as="h2">{isAdmin ? "Every member's devices" : 'Your devices'}</SectionLabel>
        {devices?.length === 0 && <p className="text-ui text-muted">No devices yet. A device is recorded the first time it connects through an edge.</p>}
        {devices && devices.length > 0 && (
          <ul aria-label="Devices" className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
            {devices.map((device) => (
              <li key={device.id} className="flex min-h-11 min-w-0 items-center gap-3 px-3 py-2">
                <span className="flex min-w-0 flex-1 flex-col" title={device.fingerprint}>
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-ui font-medium text-text">{device.label}</span>
                    <Badge tone={status[device.status].tone}>{status[device.status].label}</Badge>
                  </span>
                  <span className="text-ui-sm text-muted">
                    {isAdmin && <>{owner(device)} · </>}
                    {device.account} on {provider(device)}
                    {' · added '}<RelativeTime at={device.created_at} />
                    {' · '}{device.last_seen_at ? <>last seen <RelativeTime at={device.last_seen_at} /></> : 'never seen'}
                  </span>
                </span>
                {device.status !== 'revoked' && (
                  <Button size="sm" variant="ghost" aria-label={`Revoke ${device.label}`} onClick={() => setRevoking(device)}>Revoke</Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      {approve}
      {revoking && (
        <Confirm
          title={`Revoke ${revoking.label}${isAdmin ? ` of ${owner(revoking)}` : ''}?`}
          description="The server refuses its device key on every path, direct and through the edge, and closes its open connections. aether on that computer stops reaching this server. This cannot be undone."
          action="Revoke"
          onConfirm={() => client.memberDeviceRevoke(revoking.id)}
          onDone={() => {
            toast.success(`${revoking.label} revoked`)
            void refetch()
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </div>
  )
}
