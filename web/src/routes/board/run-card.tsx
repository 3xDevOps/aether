import { memo, useMemo, useRef } from 'react'
import { Slot } from '@/components/slots'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Card, CardControls, CardTitle } from '@/components/ui/card'
import { Popover, PopoverAnchor } from '@/components/ui/popover'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { runLabel, stateDetail, stateLabel, stateTitle, stateTone } from '@/lib/status'
import type { MissionPhase } from '@/lib/types'
import { cn } from '@/lib/utils'
import { CardActionButton, cardAction, openCard, ReplyComposer, setReplying, useReply } from '@/routes/board/card-action'
import type { BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { diffTotals } from '@/store/diff'
import type { SwarmSummary } from '@/store/selectors'

const phaseWord: Record<MissionPhase, string> = {
  planning: 'Swarm planning',
  active: 'Swarm active',
  completed: 'Swarm completed',
  cancelled: 'Swarm cancelled',
}

/** Indents a wide card's description and footer under its title, past the 14px glyph and 12px gap. */
export const wideIndent = '@min-[365px]:pl-6.5'

export const RunCard = memo(function RunCard({ card, agentName, variant = 'board' }: {
  card: BoardCard
  agentName?: string
  variant?: 'board' | 'map'
}) {
  const { run } = card
  const navigate = useStore((s) => s.navigate)
  const mission = useStore((s) => (card.swarm && run.mission_id ? s.missions[run.mission_id] : undefined))
  const snapshot = useStore((s) => s.diffs[run.id]?.snapshots[0])
  const totals = useMemo(() => diffTotals(snapshot?.files), [snapshot])
  const replying = useReply((s) => s.reply?.runId === run.id)
  const cardRef = useRef<HTMLElement>(null)
  const action = cardAction(card)
  const needsYou = card.group === 'needs-you'
  const title = card.swarm ? mission?.objective ?? runLabel(run) : runLabel(run)
  const state = needsYou && !card.needsYou ? 'needs-you' : card.state
  const label = stateLabel[state]
  const reason = card.swarm ? swarmReason(card, mission?.phase) : card.reason
  const detail = needsYou || card.swarm ? reason : stateDetail({ state, reason })
  const description = detail && detail !== label ? detail : undefined
  const owner = card.owner?.display_name ?? run.member_id
  const since = needsYou ? card.waitingSince : run.stateChangedAt

  return (
    <Popover open={replying} onOpenChange={(open) => setReplying(run.id, open)}>
      <PopoverAnchor asChild>
        <Card
          ref={cardRef}
          data-run-id={run.id}
          raised={variant === 'board'}
          selected={replying}
          className="@container"
          style={variant === 'map' ? { height: '100%' } : undefined}
        >
          <div className="flex items-start gap-3">
            <AgentGlyph agent={run.harness} colored className="mt-0.75" />
            <CardTitle onOpen={() => variant === 'map' ? navigate('run', { runId: run.id }) : openCard(card, navigate)}>{title}</CardTitle>
          </div>
          {description && (
            <p className={cn('mt-0.5 text-ui-sm break-words', wideIndent, needsYou ? 'text-text' : 'text-muted', card.group === 'working' || variant === 'map' ? 'line-clamp-1' : 'line-clamp-2')}>
              {description}
            </p>
          )}
          <div className={cn('mt-2 flex min-h-4 min-w-0 flex-wrap items-center gap-2 text-ui-sm text-muted', wideIndent)}>
            <span
              className="flex min-w-0 flex-1 items-center gap-1.5"
              title={card.swarm ? undefined : stateTitle({ state, reason, next: card.next }, since)}
            >
              <StatusDot tone={stateTone[state]} label={label} />
              <span className="min-w-0 truncate">
                <span>{label}</span> · <RelativeTime compact at={since} className="tabular-nums" />
                {card.workspaceName && <> · <span>{card.workspaceName}</span></>}
              </span>
            </span>
            {card.swarm ? (
              <SwarmCounts swarm={card.swarm} />
            ) : (
              <span className="sr-only @min-[365px]:not-sr-only @min-[365px]:max-w-40 @min-[365px]:shrink-0 @min-[365px]:truncate">
                {agentName ?? run.harness}
              </span>
            )}
            <Avatar name={owner} color={card.owner?.color} />
            {snapshot?.truncated ? (
              <span className="shrink-0 tabular-nums">{snapshot.files.length} files</span>
            ) : (
              totals && (
                <span className="shrink-0 tabular-nums">
                  <span className="text-diff-add">+{totals.additions}</span>{' '}
                  <span className="text-diff-del">−{totals.deletions}</span>
                </span>
              )
            )}
            <CardControls className="shrink-0 empty:hidden">
              <Slot name="card:meta" run={run} />
            </CardControls>
            {action && (
              <CardControls className="-my-1 hidden shrink-0 group-focus-within/card:flex group-hover/card:flex coarse:my-0 coarse:flex">
                <CardActionButton card={card} action={action} onReply={() => setReplying(run.id, true)} />
              </CardControls>
            )}
          </div>
        </Card>
      </PopoverAnchor>
      {replying && (
        <ReplyComposer
          card={card}
          title={title}
          onDone={() => setReplying(run.id, false)}
          returnFocus={() => cardRef.current?.querySelector<HTMLElement>('[data-card-open]')?.focus()}
        />
      )}
    </Popover>
  )
})

function swarmReason(card: BoardCard, phase: MissionPhase | undefined): string {
  if (card.needsYou) return card.reason
  if (card.group === 'needs-you') return card.children[0]?.reason ?? card.reason
  if (card.unread) return card.reason
  return phase ? phaseWord[phase] : 'Swarm'
}

const workerStates: { key: keyof SwarmSummary['counts']; tone: Tone; words: string }[] = [
  { key: 'needsYou', tone: 'needs-you', words: 'needs you' },
  { key: 'working', tone: 'working', words: 'working' },
  { key: 'done', tone: 'done', words: 'done' },
  { key: 'failed', tone: 'failed', words: 'failed' },
]

function SwarmCounts({ swarm: { counts } }: { swarm: SwarmSummary }) {
  const shown = workerStates.filter(({ key }) => counts[key] > 0)
  if (shown.length === 0) return <span className="shrink-0">No workers yet</span>
  return (
    <span className="flex shrink-0 items-center gap-2 tabular-nums">
      {shown.map(({ key, tone, words }) => (
        <span key={key} role="img" aria-label={`${counts[key]} ${words}`} title={`${counts[key]} ${words}`} className="flex items-center gap-1">
          <StatusDot tone={tone} />
          {counts[key]}
        </span>
      ))}
    </span>
  )
}
