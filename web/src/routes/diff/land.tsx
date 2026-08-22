import { Archive, Copy, GitMerge, Loader2 } from 'lucide-react'
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
import type { PullResult } from '@/lib/types'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/**
 * The "review and land" tail of the diff tab: fetch the run branch into the
 * linked repository (desktop gateways only), copy the commands that review
 * it there, and close the run once it is judged. Closing never touches the
 * store - the run.status event that follows does.
 */
export function LandControls({ run }: { run: RunRecord }) {
  const caps = useCapability()
  const base = useStore((s) => s.diffs[run.id]?.base ?? '')
  const [pulling, setPulling] = useState(false)
  const [pulled, setPulled] = useState<PullResult | null>(null)
  const [closing, setClosing] = useState<'merged' | 'abandoned' | null>(null)

  const pull = async () => {
    setPulling(true)
    try {
      const result = await api.localPull(run.id)
      setPulled(result)
      toast.success(`fetched ${result.ref}`)
    } catch (err) {
      toast.error(message(err))
    } finally {
      setPulling(false)
    }
  }

  const close = async () => {
    if (!closing) return
    const outcome = closing
    setClosing(null)
    try {
      await api.runClose(run.id, outcome)
      toast.success(`Closed as ${outcome}`)
    } catch (err) {
      toast.error(message(err))
    }
  }

  return (
    <>
      {caps.hasLocal('pull') && (
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2"
          disabled={pulling}
          onClick={() => void pull()}
        >
          {pulling && <Loader2 className="size-3 animate-spin" aria-hidden />}
          Pull branch
        </Button>
      )}
      {run.status === 'needs-attention' && (
        <>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2"
            onClick={() => setClosing('merged')}
          >
            <GitMerge className="size-3" aria-hidden />
            Close as merged
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2"
            onClick={() => setClosing('abandoned')}
          >
            <Archive className="size-3" aria-hidden />
            Close as abandoned
          </Button>
        </>
      )}

      {pulled && (
        <details className="basis-full">
          <summary className="cursor-pointer select-none">
            fetched {pulled.ref}
          </summary>
          <pre className="mt-1 max-h-48 overflow-auto rounded-md border bg-muted/50 p-2 font-mono text-[11px] whitespace-pre-wrap">
            {pulled.output}
          </pre>
        </details>
      )}

      {run.branch && (
        <div className="basis-full space-y-1">
          <ReviewCommand command={`git log --oneline aether/${run.branch}`} />
          <ReviewCommand
            command={`git diff ${base.slice(0, 8) || 'main'}...aether/${run.branch}`}
          />
        </div>
      )}

      {closing && (
        <Dialog open onOpenChange={() => setClosing(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Close as {closing}?</DialogTitle>
              <DialogDescription>
                {closing === 'merged'
                  ? `The run for "${run.task}" is recorded as merged and leaves the board.`
                  : `The run for "${run.task}" is recorded as abandoned and leaves the board.`}
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setClosing(null)}>
                Cancel
              </Button>
              <Button variant="destructive" onClick={() => void close()}>
                Close as {closing}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}

/** One copyable review command: the text, and a button that clipboards it. */
function ReviewCommand({ command }: { command: string }) {
  return (
    <div className="flex items-center gap-1">
      <code className="min-w-0 truncate rounded bg-muted/50 px-1.5 py-0.5 font-mono text-[11px]">
        {command}
      </code>
      <Button
        variant="ghost"
        size="icon"
        className="size-5"
        aria-label={`Copy ${command}`}
        onClick={() => {
          void navigator.clipboard.writeText(command).then(
            () => toast.success('Copied'),
            (err: unknown) => toast.error(message(err)),
          )
        }}
      >
        <Copy className="size-3" aria-hidden />
      </Button>
    </div>
  )
}
