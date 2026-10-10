import { memo, useMemo, useRef, useState } from 'react'
import { Slot } from '@/components/slots'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Card, CardControls, CardTitle } from '@/components/ui/card'
import { Popover, PopoverAnchor } from '@/components/ui/popover'
import { RelativeTime } from '@/components/ui/relative-time'
import { StateLine, type Tone } from '@/components/ui/status-dot'
import { runLabel } from '@/lib/status'
import type { MissionPhase } from '@/lib/types'
import { CardActionButton, cardAction, openCard, ReplyComposer } from '@/routes/board/card-action'
import type { BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import type { SwarmSummary } from '@/store/selectors'

const phaseWord: Record<MissionPhase, string> = {
  planning: 'Swarm planning',
  active: 'Swarm active',
  completed: 'Swarm completed',
  cancelled: 'Swarm cancelled',
}

export const RunCard = memo(function RunCard({ card, agentName, variant = 'board' }: {
  card: BoardCard
  agentName?: string
  variant?: 'board' | 'map'
}) {
  const { run } = card
  const navigate = useStore((s) => s.navigate)
  const approval = useStore((s) => s.approvalsByRun[run.id]?.[0])
  const mission = useStore((s) => (card.swarm && run.mission_id ? s.missions[run.mission_id] : undefined))
  const snapshot = useStore((s) => s.diffs[run.id]?.snapshots[0])
  const totals = useMemo(() => diffTotals(snapshot?.files), [snapshot])
  const [replying, setReplying] = useState(false)
  const cardRef = useRef<HTMLElement>(null)
  const action = cardAction(card, approval)
  const needsYou = card.group === 'needs-you'
  const title = card.swarm ? mission?.objective ?? runLabel(run) : runLabel(run)
  const tone: Tone = needsYou ? 'needs-you' : card.state
  const reason = card.swarm ? swarmReason(card, mission?.phase) : card.reason
  const owner = card.owner?.display_name ?? run.member_id

  return (
    <Popover open={replying} onOpenChange={setReplying}>
      <PopoverAnchor asChild>
        <Card ref={cardRef} data-run-id={run.id} selected={replying} style={variant === 'map' ? { height: '100%' } : undefined}>
          <StateLine tone={tone} trailing={<RelativeTime at={needsYou ? card.waitingSince : run.stateChangedAt} />}>
            {reason}
          </StateLine>
          <CardTitle onOpen={() => variant === 'map' ? navigate('run', { runId: run.id }) : openCard(card, navigate)}>{title}</CardTitle>
          <div className="flex min-h-4 min-w-0 items-center gap-1.5 text-ui-sm text-muted">
            {card.swarm ? (
              <span className="min-w-0 truncate">{swarmCounts(card.swarm)}</span>
            ) : (
              <>
                <AgentGlyph agent={run.harness} />
                <span className="min-w-0 truncate">{agentName ?? run.harness}</span>
              </>
            )}
            <Avatar name={owner} color={card.owner?.color} />
            {snapshot?.truncated ? (
              <span className="shrink-0 tabular-nums">{snapshot.files.length}+ files</span>
            ) : (
              totals && (
                <span className="shrink-0 tabular-nums">
                  <span className="text-diff-add">+{totals.additions}</span>{' '}
                  <span className="text-diff-del">−{totals.deletions}</span>
                </span>
              )
            )}
            {card.workspaceName && <span className="min-w-0 truncate">{card.workspaceName}</span>}
            <CardControls className="empty:hidden">
              <Slot name="card:meta" run={run} />
            </CardControls>
            {action && (
              <CardControls className="invisible ml-auto group-focus-within/card:visible group-hover/card:visible coarse:visible">
                <CardActionButton card={card} action={action} approval={approval} onReply={() => setReplying(true)} />
              </CardControls>
            )}
          </div>
        </Card>
      </PopoverAnchor>
      {replying && (
        <ReplyComposer
          card={card}
          title={title}
          onDone={() => setReplying(false)}
          returnFocus={() => cardRef.current?.querySelector<HTMLElement>('[data-card-open]')?.focus()}
        />
      )}
    </Popover>
  )
})

function swarmReason(card: BoardCard, phase: MissionPhase | undefined): string {
  if (card.state === 'needs-you') return card.reason
  if (card.group === 'needs-you') return card.children[0]?.reason ?? card.reason
  if (card.unread) return card.reason
  return phase ? phaseWord[phase] : 'Swarm'
}

function diffTotals(files: { additions: number; deletions: number }[] | undefined) {
  if (!files) return undefined
  let additions = 0
  let deletions = 0
  for (const file of files) {
    additions += file.additions
    deletions += file.deletions
  }
  return { additions, deletions }
}

function swarmCounts({ counts }: SwarmSummary): string {
  return (
    [
      counts.needsYou > 0 && `${counts.needsYou} needs you`,
      counts.working > 0 && `${counts.working} working`,
      counts.done > 0 && `${counts.done} done`,
      counts.failed > 0 && `${counts.failed} failed`,
    ]
      .filter(Boolean)
      .join(' · ') || 'No workers yet'
  )
}
