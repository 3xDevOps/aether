import type { Point } from '@/routes/insights/system/model'

export function Sparkline({ points, max, color = 'var(--text-muted)', className }: { points: Point[]; max?: number; color?: string; className?: string }) {
  if (points.length < 2) return <span aria-hidden className={className} />
  const top = max ?? Math.max(...points.map((p) => p.v))
  const from = points[0]!.t
  const span = points.at(-1)!.t - from || 1
  const path = points
    .map((p) => `${(((p.t - from) / span) * 100).toFixed(1)},${(22 - (top > 0 ? Math.min(1, p.v / top) : 0) * 20).toFixed(1)}`)
    .join(' ')
  return (
    <svg aria-hidden viewBox="0 0 100 24" preserveAspectRatio="none" className={className}>
      <polyline points={path} fill="none" stroke={color} strokeWidth="1.5" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}
