// One template's cron rule. Cron is evaluated by the server (UTC), so the
// next-fire preview is whatever schedule.save returned - this view never
// computes cron client-side.

import { useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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
  /** The rule as the last schedule.list fetch saw it, if any. */
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
    <div className="space-y-2">
      <form
        className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-end"
        aria-label={`Schedule for ${template}`}
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <Label className="min-w-0 flex-1 space-y-1">
          <span>Schedule (UTC)</span>
          <Input
            aria-label="Cron schedule"
            placeholder="cron, e.g. 0 3 * * * (UTC)"
            value={cron}
            onChange={(e) => setCron(e.target.value)}
          />
        </Label>
        <div className="flex flex-wrap gap-2 sm:shrink-0">
          <Button type="submit" size="sm" variant="outline" disabled={busy || !cron.trim()}>
            Schedule
          </Button>
          {current && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => void remove()}
            >
              Unschedule
            </Button>
          )}
        </div>
      </form>
      {current?.next_fire_at && (
        <p className="text-xs text-muted-foreground">
          Next fire {current.next_fire_at}
        </p>
      )}
      {error && (
        <p role="alert" className="text-xs text-state-failed">
          {error}
        </p>
      )}
    </div>
  )
}
