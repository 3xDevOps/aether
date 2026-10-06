import { useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { FormField } from '@/components/ui/form-field'
import { RelativeTime } from '@/components/ui/relative-time'
import { api, type Api } from '@/lib/api'
import type { Schedule } from '@/lib/types'

export function ScheduleEditor({
  workspaceID,
  template,
  schedule,
  client = api,
  onChanged,
}: {
  workspaceID: string
  template: string
  schedule?: Schedule
  client?: Api
  onChanged: () => void
}) {
  const [cron, setCron] = useState(schedule?.cron ?? '')
  const [saved, setSaved] = useState<Schedule | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const current = saved ?? schedule

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      setSaved(
        await client.scheduleSave({
          workspace_id: workspaceID,
          template,
          cron: cron.trim(),
        }),
      )
      onChanged()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.scheduleDelete(workspaceID, template)
      setSaved(null)
      setCron('')
      onChanged()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <form
        className="flex min-w-0 flex-col gap-2"
        aria-label={`Schedule for ${template}`}
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <FormField label="Schedule (UTC)" help="Five-field cron or an @descriptor such as @daily. Leave it unscheduled for manual launches.">
          <Input placeholder="0 3 * * *" value={cron} onChange={(e) => setCron(e.target.value)} className="font-code" />
        </FormField>
        {current?.next_fire_at && (
          <p className="text-ui-sm text-muted">
            Next launch <RelativeTime at={current.next_fire_at} /> ({new Date(current.next_fire_at).toUTCString()})
          </p>
        )}
        {error && (
          <p role="alert" className="text-ui-sm text-state-failed">
            {error}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          {current && (
            <Button type="button" variant="ghost" disabled={busy} onClick={() => void remove()}>
              Unschedule
            </Button>
          )}
          <Button type="submit" disabled={busy || !cron.trim()}>
            {current ? 'Update schedule' : 'Schedule'}
          </Button>
        </div>
      </form>
    </div>
  )
}
