import { GitBranch, PauseCircle } from 'lucide-react'
import type { ReactNode } from 'react'
import { Slot, type CardSlotName } from '@/components/slots'
import { StateDot } from '@/components/state-dot'
import { timeAgo } from '@/lib/format'
import { runLabel, stateLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { HarnessGlyph } from '@/routes/board/harness-glyph'
import { MemberAvatar } from '@/routes/board/member-avatar'
import type { BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { approvalsForRun } from '@/store/approvals'
import type { RunRecord } from '@/store/runs'

/**
 * One run, as it appears on the board. Everything a later ticket adds goes
 * through the slots (`card:badges`, `card:chips`, `card:footer`) rather than
 * into this file. The whole card is one click target: an overlay button sits
 * above the text and below the slots, so slot content stays interactive.
 */
export function RunCard({ card }: { card: BoardCard }) {
  const { run, state, owner, unseen, paused } = card
  const navigate = useStore((s) => s.navigate)
  // An approval pause fires no run.status event, so the run's reason stays
  // empty; the pending question itself is the summary the card needs then.
  const summary = useStore((s) =>
    state === 'needs-attention' && !run.reason
      ? (approvalsForRun(s.inbox, run.id)[0]?.action ?? '')
      : run.reason,
  )

  return (
    <article
      style={{ borderLeftColor: owner?.color }}
      className={cn(
        'relative rounded-md border border-l-3 bg-card shadow-xs transition-colors hover:bg-accent/50',
        unseen ? 'border-foreground/25' : 'opacity-90',
      )}
    >
      <div className="space-y-2 p-2">
        <div className="flex items-start gap-2">
          <StateDot state={state} className="mt-1" />
          <span
            className={cn('min-w-0 flex-1 text-sm break-words', unseen && 'font-medium')}
          >
            <span className="line-clamp-2">{runLabel(run)}</span>
          </span>
          {paused && (
            <span
              title="Paused"
              className="flex shrink-0 items-center gap-1 text-[11px] text-muted-foreground"
            >
              <PauseCircle className="size-3.5" />
              Paused
            </span>
          )}
          {unseen && (
            <span
              role="img"
              aria-label="Unseen"
              title="Changed since you last looked"
              className="mt-1.5 size-1.5 shrink-0 rounded-full bg-foreground"
            />
          )}
          <CardSlot name="card:badges" run={run} />
        </div>

        {state === 'needs-attention' && summary && (
          <p className="rounded-sm bg-state-needs-attention/10 px-1.5 py-1 text-xs text-foreground/80">
            {summary}
          </p>
        )}

        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
          <HarnessGlyph harness={run.harness} mode={run.mode} />
          <span className="flex min-w-0 items-center gap-1">
            <GitBranch className="size-3 shrink-0" aria-hidden />
            <span className="truncate">{run.branch}</span>
          </span>
          <CardSlot name="card:chips" run={run} />
        </div>

        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <MemberAvatar member={owner} fallback={run.member_id} />
          <span className="truncate">{owner?.display_name ?? run.member_id}</span>
          <time className="ml-auto shrink-0" title={timestamps(card)}>
            {timeAgo(run.stateChangedAt)}
          </time>
          <CardSlot name="card:footer" run={run} />
        </div>
      </div>

      <button
        type="button"
        aria-label={runLabel(run)}
        onClick={() => navigate('run', { runId: run.id })}
        className="absolute inset-0 z-10 rounded-md focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
      />
    </article>
  )
}

/** Slot content stays above the card's click overlay so it can be clicked. */
function CardSlot({ name, run }: { name: CardSlotName; run: RunRecord }): ReactNode {
  return (
    <span className="relative z-20 flex flex-wrap items-center gap-1 empty:hidden">
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
