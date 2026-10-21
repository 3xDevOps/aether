import { useState, type CSSProperties } from 'react'
import { Button } from '@/components/ui/button'
import type { InsightsResult } from '@/lib/types'
import { cn } from '@/lib/utils'
import { chartDays, chartMax, dayLabel, dayWords, memberTags, outcomeKeys, plural, type Day, type OutcomeKey, type Unit } from '@/routes/insights/model'

const stripes = 'repeating-linear-gradient(45deg, color-mix(in srgb, var(--canvas) 55%, transparent) 0 2px, transparent 2px 5px)'
const dots = 'radial-gradient(color-mix(in srgb, var(--canvas) 70%, transparent) 1px, transparent 1.6px)'

/** Each outcome has a letter and a texture besides its color, so a member's red cannot be taken for failed. */
const outcomes: Record<OutcomeKey, { label: string; letter: string; fill: string; texture: CSSProperties }> = {
  done: { label: 'Done', letter: 'D', fill: 'bg-state-done', texture: {} },
  failed: { label: 'Failed', letter: 'F', fill: 'bg-state-failed', texture: { backgroundImage: stripes } },
  abandoned: { label: 'Abandoned', letter: 'A', fill: 'bg-state-paused', texture: { backgroundImage: dots, backgroundSize: '4px 4px' } },
}

const units: { value: Unit; label: string }[] = [
  { value: 'runs', label: 'Runs' },
  { value: 'swarms', label: 'Swarms' },
]

export function Swatch({ fill }: { fill: string }) {
  return (
    <svg viewBox="0 0 10 10" aria-hidden className="size-2.5 shrink-0">
      <rect x="0.5" y="0.5" width="9" height="9" rx="2" className={fill} />
    </svg>
  )
}

function Dot({ className, style }: { className?: string; style?: CSSProperties }) {
  return <span aria-hidden className={cn('size-2.5 shrink-0 rounded-[3px]', className)} style={style} />
}

const memberColor = (color: string): CSSProperties => ({ backgroundColor: color || 'var(--icon-faint)' })

function Member({ name, tag, color }: { name: string; tag: string; color: string }) {
  return (
    <>
      <Dot className="ring-1 ring-text/40" style={memberColor(color)} />
      <span className="font-medium tabular-nums text-text">{tag}</span>
      <span className="min-w-0 truncate">{name}</span>
    </>
  )
}

function Bar({ day, outcome, max }: { day: Day; outcome: OutcomeKey; max: number }) {
  const texture = outcomes[outcome].texture
  const count = day.counts[outcome]
  const slices = day.members[outcome]
  return (
    <div className="relative flex h-full min-w-0 max-w-6 flex-1 flex-col justify-end">
      <div className="flex flex-col-reverse overflow-hidden rounded-t-[3px]" style={{ height: `${(count / max) * 100}%` }}>
        {slices.length === 0 ? (
          <div className={cn('size-full', outcomes[outcome].fill)} style={texture} />
        ) : (
          slices.map((slice) => (
            <div key={slice.id} data-member={slice.id} className="border-t border-canvas first:border-t-0" style={{ ...texture, ...memberColor(slice.color), height: `${(slice.count / count) * 100}%` }} />
          ))
        )}
      </div>
      {count > 0 && (
        <span aria-hidden className="absolute inset-x-0 text-center text-ui-xs tabular-nums text-muted" style={{ bottom: `${(count / max) * 100}%` }}>
          {count}
        </span>
      )}
    </div>
  )
}

const OutcomeDot = ({ outcome, team }: { outcome: OutcomeKey; team: boolean }) => <Dot className={team ? 'bg-icon-faint' : outcomes[outcome].fill} style={outcomes[outcome].texture} />

function Tooltip({ day, index, count, tags, team }: { day: Day; index: number; count: number; tags: Record<string, string>; team: boolean }) {
  const edge = index < 2 ? 'translate-x-[-0.5rem]' : index > count - 3 ? '-translate-x-[calc(100%-0.5rem)]' : '-translate-x-1/2'
  return (
    <div
      role="tooltip"
      className={cn('pointer-events-none absolute bottom-full z-10 mb-1 w-max max-w-56 rounded-panel border border-seam bg-raised px-2.5 py-2 text-ui-sm shadow-overlay', edge)}
      style={{ left: `${((index + 0.5) / count) * 100}%` }}
    >
      <p className="font-medium text-text">{dayLabel(day.start, { weekday: 'short', month: 'short', day: 'numeric' })}</p>
      <ul className="mt-1 flex flex-col gap-1">
        {outcomeKeys.map((key) => (
          <li key={key} className="flex flex-col">
            <span className="flex items-center gap-1.5 text-text">
              <OutcomeDot outcome={key} team={team} />
              {outcomes[key].label}
              <span className="ml-auto pl-3 tabular-nums">{day.counts[key]}</span>
            </span>
            {day.members[key].map((slice) => (
              <span key={slice.id} className="flex items-center gap-1.5 pl-4 text-muted">
                <Member name={slice.name} tag={tags[slice.id]} color={slice.color} />
                <span className="ml-auto pl-3 tabular-nums">{slice.count}</span>
              </span>
            ))}
          </li>
        ))}
      </ul>
    </div>
  )
}

