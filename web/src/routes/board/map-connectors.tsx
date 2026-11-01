import type { MapLine, MapPoint, MapUnit } from '@/routes/board/map-layout'

/** An orthogonal polyline as an SVG path whose corners turn on arcs of `radius`. */
export function roundedPath({ points, radius }: MapLine): string {
  const toward = (from: MapPoint, to: MapPoint, distance: number) => {
    const length = Math.hypot(to.x - from.x, to.y - from.y)
    const step = Math.min(distance, length / 2)
    return { x: from.x + ((to.x - from.x) / length) * step, y: from.y + ((to.y - from.y) / length) * step }
  }
  let d = `M${points[0].x} ${points[0].y}`
  for (let index = 1; index < points.length - 1; index++) {
    const corner = points[index]
    const start = toward(corner, points[index - 1], radius)
    const end = toward(corner, points[index + 1], radius)
    d += ` L${start.x} ${start.y} Q${corner.x} ${corner.y} ${end.x} ${end.y}`
  }
  const last = points[points.length - 1]
  return `${d} L${last.x} ${last.y}`
}

/** A family's trunk, bus, rails and elbows, in the unit's own coordinates so they move with it. */
export function MapConnectors({ unit }: { unit: MapUnit }) {
  if (!unit.lines.length) return null
  return (
    <svg
      aria-hidden
      data-map-connectors
      className="pointer-events-none absolute inset-0 overflow-visible text-icon-faint"
      width={unit.width}
      height={unit.height}
    >
      {unit.lines.map((line, index) => (
        <path key={index} d={roundedPath(line)} fill="none" stroke="currentColor" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
      ))}
      {unit.dots.map((dot) => (
        <circle key={`${dot.x},${dot.y}`} cx={dot.x} cy={dot.y} r={4} className="fill-chrome" stroke="currentColor" strokeWidth={2} />
      ))}
    </svg>
  )
}
