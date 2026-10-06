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
import type { ClearDonePlan, ReleaseFinishedPlan } from '@/lib/commands'

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
            {n === 0 ? 'No closed runs to archive' : `Archive ${n} closed ${n === 1 ? 'run' : 'runs'}?`}
          </DialogTitle>
          <DialogDescription>
            {n === 0 ? (
              'Archive acts on merged, abandoned, failed and interrupted runs you may act on.'
            ) : (
              <>
                Archive hides these runs and schedules their deletion after the retention
                period. It does not free container memory; release resources separately
                before archiving if you want to free them now.
              </>
            )}
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
          <Button variant="secondary" disabled={running} onClick={onCancel}>
            {n === 0 ? 'Close' : 'Cancel'}
          </Button>
          {n > 0 && (
            <Button disabled={running} onClick={onConfirm}>
              {running && <Loader2 className="size-3 animate-spin" aria-hidden />}
              Archive {n}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function ReleaseFinishedConfirm({
  plan,
  running,
  onConfirm,
  onCancel,
}: {
  plan: ReleaseFinishedPlan
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
            {n === 0
              ? 'No finished runs hold resources'
              : `Release resources for ${n} finished ${n === 1 ? 'run' : 'runs'}?`}
          </DialogTitle>
          <DialogDescription>
            {n === 0 ? (
              'Release acts on finished runs you may act on that still keep their container.'
            ) : (
              <>
                Their retained containers will be removed and cannot be relaunched.
                Run records and history remain visible; this does not archive or delete them.
              </>
            )}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="secondary" disabled={running} onClick={onCancel}>
            {n === 0 ? 'Close' : 'Cancel'}
          </Button>
          {n > 0 && (
            <Button disabled={running} onClick={onConfirm}>
              {running && <Loader2 className="size-3 animate-spin" aria-hidden />}
              Release {n}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
