type Vertex = { left: boolean; x: number; top: boolean; y: number; r: number }

const outer = 8
const inner = 3
const segments = 5

/** A forward slash from the top right to the bottom left, leaning `lean` px over a `height` px control. */
function halves(height: number, lean: number, grow: number, pad: number): { first: Vertex[]; second: Vertex[] } {
  const along = (grow * lean) / height
  const across = grow * (Math.hypot(lean, height) / height)
  // `pad` moves the coordinates inward for a layer that extends `pad` px past the control's box.
  const v = (left: boolean, x: number, top: boolean, r: number): Vertex => ({ left, x: left ? x + pad : x - pad, top, y: top ? pad - grow : grow - pad, r: r + grow })
  return {
    first: [v(true, -grow, true, outer), v(false, along + across, true, inner), v(false, -lean - along + across, false, inner), v(true, -grow, false, outer)],
    second: [v(true, lean + along - across, true, inner), v(false, grow, true, outer), v(false, grow, false, outer), v(true, -along - across, false, inner)],
  }
}

function direction(a: Vertex, b: Vertex, height: number): [number, number] {
  const dx = a.left === b.left ? b.x - a.x : b.left ? -1 : 1
  const dy = a.left === b.left ? (b.top ? 0 : height) - (a.top ? 0 : height) + b.y - a.y : 0
  const length = Math.hypot(dx, dy)
  return [dx / length, dy / length]
}

const px = (left: boolean, x: number) => (left ? `${x}px` : `calc(100% + ${x}px)`)
const py = (top: boolean, y: number) => (top ? `${y}px` : `calc(100% + ${y}px)`)

function polygon(vertices: Vertex[], height: number): string {
  const points: string[] = []
  vertices.forEach((p, i) => {
    const before = vertices[(i + vertices.length - 1) % vertices.length]!
    const after = vertices[(i + 1) % vertices.length]!
    const [ax, ay] = direction(before, p, height)
    const [bx, by] = direction(p, after, height)
    const turn = Math.acos(Math.max(-1, Math.min(1, -(ax * bx + ay * by))))
    const reach = p.r / Math.tan(turn / 2)
    // The arc runs between the two tangent points, bent toward the corner.
    const start = { x: p.x - ax * reach, y: p.y - ay * reach }
    const end = { x: p.x + bx * reach, y: p.y + by * reach }
    for (let step = 0; step <= segments; step++) {
      const u = step / segments
      const mix = (s: number, c: number, e: number) => (1 - u) * (1 - u) * s + 2 * u * (1 - u) * c + u * u * e
      points.push(`${px(p.left, mix(start.x, p.x, end.x))} ${py(p.top, mix(start.y, p.y, end.y))}`)
    }
  })
  return `polygon(${points.join(',')})`
}

export type SplitShape = { first: string; second: string }

/** The two halves' clip paths, grown by `grow` px on every side; the controls are `height` px tall. */
export function splitShape(height: number, lean: number, grow = 0, pad = 0): SplitShape {
  const { first, second } = halves(height, lean, grow, pad)
  return { first: polygon(first, height + 2 * pad), second: polygon(second, height + 2 * pad) }
}
