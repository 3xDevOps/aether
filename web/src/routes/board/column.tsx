import { useRef, useState, type ReactNode, type RefObject } from 'react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { CountBadge } from '@/components/ui/count-badge'
import { Skeleton } from '@/components/ui/skeleton'
import type { DeviceNeed } from '@/lib/needs-you'
import type { MergeProposal } from '@/lib/types'
import { CardList, type CardEntry } from '@/routes/board/card-list'
import { DeviceCard } from '@/routes/board/device-card'
import { MergeProposalCard } from '@/routes/board/merge-card'
import { RunCard } from '@/routes/board/run-card'
import type { BoardColumn } from '@/routes/board/selectors'

export type Placeholder = 'skeleton' | 'empty' | 'none'

/**
 * One status group in a `bg-chrome` well. On the board it is a column that
 * scrolls under its fixed header; in the list it is a full-width section of
 * the list's own scroll, which `listScroll` names.
 */
export function Column({
  column,
  label = column.label,
  placeholder,
  agentNames,
  listScroll,
  collapsible = false,
  actions,
  footer,
  devices = [],
  merges = [],
}: {
  column: BoardColumn
  label?: string
  placeholder: Placeholder
  agentNames: Record<string, string>
  listScroll?: RefObject<HTMLElement | null>
  collapsible?: boolean
  actions?: ReactNode
  footer?: ReactNode
  devices?: DeviceNeed[]
  merges?: MergeProposal[]
}) {
  const [open, setOpen] = useState(!collapsible)
  const ownScroll = useRef<HTMLDivElement>(null)
  const entries: CardEntry[] = [
    ...merges.map((proposal) => ({ key: `merge:${proposal.id}`, node: <MergeProposalCard proposal={proposal} /> })),
    ...devices.map((need) => ({ key: `device:${need.device.id}`, node: <DeviceCard need={need} /> })),
    ...column.cards.map((card) => ({ key: card.run.id, node: <RunCard card={card} agentName={agentNames[card.run.harness]} /> })),
  ]
  const count = entries.length
  const heading = (
    <>
      <span className="truncate">{label}</span>{' '}
      <CountBadge>{count}</CountBadge>
    </>
  )
  const body = (
    <>
      <CardList entries={entries} scrollRef={listScroll ?? ownScroll} />
      {count === 0 && placeholder === 'skeleton' && (
        <div className="flex flex-col gap-1 pb-1">
          <Skeleton className="h-25 rounded-panel bg-raised" />
          <Skeleton className="h-25 rounded-panel bg-raised" />
        </div>
      )}
      {count === 0 && placeholder === 'empty' && <p className="px-3 pb-3 text-ui text-muted">Nothing here.</p>}
      {footer}
    </>
  )
  const header = (title: ReactNode) => (
    <div className="flex h-9 shrink-0 items-center gap-2 pr-1.5 pl-3">
      {title}
      {actions && <div className="ml-auto flex">{actions}</div>}
    </div>
  )
  const well = 'flex min-w-0 flex-col rounded-composer bg-chrome px-0.75'

  if (listScroll) {
    if (!collapsible) {
      return (
        <section aria-label={column.label} className={well}>
          {header(<h2 className="flex min-w-0 items-center gap-1.5 text-ui font-medium text-text">{heading}</h2>)}
          {body}
        </section>
      )
    }
    return (
      <Collapsible asChild open={open} onOpenChange={setOpen}>
        <section aria-label={column.label} className={well}>
          {header(
            <h2 className="-ml-2.25 min-w-0 flex-1 text-ui font-medium text-text">
              <CollapsibleTrigger className="gap-1.5 font-medium">{heading}</CollapsibleTrigger>
            </h2>,
          )}
          <CollapsibleContent>{body}</CollapsibleContent>
        </section>
      </Collapsible>
    )
  }
  return (
    <section aria-label={column.label} className={`${well} max-h-full min-h-0 flex-1 basis-0 overflow-hidden pb-0.75`}>
      {header(<h2 className="flex min-w-0 items-center gap-1.5 text-ui font-medium text-text">{heading}</h2>)}
      <div ref={ownScroll} className="-mx-0.75 min-h-0 overflow-x-hidden overflow-y-auto px-0.75">
        {body}
      </div>
    </section>
  )
}
