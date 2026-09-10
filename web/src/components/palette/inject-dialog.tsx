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
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'

export function InjectDialog() {
  const runID = useStore((s) => s.paletteRunID)
  const run = useStore((s) => (s.paletteRunID ? s.runs[s.paletteRunID] : undefined))
  const close = useStore((s) => s.closePaletteDialog)
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const send = async () => {
    if (!runID) return
    setSending(true)
    setError(null)
    try {
      await api.runInject(runID, text.trim())
      close()
      toast.success('Message sent')
    } catch (err) {
      setSending(false)
      const detail = `Send failed: ${message(err)}`
      setError(detail)
      toast.error(detail)
    }
  }

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(520px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0">
        <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
          <DialogTitle>Send a message to the agent</DialogTitle>
          <DialogDescription>
            {run ? runLabel(run) : 'The message lands in the run transcript, attributed to you.'}
          </DialogDescription>
        </DialogHeader>
        <form
          id="inject-message"
          className="min-h-0 min-w-0 space-y-2 overflow-y-auto px-3 py-3 sm:px-4"
          onSubmit={(e) => {
            e.preventDefault()
            void send()
          }}
        >
          <Label htmlFor="inject-text">Message</Label>
          <Textarea
            id="inject-text"
            autoFocus
            rows={5}
            aria-describedby="inject-help"
            placeholder="Steer the agent..."
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          <p id="inject-help" className="text-xs leading-4 text-muted-foreground">
            This message is added to the run transcript and delivered to the agent.
          </p>
          {error && (
            <p role="alert" className="break-words text-xs text-state-failed">
              {error}
            </p>
          )}
        </form>
        <DialogFooter className="border-t px-3 py-3 sm:px-4">
          <Button variant="outline" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" form="inject-message" disabled={sending || !text.trim()}>
            {sending ? 'Sending...' : 'Send'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
