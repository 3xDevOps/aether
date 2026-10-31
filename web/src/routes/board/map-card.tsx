import { dequal } from 'dequal'
import { memo, useMemo, useRef, useState } from 'react'
import { ChevronDown } from '@/components/icons'
import { Slot } from '@/components/slots'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardControls, CardTitle } from '@/components/ui/card'
import { Popover, PopoverAnchor } from '@/components/ui/popover'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot } from '@/components/ui/status-dot'
import { runLabel, stateName, stateTitle, stateWords, toneOf } from '@/lib/status'
import { useClock } from '@/lib/clock'
import { cn } from '@/lib/utils'
import { CardActionButton, cardAction, ReplyComposer } from '@/routes/board/card-action'
import type { MapCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { diffTotals } from '@/store/diff'

export interface FoldProps {
  unitKey: string
  childRuns: number
  hidden: number
  hiddenNeedsYou: number
  controls: string
  onFold: (unitKey: string, fold: boolean) => void
}

export const MapNodeCard = memo(function MapNodeCard({ card, agentName, showOwner, fold }: {
  card: MapCard
  agentName?: string
  showOwner: boolean
  fold?: FoldProps
}) {
  const { run } = card
  const navigate = useStore((s) => s.navigate)
  const snapshot = useStore((s) => s.diffs[run.id]?.snapshots[0])
  const totals = useMemo(() => diffTotals(snapshot?.files), [snapshot])
  const [replying, setReplying] = useState(false)
  const cardRef = useRef<HTMLElement>(null)
  const action = cardAction(card)
  const needsYou = card.group === 'needs-you'
  const label = runLabel(run)
  const since = needsYou ? card.waitingSince : run.stateChangedAt
  const now = useClock()
  const owner = card.owner?.display_name ?? run.member_id

  return (
    <Popover open={replying} onOpenChange={setReplying}>
      <PopoverAnchor asChild>
        <Card ref={cardRef} data-run-id={run.id} selected={replying} className="h-full gap-0 bg-canvas p-2.5">
          <div className="flex min-w-0 items-start gap-2">
            <span className="mt-0.5 flex size-5.5 shrink-0 items-center justify-center rounded-chip border border-seam bg-chrome">
              <AgentGlyph agent={run.harness} />
            </span>
            <div className="min-w-0 flex-1">
              <CardTitle className="line-clamp-1 break-all" onOpen={() => navigate('run', { runId: run.id })}>{label}</CardTitle>
              <p className="truncate text-ui-xs text-muted">
                {[agentName ?? run.harness, run.model, card.workspaceName].filter(Boolean).join(' · ')}
              </p>
            </div>
            <Badge
              tone={toneOf(card)}
              title={stateTitle(card, since, now)}
              className="mt-0.5 text-ui-xs"
            >
              <StatusDot tone={toneOf(card)} />
              <span className="truncate">{stateName(card)}</span>
            </Badge>
          </div>
          <p className={cn('mt-1.5 line-clamp-2 text-ui-sm break-words', needsYou ? 'text-text' : 'text-muted')}>
            {stateWords(card, undefined, now).detail ?? card.reason}
          </p>
          <div className="mt-auto flex min-h-6 min-w-0 items-center gap-1.5 text-ui-sm text-muted">
            <span className="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-clip">
              {showOwner && <Avatar name={owner} color={card.owner?.color} />}
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
              <RelativeTime compact at={since} className="shrink-0 tabular-nums" />
              <CardControls className="empty:hidden">
                <Slot name="card:meta" run={run} />
              </CardControls>
            </span>
            {action && (
              <CardControls className="invisible shrink-0 group-focus-within/card:visible group-hover/card:visible coarse:visible">
                <CardActionButton card={card} action={action} onReply={() => setReplying(true)} />
              </CardControls>
            )}
            {fold && (
              <CardControls className="shrink-0">
                <FoldButton label={label} {...fold} />
              </CardControls>
            )}
          </div>
        </Card>
      </PopoverAnchor>
      {replying && (
        <ReplyComposer
          card={card}
          title={label}
          onDone={() => setReplying(false)}
          returnFocus={() => cardRef.current?.querySelector<HTMLElement>('[data-card-open]')?.focus()}
        />
      )}
    </Popover>
  )
}, dequal)

function FoldButton({ label, unitKey, childRuns, hidden, hiddenNeedsYou, controls, onFold }: FoldProps & { label: string }) {
  const folded = hidden > 0
  return (
    <Button
      variant="ghost"
      size="sm"
      data-map-fold
      aria-expanded={!folded}
      aria-controls={controls}
      aria-label={folded ? `Expand the subsessions of ${label}, ${hidden} hidden` : `Collapse the subsessions of ${label}`}
      className={cn('gap-1 px-1.5 tabular-nums', hiddenNeedsYou > 0 && 'text-state-needs-you')}
      onClick={() => onFold(unitKey, !folded)}
    >
      {hiddenNeedsYou > 0 && <StatusDot tone="needs-you" />}
      {folded ? `+${hidden}` : childRuns}
      {hiddenNeedsYou > 0 && ` · ${hiddenNeedsYou} need you`}
      <ChevronDown aria-hidden className={cn('size-3 transition-transform duration-100 motion-reduce:transition-none', folded && '-rotate-90')} />
    </Button>
  )
}
