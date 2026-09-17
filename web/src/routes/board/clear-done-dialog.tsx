import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { ClearDonePlan } from '@/lib/commands'

/**
 * The one Clear done confirm dialog, shared by the Done header's button and
 * the "Clear done runs" palette entry. `plan` is a snapshot taken when the
 * dialog opened, so a run leaving Done mid-archive cannot change what the
 * title, the counts, or the confirm button say while it is open.
 */
export function ClearDoneConfirm({
  plan,
  running,
  onConfirm,
  onCancel,
}: {
  plan: ClearDonePlan
  running: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  const n = plan.eligible.length
  return (
    <Dialog open onOpenChange={(next) => !running && !next && onCancel()}>
      <DialogContent className="max-w-[min(440px,calc(100%-2rem))] p-3 sm:p-4">
        <DialogHeader>
          <DialogTitle>
            Archive {n} finished {n === 1 ? 'run' : 'runs'}?
          </DialogTitle>
          <DialogDescription>
            Archived runs leave the board. The server deletes them once
            the retention period passes; until then, restore them from
            the Archived toggle.
          </DialogDescription>
        </DialogHeader>
        {(plan.notClosed > 0 || plan.notAllowed > 0) && (
          <ul className="list-disc space-y-1 pl-4 text-[13px] leading-5 text-muted-foreground">
            {plan.notClosed > 0 && (
              <li>
                {plan.notClosed} {plan.notClosed === 1 ? 'run stays' : 'runs stay'}: completed but
                not yet closed - close them as merged or abandoned first.
              </li>
            )}
            {plan.notAllowed > 0 && (
              <li>
                {plan.notAllowed} {plan.notAllowed === 1 ? 'run stays' : 'runs stay'}: you may not
                act on {plan.notAllowed === 1 ? 'it' : 'them'}.
              </li>
            )}
          </ul>
        )}
        <DialogFooter>
          <Button variant="outline" disabled={running} onClick={onCancel}>
            Cancel
          </Button>
          <Button disabled={running} onClick={onConfirm}>
            {running && <Loader2 className="size-3 animate-spin" aria-hidden />}
            Archive {n}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
