import type { LaneWindow } from '@/store/lanes'

const minute = 60_000
const hour = 60 * minute
const day = 24 * hour

export const windowSpan: Record<LaneWindow, number> = { '1h': hour, '6h': 6 * hour, '24h': day, '7d': 7 * day }
export const minSpan = 5 * minute
export const maxSpan = 30 * day

export interface TimeWindow {
  start: number
  end: number
}

export function presetOf(span: number): LaneWindow | undefined {
  return (Object.keys(windowSpan) as LaneWindow[]).find((key) => windowSpan[key] === span)
}

/** The next larger preset, for "Show 24h" when the current one hides runs. */
export function widerWindow(span: number): LaneWindow | undefined {
  return (Object.keys(windowSpan) as LaneWindow[]).find((key) => windowSpan[key] > span)
}

export function xOf(t: number, window: TimeWindow, width: number): number {
  return ((t - window.start) / (window.end - window.start)) * width
}

const steps = [minute, 2 * minute, 5 * minute, 10 * minute, 15 * minute, 30 * minute, hour, 2 * hour, 3 * hour, 6 * hour, 12 * hour, day, 2 * day, 7 * day]

/** The smallest step that keeps the axis at eight ticks or fewer: 1h reads 10m, 6h 1h, 24h 3h, 7d 1d. */
export function tickStep(span: number): number {
  return steps.find((step) => span / step <= 8) ?? steps.at(-1)!
}

export interface Tick {
  t: number
  label: string
}

/** "−1h 20m" from a span in whole steps. */
export function agoLabel(ms: number): string {
  const minutes = Math.round(ms / minute)
  if (minutes < 60) return `−${minutes}m`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return `−${hours}h${rest ? ` ${rest}m` : ''}`
}

/**
 * Up to an hour wide the ticks count back from now; wider windows sit on local
 * clock boundaries and read the time, or the weekday and date for day steps.
 */
export function ticks(window: TimeWindow, now: number): Tick[] {
  const span = window.end - window.start
  const step = tickStep(span)
  const out: Tick[] = []
  if (span <= hour) {
    for (let t = now - step * Math.max(1, Math.ceil((now - window.end) / step)); t > window.start; t -= step) {
      out.push({ t, label: agoLabel(now - t) })
    }
    return out.reverse()
  }
  const midnight = new Date(window.start)
  midnight.setHours(0, 0, 0, 0)
  const origin = midnight.getTime()
  for (let t = origin + Math.ceil((window.start - origin) / step) * step; t < window.end; t += step) {
    if (t <= window.start) continue
    const when = new Date(t)
    const label = step >= day
      ? `${when.toLocaleDateString([], { weekday: 'short' })} ${when.getDate()}`
      : when.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    out.push({ t, label })
  }
  return out
}

/** How often live bars move a pixel, bounded so a wide window still ticks twice a minute. */
export function tickInterval(span: number, width: number): number {
  return Math.min(30_000, Math.max(1000, width > 0 ? span / width : 30_000))
}
