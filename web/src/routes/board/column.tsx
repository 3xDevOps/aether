import { useState, type ReactNode } from 'react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Skeleton } from '@/components/ui/skeleton'
import { RunCard } from '@/routes/board/run-card'
import type { BoardColumn } from '@/routes/board/selectors'

export type Placeholder = 'skeleton' | 'empty' | 'none'

export function Column({
  column,
  label = column.label,
  placeholder,
  agentNames,
  collapsed,
  actions,
  footer,
}: {
  column: BoardColumn
  label?: string
  placeholder: Placeholder
  agentNames: Record<string, string>
  collapsed?: boolean
  actions?: ReactNode
  footer?: ReactNode
}) {
  const [open, setOpen] = useState(!collapsed)
  const count = column.cards.length
  const heading = (
    <>
      <span>{label}</span>{' '}
      <span className="text-ui-sm text-muted tabular-nums">{count}</span>
    </>
  )
  const body = (
    <>
      <div className="flex flex-col gap-2">
        {column.cards.map((card) => (
          <RunCard key={card.run.id} card={card} agentName={agentNames[card.run.harness]} />
        ))}
        {count === 0 && placeholder === 'skeleton' && (
          <>
            <div className="h-[86px]"><Skeleton className="size-full" /></div>
            <div className="h-[86px]"><Skeleton className="size-full" /></div>
          </>
        )}
        {count === 0 && placeholder === 'empty' && <p className="px-1 text-ui text-muted">Nothing here.</p>}
      </div>
      {footer}
    </>
  )

  if (collapsed !== undefined) {
    return (
      <Collapsible asChild open={open} onOpenChange={setOpen}>
        <section aria-label={column.label} className="flex min-w-0 flex-col gap-2">
          <div className="flex items-center gap-2">
            <h2 className="text-ui font-medium text-text">
              <CollapsibleTrigger className="gap-2">{heading}</CollapsibleTrigger>
            </h2>
            {actions && <div className="ml-auto">{actions}</div>}
          </div>
          <CollapsibleContent className="flex flex-col gap-2">{body}</CollapsibleContent>
        </section>
      </Collapsible>
    )
  }
  return (
    <section aria-label={column.label} className="flex min-w-0 flex-col gap-2 lg:min-h-0">
      <div className="flex h-7 shrink-0 items-center gap-2 px-1">
        <h2 className="flex items-center gap-2 text-ui font-medium text-text">{heading}</h2>
        {actions && <div className="ml-auto">{actions}</div>}
      </div>
      <div className="flex flex-1 flex-col gap-2 lg:-mx-1 lg:min-h-0 lg:overflow-y-auto lg:px-1 lg:pb-4">{body}</div>
    </section>
  )
}
