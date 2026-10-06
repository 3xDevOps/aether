import type * as React from 'react'
import { cn } from '@/lib/utils'

export type Tone = 'working' | 'needs-you' | 'failed' | 'done' | 'paused' | 'neutral'

const classes: Record<Tone, { text: string; soft: string }> = {
  working: { text: 'text-state-working', soft: 'bg-state-working-soft' },
  'needs-you': { text: 'text-state-needs-you', soft: 'bg-state-needs-you-soft' },
  failed: { text: 'text-state-failed', soft: 'bg-state-failed-soft' },
  done: { text: 'text-state-done', soft: 'bg-state-done-soft' },
  paused: { text: 'text-state-paused', soft: 'bg-state-paused-soft' },
  neutral: { text: 'text-muted', soft: 'bg-chrome' },
}

export function toneClasses(tone: Tone): { text: string; soft: string } {
  return classes[tone]
}

const shapes: Record<Tone, React.ReactNode> = {
  working: <circle cx="5" cy="5" r="4" fill="currentColor" />,
  'needs-you': <circle cx="5" cy="5" r="3.5" fill="none" stroke="currentColor" strokeWidth="2" />,
  done: (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
      <path d="M3.2 5.1 4.5 6.4 6.9 3.8" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round" />
    </>
  ),
  failed: (
    <>
      <circle cx="5" cy="5" r="4.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
      <path d="M3.5 3.5 6.5 6.5M6.5 3.5 3.5 6.5" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" />
    </>
  ),
  paused: (
    <>
      <rect x="2" y="1.5" width="2.25" height="7" rx="0.5" fill="currentColor" />
      <rect x="5.75" y="1.5" width="2.25" height="7" rx="0.5" fill="currentColor" />
    </>
  ),
  neutral: <circle cx="5" cy="5" r="4" fill="none" stroke="currentColor" strokeWidth="1" />,
}

export function StatusDot({
  tone,
  pulse = false,
  label,
  className,
}: {
  tone: Tone
  pulse?: boolean
  label?: string
  className?: string
}) {
  return (
    <svg
      data-slot="status-dot"
      data-tone={tone}
      viewBox="0 0 10 10"
      {...(label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': true })}
      className={cn(
        'size-2.5 shrink-0',
        tone === 'neutral' ? 'text-icon-faint' : classes[tone].text,
        pulse && 'state-pulse',
        className,
      )}
    >
      {shapes[tone]}
    </svg>
  )
}

export function StateLine({
  tone,
  children,
  trailing,
  pulse,
  className,
}: {
  tone: Tone
  children: React.ReactNode
  trailing?: React.ReactNode
  pulse?: boolean
  className?: string
}) {
  return (
    <span data-slot="state-line" className={cn('flex min-w-0 items-center gap-1.5 text-ui-sm', className)}>
      <StatusDot tone={tone} pulse={pulse} />
      <span className={cn('min-w-0 truncate', tone === 'needs-you' ? 'text-text' : 'text-muted')}>{children}</span>
      {trailing !== undefined && <span className="ml-auto shrink-0 text-muted tabular-nums">{trailing}</span>}
    </span>
  )
}
