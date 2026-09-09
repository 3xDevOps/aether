// Workspace settings, over workspace.settings (admin only on the wire).
// steer_others mirrors protocol.WorkspaceSettingsParams: "" is the permissive
// default (everyone with steer may act), "admins_only" restricts it.

import { useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { api, type Api } from '@/lib/api'
import { field } from '@/lib/utils'
import { useStore } from '@/store'

export function WorkspaceSettingsDialog({
  workspaceID,
  client = api,
  onClose,
}: {
  workspaceID: string
  client?: Api
  onClose: () => void
}) {
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const upsertWorkspace = useStore((s) => s.upsertWorkspace)
  const [steerOthers, setSteerOthers] = useState(workspace?.steer_others ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const updated = await client.workspaceSettings({
        workspace_id: workspaceID,
        steer_others: steerOthers,
      })
      upsertWorkspace(updated)
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
          <DialogTitle>Workspace settings</DialogTitle>
          <DialogDescription>
            {workspace ? workspace.name : workspaceID}
          </DialogDescription>
        </DialogHeader>
        <form
          id="workspace-settings"
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          {/* The base branch is not editable here; runs already forked from
              it. It is shown so the policy and the branch it governs read
              together. */}
          <p className="text-sm">
            Base branch{' '}
            <span className="font-mono text-muted-foreground">
              {workspace?.base_branch || 'unknown'}
            </span>
          </p>
          <Label className="block space-y-1">
            Who may steer others' runs
            <select
              className={field}
              value={steerOthers}
              onChange={(e) => setSteerOthers(e.target.value)}
            >
              <option value="">everyone with steer</option>
              <option value="admins_only">admins only</option>
            </select>
          </Label>
        </form>
        {error && <p className="text-xs text-state-failed">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="workspace-settings" disabled={busy}>
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