export function ActivityChart({ result, team }: { result: InsightsResult; team: boolean }) {
  const [unit, setUnit] = useState<Unit>('runs')
  const [hovered, setHovered] = useState<number | null>(null)
  const [focused, setFocused] = useState<number | null>(null)
  const active = hovered ?? focused
  const days = chartDays(result, unit)
  const max = chartMax(days)
  const total = days.reduce((sum, d) => sum + outcomeKeys.reduce((n, key) => n + d.counts[key], 0), 0)
  const members = result.outcomes.members
  const tags = memberTags(members)

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-ui-sm text-muted">{plural(total, unit === 'runs' ? 'run' : 'swarm')} ended in the last 7 days</p>
        <div role="group" aria-label="Chart shows" className="flex items-center gap-0.5">
          {units.map((u) => (
            <Button key={u.value} size="sm" variant={unit === u.value ? 'secondary' : 'ghost'} aria-pressed={unit === u.value} onClick={() => setUnit(u.value)}>
              {u.label}
            </Button>
          ))}
        </div>
      </div>
      <div className="flex min-w-0 gap-2">
        <div aria-hidden className="flex h-40 w-6 shrink-0 flex-col justify-between pt-5 text-right text-ui-xs tabular-nums text-muted">
          <span>{max}</span>
          <span>0</span>
        </div>
        <div className="relative min-w-0 flex-1" onMouseLeave={() => setHovered(null)}>
          {active !== null && <Tooltip day={days[active]} index={active} count={days.length} tags={tags} team={team} />}
          <div
            role="group"
            aria-label={`${unit === 'runs' ? 'Runs' : 'Swarms'} per day, ${team ? 'everyone' : 'yours'}, by how they ended`}
            className="flex h-40 items-stretch gap-1 border-b border-seam pt-5 sm:gap-2"
          >
            {days.map((day, i) => (
              <div
                key={day.start}
                role="img"
                tabIndex={0}
                aria-label={dayWords(day, unit)}
                onMouseEnter={() => setHovered(i)}
                onFocus={() => setFocused(i)}
                onBlur={() => setFocused(null)}
                data-active={active === i || undefined}
                data-dimmed={active !== null && active !== i || undefined}
                className="flex min-w-0 flex-1 items-stretch justify-center gap-0.5 rounded-control px-0.5 outline-none transition-opacity focus-visible:ring-2 focus-visible:ring-accent data-active:bg-hover data-dimmed:opacity-40 motion-reduce:transition-none"
              >
                {outcomeKeys.map((key) => <Bar key={key} day={day} outcome={key} max={max} />)}
              </div>
            ))}
          </div>
          <div aria-hidden className="mt-1 flex gap-1 sm:gap-2">
            {days.map((day, i) => (
              <div key={day.start} className={cn('flex min-w-0 flex-1 flex-col items-center gap-0.5 px-0.5 text-ui-xs tabular-nums text-muted transition-opacity motion-reduce:transition-none', active !== null && active !== i && 'opacity-40')}>
                <div className="flex w-full justify-center gap-0.5">
                  {outcomeKeys.map((key) => <span key={key} className="min-w-0 max-w-6 flex-1 text-center">{outcomes[key].letter}</span>)}
                </div>
                <span className="text-text">{dayLabel(day.start, { weekday: 'short' })}</span>
                <span className="max-w-full truncate">{dayLabel(day.start)}</span>
              </div>
            ))}
          </div>
          {total === 0 && (
            <p className="absolute inset-x-0 top-1/3 text-center text-ui text-muted">No {unit} ended in the last 7 days.</p>
          )}
        </div>
      </div>
      <ul aria-label="Legend" className="ml-8 flex flex-wrap gap-x-4 gap-y-1 text-ui-sm text-muted">
        <li className="text-muted">Bars, left to right:</li>
        {outcomeKeys.map((key) => (
          <li key={key} className="flex items-center gap-1.5"><OutcomeDot outcome={key} team={team} />{outcomes[key].letter} {outcomes[key].label}</li>
        ))}
      </ul>
      {team && (
        <ul aria-label="Members" className="ml-8 flex flex-wrap gap-x-4 gap-y-1 text-ui-sm text-muted">
          {members.map((m) => (
            <li key={m.member_id} className="flex min-w-0 items-center gap-1.5">
              <Member name={m.display_name} tag={tags[m.member_id]} color={m.color} />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
