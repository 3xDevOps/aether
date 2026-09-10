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
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <DialogHeader>
          <DialogTitle>Send a message to the agent</DialogTitle>
          <DialogDescription>
            {run ? runLabel(run) : 'The message lands in the run transcript, attributed to you.'}
          </DialogDescription>
        </DialogHeader>
        <form
          id="inject-message"
          className="min-h-0 space-y-2 overflow-y-auto -mx-1 px-1"
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
          <p id="inject-help" className="text-xs text-muted-foreground">
            This message is added to the run transcript and delivered to the agent.
          </p>
          {error && (
            <p role="alert" className="text-xs text-state-failed">
              {error}
            </p>
          )}
        </form>
        <DialogFooter className="border-t pt-4">
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
