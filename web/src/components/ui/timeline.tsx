import { useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import {
  ArrowRightLeft,
  Brain,
  ChevronRight,
  CircleCheck,
  CircleX,
  FileText,
  Globe,
  LoaderCircle,
  Pencil,
  Search,
  SquareTerminal,
  Trash2,
  Wrench,
  type LucideIcon,
} from '@/components/icons'
import { type Tone, StatusDot } from '@/components/ui/status-dot'
import { cn, focusRingInset } from '@/lib/utils'

const collapsedLines = 8

export function MessageRow({
  author,
  meta,
  children,
  attachments,
  variant = 'message',
}: {
  author: React.ReactNode
  meta?: React.ReactNode
  children: React.ReactNode
  attachments?: React.ReactNode
  variant?: 'message' | 'note'
}) {
  const body = useRef<HTMLDivElement>(null)
  const [long, setLong] = useState(false)
  const [open, setOpen] = useState(false)
  useLayoutEffect(() => {
    const node = body.current
    if (!node) return
    const lineHeight = Number.parseFloat(getComputedStyle(node).lineHeight) || 22
    setLong(node.scrollHeight > lineHeight * collapsedLines + 1)
  }, [children])
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
      <div
        ref={body}
        className={cn('mt-0.5 text-prose break-words whitespace-pre-wrap text-text', long && !open && 'line-clamp-8')}
      >
        {children}
      </div>
      {long && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className={cn(focusRingInset, 'mt-1 rounded-control text-ui-sm text-accent hover:underline coarse:min-h-11')}
        >
          {open ? 'Show less' : 'Show more'}
        </button>
      )}
      {attachments}
    </div>
  )
}

export function AssistantRow({ meta, children }: { meta?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div data-slot="assistant-row" className="group/assistant flex flex-col gap-1">
      {children}
      {meta && (
        <div className="flex h-6 items-center gap-1 text-ui-sm text-muted tabular-nums opacity-0 transition-opacity duration-100 group-focus-within/assistant:opacity-100 group-hover/assistant:opacity-100 coarse:opacity-100 motion-reduce:transition-none">
          {meta}
        </div>
      )}
    </div>
  )
}

export const toolIcon: Record<string, LucideIcon> = {
  read: FileText,
  edit: Pencil,
  delete: Trash2,
  move: ArrowRightLeft,
  search: Search,
  execute: SquareTerminal,
  bash: SquareTerminal,
  think: Brain,
  fetch: Globe,
}

const statusIcon = {
  running: <LoaderCircle aria-label="Running" className="animate-spin text-state-working motion-reduce:animate-none" />,
  done: <CircleCheck aria-label="Done" />,
  failed: <CircleX aria-label="Failed" className="text-state-failed" />,
}

export function duration(ms: number | undefined): string | undefined {
  if (ms === undefined) return undefined
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
}

export function WorkEntry({
  tool = 'other',
  status,
  trailing,
  expanded,
  onToggle,
  children,
}: {
  tool?: string
  status: keyof typeof statusIcon
  trailing?: React.ReactNode
  expanded?: boolean
  onToggle?: () => void
  children: React.ReactNode
}) {
  const Icon = toolIcon[tool.toLowerCase()] ?? Wrench
  const content = (
    <>
      <Icon aria-hidden className="text-icon-faint" />
      <span className={cn('min-w-0 flex-1 truncate', status === 'failed' && 'text-text')}>{children}</span>
      {statusIcon[status]}
      {trailing !== undefined && <span className="w-14 shrink-0 text-right tabular-nums">{trailing}</span>}
    </>
  )
  const row = 'flex h-6 w-full min-w-0 items-center gap-2 pl-5 text-left text-ui-sm text-muted [&_svg]:size-3.5 [&_svg]:shrink-0'
  return (
    <div data-slot="work-entry" data-status={status}>
      {onToggle ? (
        <button
          type="button"
          aria-expanded={expanded}
          onClick={onToggle}
          className={cn(focusRingInset, row, 'rounded-control hover:text-text coarse:h-11')}
        >
          {content}
        </button>
      ) : (
        <div className={row}>{content}</div>
      )}
    </div>
  )
}

export function WorkGroup({
  summary,
  trailing,
  expanded,
  onToggle,
}: {
  summary: React.ReactNode
  trailing?: React.ReactNode
  expanded: boolean
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      data-slot="work-group"
      aria-expanded={expanded}
      onClick={onToggle}
      className={cn(
        focusRingInset,
        'flex h-6 w-full min-w-0 items-center gap-2 rounded-control text-left text-ui-sm text-muted hover:text-text coarse:h-11',
      )}
    >
      <ChevronRight className={cn('size-3.5 shrink-0 transition-transform duration-100 motion-reduce:transition-none', expanded && 'rotate-90')} />
      <span className="min-w-0 flex-1 truncate">{summary}</span>
      {trailing !== undefined && <span className="shrink-0 tabular-nums">{trailing}</span>}
    </button>
  )
}

export function WorkDetail({ children }: { children: React.ReactNode }) {
  return <div data-slot="work-detail" className="flex flex-col gap-2 pt-1 pb-2 pl-10">{children}</div>
}

export function LiveActivityRow({ waiting = false, children }: { waiting?: boolean; children: React.ReactNode }) {
  return (
    <div data-slot="live-row" data-waiting={waiting || undefined} className="flex h-6 min-w-0 items-center gap-2 text-ui-sm">
      <StatusDot tone={waiting ? 'needs-you' : 'working'} pulse={!waiting} />
      <span className={cn('min-w-0 truncate', waiting ? 'text-text' : 'live-shimmer')}>{children}</span>
    </div>
  )
}

export function EventRow({
  tone = 'neutral',
  trailing,
  detail,
  children,
}: {
  tone?: Tone
  trailing?: React.ReactNode
  detail?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div data-slot="event-row" className="flex min-w-0 flex-col text-ui-sm text-muted">
      <div className="flex min-h-6 min-w-0 items-center gap-2">
        <StatusDot tone={tone} />
        <span className={cn('min-w-0 flex-1 break-words', tone !== 'neutral' && 'text-text')}>{children}</span>
        {trailing !== undefined && <span className="shrink-0 tabular-nums">{trailing}</span>}
      </div>
      {detail && <div className={cn('line-clamp-4 max-h-[4lh] pl-4 break-words', typeof detail === 'string' && 'whitespace-pre-wrap')}>{detail}</div>}
    </div>
  )
}
