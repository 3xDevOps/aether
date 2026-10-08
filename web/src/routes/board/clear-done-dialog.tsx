import { LoaderCircle } from '@/components/icons'
import { RunRetention } from '@/components/run-retention'
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
import { runLabel } from '@/lib/status'

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
  const swarmRuns = plan.eligible.some((run) => run.mission_id)
  return (
    <Dialog open onOpenChange={(next) => !running && !next && onCancel()}>
      <DialogContent className="max-w-[min(440px,calc(100%-2rem))]">
        <DialogHeader>
          <DialogTitle>
            {n === 0 ? 'No closed runs to archive' : `Archive ${n} closed ${n === 1 ? 'run' : 'runs'}?`}
          </DialogTitle>
          <DialogDescription>
            {n === 0 ? (
              'Archive acts on merged, abandoned, failed and interrupted runs you may act on.'
            ) : (
              <>
                Archive hides these runs from Finished and keeps their history until you
                explicitly delete them. You can restore them at any time. It does not free
                their containers; free them separately if you want the memory back now.
              </>
            )}
          </DialogDescription>
        </DialogHeader>
        {(plan.notClosed > 0 || plan.notAllowed > 0 || swarmRuns) && (
          <ul className="list-disc space-y-1 pl-4 text-ui leading-5 text-muted">
            {swarmRuns && <li>Swarm runs are archived, but their swarms stay in Swarms; archive a swarm from its page.</li>}
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
              {running && <LoaderCircle className="size-3 animate-spin" aria-hidden />}
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
      <DialogContent className="max-w-[min(440px,calc(100%-2rem))]">
        <DialogHeader>
          <DialogTitle>
            {n === 0
              ? 'No finished runs keep a container'
              : `Free the retained containers of ${n} finished ${n === 1 ? 'run' : 'runs'}?`}
          </DialogTitle>
          <DialogDescription>
            {n === 0 ? (
              'This acts on finished runs you may act on that still keep their container.'
            ) : (
              <>
                Their retained containers will be removed and the runs cannot be reopened.
                Checkouts, homes and repositories follow their existing retention rules;
                history is kept until explicit deletion. This does not archive the runs.
              </>
            )}
          </DialogDescription>
        </DialogHeader>
        {n > 0 && (
          <ul className="max-h-60 space-y-3 overflow-y-auto">
            {plan.eligible.map((run) => (
              <li key={run.id}>
                <p className="text-ui font-medium">{runLabel(run)}</p>
                <RunRetention run={run} />
              </li>
            ))}
          </ul>
        )}
        <DialogFooter>
          <Button variant="secondary" disabled={running} onClick={onCancel}>
            {n === 0 ? 'Close' : 'Cancel'}
          </Button>
          {n > 0 && (
            <Button disabled={running} onClick={onConfirm}>
              {running && <LoaderCircle className="size-3 animate-spin" aria-hidden />}
              Free {n}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
