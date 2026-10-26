import { useState } from 'react'
import { toast } from 'sonner'
import { MonitorSmartphone } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot } from '@/components/ui/status-dot'
import { api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { DeviceNeed } from '@/lib/needs-you'
import { cn } from '@/lib/utils'
import { wideIndent } from '@/routes/board/run-card'
import { useStore } from '@/store'
import { loadDevices } from '@/store/devices'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

export function DeviceCard({ need }: { need: DeviceNeed }) {
  const { device } = need
  const [pending, setPending] = useState<'Approve' | 'Deny' | null>(null)
  const late = usePastDeadline(pending !== null)

  const act = async (verb: 'Approve' | 'Deny') => {
    setPending(verb)
    try {
      if (verb === 'Approve') await api.memberDeviceApprove(device.id)
      else await api.memberDeviceRevoke(device.id)
      toast.success(`${device.label} ${verb === 'Approve' ? 'approved' : 'denied'}`)
    } catch (err) {
      toast.error(`${verb} failed: ${errorSentence(err)}`)
    } finally {
      setPending(null)
    }
    await loadDevices(useStore, api).catch((err: unknown) => toast.error(errorSentence(err)))
  }

  const label = (verb: 'Approve' | 'Deny', doing: string) => (pending !== verb ? verb : late ? stillWaiting : doing)

  return (
    <Card data-device-id={device.id} raised className="@container">
      <div className="flex items-start gap-3">
        <MonitorSmartphone aria-hidden className="mt-0.75 size-3.5 shrink-0 text-state-needs-you" />
        <h3 className="min-w-0 text-ui font-medium break-words text-text">{need.reason}</h3>
      </div>
      <div className={cn('mt-0.5 flex min-w-0 flex-col text-ui-sm text-muted', wideIndent)}>
        <span className="truncate">{device.provider} account {device.account} · {need.admits}</span>
        <span className="truncate font-code" title={device.fingerprint}>{device.fingerprint}</span>
      </div>
      <div className={cn('mt-2 flex min-w-0 items-center gap-1.5 text-ui-sm text-muted', wideIndent)}>
        <StatusDot tone="needs-you" label="Needs you" />
        <span className="min-w-0 flex-1 truncate">
          Needs you · <RelativeTime compact at={need.waitingSince} className="tabular-nums" />
        </span>
        <Button variant="ghost" size="sm" disabled={pending !== null} onClick={() => void act('Deny')}>{label('Deny', 'Denying…')}</Button>
        <Button variant="secondary" size="sm" disabled={pending !== null} onClick={() => void act('Approve')}>{label('Approve', 'Approving…')}</Button>
      </div>
    </Card>
  )
}
