// Budget administration for one workspace, over budget.set. The server owns
// the arithmetic and the refusal; this form only carries the numbers.

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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api, type Api } from '@/lib/api'

export function BudgetDialog({
  workspaceID,
  client = api,
  onClose,
}: {
  workspaceID: string
  client?: Api
  onClose: () => void
}) {
  const [limit, setLimit] = useState('')
  const [warn, setWarn] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const save = async (clear: boolean) => {
    setBusy(true)
    setError(null)
    try {
      if (clear) {
        await client.budgetSet({ workspace_id: workspaceID, clear: true })
      } else {
        await client.budgetSet({
          workspace_id: workspaceID,
          limit_usd: Number(limit),
          warn_usd: warn.trim() ? Number(warn) : undefined,
        })
      }
      onClose()
      toast.success(clear ? 'Budget cleared' : 'Budget set')
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  const limitValid = limit.trim() !== '' && Number.isFinite(Number(limit))

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <DialogHeader>
          <DialogTitle>Workspace budget</DialogTitle>
          <DialogDescription>
            A budget warns and reports; it never stops a run.
          </DialogDescription>
        </DialogHeader>
        <form
          id="budget-set"
          className="min-h-0 space-y-4 overflow-y-auto -mx-1 px-1"
          onSubmit={(e) => {
            e.preventDefault()
            void save(false)
          }}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="budget-limit">Limit (USD)</Label>
              <Input
                id="budget-limit"
                autoFocus
                type="number"
                min="0"
                step="any"
                value={limit}
                onChange={(e) => setLimit(e.target.value)}
                aria-describedby="budget-limit-help"
              />
              <p id="budget-limit-help" className="text-xs text-muted-foreground">
                Maximum spend reported for this workspace.
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="budget-warn">Warn at (USD)</Label>
              <Input
                id="budget-warn"
                type="number"
                min="0"
                step="any"
                value={warn}
                onChange={(e) => setWarn(e.target.value)}
                aria-describedby="budget-warn-help"
              />
              <p id="budget-warn-help" className="text-xs text-muted-foreground">
                Optional threshold for an early warning.
              </p>
            </div>
          </div>
          {error && (
            <p role="alert" className="text-xs text-state-failed">
              {error}
            </p>
          )}
        </form>
        <DialogFooter className="border-t pt-4">
          <Button variant="ghost" disabled={busy} onClick={() => void save(true)}>
            Clear budget
          </Button>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="budget-set" disabled={busy || !limitValid}>
            {busy ? 'Saving...' : 'Set'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
