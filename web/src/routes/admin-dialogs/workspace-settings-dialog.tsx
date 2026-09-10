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
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <DialogHeader>
          <DialogTitle>Workspace settings</DialogTitle>
          <DialogDescription>
            {workspace ? workspace.name : workspaceID}
          </DialogDescription>
        </DialogHeader>
        <form
          id="workspace-settings"
          className="min-h-0 space-y-4 overflow-y-auto -mx-1 px-1"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <div className="rounded-md border border-border/70 bg-muted/30 px-3 py-2.5">
            <p className="text-xs font-medium uppercase tracking-[0.08em] text-muted-foreground">
              Base branch
            </p>
            <p className="mt-1 font-mono text-sm" aria-label="Base branch">
              {workspace?.base_branch || 'unknown'}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              New runs fork from this branch.
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="workspace-steer-others">Who may steer others&apos; runs</Label>
            <select
              id="workspace-steer-others"
              className={field}
              value={steerOthers}
              onChange={(e) => setSteerOthers(e.target.value)}
              aria-describedby="workspace-steer-help"
            >
              <option value="">everyone with steer</option>
              <option value="admins_only">admins only</option>
            </select>
            <p id="workspace-steer-help" className="text-xs text-muted-foreground">
              This policy controls steering for runs owned by another member.
            </p>
          </div>
          {error && (
            <p role="alert" className="text-xs text-state-failed">
              {error}
            </p>
          )}
        </form>
        <DialogFooter className="border-t pt-4">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="workspace-settings" disabled={busy}>
            {busy ? 'Saving...' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
