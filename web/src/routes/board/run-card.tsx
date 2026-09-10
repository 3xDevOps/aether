import { Copy, GitBranch, GitCommit, PauseCircle, Shield } from 'lucide-react'
import { useRef, type MouseEvent, type ReactNode } from 'react'
import { Slot, type CardSlotName } from '@/components/slots'
import { StateIndicator } from '@/components/state-dot'
import { Chip } from '@/components/ui/heroui'
import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/clipboard'
import { timeAgo } from '@/lib/format'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import { HarnessGlyph } from '@/routes/board/harness-glyph'
import { MemberAvatar } from '@/routes/board/member-avatar'
import type { BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { approvalsForRun } from '@/store/approvals'
import type { RunRecord } from '@/store/runs'

/**
 * One run, as it appears on the board. Another feature contributes to the
 * card through the slots (`card:badges`, `card:chips`, `card:footer`); the
 * card's own content is written here.
 *
 * The article is a forgiving pointer surface for its noninteractive metadata,
 * while the title block is a real button for keyboard users. Branch text and
 * slot controls opt out of the article surface so selecting or copying a
 * branch never reveals the run.
 */
export function RunCard({ card }: { card: BoardCard }) {
  const { run, state, owner, unseen, paused } = card
  const navigate = useStore((s) => s.navigate)
  const branchRef = useRef<HTMLSpanElement>(null)
  // An approval pause fires no run.status event, so the run's reason stays
  // empty; the pending question itself is the summary the card needs then.
  const summary = useStore((s) =>
    state === 'needs-attention' && !run.reason
      ? (approvalsForRun(s.inbox, run.id)[0]?.action ?? '')
      : run.reason,
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

  return (
    <article
      onClick={handleCardClick}
      style={{ borderLeftColor: owner?.color }}
      className={cn(
        'group min-w-0 cursor-pointer border-b border-l-2 border-border bg-background px-3 py-2.5 transition-colors duration-100 hover:bg-toolbar-hover',
        unseen ? 'border-foreground/25' : 'opacity-90',
      )}
    >
      <div className="flex min-w-0 items-start gap-2">
        {/* This is the first actionable element in each card for keyboard users. */}
        <button
          type="button"
          aria-label={runLabel(run)}
          onClick={(event) => {
            event.stopPropagation()
            navigate('terminal', { runId: run.id })
          }}
          className={cn(
            focusRing,
            'flex min-w-0 flex-1 items-start gap-2 rounded-[2px] text-left',
          )}
        >
          <StateIndicator state={state} decorative className="mt-1.5" />
          <span className="min-w-0 flex-1">
            <span className="flex flex-wrap items-center gap-1.5">
              <StateChip state={state} />
              {unseen && (
                <Chip
                  color="accent"
                  variant="soft"
                  size="sm"
                  aria-label="Unseen"
                >
                  <Chip.Label>New</Chip.Label>
                </Chip>
              )}
              {paused && (
                <span title="Paused">
                  <Chip color="warning" variant="soft" size="sm">
                    <PauseCircle className="size-3" aria-hidden />
                    <Chip.Label>Paused</Chip.Label>
                  </Chip>
                </span>
              )}
            </span>
            <span
              className={cn(
                'mt-1 block line-clamp-3 break-words text-[14px] font-medium leading-5',
                unseen && 'font-semibold',
              )}
            >
              {runLabel(run)}
            </span>
            {run.title?.trim() && run.task.trim() && (
              <span className="mt-0.5 block break-words text-xs leading-4 text-muted-foreground">
                {run.task.trim()}
              </span>
            )}
          </span>
        </button>
        <div className="flex shrink-0 items-center gap-1">
          {run.protected && (
            <span
              role="img"
              aria-label="Protected: only the owner or an admin can steer or kill this run"
              title="Protected: only the owner or an admin can steer or kill this run"
              className="flex size-[22px] items-center justify-center rounded-[2px] text-muted-foreground"
            >
              <Shield className="size-3.5" aria-hidden />
            </span>
          )}
          <CardSlot name="card:badges" run={run} />
        </div>
      </div>

      {state === 'needs-attention' && summary && (
        <div className="mt-2 border-l-2 border-state-needs-attention/60 bg-state-needs-attention/10 px-2.5 py-1.5">
          <p className="break-words text-xs leading-4 text-foreground/85">{summary}</p>
        </div>
      )}

      <div className="mt-2 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 border-t border-border/80 pt-2 text-xs text-muted-foreground">
        <HarnessGlyph harness={run.harness} mode={run.mode} />
        {/* Branch content is selectable and intentionally outside navigation. */}
        {run.branch && (
          <span
            data-run-navigation-exempt
            className="flex min-w-0 max-w-full items-center gap-1"
            onMouseDown={(event) => event.stopPropagation()}
            onClick={(event) => event.stopPropagation()}
          >
            <GitBranch className="size-3.5 shrink-0" aria-hidden />
            <span
              ref={branchRef}
              className="min-w-0 break-all select-text font-mono text-xs"
              title={run.branch}
            >
              {run.branch}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label={`Copy branch ${run.branch}`}
              className="size-[22px] shrink-0"
              onClick={(event) => {
                event.stopPropagation()
                void copyText(run.branch, branchRef.current)
              }}
            >
              <Copy className="size-3" aria-hidden />
            </Button>
          </span>
        )}
        {run.last_commit && (
          <span className="flex items-center gap-1" title={run.last_commit}>
            <GitCommit className="size-3.5 shrink-0" aria-hidden />
            committed {timeAgo(run.last_commit_at ?? run.created_at)}
          </span>
        )}
        <CardSlot name="card:chips" run={run} />
      </div>

      <div className="mt-2 flex min-w-0 items-center gap-2 border-t border-border/80 pt-2 text-xs text-muted-foreground">
        <MemberAvatar member={owner} fallback={run.member_id} className="size-5 text-[9px]" />
        <span className="min-w-0 truncate">{owner?.display_name ?? run.member_id}</span>
        <time className="ml-auto shrink-0 tabular-nums" title={timestamps(card)}>
          {timeAgo(run.stateChangedAt)}
        </time>
        <CardSlot name="card:footer" run={run} />
      </div>
    </article>
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
    <span className="flex flex-wrap items-center gap-1 empty:hidden">
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
