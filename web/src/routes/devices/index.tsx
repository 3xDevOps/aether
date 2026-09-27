// Devices: the computers and browsers members reach this server through an
// edge with. A member sees their own and an admin sees everyone's; both
// approve a pending device by the code it shows and revoke one. Every
// refusal is the server's message, shown verbatim.

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
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { message, timeAgo } from '@/lib/format'
import type { Device } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useIsAdmin } from '@/store/hooks'

const kindLabel: Record<Device['kind'], string> = {
  ssh: 'Computer',
  browser: 'Browser',
}

const statusColor: Record<Device['status'], 'warning' | 'success' | 'default'> = {
  pending: 'warning',
  approved: 'success',
  revoked: 'default',
}

/** What stops working when a device is revoked, said before it happens. */
const revokeEffect: Record<Device['kind'], string> = {
  ssh: 'The server refuses its device key on every path, direct and through the edge, and closes its open connections. aether on that computer stops reaching this server.',
  browser:
    'The server ends its dashboard session, closes its open connections and refuses it from now on.',
}

export function DevicesRoute({ client = api }: RouteProps & { client?: Api }) {
  const members = useStore((s) => s.members)
  const isAdmin = useIsAdmin()
  const [devices, setDevices] = useState<Device[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [code, setCode] = useState('')
  const [approving, setApproving] = useState(false)
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

  const approve = async (value: string) => {
    setApproving(true)
    setError(null)
    try {
      const device = await client.memberDeviceApprove(value.trim())
      setCode('')
      toast.success(`${device.label} approved`)
      await refetch()
    } catch (err) {
      setError(message(err))
    } finally {
      setApproving(false)
    }
  }

  const owner = (device: Device) =>
    members[device.member_id]?.display_name ?? device.member_id

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ViewHeader
        title="Devices"
        subtitle={isAdmin ? "every member's devices" : 'your devices'}
      />
      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
          <p className="max-w-3xl text-[13px] leading-5 text-muted-foreground">
            A device is one computer running aether, or one browser, that reaches this server
            through an edge. A member&apos;s first device is accepted; later ones stay pending
            until a device the member already uses, or an admin, approves them. Approve with the
            code the new device shows on its own screen: a pending device nobody is holding is
            someone else signed in with the member&apos;s account, so revoke it instead.
          </p>

          <form
            aria-label="Approve a device"
            className="flex min-w-0 flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              void approve(code)
            }}
          >
            <Label className="block min-w-0 flex-[1_1_12rem] space-y-1 sm:max-w-xs">
              Approval code
              <Input
                className="min-w-0 font-mono"
                value={code}
                placeholder="ABCD-EFGH"
                autoComplete="off"
                disabled={approving}
                onChange={(e) => setCode(e.target.value)}
              />
            </Label>
            <Button type="submit" size="default" disabled={approving || !code.trim()}>
              Approve
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
                      <Chip color="default" variant="tertiary" size="sm">
                        <Chip.Label>{kindLabel[device.kind]}</Chip.Label>
                      </Chip>
                      <Chip color={statusColor[device.status]} variant="soft" size="sm">
                        <Chip.Label>{device.status}</Chip.Label>
                      </Chip>
                    </div>
                    <p className="min-w-0 break-words text-xs text-muted-foreground">
                      {isAdmin && <>{owner(device)} · </>}
                      <span title={device.created_at}>added {timeAgo(device.created_at)}</span>
                      {' · '}
                      {device.last_seen_at ? (
                        <span title={device.last_seen_at}>
                          last seen {timeAgo(device.last_seen_at)}
                        </span>
                      ) : (
                        'never seen'
                      )}
                    </p>
                    {device.fingerprint && (
                      <p className="min-w-0 break-all font-mono text-xs text-muted-foreground">
                        {device.fingerprint}
                      </p>
                    )}
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
            {revokeEffect[device.kind]} This cannot be undone.
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
