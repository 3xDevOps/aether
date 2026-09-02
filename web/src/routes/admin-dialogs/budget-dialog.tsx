// Budget administration for one workspace, over budget.set. The server owns
// the arithmetic and the refusal; this form only carries the numbers.

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

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

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
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Workspace budget</DialogTitle>
          <DialogDescription>
            A budget warns and reports; it never stops a run.
          </DialogDescription>
        </DialogHeader>
        <form
          id="budget-set"
          className="flex gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void save(false)
          }}
        >
          <label className="flex-1 space-y-1 text-sm">
            Limit (USD)
            <input
              autoFocus
              type="number"
              min="0"
              step="any"
              className={field}
              value={limit}
              onChange={(e) => setLimit(e.target.value)}
            />
          </label>
          <label className="flex-1 space-y-1 text-sm">
            Warn at (USD)
            <input
              type="number"
              min="0"
              step="any"
              className={field}
              value={warn}
              onChange={(e) => setWarn(e.target.value)}
            />
          </label>
        </form>
        {error && <p className="text-xs text-state-failed">{error}</p>}
        <DialogFooter>
          <Button variant="ghost" disabled={busy} onClick={() => void save(true)}>
            Clear budget
          </Button>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="budget-set" disabled={busy || !limitValid}>
            Set
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
