// Devices: the computers members reach this server through an edge with. A
// member sees their own and an admin sees everyone's; both approve a
// device by the code it shows on its own screen, which no list carries,
// after seeing which member and role the code admits it as, and revoke
// one. Every refusal is the server's message, shown verbatim.

import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Chip } from '@/components/ui/heroui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RelativeTime } from '@/components/ui/relative-time'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { message, providerName } from '@/lib/format'
import type { Device, DeviceLookup } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useIsAdmin } from '@/store/hooks'

const statusColor: Record<Device['status'], 'warning' | 'success' | 'default'> = {
  registered: 'warning',
  pending: 'warning',
  approved: 'success',
  revoked: 'default',
}

/** What stops working when a device is revoked, said before it happens. */
const revokeEffect =
  'The server refuses its device key on every path, direct and through the edge, and closes its open connections. aether on that computer stops reaching this server.'

export function DevicesRoute({ client = api }: RouteProps & { client?: Api }) {
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
      (list) => {
        if (live) setDevices(list)
      },
      (err: unknown) => {
        if (live) setError(message(err))
      },
    )
    return () => {
      live = false
    }
  }, [client])

  const refetch = async () => setDevices(await client.memberDeviceList())

  const lookup = async (value: string) => {
    setLooking(true)
    setError(null)
    try {
      const trimmed = value.trim()
      setConfirming({ code: trimmed, found: await client.memberDeviceLookup(trimmed) })
    } catch (err) {
      setError(message(err))
    } finally {
      setLooking(false)
    }
  }

  const owner = (device: Device) =>
    device.invitation_id
      ? `invitation ${device.invitation_id}`
      : (members[device.member_id]?.display_name ?? device.member_id)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ViewHeader
        title="Devices"
        subtitle={isAdmin ? "every member's devices" : 'your devices'}
      />
      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
          <p className="max-w-3xl text-[13px] leading-5 text-muted-foreground">
            A device is one computer running aether that reaches this server through an edge.
            When signing in is enough for this server, a new device is registered on its first
            connection. When the server admits approved devices only, a new device stays pending,
            and a registered one is refused, until the member from a device they already use, an
            admin, or <span className="font-mono">sudo aether-server device approve</span> on the
            server approves it. Approve with the code the device shows on its own screen: a
            device nobody is holding is someone else signed in with the member&apos;s account, so
            revoke it instead. Before approving, check the member and role the code admits the
            device as: whoever holds it gets that member&apos;s access.
          </p>

          <form
            aria-label="Approve a device"
            className="flex min-w-0 flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              void lookup(code)
            }}
          >
            <Label className="block min-w-0 flex-[1_1_12rem] space-y-1 sm:max-w-xs">
              Approval code
              <Input
                className="min-w-0 font-mono"
                value={code}
                placeholder="ABCD-EFGH"
                autoComplete="off"
                disabled={looking}
                onChange={(e) => setCode(e.target.value)}
              />
            </Label>
            <Button type="submit" size="default" disabled={looking || !code.trim()}>
              Review
            </Button>
          </form>

          {error && (
            <p
              role="alert"
              className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed"
            >
              {error}
            </p>
          )}

          {devices?.length === 0 && (
            <p className="border-y border-border px-3 py-3 text-[13px] text-muted-foreground">
              No devices yet. A device is recorded the first time it connects through an edge.
            </p>
          )}
          {devices && devices.length > 0 && (
            <ul aria-label="Devices" className="border-y border-border">
              {devices.map((device) => (
                <li
                  key={device.id}
                  className="grid min-w-0 gap-x-3 gap-y-1 border-b border-border px-3 py-2 last:border-b-0 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center"
                >
                  <div className="min-w-0 space-y-0.5">
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <span className="min-w-0 break-words text-[13px] font-medium">
                        {device.label}
                      </span>
                      <Chip color={statusColor[device.status]} variant="soft" size="sm">
                        <Chip.Label>{device.status}</Chip.Label>
                      </Chip>
                    </div>
                    <p className="min-w-0 break-words text-xs text-muted-foreground">
                      {isAdmin && <>{owner(device)} · </>}
                      <span title={device.created_at}>added <RelativeTime at={device.created_at} /></span>
                      {' · '}
                      {device.last_seen_at ? (
                        <span title={device.last_seen_at}>
                          last seen <RelativeTime at={device.last_seen_at} />
                        </span>
                      ) : (
                        'never seen'
                      )}
                    </p>
                    <p className="min-w-0 break-words text-xs text-muted-foreground">
                      signed in as {device.account} on{' '}
                      {providerName[device.provider] ?? device.provider}
                    </p>
                    <p className="min-w-0 break-all font-mono text-xs text-muted-foreground">
                      {device.fingerprint}
                    </p>
                  </div>
                  {device.status !== 'revoked' && (
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5 sm:justify-end">
                      <Button
                        size="default"
                        variant="ghost"
                        aria-label={`Revoke ${device.label}`}
                        onClick={() => setRevoking(device)}
                      >
                        Revoke
                      </Button>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      {confirming && (
        <ApproveDialog
          code={confirming.code}
          found={confirming.found}
          client={client}
          onClose={() => setConfirming(null)}
          onApproved={() => {
            setCode('')
            void refetch().catch((err: unknown) => setError(message(err)))
          }}
        />
      )}
      {revoking && (
        <RevokeDialog
          device={revoking}
          owner={isAdmin ? owner(revoking) : null}
          client={client}
          onClose={() => setRevoking(null)}
          onRevoked={() => void refetch().catch((err: unknown) => setError(message(err)))}
        />
      )}
    </div>
  )
}

function ApproveDialog({
  code,
  found,
  client,
  onClose,
  onApproved,
}: {
  code: string
  found: DeviceLookup
  client: Api
  onClose: () => void
  onApproved: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const { device } = found
  const admits = found.member_id
    ? `${found.display_name ?? found.member_id} (${found.role})`
    : `a new member (${found.role})`

  const approve = async () => {
    setBusy(true)
    setError(null)
    try {
      const approved = await client.memberDeviceApprove(code, device.id)
      onApproved()
      onClose()
      toast.success(`${approved.label} approved`)
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <AlertDialog
      open
      onOpenChange={() => {
        if (!busy) onClose()
      }}
    >
      <AlertDialogContent className="max-h-[calc(100dvh-1rem)] sm:max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>
            Approve {device.label} as {admits}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            It signed in as {device.account} on {providerName[device.provider] ?? device.provider}
            {device.invitation_id ? `, and approving accepts invitation ${device.invitation_id}` : ''}.
            Whoever holds this device gets that member&apos;s access.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <p className="min-w-0 break-all font-mono text-xs text-muted-foreground">{device.fingerprint}</p>
        {error && (
          <p role="alert" className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void approve()
            }}
          >
            Approve
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function RevokeDialog({
  device,
  owner,
  client,
  onClose,
  onRevoked,
}: {
  device: Device
  /** Named for an admin, who may be revoking someone else's device. */
  owner: string | null
  client: Api
  onClose: () => void
  onRevoked: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const revoke = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.memberDeviceRevoke(device.id)
      onRevoked()
      onClose()
      toast.success(`${device.label} revoked`)
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <AlertDialog
      open
      onOpenChange={() => {
        if (!busy) onClose()
      }}
    >
      <AlertDialogContent className="max-h-[calc(100dvh-1rem)] sm:max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>
            Revoke {device.label}
            {owner ? ` of ${owner}` : ''}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            {revokeEffect} This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void revoke()
            }}
          >
            Revoke
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

registerRoute('devices', DevicesRoute)
