import { useLayoutEffect, useRef, useState } from 'react'
import { CloseWarningLines } from '@/components/close-warnings'
import { ChevronRight } from '@/components/icons'
import { RunRetention } from '@/components/run-retention'
import { Checkbox } from '@/components/ui/checkbox'
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
import type { IdleCandidate, IdleRow } from '@/lib/idle-archive'
import { cn, focusRingShown } from '@/lib/utils'
import type { RunCloseCheck } from '@/lib/types'
import { compactAge } from '@/lib/format'
import { finishedReason, runLabel } from '@/lib/status'

const shownNames = 5

export function ClearDoneConfirm({
  plan,
  held,
  idle,
  excluded,
  selected,
  running,
  onSelect,
  onConfirm,
  onCancel,
  onRecheck,
}: {
  plan: ClearDonePlan
  /** Completed sessions kept back and what they hold; undefined while they are being checked. */
  held: Map<string, RunCloseCheck> | undefined
  /** Live sessions idle and done for over a day; undefined while they are being checked. */
  idle: { idle: IdleRow[] } | undefined
  /** Live sessions not offered, with what keeps each. */
  excluded: IdleCandidate[]
  /** Idle sessions the member chose to close and archive. */
  selected: Set<string>
  running: boolean
  onSelect: (selected: Set<string>) => void
  onConfirm: () => void
  onCancel: () => void
  onRecheck: () => void
}) {
  const [allNames, setAllNames] = useState(false)
  const cancel = useRef<HTMLButtonElement>(null)
  const archived = plan.eligible.filter((run) => !held?.has(run.id))
  const kept = plan.eligible.filter((run) => held?.has(run.id) && !held.get(run.id)?.error)
  const unchecked = plan.eligible.filter((run) => held?.get(run.id)?.error)
  const rows = idle?.idle ?? []
  const closing = rows.filter((row) => selected.has(row.run.id)).length
  const n = archived.length + closing
  const checking = held === undefined || idle === undefined
  const names = allNames ? archived : archived.slice(0, shownNames)
  const sessions = (count: number) => `${count} ${count === 1 ? 'session' : 'sessions'}`
  return (
    <Dialog open onOpenChange={(next) => !running && !next && onCancel()}>
      <DialogContent
        className="max-w-[min(520px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto]"
        onOpenAutoFocus={(event) => {
          event.preventDefault()
          cancel.current?.focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>
            {checking
              ? 'Checking sessions…'
              : n === 0
                ? 'Nothing to archive'
                : closing > 0
                  ? `Archive ${sessions(n)}, closing ${closing} first?`
                  : `Archive ${n} finished ${n === 1 ? 'session' : 'sessions'}?`}
          </DialogTitle>
          <DialogDescription>
            {n === 0 && !checking && kept.length === 0 && unchecked.length === 0 && rows.length === 0
              ? 'Archive acts on completed, merged, abandoned, failed and interrupted sessions you may act on, and on live ones idle and done for over a day.'
              : 'Moves them to Archived, where you can restore them.'}
            {closing > 0 && ' Checked idle sessions are closed with the outcome shown — their agents stop — then archived.'}
          </DialogDescription>
        </DialogHeader>
        {/* Only the body scrolls, so the buttons stay in view on a phone sheet. */}
        <div className="-mx-1 flex min-h-0 flex-col gap-3 overflow-y-auto px-1">
          {plan.notAllowed > 0 && (
            <p className="text-ui leading-5 text-muted">
              {plan.notAllowed} {plan.notAllowed === 1 ? 'session stays' : 'sessions stay'}: you may not act on{' '}
              {plan.notAllowed === 1 ? 'it' : 'them'}.
            </p>
          )}
          {checking && (
            <p className="flex items-center gap-2 text-ui text-muted">
              <Spinner className="size-3" /> Checking sessions for open pull requests, waiting sessions, subsessions and cleanup
            </p>
          )}
          <div className="flex min-w-0 flex-col divide-y divide-seam [&>section]:py-4 [&>section:first-child]:pt-0 [&>section:last-child]:pb-0">
            {!checking && archived.length > 0 && (
              <section className="flex flex-col gap-1.5">
                <SectionLabel as="h3" className="flex items-center gap-1.5">Archiving <CountBadge>{archived.length}</CountBadge></SectionLabel>
                <InsetList tabIndex={0} aria-label="Sessions to archive" className="max-h-48 overflow-y-auto">
                  {names.map((run) => (
                    <li key={run.id} className="flex min-w-0 shrink-0 items-baseline gap-2 px-3 py-1.5">
                      <span className="min-w-0 truncate text-ui">{runLabel(run)}</span>
                      <span className="ml-auto shrink-0 text-ui-sm text-muted">{finishedReason(run)} · {compactAge(run.finished_at ?? run.stateChangedAt)}</span>
                    </li>
                  ))}
                </InsetList>
                {archived.length > shownNames && (
                  <Button variant="quiet" size="sm" className="self-start text-ui-sm" aria-expanded={allNames} onClick={() => setAllNames(!allNames)}>
                    {allNames ? 'Show fewer' : `Show ${archived.length - shownNames} more`}
                  </Button>
                )}
              </section>
            )}
            {!checking && rows.length > 0 && (
              <IdleSection rows={rows} selected={selected} running={running} onSelect={onSelect} onLeave={onCancel} />
            )}
            {kept.length > 0 && (
              <section className="flex flex-col gap-1.5">
                <SectionLabel as="h3" className="flex items-center gap-1.5">
                  Kept <CountBadge>{kept.length}</CountBadge>
                  <span className="font-normal">— completed, still holding work</span>
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
            {!checking && excluded.length > 0 && <NotIncluded excluded={excluded} />}
          </div>
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
        </div>
        <DialogFooter>
          <Button ref={cancel} variant="secondary" className={focusRingShown} disabled={running} onClick={onCancel}>
            {n === 0 && !checking ? 'Close' : 'Cancel'}
          </Button>
          <Button disabled={running || checking || n === 0} onClick={onConfirm}>
            {(running || checking) && <Spinner className="size-3 text-current" />}
            {checking ? 'Archive' : closing > 0 ? `Close ${closing}, archive ${n}` : `Archive ${n}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function IdleSection({ rows, selected, running, onSelect, onLeave }: {
  rows: IdleRow[]
  selected: Set<string>
  running: boolean
  onSelect: (selected: Set<string>) => void
  onLeave: () => void
}) {
  const list = useRef<HTMLUListElement>(null)
  const below = useMoreBelow(list)
  const checked = rows.filter((row) => selected.has(row.run.id)).length
  // Select all takes only rows with nothing to warn about; a warned row needs its own click.
  const clean = rows.filter((row) => row.clean)
  const allClean = clean.length > 0 && clean.every((row) => selected.has(row.run.id))
  const toggle = (id: string) => {
    const next = new Set(selected)
    if (!next.delete(id)) next.add(id)
    onSelect(next)
  }
  const selectClean = () => {
    const next = new Set(selected)
    for (const row of clean) {
      if (allClean) next.delete(row.run.id)
      else next.add(row.run.id)
    }
    onSelect(next)
  }
  return (
    <section className="flex flex-col gap-1.5">
      <div className="flex items-center gap-2.5 px-3">
        <Checkbox
          aria-label="Select all idle sessions without warnings"
          checked={allClean ? true : checked === 0 ? false : 'indeterminate'}
          disabled={running || clean.length === 0}
          onCheckedChange={selectClean}
        />
        <SectionLabel as="h3" className="flex items-center gap-1.5" title="Live sessions whose agent replied or ended its turn over a day ago, with nothing left to do">
          Idle <CountBadge>{`${checked} of ${rows.length}`}</CountBadge>
          <span className="font-normal">· closed, then archived</span>
        </SectionLabel>
      </div>
      <InsetList
        ref={list}
        aria-label="Idle sessions"
        className="md:max-h-64 md:overflow-y-auto"
        style={below ? { maskImage: 'linear-gradient(to bottom, #000 calc(100% - 28px), transparent)' } : undefined}
      >
        {rows.map((row) => {
          const id = `idle-${row.run.id}`
          const on = selected.has(row.run.id)
          return (
            <li key={row.run.id} className="flex min-w-0 shrink-0 gap-2.5 px-3 py-2">
              <Checkbox id={id} className="mt-0.5" checked={on} disabled={running} onCheckedChange={() => toggle(row.run.id)} />
              <div className="flex min-w-0 flex-col gap-1">
                <label htmlFor={id} className="truncate text-ui">{runLabel(row.run)}</label>
                <span className="text-ui-sm">
                  <span className="text-text">Closes as {row.outcome}</span>
                  <span className="text-muted"> · {row.reason}</span>
                </span>
                {row.outcomeUnknown && (
                  <span className="text-ui-sm text-muted">This server cannot tell whether its pull request merged.</span>
                )}
                {!row.clean && (on
                  ? <span className="text-ui-sm text-text">Closing anyway.</span>
                  : <span className="text-ui-sm text-muted">Check to close anyway.</span>)}
                {(row.check.error || row.check.warnings.length > 0) && <CloseWarningLines check={row.check} run={row.run} onLeave={onLeave} />}
              </div>
            </li>
          )
        })}
      </InsetList>
    </section>
  )
}

/** Whether the element scrolls and hides more below its bottom edge. */
function useMoreBelow(ref: React.RefObject<HTMLElement | null>): boolean {
  const [below, setBelow] = useState(false)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const measure = () => setBelow(el.scrollTop < el.scrollHeight - el.clientHeight - 1)
    measure()
    el.addEventListener('scroll', measure, { passive: true })
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => {
      el.removeEventListener('scroll', measure)
      observer.disconnect()
    }
  }, [ref])
  return below
}

function NotIncluded({ excluded }: { excluded: IdleCandidate[] }) {
  const [open, setOpen] = useState(false)
  return (
    <section className="flex flex-col gap-1.5">
      <Button variant="quiet" size="sm" className="self-start text-ui-sm" aria-expanded={open} onClick={() => setOpen(!open)}>
        <ChevronRight aria-hidden className={cn('size-3.5 transition-transform motion-reduce:transition-none', open && 'rotate-90')} />
        Not included <CountBadge>{excluded.length}</CountBadge>
      </Button>
      {open && (
        <InsetList aria-label="Live sessions not included" className="max-h-48 overflow-y-auto">
          {excluded.map(({ run, reason }) => (
            <li key={run.id} className="flex min-w-0 shrink-0 flex-col px-3 py-1.5">
              <span className="truncate text-ui">{runLabel(run)}</span>
              <span className="text-ui-sm text-muted">{reason}</span>
            </li>
          ))}
        </InsetList>
      )}
    </section>
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
