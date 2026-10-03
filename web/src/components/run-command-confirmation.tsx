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
import type { Command } from '@/lib/commands'
import { runLabel } from '@/lib/status'
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
  return (
    <AlertDialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <AlertDialogContent
        className="max-w-[min(420px,calc(100%-2rem))] p-3 sm:p-4"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>{confirmation.title}</AlertDialogTitle>
          <AlertDialogDescription>
            &quot;{runLabel(run)}&quot; - {confirmation.body}
          </AlertDialogDescription>
        </AlertDialogHeader>
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
