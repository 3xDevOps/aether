// steer_others mirrors protocol.WorkspaceSettingsParams: "" is the permissive default.

import { useEffect, useState } from 'react'
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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { api, type Api } from '@/lib/api'
import { useReturnFocus } from '@/lib/hooks'
import type { ChildLimits } from '@/lib/types'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

/** The permissive default's form value; see the Styleguide in docs/dashboard-frontend.md. */
const everyone = 'everyone'

const limitFields: { key: keyof ChildLimits; label: string; help: string }[] = [
  { key: 'max_live', label: 'Child runs busy at once per parent run (more queue)', help: 'Child runs of one parent run working at the same time; more wait in a queue.' },
]

export function WorkspaceSettingsDialog({
  workspaceID,
  client = api,
  onClose,
  onRepository,
}: {
  workspaceID: string
  client?: Api
  onClose: () => void
  onRepository?: () => void
}) {
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const upsertWorkspace = useStore((s) => s.upsertWorkspace)
  const [steerOthers, setSteerOthers] = useState(workspace?.steer_others ?? '')
  const [busy, setBusy] = useState(false)
  const late = usePastDeadline(busy)
  const [error, setError] = useState<string | null>(null)
  const returnFocus = useReturnFocus()
  const limitsOffered = useCapability().hasMethod('workspace.child_limits')
  const [savedLimits, setSavedLimits] = useState<ChildLimits | null>(null)
  const [limits, setLimits] = useState<Record<keyof ChildLimits, string> | null>(null)
  const [limitsError, setLimitsError] = useState<string | null>(null)

  useEffect(() => {
    if (!limitsOffered) return
    let live = true
    client.workspaceChildLimits(workspaceID).then((read) => {
      if (!live) return
      setSavedLimits(read)
      setLimits({ max_live: String(read.max_live) })
    }, (err: unknown) => live && setLimitsError(message(err)))
    return () => { live = false }
  }, [client, workspaceID, limitsOffered])

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const changed = limits && savedLimits
        ? Object.fromEntries(limitFields.filter(({ key }) => Number(limits[key]) !== savedLimits[key]).map(({ key }) => [key, Number(limits[key])]))
        : {}
      if (Object.keys(changed).length > 0) setSavedLimits(await client.workspaceChildLimits(workspaceID, changed))
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
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Workspace settings</DialogTitle>
          <DialogDescription>
            {workspace ? workspace.name : workspaceID}
          </DialogDescription>
        </DialogHeader>
        <form
          id="workspace-settings"
          className="min-h-0 space-y-3 overflow-y-auto -mx-1 px-1"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <div className="border-y bg-chrome px-3 py-2.5">
            <p className="text-ui-sm font-medium text-muted">Base branch</p>
            <p className="mt-1 break-all font-code text-ui" aria-label="Base branch">
              {workspace?.base_branch || 'unknown'}
            </p>
            <p className="mt-1 text-ui-sm text-muted">
              New runs fork from this branch.
            </p>
          </div>
          {onRepository && <section className="space-y-2 border-b pb-3 text-ui-sm">
            <p>Manage the public/private read source, or link and relink a local clone. Deploy-key read access is separate from checkout Origin and your native Git/gh publishing credentials.</p>
            <Button type="button" size="sm" variant="secondary" onClick={onRepository}>Repository</Button>
          </section>}
          <div className="min-w-0 space-y-1 text-ui">
            <Label htmlFor="workspace-steer">Who may message others&apos; runs</Label>
            <Select
              value={steerOthers || everyone}
              onValueChange={(value) => setSteerOthers(value === everyone ? '' : value)}
            >
              <SelectTrigger id="workspace-steer" aria-describedby="workspace-steer-help">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={everyone}>everyone who can message runs</SelectItem>
                <SelectItem value="admins_only">admins only</SelectItem>
              </SelectContent>
            </Select>
            <p id="workspace-steer-help" className="text-ui-sm text-muted">
              Applies to runs started by another member.
            </p>
          </div>
          {limitsOffered && (
            <fieldset className="min-w-0 space-y-2 border-t pt-3 text-ui">
              <legend className="float-left mb-1 font-medium">Child runs</legend>
              <p className="clear-left text-ui-sm text-muted">Limits for each tree of runs that agents start from one another. Swarm workers are not counted; their child runs are.</p>
              {limitsError && <p role="alert" className="text-ui-sm text-state-failed">{limitsError}</p>}
              {limits && limitFields.map(({ key, label, help }) => (
                <div key={key} className="grid grid-cols-[minmax(0,1fr)_6rem] items-center gap-x-3">
                  <Label htmlFor={`workspace-child-${key}`}>{label}</Label>
                  <Input
                    id={`workspace-child-${key}`}
                    type="number"
                    min={1}
                    required
                    value={limits[key]}
                    aria-describedby={`workspace-child-${key}-help`}
                    onChange={(event) => setLimits({ ...limits, [key]: event.target.value })}
                  />
                  <p id={`workspace-child-${key}-help`} className="col-span-2 text-ui-sm text-muted">{help}</p>
                </div>
              ))}
            </fieldset>
          )}
          {error && (
            <p role="alert" className="text-ui-sm text-state-failed">
              {error}
            </p>
          )}
        </form>
        <DialogFooter className="border-t pt-3">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="workspace-settings" disabled={busy}>
            {late ? stillWaiting : busy ? 'Saving…' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
