import type * as React from 'react'
import { ChevronRight, CircleCheck, CircleX, type LucideIcon } from '@/components/icons'
import { Spinner } from '@/components/ui/spinner'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { TextSwap } from '@/components/ui/text-swap'
import { Tooltip } from '@/components/ui/tooltip'
import { cn, focusRing, focusRingInset } from '@/lib/utils'

// The steps of an agent's turn: a header that folds them, then one row per
// step on a rail. Each row is its own virtual item, so the rail is drawn per
// row and a block carries no card around it.

const chevron = 'size-3.5 shrink-0 text-icon-faint transition-transform duration-200 ease-standard motion-reduce:transition-none'

export function IconStack({ icons }: { icons: LucideIcon[] }) {
  return (
    <span aria-hidden className="ml-0.5 flex shrink-0 items-center">
      {icons.map((Icon, index) => (
        <span key={index} className={cn('flex size-5 items-center justify-center rounded-chip border border-seam bg-chrome text-muted', index > 0 && '-ml-1.5')}>
          <Icon className="size-3" />
        </span>
      ))}
    </span>
  )
}

export function StepsHeader({
  name,
  open,
  onToggle,
  lead,
  label,
  meta,
}: {
  /** The full summary and counts, read in place of the visible parts. */
  name: string
  open: boolean
  onToggle: () => void
  lead: React.ReactNode
  label: React.ReactNode
  meta?: React.ReactNode
}) {
  return (
    <button
      type="button"
      data-slot="steps-header"
      aria-label={name}
      aria-expanded={open}
      onClick={onToggle}
      className={cn(focusRingInset, 'flex h-7 w-full min-w-0 items-center gap-2 rounded-control text-left text-ui text-muted hover:text-text coarse:h-11')}
    >
      {lead}
      <span className="min-w-0 truncate">{label}</span>
      {meta && <span className="flex shrink-0 items-center gap-1.5 tabular-nums">{meta}</span>}
      <ChevronRight className={cn(chevron, open && 'rotate-90')} />
    </button>
  )
}

/** A live header's lead: the run-state dot, centred on the rail. */
export function StepsLiveMark({ tone }: { tone: Tone }) {
  return (
    <span className="-mr-2 flex w-6 shrink-0 justify-center">
      <StatusDot tone={tone} pulse={tone === 'working'} />
    </span>
  )
}

/**
 * One step: a 24px gutter holding the step's mark on the rail, then the step.
 * The mark centres on a 28px step line, or a `tall` one, a disclosure that
 * grows to 44px on a coarse pointer. A row without a mark continues the rail
 * past a step's detail.
 */
export function StepRow({
  mark,
  tall = false,
  first = false,
  last = false,
  children,
}: {
  mark?: React.ReactNode
  tall?: boolean
  first?: boolean
  last?: boolean
  children: React.ReactNode
}) {
  return (
    <div data-slot="step" className="relative flex min-w-0">
      {mark !== undefined && !first && <span aria-hidden className={cn('absolute top-0 left-[11.5px] h-1 w-px bg-icon-faint/40', tall && 'coarse:h-3')} />}
      {!last && <span aria-hidden className={cn('absolute bottom-0 left-[11.5px] w-px bg-icon-faint/40', mark === undefined ? 'top-0' : tall ? 'top-6 coarse:top-8' : 'top-6')} />}
      <span aria-hidden className={cn('flex h-7 w-6 shrink-0 items-center justify-center text-icon-faint [&_svg]:size-3.5', tall && 'coarse:h-11')}>{mark}</span>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  )
}

export function StepLine({
  expanded,
  onToggle,
  trailing,
  className,
  children,
}: {
  expanded?: boolean
  onToggle?: () => void
  trailing?: React.ReactNode
  className?: string
  children: React.ReactNode
}) {
  const line = cn('flex h-7 w-full min-w-0 items-center gap-2 text-left text-ui text-muted', className)
  const content = (
    <>
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {trailing}
      {onToggle && <ChevronRight className={cn(chevron, expanded && 'rotate-90')} />}
    </>
  )
  if (!onToggle) return <div className={line}>{content}</div>
  return (
    <button type="button" aria-expanded={expanded} onClick={onToggle} className={cn(focusRingInset, line, 'rounded-control hover:text-text coarse:h-11')}>
      {content}
    </button>
  )
}

export function StepStatus({ status, waiting = false, children }: { status: 'running' | 'done' | 'failed'; waiting?: boolean; children?: React.ReactNode }) {
  const state = waiting ? 'waiting' : status
  return (
    <span className="flex shrink-0 items-center gap-1.5 text-ui-sm text-muted tabular-nums">
      <TextSwap swapKey={state} mode="crossfade" layerClassName="flex items-center">
        {state === 'waiting' ? <StatusDot tone="needs-you" />
          : state === 'running' ? <Spinner label="Running" className="text-state-working" />
          : state === 'done' ? <CircleCheck aria-label="Done" className="size-3.5 text-icon-faint" />
          : <CircleX aria-label="Failed" className="size-3.5 text-state-failed" />}
      </TextSwap>
      {state === 'waiting' ? <span className="text-text">Waiting for approval</span> : children}
    </span>
  )
}

const chip = 'inline-flex h-6 max-w-60 min-w-0 items-center gap-1 rounded-control border border-seam bg-chrome px-1.5 font-code text-ui-xs text-text coarse:h-8 [&_svg]:size-3 [&_svg]:shrink-0 [&_svg]:text-icon-faint'

export function StepChip({ icon: Icon, tip, onOpen, href, children }: { icon: LucideIcon; tip: string; onOpen?: () => void; href?: string; children: string }) {
  const content = (
    <>
      <Icon aria-hidden />
      <span className="truncate">{children}</span>
    </>
  )
  const active = cn(focusRing, chip, 'cursor-pointer transition-colors hover:bg-hover-chrome')
  if (href) {
    return (
      <Tooltip content={tip}>
        <a href={href} target="_blank" rel="noreferrer" className={active}>{content}</a>
      </Tooltip>
    )
  }
  if (onOpen) {
    return (
      <Tooltip content={tip}>
        <button type="button" onClick={onOpen} className={active}>{content}</button>
      </Tooltip>
    )
  }
  return <span title={tip} className={chip}>{content}</span>
}

export function StepChipToggle({ open, hidden, onToggle }: { open: boolean; hidden: number; onToggle: () => void }) {
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className={cn(focusRing, 'inline-flex h-6 items-center rounded-control px-1.5 text-ui-xs text-muted transition-colors hover:bg-hover-chrome hover:text-text coarse:h-8')}
    >
      {open ? 'Show less' : `+${hidden} more`}
    </button>
  )
}
