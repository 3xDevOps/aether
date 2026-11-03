// Recording either outcome also makes the server stop a still-live container.

import { useRef, useState } from 'react'
import { toast } from 'sonner'
import { CloseWarnings, useCloseCheck } from '@/components/close-warnings'
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
import { closeWarned } from '@/lib/close-check'
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
  const check = useCloseCheck(runID)
  const cancel = useRef<HTMLButtonElement>(null)

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
  const label = (outcome: Outcome, plain: string) => {
    if (closing === outcome) return pending
    if (!check) return 'Checking…'
    return closeWarned(check, outcome) ? `Close anyway as ${outcome}` : plain
  }

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent
        {...returnFocus}
        onOpenAutoFocus={(event) => {
          returnFocus.onOpenAutoFocus()
          event.preventDefault()
          cancel.current?.focus()
        }}
        className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0">
        <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
          <DialogTitle>Close this session?</DialogTitle>
          <DialogDescription>
            {run ? `"${runLabel(run)}" - ` : ''}The outcome is recorded and the session leaves the
            board. Its branch stays.
          </DialogDescription>
        </DialogHeader>
        <div className="flex min-h-0 min-w-0 flex-col gap-2 overflow-y-auto px-3 py-3 sm:px-4">
          {!check && <p className="text-ui-sm text-muted">Checking what closing affects…</p>}
          {check && run && <CloseWarnings check={check} run={run} onLeave={close} />}
          {error && (
            <p role="alert" className="break-words text-ui-sm text-state-failed">
              {error}
            </p>
          )}
        </div>
        <DialogFooter className="border-t px-3 py-3 sm:flex-wrap sm:px-4">
          <Button ref={cancel} variant="secondary" onClick={close}>
            Cancel
          </Button>
          <Button variant="secondary" disabled={closing !== null || !check} onClick={() => void finish('abandoned')}>
            <Archive aria-hidden />
            {label('abandoned', 'Abandoned')}
          </Button>
          <Button disabled={closing !== null || !check} onClick={() => void finish('merged')}>
            <GitMerge aria-hidden />
            {label('merged', 'Merged')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
