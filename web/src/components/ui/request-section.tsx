import { Collapsible as CollapsiblePrimitive } from 'radix-ui'
import { useId, useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { ChevronRight } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { CollapsibleContent } from '@/components/ui/collapsible'
import { CountBadge } from '@/components/ui/count-badge'
import { playEntrance } from '@/lib/motion'
import { cn, focusRing } from '@/lib/utils'

export function RequestSection({ label, count, className, children }: {
  label?: React.ReactNode
  count?: number
  className?: string
  children: React.ReactNode
}) {
  const headingID = useId()
  return (
    <section
      data-slot="request-section"
      aria-labelledby={label ? headingID : undefined}
      className={cn('flex min-w-0 flex-col gap-2 border-t border-seam px-4 py-3', className)}
    >
      {label && (
        <h4 id={headingID} className="flex items-center gap-2 text-ui-sm font-normal text-muted">
          {label}
          {count !== undefined && <CountBadge>{count}</CountBadge>}
        </h4>
      )}
      {children}
    </section>
  )
}

const shownRows = 3

export function RequestRows({ rows, flush = false }: { rows: React.ReactNode[]; flush?: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const list = useRef<HTMLUListElement>(null)
  useLayoutEffect(() => {
    if (!expanded) return
    list.current?.querySelectorAll('[data-extra]').forEach((el, index) => {
      playEntrance(el, { motion: { duration: '--duration-300', easing: '--ease-enter' }, index })
    })
  }, [expanded])
  const extra = rows.length - shownRows
  return (
    <>
      <ul ref={list} className="flex min-w-0 flex-col gap-1">
        {(expanded ? rows : rows.slice(0, shownRows)).map((row, index) => (
          <li key={index} data-extra={index >= shownRows || undefined} className="min-w-0">{row}</li>
        ))}
      </ul>
      {extra > 0 && (
        <Button variant="quiet" size="sm" className={cn('self-start text-ui-sm', !flush && 'ml-5.5')} aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>
          {expanded ? 'Show less' : `${extra} more`}
        </Button>
      )}
    </>
  )
}

const rowLine = 'flex min-h-6 w-full min-w-0 items-center gap-2 text-left text-ui [&_svg]:size-3.5 [&_svg]:shrink-0'

export function RequestRow({ icon, meta, detail, open, onOpenChange, children }: {
  icon?: React.ReactNode
  meta?: React.ReactNode
  detail?: React.ReactNode
  open?: boolean
  onOpenChange?: (open: boolean) => void
  children: React.ReactNode
}) {
  const body = (
    <>
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {meta !== undefined && <span className="shrink-0 text-ui-sm text-muted tabular-nums">{meta}</span>}
    </>
  )
  if (!detail) {
    return (
      <div data-slot="request-row" className={rowLine}>
        <span aria-hidden className="text-icon-faint">{icon}</span>
        {body}
      </div>
    )
  }
  return (
    <CollapsiblePrimitive.Root data-slot="request-row" open={open} onOpenChange={onOpenChange}>
      <CollapsiblePrimitive.Trigger
        className={cn(focusRing, rowLine, 'group -mx-1.5 w-[calc(100%+0.75rem)] cursor-pointer rounded-control px-1.5 transition-colors duration-hover ease-hover hover:bg-hover motion-reduce:transition-none coarse:min-h-11')}
      >
        <ChevronRight aria-hidden className="text-icon-faint transition-[transform,color] duration-200 ease-standard group-hover:text-muted group-data-[state=open]:rotate-90 motion-reduce:transition-none" />
        {body}
      </CollapsiblePrimitive.Trigger>
      <CollapsibleContent className="pt-1 pb-2">{detail}</CollapsibleContent>
    </CollapsiblePrimitive.Root>
  )
}
