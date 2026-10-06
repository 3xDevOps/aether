import { useState } from 'react'
import type * as React from 'react'
import { ChevronRight, CircleCheck, CircleX, LoaderCircle } from '@/components/icons'
import { type Tone, StatusDot } from '@/components/ui/status-dot'
import { cn, focusRingInset } from '@/lib/utils'

export function MessageRow({
  author,
  meta,
  children,
  variant = 'message',
}: {
  author: React.ReactNode
  meta?: React.ReactNode
  children: React.ReactNode
  variant?: 'message' | 'note'
}) {
  return (
    <div
      data-slot="message-row"
      data-variant={variant}
      className={cn('rounded-panel px-3 py-2', variant === 'message' ? 'bg-chrome' : 'border border-seam')}
    >
      <div className="flex min-w-0 items-baseline gap-2 text-ui-sm">
        <span className="min-w-0 truncate font-medium text-text">{author}</span>
        {meta && <span className="ml-auto flex shrink-0 items-center gap-1.5 text-muted tabular-nums">{meta}</span>}
      </div>
      <div className="mt-0.5 text-prose break-words whitespace-pre-wrap text-text">{children}</div>
    </div>
  )
}

const entryIcon = {
  running: <LoaderCircle className="animate-spin text-state-working motion-reduce:animate-none" />,
  done: <CircleCheck />,
  failed: <CircleX className="text-state-failed" />,
}

export function WorkEntry({
  status,
  trailing,
  children,
}: {
  status: keyof typeof entryIcon
  trailing?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <li
      data-slot="work-entry"
      data-status={status}
      className="flex h-6 min-w-0 items-center gap-2 pl-5 text-ui-sm text-muted [&_svg]:size-3.5 [&_svg]:shrink-0"
    >
      {entryIcon[status]}
      <span className={cn('min-w-0 flex-1 truncate font-code', status === 'failed' && 'text-text')}>{children}</span>
      {trailing !== undefined && <span className="shrink-0 tabular-nums">{trailing}</span>}
    </li>
  )
}

export function WorkGroup({
  summary,
  trailing,
  children,
  defaultOpen = false,
}: {
  summary: React.ReactNode
  trailing?: React.ReactNode
  children: React.ReactNode
  defaultOpen?: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div data-slot="work-group">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className={cn(
          focusRingInset,
          'flex h-6 w-full min-w-0 items-center gap-2 rounded-control text-left text-ui-sm text-muted hover:text-text coarse:h-11',
        )}
      >
        <ChevronRight className={cn('size-3.5 shrink-0 transition-transform duration-100 motion-reduce:transition-none', open && 'rotate-90')} />
        <span className="min-w-0 flex-1 truncate">{summary}</span>
        {trailing !== undefined && <span className="shrink-0 tabular-nums">{trailing}</span>}
      </button>
      {open && <ul>{children}</ul>}
    </div>
  )
}

export function EventRow({
  tone = 'neutral',
  trailing,
  children,
}: {
  tone?: Tone
  trailing?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div data-slot="event-row" className="flex min-h-6 min-w-0 items-center gap-2 text-ui-sm text-muted">
      <StatusDot tone={tone} />
      <span className={cn('min-w-0 flex-1 break-words', tone !== 'neutral' && 'text-text')}>{children}</span>
      {trailing !== undefined && <span className="shrink-0 tabular-nums">{trailing}</span>}
    </div>
  )
}
