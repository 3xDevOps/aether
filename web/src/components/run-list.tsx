import { useMemo, useState, type MouseEvent } from 'react'
import { Button } from '@/components/ui/button'
import { ChevronDown, ChevronRight } from '@/components/icons'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { RunInputIndicator } from '@/components/run-input-indicator'
import { Skeleton } from '@/components/ui/skeleton'
import { RelativeTime } from '@/components/ui/relative-time'
import { useClock } from '@/lib/clock'
import { useDelayed } from '@/lib/hooks'
import { runLabel, stateLabel, stateTitle, stateTone, stateWords, toneOf, type PresentationState } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import { useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { RunRow } from '@/store/selectors'
import { rootKey, runKey, runParents } from '@/routes/board/map-layout'
import { connectors } from '@/components/shell/sidebar-runs'

export function RunList({ runs, empty }: { runs: RunRow[]; empty: string }) {
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const tree = useMemo(() => nestWorkers(runs), [runs])
  // A swarm lists its workers; child runs stay folded until asked for.
  const open = (row: RunRow) => expanded[row.run.id] ?? row.run.mission_role === 'integrator'
  const shown = (row: RunRow, workers: RunRow[]) => open(row) ? workers : workers.filter((worker) => worker.needsYou)
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable && runs.length === 0)

  if (runs.length === 0) {
    if (unreachable) {
      // A dead token is not an unreachable server: nothing retries, only a fresh token helps.
      return (
        <div
          role="alert"
          className="border-y border-state-failed/35 bg-state-failed/10 px-3 py-2 text-ui leading-5 text-state-failed sm:px-4"
        >
          <span className="break-words whitespace-pre-wrap">
            {dead ? error : 'Cannot reach the server. Retrying.'}
          </span>
        </div>
      )
    }
    if (loading) {
      return (
        <div className="grid gap-0 border-y border-seam px-3 py-2 sm:px-4">
          <div className="h-7"><Skeleton className="size-full" /></div>
          <div className="h-7"><Skeleton className="size-full" /></div>
          <div className="h-7"><Skeleton className="size-full" /></div>
        </div>
      )
    }
    return hydrated ? (
      <div className="w-full px-3 py-4 sm:px-4 sm:py-5">
        <div className="border-y border-seam px-3 py-3">
          <p className="text-ui font-medium">{empty}</p>
          <p className="mt-1 text-ui-sm leading-4 text-muted">
            New runs will appear here as soon as they are launched.
          </p>
        </div>
      </div>
    ) : null
  }

  return (
    <div className="w-full px-3 py-2 sm:px-4 sm:py-3">
      <div className="border-y border-seam">
        <div className="hidden grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] gap-4 border-b bg-chrome/30 px-3 py-1.5 text-ui-sm font-medium text-muted md:grid">
          <span>Run</span>
          <span>Owner</span>
          <span>Status</span>
          <span className="text-right">Changed</span>
        </div>
        <ul>
          {tree.map(({ row, workers }) => (
            <li key={row.run.id} className="border-b last:border-b-0">
              <Row
                row={row}
                toggle={
                  foldable(workers) > 0
                    ? {
                        count: foldable(workers),
                        expanded: open(row),
                        noun: row.run.mission_role === 'integrator' ? 'worker' : 'child run',
                        onToggle: () => setExpanded((prev) => ({ ...prev, [row.run.id]: !open(row) })),
                      }
                    : undefined
                }
              />
              {workers.length > 0 && (
                <ul
                  id={workersID(row.run.id)}
                  hidden={shown(row, workers).length === 0}
                  aria-label={`${row.run.mission_role === 'integrator' ? 'Workers' : 'Child runs'} of ${runLabel(row.run)}`}
                  className={cn(
                    '[--wire:--spacing(4.75)]',
                    foldable(workers) > 0 ? '[--trunk:--spacing(12.75)] coarse:[--trunk:--spacing(17.75)]' : '[--trunk:--spacing(4.75)]',
                  )}
                >
                  {shown(row, workers).map((worker) => (
                    <li key={worker.run.id} className={cn(connectors, 'pl-0')}>
                      <Row row={worker} nested />
                    </li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

/** Workers and child runs at any depth list under their root, in the order given. */
function nestWorkers(runs: RunRow[]): { row: RunRow; workers: RunRow[] }[] {
  const parents = runParents(runs)
  const nodes = new Map(runs.map((row) => [runKey(row), { row, workers: [] as RunRow[] }]))
  const top = new Set<{ row: RunRow; workers: RunRow[] }>()
  for (const row of runs) {
    const key = runKey(row)
    const root = nodes.get(rootKey(key, parents))!
    if (root.row !== row) root.workers.push(row)
    top.add(root)
  }
  return [...top]
}

const foldable = (workers: RunRow[]) => workers.filter((worker) => !worker.needsYou).length

const workersID = (runID: string) => `workers-${runID}`

function Row({
  row: { run, state, reason, next, cause, wait, since, owner },
  toggle,
  nested,
}: {
  row: RunRow
  nested?: boolean
  toggle?: { count: number; expanded: boolean; noun: string; onToggle: () => void }
}) {
  const navigate = useStore((s) => s.navigate)
  const now = useClock()
  const words = stateWords({ state, reason, cause, wait }, undefined, now)
  const title = stateTitle({ state, reason, next, cause, wait }, since, now)
  return (
    <div
      onClick={(event: MouseEvent<HTMLDivElement>) => {
        const target = event.target
        if (
          target instanceof Element &&
          target.closest('button, a, input, select, textarea, [data-run-navigation-exempt]')
        ) {
          return
        }
        navigate('run', { runId: run.id })
      }}
      style={{ borderLeftColor: owner?.color }}
      className={cn(
        focusRing,
        'grid w-full cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 gap-y-1 border-l-2 border-l-transparent px-3 py-2 text-left transition-colors hover:bg-hover-chrome md:grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] md:items-center',
        nested && 'pl-[calc(var(--trunk)+--spacing(3.25)+--spacing(3))]',
      )}
    >
      <div className="col-start-1 row-start-1 flex min-w-0 items-start gap-2 md:col-auto md:row-auto">
        {toggle && (
          <Button
            variant="ghost"
            size="icon-sm"
            label={`${toggle.expanded ? 'Hide' : 'Show'} ${toggle.count} ${toggle.noun}${toggle.count === 1 ? '' : 's'}`}
            aria-expanded={toggle.expanded}
            aria-controls={workersID(run.id)}
            onClick={toggle.onToggle}
          >
            {toggle.expanded ? <ChevronDown /> : <ChevronRight />}
          </Button>
        )}
        <StatusDot tone={toneOf({ state, wait })} label={stateLabel[state]} className="mt-1.5" />
        <div className="min-w-0 flex-1">
          <button
            type="button"
            aria-label={runLabel(run)}
            onClick={(event) => {
              event.stopPropagation()
              navigate('run', { runId: run.id })
            }}
            className={cn(
              focusRing,
              'block min-h-[22px] coarse:min-h-11 max-w-full rounded-chip text-left',
            )}
          >
            <span className="block line-clamp-2 break-words text-ui font-medium leading-5">
              {runLabel(run)}
            </span>
          </button>
          <span className="mt-0.5 block break-words text-ui-sm leading-4 text-muted">
            <span>{run.model ? `${run.harness} · ${run.model}` : run.harness}</span>
            <span aria-hidden> · </span>
            <span
              data-run-navigation-exempt={run.branch ? 'true' : undefined}
              className={run.branch ? 'select-text font-code' : undefined}
              title={run.branch || undefined}
              onMouseDown={
                run.branch
                  ? (event) => event.stopPropagation()
                  : undefined
              }
              onClick={
                run.branch
                  ? (event) => event.stopPropagation()
                  : undefined
              }
            >
              {run.branch || 'No branch assigned'}
            </span>
            {words.detail && (
              <>
                <span aria-hidden> · </span>
                <span title={title}>{words.detail}</span>
              </>
            )}
          </span>
          <span className="mt-0.5 block break-words text-ui-sm leading-4 text-muted md:hidden">
            {owner?.display_name ?? run.member_id}
          </span>
        </div>
      </div>
      <span className="hidden min-w-0 items-center gap-2 break-words text-ui-sm leading-4 text-muted md:flex">
        {owner?.display_name ?? run.member_id}
      </span>
      <span className="col-start-1 row-start-2 flex min-w-0 flex-wrap items-center gap-1.5 text-ui-sm text-muted md:col-auto md:row-auto">
        <span className="md:hidden">Status</span>
        <StatusChip state={state} tone={toneOf({ state, wait })} label={words.label} title={title} />
        <RunInputIndicator run={run} />
      </span>
      <RelativeTime
        at={run.stateChangedAt}
        className="col-start-2 row-start-1 whitespace-nowrap text-right text-ui-sm tabular-nums text-muted md:col-auto md:row-auto"
        title={run.stateChangedAt}
      />
    </div>
  )
}

export function RunStatusChip({ run }: { run: RunRecord }) {
  const shown = useRunPresentation(run)
  return <StatusChip state={shown.state} tone={toneOf(shown)} />
}

export function StatusChip({ state, tone = stateTone[state], label = stateLabel[state], title }: { state: PresentationState; tone?: Tone; label?: string; title?: string }) {
  return (
    <span title={title} className="inline-flex min-h-5 items-center gap-1.5 rounded-chip border border-seam/80 px-1.5 py-px text-ui-sm leading-4 text-text">
      <StatusDot tone={tone} />
      <span>{label}</span>
    </span>
  )
}
