import type * as React from 'react'
import { ResponsiveContainer, type TooltipContentProps } from 'recharts'
import { cn } from '@/lib/utils'

export function ChartContainer({
  height,
  className,
  children,
  ...props
}: Omit<React.ComponentProps<'div'>, 'children'> & { height: number; children: React.ReactElement }) {
  return (
    <div
      data-slot="chart"
      className={cn(
        'w-full min-w-0 text-ui-xs tabular-nums',
        '[&_.recharts-cartesian-axis-tick_text]:fill-[var(--text-muted)] [&_.recharts-cartesian-grid_line]:stroke-seam',
        '[&_.recharts-cartesian-axis-line]:stroke-seam [&_.recharts-cartesian-axis-tick-line]:stroke-seam',
        '[&_.recharts-sector]:outline-none [&_.recharts-surface]:outline-none',
        className,
      )}
      style={{ height }}
      {...props}
    >
      <ResponsiveContainer width="100%" height="100%">
        {children}
      </ResponsiveContainer>
    </div>
  )
}

export interface ChartSeries {
  name: string
  color: string
}

export function ChartTooltipContent({
  active,
  payload,
  label,
  formatLabel,
  formatValue,
}: Partial<TooltipContentProps<number, string>> & {
  formatLabel?: (label: number) => string
  formatValue: (value: number, name: string) => string
}) {
  if (!active || !payload?.length) return null
  return (
    <div className="min-w-32 rounded-control border border-seam bg-raised px-2.5 py-1.5 text-ui-sm text-text shadow-[var(--overlay-shadow)]">
      {formatLabel && <p className="mb-1 text-muted">{formatLabel(Number(label))}</p>}
      <ul className="flex flex-col gap-0.5">
        {payload.map((item) => (
          <li key={String(item.dataKey)} className="flex items-center gap-2">
            {item.color && <span aria-hidden className="size-2 shrink-0 rounded-[2px]" style={{ background: item.color }} />}
            <span className="font-medium tabular-nums">{formatValue(Number(item.value), String(item.name))}</span>
            <span className="text-muted">{item.name}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function ChartLegend({ series }: { series: ChartSeries[] }) {
  return (
    <ul className="flex flex-wrap gap-x-3 gap-y-1 text-ui-sm text-muted">
      {series.map((s) => (
        <li key={s.name} className="flex items-center gap-1.5">
          <span aria-hidden className="size-2 rounded-[2px]" style={{ background: s.color }} />
          {s.name}
        </li>
      ))}
    </ul>
  )
}
