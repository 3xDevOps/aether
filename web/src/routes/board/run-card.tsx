import { memo, useState } from 'react'
import { Slot } from '@/components/slots'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Card, CardControls, CardTitle } from '@/components/ui/card'
import { Popover, PopoverAnchor } from '@/components/ui/popover'
import { RelativeTime } from '@/components/ui/relative-time'
import { StateLine, type Tone } from '@/components/ui/status-dot'
import { deletesInLabel } from '@/lib/format'
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

export const RunCard = memo(function RunCard({ card, agentName }: { card: BoardCard; agentName?: string }) {
  const { run } = card
  const navigate = useStore((s) => s.navigate)
  const approval = useStore((s) => s.approvalsByRun[run.id]?.[0])
  const mission = useStore((s) => (card.swarm && run.mission_id ? s.missions[run.mission_id] : undefined))
  const additions = useStore((s) => diffTotal(s.diffs[run.id]?.snapshots.at(-1)?.files, 'additions'))
  const deletions = useStore((s) => diffTotal(s.diffs[run.id]?.snapshots.at(-1)?.files, 'deletions'))
  const [replying, setReplying] = useState(false)
  const action = cardAction(card, approval)
  const needsYou = card.group === 'needs-you'
  const title = card.swarm ? mission?.objective ?? runLabel(run) : runLabel(run)
  const tone: Tone = needsYou ? 'needs-you' : card.state
  const reason = card.swarm ? swarmReason(card, mission?.phase) : archivedReason(card)
  const owner = card.owner?.display_name ?? run.member_id

  return (
    <Popover open={replying} onOpenChange={setReplying}>
      <PopoverAnchor asChild>
        <Card data-run-id={run.id} selected={replying}>
          <StateLine tone={tone} trailing={<RelativeTime at={needsYou ? card.waitingSince : run.stateChangedAt} />}>
            {reason}
          </StateLine>
          <CardTitle onOpen={() => openCard(card, navigate)}>{title}</CardTitle>
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
            {additions !== undefined && deletions !== undefined && (
              <span className="shrink-0 tabular-nums">
                <span className="text-diff-add">+{additions}</span> <span className="text-diff-del">−{deletions}</span>
              </span>
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
      {replying && <ReplyComposer card={card} title={title} onDone={() => setReplying(false)} />}
    </Popover>
  )
})

function swarmReason(card: BoardCard, phase: MissionPhase | undefined): string {
  if (card.state === 'needs-you') return card.reason
  if (card.group === 'needs-you') return card.children[0]?.reason ?? card.reason
  return phase ? phaseWord[phase] : 'Swarm'
}

function archivedReason(card: BoardCard): string {
  const { run } = card
  if (!run.archived_at || !run.deletes_at) return card.reason
  return `${card.reason}, ${deletesInLabel(run.deletes_at)}`
}

function diffTotal(files: { additions: number; deletions: number }[] | undefined, key: 'additions' | 'deletions') {
  return files?.reduce((sum, file) => sum + file[key], 0)
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
