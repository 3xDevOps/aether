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
import { api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'

export function InjectDialog() {
  const runID = useStore((s) => s.paletteRunID)
  const run = useStore((s) => (s.paletteRunID ? s.runs[s.paletteRunID] : undefined))
  const close = useStore((s) => s.closePaletteDialog)
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)

  const send = async () => {
    if (!runID) return
    setSending(true)
    try {
      await api.runInject(runID, text.trim())
      close()
      toast.success('Message injected')
    } catch (err) {
      setSending(false)
      toast.error(`Inject failed: ${message(err)}`)
    }
  }

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Inject a message</DialogTitle>
          <DialogDescription>
            {run ? runLabel(run) : 'The message lands in the run transcript, attributed to you.'}
          </DialogDescription>
        </DialogHeader>
        <form
          id="inject-message"
          onSubmit={(e) => {
            e.preventDefault()
            void send()
          }}
        >
          <textarea
            autoFocus
            rows={4}
            className="w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            placeholder="Steer the agent..."
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" form="inject-message" disabled={sending || !text.trim()}>
            Inject
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
