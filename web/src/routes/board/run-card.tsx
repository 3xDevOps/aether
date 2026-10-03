import { Archive, ChevronDown, Copy, GitBranch, GitCommit, PauseCircle, Shield } from 'lucide-react'
import { useId, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import { Slot, type CardSlotName } from '@/components/slots'
import { RunInputIndicator } from '@/components/run-input-indicator'
import { StateIndicator } from '@/components/state-dot'
import { Chip } from '@/components/ui/heroui'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { copyText } from '@/lib/clipboard'
import { deletesInLabel, timeAgo } from '@/lib/format'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import { HarnessGlyph } from '@/routes/board/harness-glyph'
import { MemberAvatar } from '@/routes/board/member-avatar'
import type { BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { approvalsForRun } from '@/store/approvals'
import { useRunInput } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

const lifecycleLabel: Record<RunRecord['status'], string> = {
  queued: 'Queued',
  provisioning: 'Provisioning',
  running: 'Running',
  'needs-attention': 'Idle',
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
 */
export function RunCard({
  card,
  variant = 'cards',
}: {
  card: BoardCard
  variant?: 'cards' | 'map'
}) {
  const { run, state, owner, unseen, paused } = card
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
    questionAction || approvalsForRun(s.inbox, run.id)[0]?.action || input.summary || run.reason || '',
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
      className="ml-auto shrink-0"
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
        <p className="break-all select-text font-mono text-muted-foreground">
          <span className="font-sans">Branch: </span>{run.branch}
        </p>
      )}
      {run.last_commit && (
        <p className="flex items-start gap-1 text-muted-foreground">
          <GitCommit className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          <span className="min-w-0 break-all select-text">
            {run.last_commit} · committed {timeAgo(run.last_commit_at ?? run.created_at)}
          </span>
        </p>
      )}
      <p className="text-muted-foreground">{timestamps(card)}</p>
      <CardSlot name="card:chips" run={run} />
      <CardSlot name="card:footer" run={run} />
    </div>
  )

  return (
    <Dialog open={variant === 'map' && expanded} onOpenChange={setExpanded}>
      <article
        data-run-id={run.id}
        onClick={handleCardClick}
        style={{ borderLeftColor: owner?.color }}
        className={cn(
          'group min-w-0 cursor-pointer border border-l-2 border-border bg-background transition-colors duration-100 hover:bg-toolbar-hover motion-reduce:transition-none',
          unseen && 'border-foreground/25',
        )}
      >
        <div className="grid h-[190px] min-w-0 grid-rows-[22px_40px_32px_20px_1fr] gap-1 px-3 py-2 coarse:grid-rows-[44px_40px_16px_20px_1fr] coarse:gap-0.5">
          <div className="flex min-w-0 items-center gap-1">
            <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [&::-webkit-scrollbar]:hidden [&_button]:focus-visible:-outline-offset-2">
              <StateIndicator state={state} decorative className="shrink-0" />
              <StateChip state={state} />
              <RunInputIndicator run={run} />
              {unseen && (
                <Chip color="accent" variant="soft" size="sm" aria-label="Unseen">
                  <Chip.Label>New</Chip.Label>
                </Chip>
              )}
              {paused && (
                <span title="Paused" className="shrink-0">
                  <Chip color="warning" variant="soft" size="sm">
                    <PauseCircle className="size-3" aria-hidden />
                    <Chip.Label>Paused</Chip.Label>
                  </Chip>
                </span>
              )}
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
          </div>
          <button
            type="button"
            aria-label={runLabel(run)}
            onClick={() => navigate('terminal', { runId: run.id })}
            className={cn(focusRing, 'min-w-0 self-start text-left text-sm leading-5')}
          >
            <span className={cn('line-clamp-2 break-words font-medium', unseen && 'font-semibold')}>
              {runLabel(run)}
            </span>
          </button>
          <div className="min-w-0 overflow-hidden">
            {(input.count > 0 || state === 'needs-attention') && summary && (
              <p className="line-clamp-2 break-words border-l-2 border-state-needs-attention/60 pl-2 text-xs leading-4 text-foreground/85 coarse:line-clamp-1">
                {summary}
              </p>
            )}
          </div>
          <div className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
            <span className="min-w-0 max-w-[45%]"><HarnessGlyph harness={run.harness} mode={run.mode} /></span>
            <MemberAvatar member={owner} fallback={run.member_id} className="size-4 shrink-0 text-[9px]" />
            <span className="min-w-0 truncate" title={owner?.display_name ?? run.member_id}>{owner?.display_name ?? run.member_id}</span>
            <time className="ml-auto shrink-0 tabular-nums" title={timestamps(card)}>
              {timeAgo(run.stateChangedAt)}
            </time>
          </div>
          <div className="flex min-w-0 items-center gap-1 border-t border-border text-xs text-muted-foreground">
            {run.branch && (
              <span
                data-run-navigation-exempt
                className="flex min-w-0 flex-1 items-center gap-1"
                onMouseDown={(event) => event.stopPropagation()}
                onClick={(event) => event.stopPropagation()}
              >
                <GitBranch className="size-3.5 shrink-0" aria-hidden />
                <span
                  ref={branchRef}
                  className="min-w-0 truncate select-text font-mono text-xs"
                  title={run.branch}
                >
                  {run.branch}
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={`Copy branch ${run.branch}`}
                  onClick={(event) => {
                    event.stopPropagation()
                    void copyText(run.branch, branchRef.current)
                  }}
                >
                  <Copy className="size-3" aria-hidden />
                </Button>
              </span>
            )}
            {variant === 'map' ? <DialogTrigger asChild>{disclosure}</DialogTrigger> : disclosure}
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
}

const stateChipColor: Record<
  PresentationState,
  'accent' | 'danger' | 'default' | 'success' | 'warning'
> = {
  'needs-attention': 'warning',
  failed: 'danger',
  working: 'accent',
  waiting: 'default',
  done: 'success',
  idle: 'default',
}

function StateChip({ state }: { state: PresentationState }) {
  return (
    <Chip
      color={stateChipColor[state]}
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

function timestamps({ run, state }: BoardCard): string {
  return [
    `Created ${timeAgo(run.created_at)}`,
    run.started_at ? `started ${timeAgo(run.started_at)}` : null,
    `${stateLabel[state].toLowerCase()} ${timeAgo(run.stateChangedAt)}`,
  ]
    .filter(Boolean)
    .join(' · ')
}
