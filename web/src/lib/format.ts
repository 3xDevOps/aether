const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function formatBytes(bytes: number): string {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
const steps: [Intl.RelativeTimeFormatUnit, number][] = [
  ['second', 60],
  ['minute', 60],
  ['hour', 24],
  ['day', 7],
  ['week', 4.35],
  ['month', 12],
  ['year', Number.POSITIVE_INFINITY],
]

export function timeAgo(iso: string, now = Date.now()): string {
  let delta = (new Date(iso).getTime() - now) / 1000
  if (!Number.isFinite(delta)) return ''
  for (const [unit, span] of steps) {
    if (Math.abs(delta) < span) return relative.format(Math.round(delta), unit)
    delta /= span
  }
  return ''
}
