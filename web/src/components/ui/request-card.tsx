import { useId, useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { ProgressBar } from '@/components/ui/progress-bar'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { cn, focusRingInset } from '@/lib/utils'

export function RequestCard({
  title,
  avatar,
  source,
  meta,
  tone = 'needs-you',
  description,
  timeBar,
  footer,
  children,
  className,
  ...props
}: Omit<React.ComponentProps<'div'>, 'title'> & {
  title: React.ReactNode
  avatar?: React.ReactNode
  source?: React.ReactNode
  meta?: React.ReactNode
  tone?: Tone
  description?: React.ReactNode
  timeBar?: React.ReactNode
  footer?: React.ReactNode
}) {
  const titleID = useId()
  const descriptionID = useId()
  const centered = avatar && source === undefined
  return (
    <div
      data-slot="request-card"
      role="group"
      tabIndex={-1}
      aria-labelledby={props['aria-label'] ? undefined : titleID}
      aria-describedby={description ? descriptionID : undefined}
      className={cn(
        '@container flex flex-col overflow-clip rounded-panel border border-seam bg-raised text-ui text-text outline-none',
        focusRingInset,
        className,
      )}
      {...props}
    >
      <div className="px-4 pt-3.5 pb-3">
        <div className="flex min-w-0 items-start gap-2.5">
          {avatar && <span className="flex size-7 shrink-0 items-center justify-center">{avatar}</span>}
          <div className={cn('min-w-0 flex-1', centered && 'pt-1')}>
            <p id={titleID} className="font-medium break-words">{title}</p>
            {source !== undefined && (
              <p className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted [&_svg]:size-3.5 [&_svg]:shrink-0">{source}</p>
            )}
          </div>
          {meta !== undefined && <div className={cn('flex h-5 shrink-0 items-center gap-1 text-ui-sm text-muted tabular-nums', centered && 'mt-1')}>{meta}</div>}
          <span className={cn('flex h-5 shrink-0 items-center', centered && 'mt-1')}><StatusDot tone={tone} /></span>
        </div>
        {description && (
          <div className={cn('mt-2.5 flex min-w-0 gap-2.5', avatar && '@min-[21rem]:ml-9.5')}>
            <span aria-hidden className="w-0.5 shrink-0 self-stretch rounded-full bg-icon-faint/30" />
            <div id={descriptionID} className="min-w-0 flex-1 text-ui-sm break-words whitespace-pre-wrap text-muted">{description}</div>
          </div>
        )}
      </div>
      {children}
      {timeBar}
      {footer}
    </div>
  )
}

export function RequestTimeBar({ left, total }: { left: number; total: number }) {
  return (
    <ProgressBar
      aria-hidden
      value={left}
      max={total}
      className="h-0.5 shrink-0 rounded-none bg-seam"
      fillClassName="rounded-none bg-state-needs-you duration-1000 ease-linear"
    />
  )
}

/** `className` must set the max height the text is cut to. */
export function Clamped({ className, children }: { className: string; children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [overflows, setOverflows] = useState(false)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el || open) return
    const measure = () => setOverflows(el.scrollHeight > el.clientHeight + 1)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [open, children])
  return (
    <>
      <div ref={ref} className={cn(!open && cn('overflow-hidden', className))}>{children}</div>
      {(overflows || open) && (
        <Button variant="quiet" size="sm" className="mt-1 self-start text-ui-sm" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? 'Show less' : 'Show more'}
        </Button>
      )}
    </>
  )
}
