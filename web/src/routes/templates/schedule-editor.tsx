// One template's cron rule. Cron is evaluated by the server (UTC), so the
// next-fire preview is whatever schedule.save returned - this view never
// computes cron client-side.

import { useState } from 'react'
import { message } from '@/components/palette/palette'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import type { Schedule } from '@/lib/types'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50'

export function ScheduleEditor({
  sessionID,
  template,
  schedule,
  client = api,
  onChanged,
}: {
  sessionID: string
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
          session_id: sessionID,
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
      await client.scheduleDelete(sessionID, template)
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
    <div className="space-y-1">
      <form
        className="flex items-center gap-2"
        aria-label={`Schedule for ${template}`}
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <input
          className={field}
          placeholder="cron, e.g. 0 3 * * * (UTC)"
          value={cron}
          onChange={(e) => setCron(e.target.value)}
        />
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
      </form>
      {current?.next_fire_at && (
        <p className="text-xs text-muted-foreground">
          Next fire {current.next_fire_at}
        </p>
      )}
      {error && <p className="text-xs text-state-failed">{error}</p>}
    </div>
  )
}
