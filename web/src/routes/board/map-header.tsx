import { useRef, useState, type ReactNode } from 'react'
import { ChevronDown } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { ListRow } from '@/components/ui/list-row'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot } from '@/components/ui/status-dot'
import { endedBadly, runLabel, stateDetail, stateTone } from '@/lib/status'
import { cn } from '@/lib/utils'
import { runKey } from '@/routes/board/map-layout'
import type { MapCard } from '@/routes/board/selectors'
import { byChange, byWait } from '@/store/selectors'

const chips = {
  'needs-you': { label: 'needs you', fill: 'bg-state-needs-you-soft text-state-needs-you hover:not-aria-disabled:bg-state-needs-you/20 hover:not-aria-disabled:text-state-needs-you data-[state=open]:bg-state-needs-you/20' },
  failed: { label: 'failed', fill: 'bg-state-failed-soft text-state-failed hover:not-aria-disabled:bg-state-failed/20 hover:not-aria-disabled:text-state-failed data-[state=open]:bg-state-failed/20' },
} as const

/** The Runs strip: counts over every card in scope, folded workers included, and the map's controls. */
export function MapHeader({ cards, actions, camera, onReveal }: {
  cards: MapCard[]
  actions?: ReactNode
  camera: ReactNode
  onReveal: (key: string) => void
}) {
  const working = cards.filter((card) => card.group === 'working').length
  return (
    <header className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-seam px-4 py-2">
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
        <h2 className="text-ui-sm font-medium">Runs <span className="text-muted tabular-nums">{cards.length}</span></h2>
        {working > 0 && <span className="text-ui-sm text-state-working tabular-nums">{working} working</span>}
        <Chip kind="needs-you" cards={cards.filter((card) => card.group === 'needs-you').sort(byWait)} onReveal={onReveal} />
        <Chip kind="failed" cards={cards.filter((card) => endedBadly(card.state)).sort(byChange)} onReveal={onReveal} />
      </div>
      <div className="ml-auto flex flex-wrap items-center gap-2">
        {actions}
        {camera}
      </div>
    </header>
  )
}

function Chip({ kind, cards, onReveal }: { kind: keyof typeof chips; cards: MapCard[]; onReveal: (key: string) => void }) {
  const [open, setOpen] = useState(false)
  const revealing = useRef(false)
  if (!cards.length) return null
  const { label, fill } = chips[kind]
  const title = `${cards.length} ${label}`
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" className={cn('gap-1 px-2 tabular-nums', fill)}>
          {title}
          <ChevronDown aria-hidden className={cn('size-3 transition-transform duration-100 motion-reduce:transition-none', open && 'rotate-180')} />
        </Button>
      </PopoverTrigger>
      <PopoverContent aria-label={`Runs that ${kind === 'failed' ? 'failed' : 'need you'}`} align="start" className="w-[min(360px,calc(100vw-16px))] p-1"
        onCloseAutoFocus={(event) => {
          // A revealed card takes focus; only Escape or an outside click returns it to the chip.
          if (revealing.current) event.preventDefault()
          revealing.current = false
        }}
      >
        <ul className="flex flex-col">
          {cards.map((card) => {
            const detail = [stateDetail(card), card.workspaceName].filter(Boolean).join(' · ')
            return (
              <li key={runKey(card)}>
                <ListRow
                  leading={<StatusDot tone={stateTone[card.state]} />}
                  trailing={<RelativeTime compact at={kind === 'failed' ? card.run.stateChangedAt : card.waitingSince} />}
                  title={card.reason}
                  onClick={() => {
                    revealing.current = true
                    setOpen(false)
                    onReveal(runKey(card))
                  }}
                >
                  <span className="flex min-w-0 items-baseline gap-1.5">
                    <span className="truncate">{runLabel(card.run)}</span>
                    {detail && <span className="max-w-1/2 shrink-0 truncate text-ui-sm text-muted">{detail}</span>}
                  </span>
                </ListRow>
              </li>
            )
          })}
        </ul>
      </PopoverContent>
    </Popover>
  )
}
