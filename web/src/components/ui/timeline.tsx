import { useId, useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import {
  ArrowRightLeft,
  Bot,
  Brain,
  ChevronRight,
  FileText,
  Globe,
  ListTodo,
  Pencil,
  Search,
  SquareTerminal,
  Trash2,
  type LucideIcon,
} from '@/components/icons'
import { type Tone, StatusDot } from '@/components/ui/status-dot'
import { cn, focusRingInset } from '@/lib/utils'

const collapsedLines = 8

export function ClampedBody({ children, className }: { children: React.ReactNode; className?: string }) {
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
    <>
      <div ref={body} className={cn('text-prose break-words whitespace-pre-wrap text-text', long && !open && 'line-clamp-8', className)}>
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
    </>
  )
}

export function MessageRow({
  author,
  meta,
  children,
  attachments,
}: {
  author?: React.ReactNode
  meta?: React.ReactNode
  children: React.ReactNode
  attachments?: React.ReactNode
}) {
  return (
    <div data-slot="message-row" className="ml-auto flex w-fit max-w-[85%] min-w-0 flex-col items-end gap-1">
      <div className="max-w-full min-w-0 rounded-composer bg-chrome px-3.5 py-2">
        <ClampedBody>{children}</ClampedBody>
        {attachments}
      </div>
      {(author || meta) && (
        <div className="flex max-w-full min-w-0 items-center gap-1.5 px-1 text-ui-sm text-muted tabular-nums">
          {author && <span className="min-w-0 truncate">{author}</span>}
          {meta}
        </div>
      )}
    </div>
  )
}

const onHover = 'opacity-0 transition-opacity duration-100 group-focus-within/assistant:opacity-100 group-hover/assistant:opacity-100 coarse:opacity-100 motion-reduce:transition-none'

export function AssistantRow({ outcome, actions, streaming = false, children }: { outcome?: React.ReactNode; actions?: React.ReactNode; streaming?: boolean; children: React.ReactNode }) {
  return (
    <div data-slot="assistant-row" className="group/assistant flex flex-col gap-1 pl-6">
      {children}
      {streaming && <span aria-hidden data-slot="stream-caret" className="state-pulse mt-1 size-1.5 rounded-full bg-state-working" />}
      {(outcome || actions) && (
        <div className="flex h-6 items-center gap-2 text-ui-sm text-muted tabular-nums coarse:h-11">
          {outcome}
          {actions && <span className={cn('flex items-center gap-2', onHover)}>{actions}</span>}
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
  subagent: Bot,
  plan: ListTodo,
}

export function duration(ms: number | undefined): string | undefined {
  if (ms === undefined) return undefined
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
}

export function WorkGroup({
  summary,
  trailing,
  expanded,
  controls,
  onToggle,
}: {
  summary: React.ReactNode
  trailing?: React.ReactNode
  expanded: boolean
  controls?: string
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      data-slot="work-group"
      aria-expanded={expanded}
      aria-controls={controls}
      onClick={onToggle}
      className={cn(
        focusRingInset,
        'flex h-6 w-full min-w-0 items-center gap-2 rounded-control text-left text-ui-sm text-muted hover:text-text coarse:h-11',
      )}
    >
      <ChevronRight className={cn('size-3.5 shrink-0 transition-transform duration-200 ease-standard motion-reduce:transition-none', expanded && 'rotate-90')} />
      <span className="min-w-0 flex-1 truncate">{summary}</span>
      {trailing !== undefined && <span className="shrink-0 tabular-nums">{trailing}</span>}
    </button>
  )
}

export function AgentCard({
  dot,
  label,
  type,
  meta,
  progress,
  current,
  accessibleName,
  onOpen,
}: {
  dot: React.ReactNode
  label: string
  type?: string
  meta?: React.ReactNode
  progress: React.ReactNode
  current: boolean
  accessibleName: string
  onOpen: () => void
}) {
  const progressID = useId()
  return (
    <button
      type="button"
      data-slot="agent-card"
      aria-label={accessibleName}
      aria-describedby={progressID}
      aria-current={current || undefined}
      onClick={onOpen}
      className={cn(focusRingInset, 'flex w-full min-w-0 flex-col gap-0.5 rounded-control px-2 py-1 text-left text-ui-sm hover:bg-hover coarse:min-h-11', current && 'bg-selection')}
    >
      <span className="flex min-w-0 items-center gap-2">
        {dot}
        <span className="min-w-0 truncate text-text">{label}</span>
        {type && <span className="hidden shrink-0 text-muted sm:inline">{type}</span>}
        {meta && <span className="ml-auto flex shrink-0 items-center gap-2 text-muted tabular-nums">{meta}</span>}
      </span>
      <span id={progressID} className="min-w-0 truncate pl-[1.125rem] text-muted">{progress}</span>
    </button>
  )
}

export function AgentEntry({ dot, label, state, onOpen }: { dot: React.ReactNode; label: string; state: string; onOpen: () => void }) {
  return (
    <button
      type="button"
      data-slot="agent-entry"
      onClick={onOpen}
      className={cn(focusRingInset, 'flex h-6 w-full min-w-0 items-center gap-2 rounded-control pl-6 text-left text-ui-sm text-muted hover:bg-hover hover:text-text coarse:h-11')}
    >
      {dot}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      <span className="shrink-0">{state}</span>
    </button>
  )
}

export function LiveActivityRow({ tone, children }: { tone: Tone; children: React.ReactNode }) {
  const working = tone === 'working'
  return (
    <div data-slot="live-row" data-tone={tone} className="flex h-7 min-w-0 items-center text-ui-sm">
      <span className="flex w-6 shrink-0 justify-center"><StatusDot tone={tone} pulse={working} /></span>
      <span className={cn('min-w-0 truncate', working ? 'live-shimmer' : tone === 'needs-you' ? 'text-text' : 'text-muted')}>{children}</span>
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
      <div className="flex min-h-7 min-w-0 items-center">
        <span className="flex w-6 shrink-0 justify-center"><StatusDot tone={tone} /></span>
        <span className={cn('min-w-0 flex-1 break-words', tone !== 'neutral' && 'text-text')}>{children}</span>
        {trailing !== undefined && <span className="shrink-0 pl-2 tabular-nums">{trailing}</span>}
      </div>
      {detail && <div className={cn('line-clamp-4 max-h-[4lh] pl-6 break-words', typeof detail === 'string' && 'whitespace-pre-wrap')}>{detail}</div>}
    </div>
  )
}
