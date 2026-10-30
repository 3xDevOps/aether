// Recording either outcome also makes the server stop a still-live container.

import { useState } from 'react'
import { toast } from 'sonner'
import { Archive, GitMerge } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { closeCommand, useCommandRunner } from '@/lib/commands'
import { errorSentence } from '@/lib/format'
import { runLabel } from '@/lib/status'
import { useReturnFocus } from '@/lib/hooks'
import { useStore } from '@/store'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

type Outcome = 'merged' | 'abandoned'

export function CloseDialog() {
  const returnFocus = useReturnFocus()
  const runID = useStore((s) => s.paletteRunID)
  const run = useStore((s) => (s.paletteRunID ? s.runs[s.paletteRunID] : undefined))
  const close = useStore((s) => s.closePaletteDialog)
  const perform = useCommandRunner()
  const [closing, setClosing] = useState<Outcome | null>(null)
  const late = usePastDeadline(closing !== null)
  const [error, setError] = useState<string | null>(null)

  const finish = async (outcome: Outcome) => {
    if (!runID) return
    // Over another verb's pending prediction it waits for the server instead.
    if (!useStore.getState().predicting[runID]) {
      close()
      void perform(closeCommand(runID, outcome))
      return
    }
    setClosing(outcome)
    setError(null)
    try {
      await api.runClose(runID, outcome)
      close()
      toast.success(`Closed as ${outcome}`)
    } catch (err) {
      setClosing(null)
      const detail = `Close failed: ${errorSentence(err)}`
      setError(detail)
      toast.error(detail)
    }
  }

  const pending = late ? stillWaiting : 'Closing…'

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent {...returnFocus} className="max-h-[calc(100dvh-2rem)] max-w-[min(480px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0">
        <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
          <DialogTitle>Close this session?</DialogTitle>
          <DialogDescription>
            {run ? `"${runLabel(run)}" - ` : ''}The outcome is recorded and the session leaves the
            board. Its branch stays.
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 min-w-0 overflow-y-auto px-3 py-3 sm:px-4">
          {error && (
            <p role="alert" className="break-words text-ui-sm text-state-failed">
              {error}
            </p>
          )}
        </div>
        <DialogFooter className="border-t px-3 py-3 sm:px-4">
          <Button variant="secondary" onClick={close}>
            Cancel
          </Button>
          <Button variant="secondary" disabled={closing !== null} onClick={() => void finish('abandoned')}>
            <Archive aria-hidden />
            {closing === 'abandoned' ? pending : 'Abandoned'}
          </Button>
          <Button disabled={closing !== null} onClick={() => void finish('merged')}>
            <GitMerge aria-hidden />
            {closing === 'merged' ? pending : 'Merged'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
