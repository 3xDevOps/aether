// The one question a needs-attention run ends on: was the work merged, or
// abandoned? Either answer records the outcome and removes the run from the
// board; a still-live container is stopped by the server.

import { Archive, GitMerge } from 'lucide-react'
import { useState } from 'react'
import { toast } from 'sonner'
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
import { message } from '@/lib/format'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'

export function CloseDialog() {
  const runID = useStore((s) => s.paletteRunID)
  const run = useStore((s) => (s.paletteRunID ? s.runs[s.paletteRunID] : undefined))
  const close = useStore((s) => s.closePaletteDialog)
  const [closing, setClosing] = useState(false)

  const finish = async (outcome: 'merged' | 'abandoned') => {
    if (!runID) return
    setClosing(true)
    try {
      await api.runClose(runID, outcome)
      close()
      toast.success(`Closed as ${outcome}`)
    } catch (err) {
      setClosing(false)
      toast.error(`Close failed: ${message(err)}`)
    }
  }

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Close this run?</DialogTitle>
          <DialogDescription>
            {run ? `"${runLabel(run)}" - ` : ''}The outcome is recorded and the run leaves the
            board. Its branch stays.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            Cancel
          </Button>
          <Button variant="outline" disabled={closing} onClick={() => void finish('abandoned')}>
            <Archive aria-hidden />
            Abandoned
          </Button>
          <Button disabled={closing} onClick={() => void finish('merged')}>
            <GitMerge aria-hidden />
            Merged
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
