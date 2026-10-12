import { useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Input } from '@/components/ui/input'
import { FormField } from '@/components/ui/form-field'
import { RelativeTime } from '@/components/ui/relative-time'
import { api, type Api } from '@/lib/api'
import type { Schedule } from '@/lib/types'
import type { Failure } from '@/routes/templates/template-form'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

export function ScheduleEditor({
  workspaceID,
  template,
  schedule,
  draft,
  failure,
  saving,
  client = api,
  onSave,
  onRetry,
  onChanged,
}: {
  workspaceID: string
  template: string
  schedule?: Schedule
  draft?: string
  failure?: Failure
  saving: boolean
  client?: Api
  onSave: (cron: string) => void
  onRetry: (cron: string) => void
  onChanged: () => void
}) {
  const [cron, setCron] = useState(draft ?? schedule?.cron ?? '')
  const [busy, setBusy] = useState(false)
  const late = usePastDeadline(busy || saving)
  const [error, setError] = useState<string | null>(null)

  const remove = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.scheduleDelete(workspaceID, template)
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
          onSave(cron.trim())
        }}
      >
        <FormField label="Schedule (UTC)" help="Five-field cron or an @descriptor such as @daily. Leave it unscheduled for manual launches.">
          <Input placeholder="0 3 * * *" value={cron} onChange={(e) => setCron(e.target.value)} className="font-code" />
        </FormField>
        {schedule?.next_fire_at && (
          <p className="text-ui-sm text-muted">
            Next launch <RelativeTime at={schedule.next_fire_at} /> ({new Date(schedule.next_fire_at).toUTCString()})
          </p>
        )}
        {error && (
          <p role="alert" className="text-ui-sm text-state-failed">
            {error}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          {schedule && (
            <Button type="button" variant="ghost" disabled={busy || saving} onClick={() => void remove()}>
              {late ? stillWaiting : busy ? 'Unscheduling…' : saving ? 'Saving…' : 'Unschedule'}
            </Button>
          )}
          <Button type="submit" disabled={busy || !cron.trim()}>
            {schedule ? 'Update schedule' : 'Schedule'}
          </Button>
        </div>
      </form>
      {failure && <FailureCallout error={failure.error} onRetry={failure.unanswered ? () => onRetry(cron.trim()) : undefined} />}
    </div>
  )
}
