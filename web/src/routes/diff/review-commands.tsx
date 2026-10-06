import type * as React from 'react'
import { CopyableCommand } from '@/components/copyable-command'
import { CodeBlock } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** Needs the gateway's `pull` verb: the commands run in a repository on this machine. */
export function useCanReviewLocally(run: RunRecord): boolean {
  const pulled = useStore((s) => s.pulls[run.id])
  const cap = useCapability()
  return cap.hasLocal('pull') && Boolean(pulled || run.branch)
}

export function ReviewLocallyDialog({
  run,
  open,
  onOpenChange,
  returnFocus,
}: {
  run: RunRecord
  open: boolean
  onOpenChange: (open: boolean) => void
  returnFocus: React.RefObject<HTMLElement | null>
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          returnFocus.current?.focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>Review locally</DialogTitle>
          <DialogDescription>Copy a command to inspect this branch in your repository.</DialogDescription>
        </DialogHeader>
        <ReviewCommands run={run} />
      </DialogContent>
    </Dialog>
  )
}

export function ReviewCommands({ run }: { run: RunRecord }) {
  const base = useStore((s) => s.diffs[run.id]?.base ?? '')
  const pulled = useStore((s) => s.pulls[run.id])
  if (!useCanReviewLocally(run)) return null

  return (
    <section aria-label="Review locally" className="grid min-w-0 gap-2">
      {run.branch && (
        <>
          <CopyableCommand command={`git log --oneline aether/${run.branch}`} />
          <CopyableCommand command={`git diff ${base.slice(0, 8) || 'main'}...aether/${run.branch}`} />
        </>
      )}
      {pulled && (
        <Collapsible className="min-w-0">
          <CollapsibleTrigger>
            <span className="truncate text-muted">fetched {pulled.ref}</span>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-1 max-h-48 overflow-y-auto">
              <CodeBlock className="whitespace-pre-wrap">{pulled.output}</CodeBlock>
            </div>
          </CollapsibleContent>
        </Collapsible>
      )}
    </section>
  )
}
