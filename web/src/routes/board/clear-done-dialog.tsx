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
import { Spinner } from '@/components/ui/spinner'
import type { ClearDonePlan, ReleaseFinishedPlan } from '@/lib/commands'
import { runLabel } from '@/lib/status'

export function ClearDoneConfirm({
  plan,
  held,
  running,
  onConfirm,
  onCancel,
}: {
  plan: ClearDonePlan
  /** Completed sessions kept back and why; undefined while they are being checked. */
  held: Map<string, string[]> | undefined
  running: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  const archived = plan.eligible.filter((run) => !held?.has(run.id))
  const kept = plan.eligible.filter((run) => held?.has(run.id))
  const n = archived.length
  const checking = held === undefined
  return (
    <Dialog open onOpenChange={(next) => !running && !next && onCancel()}>
      <DialogContent className="max-w-[min(440px,calc(100%-2rem))]">
        <DialogHeader>
          <DialogTitle>
            {checking
              ? 'Checking finished sessions…'
              : n === 0
                ? 'No finished sessions to archive'
                : `Archive ${n} finished ${n === 1 ? 'session' : 'sessions'}?`}
          </DialogTitle>
          <DialogDescription>
            {n === 0 && !checking
              ? 'Archive acts on completed, merged, abandoned, failed and interrupted sessions you may act on.'
              : 'Hides them from Finished. History stays; restore any time. Containers keep running; free them separately.'}
          </DialogDescription>
        </DialogHeader>
        {plan.notAllowed > 0 && (
          <p className="text-ui leading-5 text-muted">
            {plan.notAllowed} {plan.notAllowed === 1 ? 'session stays' : 'sessions stay'}: you may not act on{' '}
            {plan.notAllowed === 1 ? 'it' : 'them'}.
          </p>
        )}
        {checking && plan.eligible.length > 0 && (
          <p className="flex items-center gap-2 text-ui text-muted">
            <Spinner className="size-3" /> Checking completed sessions for open pull requests, waiting sessions, subsessions and cleanup
          </p>
        )}
        {!checking && n > 0 && (
          <ul aria-label="Sessions to archive" className="max-h-40 space-y-1 overflow-y-auto text-ui">
            {archived.map((run) => (
              <li key={run.id} className="truncate">{runLabel(run)}</li>
            ))}
          </ul>
        )}
        {kept.length > 0 && (
          <section aria-label="Sessions kept">
            <h3 className="text-ui font-medium">
              Kept: {kept.length} completed {kept.length === 1 ? 'session still holds' : 'sessions still hold'} work
            </h3>
            <ul className="mt-1 max-h-40 space-y-1.5 overflow-y-auto text-ui">
              {kept.map((run) => (
                <li key={run.id}>
                  <span className="block truncate">{runLabel(run)}</span>
                  <span className="block text-ui-sm text-muted">{held?.get(run.id)?.join(' · ')}</span>
                </li>
              ))}
            </ul>
          </section>
        )}
        <DialogFooter>
          <Button variant="secondary" disabled={running} onClick={onCancel}>
            {n === 0 && !checking ? 'Close' : 'Cancel'}
          </Button>
          {(checking || n > 0) && (
            <Button disabled={running || checking} onClick={onConfirm}>
              {(running || checking) && <Spinner className="size-3 text-current" />}
              {checking ? 'Archive' : `Archive ${n}`}
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
              ? 'No finished sessions keep a container'
              : `Free the retained containers of ${n} finished ${n === 1 ? 'session' : 'sessions'}?`}
          </DialogTitle>
          <DialogDescription>
            {n === 0 ? (
              'This acts on finished sessions you may act on that still keep their container.'
            ) : (
              <>
                Their retained containers will be removed and the sessions cannot be reopened.
                Checkouts, homes and repositories follow their existing retention rules;
                history is kept until explicit deletion. This does not archive the sessions.
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
              {running && <Spinner className="size-3 text-current" />}
              Free {n}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
