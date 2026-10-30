import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { RunRetention } from '@/components/run-retention'
import type { Command } from '@/lib/commands'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'
import { unfinishedChildren } from '@/store/children'
import type { RunRecord } from '@/store/runs'

export function RunCommandConfirmation({
  run,
  confirmation,
  onConfirm,
  onClose,
  onCloseAutoFocus,
}: {
  run: RunRecord
  confirmation: NonNullable<Command['confirm']>
  onConfirm: () => void
  onClose: () => void
  onCloseAutoFocus?: (event: Event) => void
}) {
  const runs = useStore((s) => s.runs)
  const children = confirmation.stopsChildren ? unfinishedChildren(runs, run) : []
  const stops = children.length === 1
    ? 'Its unfinished child run stops too, with any runs it started.'
    : `Its ${children.length} unfinished child runs stop too, with any runs they started.`
  return (
    <AlertDialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <AlertDialogContent
        className="max-w-[min(420px,calc(100%-2rem))]"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>{confirmation.title}</AlertDialogTitle>
          <AlertDialogDescription>
            &quot;{runLabel(run)}&quot; - {confirmation.body}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {children.length > 0 && (
          <div className="flex flex-col gap-2 text-ui-sm">
            <p>{stops}</p>
          </div>
        )}
        <RunRetention run={run} />
        <AlertDialogFooter>
          {/* Radix AlertDialog focuses Cancel when the confirmation opens. */}
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>
            {confirmation.action}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
