import { useRef, useState, type KeyboardEvent } from 'react'
import { useIsMobile } from '@/lib/breakpoints'
import { cn } from '@/lib/utils'
import { calendarDay, dayLabel, plural } from '@/routes/insights/model'
import { Swatch } from '@/routes/insights/activity-chart'

const levels = ['fill-chrome', 'fill-accent/25', 'fill-accent/50', 'fill-accent/75', 'fill-accent']
const gap = 2
const gutter = 30
const rows = ['Mon', '', 'Wed', '', 'Fri', '', '']

function level(runs: number, max: number): number {
  return runs === 0 ? 0 : Math.max(1, Math.ceil((runs / max) * 4))
}

export function Heatmap({ days }: { days: { date: string; runs: number }[] }) {
  const [focus, setFocus] = useState(() => days.length - 1)
  const refs = useRef<(SVGRectElement | null)[]>([])
  const cell = useIsMobile() ? 10 : 14
  if (days.length === 0) return null
  // Weeks run Monday to Sunday, so the first column is padded to the first day's weekday.
  const offset = (calendarDay(days[0].date).getUTCDay() + 6) % 7
  const weeks = Math.ceil((offset + days.length) / 7)
  const max = Math.max(1, ...days.map((d) => d.runs))
  const active = days.filter((d) => d.runs > 0).length
  const width = weeks * (cell + gap)
  const label = (d: { date: string; runs: number }) =>
    `${dayLabel(d.date, { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' })}: ${plural(d.runs, 'session')}`
  const months = days.flatMap((d, i) => (d.date.endsWith('-01') ? [{ col: Math.floor((offset + i) / 7), name: dayLabel(d.date, { month: 'short' }) }] : []))

  const move = (index: number) => {
    if (index < 0 || index >= days.length) return
    setFocus(index)
    refs.current[index]?.focus()
  }
  const onKeyDown = (event: KeyboardEvent<SVGRectElement>, index: number) => {
    const keys: Record<string, number> = {
      ArrowUp: index - 1,
      ArrowDown: index + 1,
      ArrowLeft: index - 7,
      ArrowRight: index + 7,
      Home: 0,
      End: days.length - 1,
    }
    if (!(event.key in keys)) return
    event.preventDefault()
    move(keys[event.key])
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="min-w-0 overflow-x-auto">
        <svg
          role="group"
          aria-label={`Sessions per day for 26 weeks: ${plural(active, 'active day')}, at most ${plural(max, 'session')} on one day. Use the arrow keys to read each day.`}
          width={width + gutter}
          height={7 * (cell + gap) + 14}
          viewBox={`-${gutter} -14 ${width + gutter} ${7 * (cell + gap) + 14}`}
          className="block"
        >
          {months.map((m) => (
            <text key={`${m.col}-${m.name}`} x={m.col * (cell + gap)} y={-4} aria-hidden className="fill-current text-ui-xs text-muted">
              {m.name}
            </text>
          ))}
          {rows.map((name, row) => name && (
            <text key={name} x={-4} y={row * (cell + gap) + cell / 2 + 4} textAnchor="end" aria-hidden className="fill-current text-ui-xs text-muted">
              {name}
            </text>
          ))}
          {days.map((d, i) => {
            const at = offset + i
            return (
              <rect
                key={d.date}
                ref={(node) => {
                  refs.current[i] = node
                }}
                role="img"
                aria-label={label(d)}
                tabIndex={i === focus ? 0 : -1}
                onFocus={() => setFocus(i)}
                onKeyDown={(event) => onKeyDown(event, i)}
                x={Math.floor(at / 7) * (cell + gap)}
                y={(at % 7) * (cell + gap)}
                width={cell}
                height={cell}
                rx={2}
                className={cn(levels[level(d.runs, max)], 'stroke-seam outline-none focus-visible:stroke-accent focus-visible:stroke-2')}
              >
                <title>{label(d)}</title>
              </rect>
            )
          })}
        </svg>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2 text-ui-sm text-muted">
        <p>A day counts every session that was active at some point that day.</p>
        <p className="flex items-center gap-1">
          <span className="sr-only">Scale from fewer sessions to more sessions</span>
          <span aria-hidden>Fewer</span>
          {levels.map((fill) => <Swatch key={fill} fill={fill} />)}
          <span aria-hidden>More</span>
        </p>
      </div>
    </div>
  )
}
