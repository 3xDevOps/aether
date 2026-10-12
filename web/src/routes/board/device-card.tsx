import { useState } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { RelativeTime } from '@/components/ui/relative-time'
import { StateLine } from '@/components/ui/status-dot'
import { api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { DeviceNeed } from '@/lib/needs-you'
import { useStore } from '@/store'
import { loadDevices } from '@/store/devices'

export function DeviceCard({ need }: { need: DeviceNeed }) {
  const { device } = need
  const [busy, setBusy] = useState(false)

  const act = async (verb: 'Approve' | 'Deny') => {
    setBusy(true)
    try {
      if (verb === 'Approve') await api.memberDeviceApprove(device.id)
      else await api.memberDeviceRevoke(device.id)
      toast.success(`${device.label} ${verb === 'Approve' ? 'approved' : 'denied'}`)
    } catch (err) {
      toast.error(`${verb} failed: ${errorSentence(err)}`)
    } finally {
      setBusy(false)
    }
    await loadDevices(useStore, api).catch((err: unknown) => toast.error(errorSentence(err)))
  }

  return (
    <Card data-device-id={device.id}>
      <StateLine tone="needs-you" trailing={<RelativeTime at={need.waitingSince} />}>
        {need.reason}
      </StateLine>
      <div className="flex min-w-0 flex-col text-ui-sm text-muted">
        <span className="truncate">{device.provider} account {device.account} · {need.admits}</span>
        <span className="truncate font-code" title={device.fingerprint}>{device.fingerprint}</span>
      </div>
      <div className="flex items-center justify-end gap-1.5">
        <Button variant="ghost" size="sm" disabled={busy} onClick={() => void act('Deny')}>Deny</Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={() => void act('Approve')}>Approve</Button>
      </div>
    </Card>
  )
}
