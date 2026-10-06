import { Archive, ChevronDown, Copy, GitBranch, GitCommit, Shield } from 'lucide-react'
import { memo, useId, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import { Slot, type CardSlotName } from '@/components/slots'
import { RunInputIndicator } from '@/components/run-input-indicator'
import { Chip } from '@/components/ui/heroui'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { RelativeTime } from '@/components/ui/relative-time'
import { copyText } from '@/lib/clipboard'
import { deletesInLabel, timeAgo } from '@/lib/format'
import { runLabel, stateLabel, stateTone, type PresentationState } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import { HarnessGlyph } from '@/routes/board/harness-glyph'
import { mapCardHeight } from '@/routes/board/map-layout'
import { MemberAvatar } from '@/routes/board/member-avatar'
import { useStore } from '@/store'
import { useRunInput } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { SwarmSummary } from '@/store/selectors'

const lifecycleLabel: Record<RunRecord['status'], string> = {
  queued: 'Queued',
  provisioning: 'Provisioning',
  running: 'Running',
  'needs-attention': 'Needs attention',
  completed: 'Completed',
  merged: 'Merged',
  abandoned: 'Abandoned',
  failed: 'Failed',
  interrupted: 'Interrupted',
}

/**
 * One run, as it appears on the board. Another feature contributes to the
 * card through the slots (`card:badges`, `card:warnings`, `card:chips`, `card:footer`); the
 * card's own content is written here.
 *
 * The article is a forgiving pointer surface for its noninteractive metadata,
 * while the title block is a real button for keyboard users. Branch text and
 * slot controls opt out of the article surface so selecting or copying a
 * branch never reveals the run.
 *
 * Memoised on its props: a reducer replaces only the record of the run that
 * changed, so an event about another run leaves this card alone.
 */
export const RunCard = memo(function RunCard({
  run,
  state,
  reason,
  swarm,
  workspaceName,
  variant = 'cards',
}: {
  run: RunRecord
  state: PresentationState
  reason: string
  swarm?: SwarmSummary
  workspaceName?: string
  variant?: 'cards' | 'map'
}) {
  const owner = useStore((s) => s.members[run.member_id])
  const navigate = useStore((s) => s.navigate)
  const branchRef = useRef<HTMLSpanElement>(null)
  const [expanded, setExpanded] = useState(false)
  const detailsId = useId()
  const input = useRunInput(run)
  const unansweredCount = input.questions
  const questionAction =
    unansweredCount > 0
      ? `${unansweredCount} unanswered ${
          unansweredCount === 1 ? 'question' : 'questions'
        } - open Run Room to answer`
      : ''
  const finishedQuestion =
    Boolean(questionAction) &&
    (run.status === 'completed' ||
      run.status === 'merged' ||
      run.status === 'abandoned' ||
      run.status === 'failed' ||
      run.status === 'interrupted')
  const deletesLabel =
    run.archived_at && run.deletes_at ? deletesInLabel(run.deletes_at) : ''
  // An unanswered question is the action the member needs to take. A failed
  // run can also carry a lifecycle reason, but that reason belongs below the
  // action rather than replacing it.
  const summary = useStore((s) =>
    questionAction || s.approvalsByRun[run.id]?.[0]?.action || input.summary || run.reason || '',
  )
  const handleCardClick = (event: MouseEvent<HTMLElement>) => {
    const target = event.target
    if (
      target instanceof Element &&
      target.closest('button, a, input, select, textarea, [data-run-navigation-exempt]')
    ) {
      return
    }
    if (window.getSelection()?.isCollapsed === false) {
      return
    }
    navigate('terminal', { runId: run.id })
  }

  const disclosure = (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      aria-label={`${expanded ? 'Hide' : 'Show'} details for ${runLabel(run)}`}
      aria-expanded={expanded}
      aria-controls={expanded ? detailsId : undefined}
      onClick={variant === 'cards' ? () => setExpanded((open) => !open) : undefined}
      className="ml-auto h-[22px] min-h-[22px] shrink-0 px-1.5 text-xs"
    >
      Details
      <ChevronDown className={cn('size-3', expanded && 'rotate-180')} aria-hidden />
    </Button>
  )
  const details = expanded && (
    <div className="space-y-3 text-xs" data-run-navigation-exempt>
      {variant === 'cards' && (
        <h3 className="break-words text-sm font-medium">{runLabel(run)}</h3>
      )}
      {run.task.trim() && run.task.trim() !== runLabel(run) && (
        <p className="whitespace-pre-wrap break-words text-muted-foreground">{run.task.trim()}</p>
      )}
      {summary && (
        <p className="whitespace-pre-wrap break-words">{summary}</p>
      )}
      {finishedQuestion && (
        <p className="break-words text-muted-foreground">
          Lifecycle: {lifecycleLabel[run.status]}{run.reason ? ` - ${run.reason}` : ''}
        </p>
      )}
      {questionAction && !finishedQuestion && run.reason && (
        <p className="whitespace-pre-wrap break-words text-muted-foreground">{run.reason}</p>
      )}
      {run.branch && (
        <div className="flex items-start gap-1 text-muted-foreground">
          <GitBranch className="mt-1 size-3.5 shrink-0" aria-hidden />
          <span ref={branchRef} className="min-w-0 flex-1 break-all select-text font-mono">
            {run.branch}
          </span>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={`Copy branch ${run.branch}`}
            onClick={() => void copyText(run.branch, branchRef.current)}
          >
            <Copy className="size-3" aria-hidden />
          </Button>
        </div>
      )}
      {run.last_commit && (
        <p className="flex items-start gap-1 text-muted-foreground">
          <GitCommit className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          <span className="min-w-0 break-all select-text">
            {run.last_commit} · committed <RelativeTime at={run.last_commit_at ?? run.created_at} />
          </span>
        </p>
      )}
      <p className="break-words text-muted-foreground">
        Owner: {owner?.display_name ?? run.member_id} · Harness: {run.harness} ({run.mode})
      </p>
      <p className="text-muted-foreground">
        Created <RelativeTime at={run.created_at} />
        {run.started_at && <> · started <RelativeTime at={run.started_at} /></>}
        {' · '}{stateLabel[state].toLowerCase()} <RelativeTime at={run.stateChangedAt} />
      </p>
      <CardSlot name="card:chips" run={run} />
      <CardSlot name="card:footer" run={run} />
    </div>
  )

  return (
    <Dialog open={variant === 'map' && expanded} onOpenChange={setExpanded}>
      <article
        data-run-id={run.id}
        onClick={handleCardClick}
        style={{ borderLeftColor: owner?.color, height: variant === 'map' ? mapCardHeight : undefined }}
        className={cn(
          'group min-w-0 cursor-pointer border border-l-2 border-border bg-background transition-colors duration-100 hover:bg-toolbar-hover motion-reduce:transition-none',
        )}
      >
        <div className="flex min-w-0 flex-col gap-0.5 px-3 py-1">
          <div className="flex h-[22px] min-w-0 items-center gap-1 coarse:h-11">
            <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [&::-webkit-scrollbar]:hidden [&_button]:focus-visible:-outline-offset-2">
              <StateChip state={state} />
              <RunInputIndicator run={run} />
              {run.protected && (
                <span
                  role="img"
                  aria-label="Protected: only the owner or an admin can steer or kill this run"
                  title="Protected: only the owner or an admin can steer or kill this run"
                  className="flex size-[22px] shrink-0 items-center justify-center text-muted-foreground"
                >
                  <Shield className="size-3.5" aria-hidden />
                </span>
              )}
              {deletesLabel && (
                <span title="Archived" className="shrink-0">
                  <Chip color="default" variant="soft" size="sm">
                    <Archive className="size-3" aria-hidden />
                    <Chip.Label>{deletesLabel}</Chip.Label>
                  </Chip>
                </span>
              )}
              <CardSlot name="card:badges" run={run} />
            </div>
            <CardSlot name="card:warnings" run={run} />
            {variant === 'map' ? <DialogTrigger asChild>{disclosure}</DialogTrigger> : disclosure}
          </div>
          <button
            type="button"
            aria-label={runLabel(run)}
            onClick={() => navigate('terminal', { runId: run.id })}
            className={cn(focusRing, 'h-10 min-w-0 shrink-0 text-left text-sm leading-5')}
          >
            <span className="line-clamp-2 break-words font-medium">
              {runLabel(run)}
            </span>
          </button>
          <p
            className={cn(
              'line-clamp-2 break-words border-l-2 pl-2 text-xs leading-4 coarse:line-clamp-1',
              reasonBorder[state],
              state === 'needs-you' ? 'text-foreground/85' : 'text-muted-foreground',
            )}
          >
            {workspaceName && <span className="text-muted-foreground">{workspaceName} · </span>}
            {reason}
          </p>
          {swarm && <p className="text-xs text-muted-foreground">{swarmCounts(swarm)}</p>}
          <div className="flex h-5 min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
            <span className="min-w-0 max-w-[45%]"><HarnessGlyph harness={run.harness} mode={run.mode} /></span>
            <MemberAvatar member={owner} fallback={run.member_id} className="size-4 shrink-0 text-[9px]" />
            <span className="min-w-0 truncate" title={owner?.display_name ?? run.member_id}>{owner?.display_name ?? run.member_id}</span>
            <RelativeTime at={run.stateChangedAt} className="ml-auto shrink-0 tabular-nums" title={timestamps(run, state)} />
          </div>
        </div>
        {variant === 'cards' && expanded && (
          <div id={detailsId} className="cursor-auto border-t border-border px-3 py-3" data-run-navigation-exempt>
            {details}
          </div>
        )}
      </article>
      {variant === 'map' && (
        <DialogContent id={detailsId} aria-describedby={undefined}>
          <DialogTitle className="break-words pr-8">{runLabel(run)}</DialogTitle>
          {details}
        </DialogContent>
      )}
    </Dialog>
  )
})

const reasonBorder: Record<PresentationState, string> = {
  'needs-you': 'border-state-needs-you/60',
  working: 'border-border',
  paused: 'border-state-paused/60',
  done: 'border-state-done/60',
  failed: 'border-state-failed/60',
}

function swarmCounts({ counts }: SwarmSummary): string {
  return [
    counts.working > 0 && `${counts.working} working`,
    counts.needsYou > 0 && `${counts.needsYou} needs you`,
    counts.done > 0 && `${counts.done} done`,
    counts.failed > 0 && `${counts.failed} failed`,
  ].filter(Boolean).join(' · ') || 'No workers yet'
}

function StateChip({ state }: { state: PresentationState }) {
  return (
    <Chip
      color={stateTone[state]}
      variant="soft"
      size="sm"
      aria-label={stateLabel[state]}
    >
      <Chip.Label>{stateLabel[state]}</Chip.Label>
    </Chip>
  )
}

/** Slot content may contain its own links or buttons. */
function CardSlot({ name, run }: { name: CardSlotName; run: RunRecord }): ReactNode {
  return (
    <span data-run-navigation-exempt className={cn('flex items-center gap-1 empty:hidden', name === 'card:badges' || name === 'card:warnings' ? 'shrink-0' : 'flex-wrap')}>
      <Slot name={name} run={run} />
    </span>
  )
}

function timestamps(run: RunRecord, state: PresentationState): string {
  return [
    `Created ${timeAgo(run.created_at)}`,
    run.started_at ? `started ${timeAgo(run.started_at)}` : null,
    `${stateLabel[state].toLowerCase()} ${timeAgo(run.stateChangedAt)}`,
  ]
    .filter(Boolean)
    .join(' · ')
}
