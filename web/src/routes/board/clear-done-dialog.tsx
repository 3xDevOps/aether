import { useState } from 'react'
import { CloseWarningLines } from '@/components/close-warnings'
import { RunRetention } from '@/components/run-retention'
import { Button } from '@/components/ui/button'
import { CountBadge } from '@/components/ui/count-badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { InsetList } from '@/components/ui/inset-list'
import { SectionLabel } from '@/components/ui/section-label'
import { Spinner } from '@/components/ui/spinner'
import type { ClearDonePlan, ReleaseFinishedPlan } from '@/lib/commands'
import type { RunCloseCheck } from '@/lib/types'
import { runLabel } from '@/lib/status'

const shownNames = 5

export function ClearDoneConfirm({
  plan,
  held,
  running,
  onConfirm,
  onCancel,
  onRecheck,
}: {
  plan: ClearDonePlan
  /** Completed sessions kept back and what they hold; undefined while they are being checked. */
  held: Map<string, RunCloseCheck> | undefined
  running: boolean
  onConfirm: () => void
  onCancel: () => void
  onRecheck: () => void
}) {
  const [allNames, setAllNames] = useState(false)
  const archived = plan.eligible.filter((run) => !held?.has(run.id))
  const kept = plan.eligible.filter((run) => held?.has(run.id) && !held.get(run.id)?.error)
  const unchecked = plan.eligible.filter((run) => held?.get(run.id)?.error)
  const n = archived.length
  const checking = held === undefined
  const names = allNames ? archived : archived.slice(0, shownNames)
  return (
    <Dialog open onOpenChange={(next) => !running && !next && onCancel()}>
      <DialogContent className="max-w-[min(480px,calc(100%-2rem))]">
        <DialogHeader>
          <DialogTitle>
            {checking
              ? 'Checking finished sessions…'
              : n === 0
                ? 'Nothing to archive'
                : `Archive ${n} finished ${n === 1 ? 'session' : 'sessions'}?`}
          </DialogTitle>
          <DialogDescription>
            {n === 0 && !checking && kept.length === 0 && unchecked.length === 0
              ? 'Archive acts on completed, merged, abandoned, failed and interrupted sessions you may act on.'
              : 'Hides them from Finished; restore any time from Archived at the foot of Finished. Archiving does not free their retained containers.'}
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
          <section className="flex flex-col gap-1.5">
            <SectionLabel as="h3" className="flex items-center gap-1.5">Archiving <CountBadge>{n}</CountBadge></SectionLabel>
            <InsetList tabIndex={0} aria-label="Sessions to archive" className="max-h-48 overflow-y-auto">
              {names.map((run) => (
                <li key={run.id} className="shrink-0 truncate px-3 py-1.5 text-ui">{runLabel(run)}</li>
              ))}
            </InsetList>
            {n > shownNames && (
              <Button variant="quiet" size="sm" className="self-start text-ui-sm" aria-expanded={allNames} onClick={() => setAllNames(!allNames)}>
                {allNames ? 'Show fewer' : `${n - shownNames} more`}
              </Button>
            )}
          </section>
        )}
        {kept.length > 0 && (
          <section className="flex flex-col gap-1.5 border-t border-seam pt-4">
            <SectionLabel as="h3" className="flex items-center gap-1.5">
              Kept <CountBadge>{kept.length}</CountBadge>
              <span className="font-normal">completed, still holding work</span>
            </SectionLabel>
            <InsetList tabIndex={0} aria-label="Sessions kept" className="max-h-56 overflow-y-auto">
              {kept.map((run) => (
                <li key={run.id} className="flex min-w-0 shrink-0 flex-col gap-1 px-3 py-2">
                  <span className="truncate text-ui text-muted">{runLabel(run)}</span>
                  <CloseWarningLines check={held!.get(run.id)!} run={run} onLeave={onCancel} />
                </li>
              ))}
            </InsetList>
          </section>
        )}
        {unchecked.length > 0 && (
          <div role="alert" className="flex flex-col items-start gap-1 text-ui-sm text-state-failed">
            <p className="break-words">
              Could not check {unchecked.length} completed {unchecked.length === 1 ? 'session' : 'sessions'}, so{' '}
              {unchecked.length === 1 ? 'it stays' : 'they stay'}: {held!.get(unchecked[0].id)!.error!.message}
            </p>
            <Button variant="quiet" size="sm" className="text-ui-sm" disabled={running} onClick={onRecheck}>
              Check again
            </Button>
          </div>
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
