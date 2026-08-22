// Session settings, over session.settings (admin only on the wire).
// steer_others mirrors protocol.SessionSettingsParams: "" is the permissive
// default (everyone with steer may act), "admins_only" restricts it.

import { useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/components/palette/palette'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import { useStore } from '@/store'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50'

export function SessionSettingsDialog({
  sessionID,
  client = api,
  onClose,
}: {
  sessionID: string
  client?: Api
  onClose: () => void
}) {
  const session = useStore((s) => s.sessions[sessionID])
  const upsertSession = useStore((s) => s.upsertSession)
  const [steerOthers, setSteerOthers] = useState(session?.steer_others ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const updated = await client.sessionSettings({
        session_id: sessionID,
        steer_others: steerOthers,
      })
      upsertSession(updated)
      onClose()
      toast.success('Settings saved')
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Session settings</DialogTitle>
          <DialogDescription>
            {session ? session.name : sessionID}
          </DialogDescription>
        </DialogHeader>
        <form
          id="session-settings"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <label className="block space-y-1 text-sm">
            Who may steer others' runs
            <select
              className={field}
              value={steerOthers}
              onChange={(e) => setSteerOthers(e.target.value)}
            >
              <option value="">everyone with steer</option>
              <option value="admins_only">admins only</option>
            </select>
          </label>
        </form>
        {error && <p className="text-xs text-state-failed">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="session-settings" disabled={busy}>
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
