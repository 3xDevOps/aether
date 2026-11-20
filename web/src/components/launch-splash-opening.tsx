import { useLayoutEffect, useRef } from 'react'
import logoSource from '../../public/cloudfield-logo-small.svg?raw'

/** Milliseconds from the splash's first paint, as on the website's opening. */
const T = {
  ring: 30,
  ringStep: 30,
  inner: 170,
  dot: 420,
  letters: 450,
  letterStep: 40,
  lineStep: 24,
  wake: 820,
} as const
const WAKE_MS = 620

const MIN_STEP = 12
/** The lattice widens on large windows so a frame never draws more strokes than this. */
const MAX_STROKES = 15_000
/** The whole awake field's opacity, so the name stays the brightest thing on screen. */
const LEVEL = 0.85
/** Near the lockup strokes are shorter and at this opacity, easing back over CALM_EASE px. */
const CALM_FLOOR = 0.46
const CALM_EASE = 95
const FIELD_STOPS = ['#aaf5ea', '#54e0d0', '#4bb9ea', '#4a8dff', '#266ce1', '#8fbaff']
const COLOURS = 40
const ALPHAS = 6
const PALETTE = Array.from({ length: COLOURS }, (_, i) => {
  const t = Math.min(0.9999, i / (COLOURS - 1)) * (FIELD_STOPS.length - 1)
  const k = Math.floor(t)
  const a = rgb(FIELD_STOPS[k])
  const b = rgb(FIELD_STOPS[k + 1])
  return `rgb(${a.map((v, q) => Math.round(v + (b[q] - v) * (t - k))).join(',')})`
})
/** x and y as fractions of the window, spin, radius in px: the website's hero poster. */
const VORTICES = [
  [0.13, 0.25, 1, 240],
  [0.93, 0.4, -1, 230],
  [0.6, 0.11, 1, 170],
  [0.5, 0.98, -1, 200],
] as const

function rgb(hex: string): number[] {
  return [1, 3, 5].map((at) => parseInt(hex.slice(at, at + 2), 16))
}

const TAU = Math.PI * 2

const noise = (x: number, y: number) =>
  0.5 + 0.25 * Math.sin(x * 0.006 + Math.sin(y * 0.004) * 2.1) + 0.25 * Math.sin(y * 0.009 - x * 0.003 + 1.7 + Math.sin(x * 0.002) * 3)

/** The x range one subpath of absolute M, L and circular A commands covers. */
function xRange(d: string): [number, number] {
  let from = Infinity
  let to = -Infinity
  let x = 0
  let y = 0
  for (const [, command, args] of d.matchAll(/([MLA])([^MLA]*)/g)) {
    const n = args.trim().split(/[\s,]+/).map(Number)
    const reach = (v: number) => {
      from = Math.min(from, v)
      to = Math.max(to, v)
    }
    if (command === 'A') {
      const [r, , , large, sweep, x1, y1] = n
      const dx = (x - x1) / 2
      const dy = (y - y1) / 2
      const h = Math.sqrt(Math.max(0, (r * r) / (dx * dx + dy * dy) - 1)) * (large === sweep ? -1 : 1)
      const cx = (x + x1) / 2 + h * dy
      const cy = (y + y1) / 2 - h * dx
      const start = Math.atan2(y - cy, x - cx)
      const turn = (angle: number) => ((((sweep ? angle - start : start - angle) % TAU) + TAU) % TAU)
      const end = turn(Math.atan2(y1 - cy, x1 - cx))
      if (turn(0) <= end) reach(cx + r)
      if (turn(Math.PI) <= end) reach(cx - r)
      x = x1
      y = y1
    } else {
      x = n[0]
      y = n[1]
    }
    reach(x)
  }
  return [from, to]
}

/**
 * Splits the logo file into the parts the opening plays: each ring dash, the
 * centre dot, and one group per letter. The file draws all the letters as one
 * path, two parallel lines per stroke, and the letters sit closer together than
 * a stroke's two lines do. So a stem's two lines are paired first, as the
 * stems of one height taken two by two from the left; then subpaths, pairs
 * and the i's dot that overlap along x are one letter.
 */
function buildLockup(source: string): { svg: SVGSVGElement; dot: SVGCircleElement } {
  const svg = new DOMParser().parseFromString(source, 'image/svg+xml').documentElement as unknown as SVGSVGElement
  const paths = [...svg.querySelectorAll('path')]
  const word = paths.find((path) => path.getAttribute('d')!.split('M').length > 2)!
  const dashes = paths.filter((path) => path !== word)
  // The source draws nine outer segments, then seven inner ones. Hollow
  // segments trace both edges, so their arc radius cannot identify the ring.
  const outerCount = 9
  for (const [index, dash] of dashes.entries()) {
    const delay = index < outerCount ? T.ring + index * T.ringStep : T.inner + (index - outerCount) * T.ringStep
    dash.classList.add('launch-splash__dash')
    dash.setAttribute('pathLength', '1')
    dash.style.animationDelay = `${delay}ms`
  }

  const [dot, ...letterDots] = [...svg.querySelectorAll('circle')]
  dot.classList.add('launch-splash__dot')
  dot.style.animationDelay = `${T.dot}ms`

  const pieces = [
    ...word.getAttribute('d')!.split(/(?=M)/).map((d) => {
      const line = word.cloneNode() as SVGPathElement
      line.setAttribute('d', d)
      line.setAttribute('pathLength', '1')
      const [from, to] = xRange(d)
      const stem = /^M(\S+)[ ,](\S+)L\1[ ,](\S+)$/.exec(d)
      return { node: line as SVGElement, line: true, from, to, height: stem && [stem[2], stem[3]].sort().join() }
    }),
    ...letterDots.map((circle) => {
      const cx = Number(circle.getAttribute('cx'))
      const r = Number(circle.getAttribute('r'))
      return { node: circle as SVGElement, line: false, from: cx - r, to: cx + r, height: null }
    }),
  ]
  for (const height of new Set(pieces.map((piece) => piece.height))) {
    if (!height) continue
    const stems = pieces.filter((piece) => piece.height === height).sort((a, b) => a.from - b.from)
    for (let k = 0; k + 1 < stems.length; k += 2) {
      stems[k].to = stems[k + 1].to
      stems[k + 1].from = stems[k].from
    }
  }
  pieces.sort((a, b) => a.from - b.from)

  const letters: { node: SVGGElement; to: number; lines: number }[] = []
  for (const piece of pieces) {
    let letter = letters.at(-1)
    if (!letter || piece.from > letter.to) {
      const node = svg.ownerDocument.createElementNS('http://www.w3.org/2000/svg', 'g')
      node.classList.add('launch-splash__letter')
      node.style.animationDelay = `${T.letters + letters.length * T.letterStep}ms`
      letter = { node, to: piece.to, lines: 0 }
      letters.push(letter)
    }
    letter.to = Math.max(letter.to, piece.to)
    if (piece.line) {
      const delay = T.letters + (letters.length - 1) * T.letterStep + (letter.lines++ % 3) * T.lineStep
      piece.node.style.animationDelay = `${delay}ms`
    }
    letter.node.append(piece.node)
  }
  word.replaceWith(...letters.map((letter) => letter.node))
  return { svg, dot }
}

/**
 * The website's slope field: one short stroke per lattice point, turned along
 * four drifting vortices. Asleep, every stroke is the same short flat dash, so
 * it is drawn once; the frame loop starts when it wakes outward from the c's
 * centre at T.wake. Strokes near the lockup shorten and dim so the name reads.
 */
function startField(canvas: HTMLCanvasElement, from: Element, lockup: Element): () => void {
  const ctx = canvas.getContext('2d')
  if (!ctx) return () => {}
  const start = performance.now()
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)')
  const buckets: number[][] = Array.from({ length: COLOURS * ALPHAS * 2 }, () => [])
  let W = 0
  let H = 0
  let dpr = 1
  let step = MIN_STEP
  let cols = 0
  let rows = 0
  let nz = new Float32Array(0)
  let tone = new Uint8Array(0)
  let calm = new Float32Array(0)
  let dim = new Float32Array(0)
  let wake: { x: number; y: number; reach: number } | null = null

  const resize = () => {
    W = window.innerWidth
    H = window.innerHeight
    dpr = Math.min(window.devicePixelRatio || 1, 2)
    canvas.width = Math.round(W * dpr)
    canvas.height = Math.round(H * dpr)
    step = Math.max(MIN_STEP, Math.ceil(Math.sqrt((W * H) / MAX_STROKES)))
    cols = Math.ceil(W / step) + 1
    rows = Math.ceil(H / step) + 1
    nz = new Float32Array(cols * rows)
    tone = new Uint8Array(cols * rows)
    calm = new Float32Array(cols * rows).fill(1)
    dim = new Float32Array(cols * rows).fill(1)
    const r = lockup.getBoundingClientRect()
    for (let j = 0; j < rows; j++) {
      for (let i = 0; i < cols; i++) {
        const at = j * cols + i
        const x = i * step
        const y = j * step
        const n = noise(x, y)
        nz[at] = n
        // Stretched about the middle so the pale ends of the run reach the corners.
        const t = 0.5 + (0.5 - ((x / W) * 0.62 + (y / H) * 0.38) + (n - 0.5) * 0.34) * 1.5
        tone[at] = Math.round(Math.max(0, Math.min(1, t)) * (COLOURS - 1))
        const d = Math.hypot(Math.max(r.left - x, 0, x - r.right), Math.max(r.top - y, 0, y - r.bottom))
        if (d >= CALM_EASE) continue
        const f = d / CALM_EASE
        calm[at] = f * f * (3 - 2 * f)
        dim[at] = CALM_FLOOR + (1 - CALM_FLOOR) * calm[at]
      }
    }
  }

  const draw = (time: number) => {
    const since = time - start
    const t = since / 1000
    const V = VORTICES.map(([fx, fy, spin, rad], q) => [
      fx * W + Math.sin(t * (0.34 + q * 0.07) + q * 1.7) * 76,
      fy * H + Math.cos(t * (0.29 + q * 0.06) + q * 2.3) * 58,
      spin * (1 + 0.3 * Math.sin(t * 0.52 + q)),
      rad * Math.min(1.25, Math.max(0.7, W / 1440 + 0.25)),
    ])

    let front = -1
    if (since >= T.wake) {
      if (!wake) {
        const box = from.getBoundingClientRect()
        const x = box.left + box.width / 2
        const y = box.top + box.height / 2
        wake = { x, y, reach: Math.hypot(Math.max(x, W - x), Math.max(y, H - y)) + 120 }
      }
      const p = Math.min(1, (since - T.wake) / WAKE_MS)
      front = p >= 1 ? Infinity : (1 - (1 - p) * (1 - p)) * wake.reach
    }

    for (const b of buckets) b.length = 0
    for (let j = 0; j < rows; j++) {
      const y = j * step
      for (let i = 0; i < cols; i++) {
        const at = j * cols + i
        const x = i * step
        const n = nz[at]
        const lit =
          front === Infinity ? 1 : front < 0 ? 0 : Math.max(0, Math.min(1, (front - Math.hypot(x - wake!.x, y - wake!.y)) / 64))

        let vx = 0.55
        let vy = 0.12
        for (const v of V) {
          const dx = x - v[0]
          const dy = y - v[1]
          const d2 = dx * dx + dy * dy
          const w = Math.exp(-d2 / (2 * v[3] * v[3])) * 2.2
          if (w < 0.004) continue
          const d = Math.sqrt(d2 + 1)
          vx += (-dy / d) * v[2] * w
          vy += (dx / d) * v[2] * w
        }
        const mag = Math.hypot(vx, vy)
        let ux = mag > 0.0001 ? vx / mag : 1
        let uy = mag > 0.0001 ? vy / mag : 0
        let len = step * (0.34 + 0.5 * n * Math.min(1.2, mag * 0.7)) * (0.5 + 0.5 * calm[at])
        const heavy = n > 0.5
        let alpha = (heavy ? 1 : 0.72) * dim[at] * LEVEL
        let colour = tone[at]

        if (lit < 1) {
          // flat and short until the front arrives; two steps, not a fade
          const s = lit < 0.5 ? 0 : 0.6
          ux = ux * s + (1 - s)
          uy *= s
          const m = Math.hypot(ux, uy) || 1
          ux /= m
          uy /= m
          len = len * s + 3.4 * (1 - s)
          alpha = lit > 0 ? 1 : 0.3
          // the front itself runs a shade lighter
          if (lit > 0) colour = Math.min(COLOURS - 1, colour + 4)
        }

        const a = Math.min(ALPHAS - 1, Math.max(0, Math.round(alpha * ALPHAS) - 1))
        const hx = (ux * len) / 2
        const hy = (uy * len) / 2
        buckets[(colour * ALPHAS + a) * 2 + (heavy ? 1 : 0)].push(x - hx, y - hy, x + hx, y + hy)
      }
    }

    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, W, H)
    ctx.lineCap = 'butt'
    for (let b = 0; b < buckets.length; b++) {
      const seg = buckets[b]
      if (!seg.length) continue
      const rest = b >> 1
      ctx.globalAlpha = ((rest % ALPHAS) + 1) / ALPHAS
      ctx.strokeStyle = PALETTE[(rest / ALPHAS) | 0]
      ctx.lineWidth = ((b & 1 ? 2.4 : 1.5) * step) / MIN_STEP
      ctx.beginPath()
      for (let s = 0; s < seg.length; s += 4) {
        ctx.moveTo(seg[s], seg[s + 1])
        ctx.lineTo(seg[s + 2], seg[s + 3])
      }
      ctx.stroke()
    }
  }

  let frame = 0
  let running = false
  const loop = (time: number) => {
    draw(time)
    frame = requestAnimationFrame(loop)
  }
  const stop = () => {
    running = false
    window.clearTimeout(wakeTimer)
    cancelAnimationFrame(frame)
  }
  const onResize = () => {
    resize()
    if (!running) draw(performance.now())
  }
  const onMotion = () => {
    if (reduced.matches) stop()
  }
  resize()
  // Drawn before the first paint, so the splash never opens on an empty sky.
  draw(start)
  const wakeTimer = window.setTimeout(() => {
    running = true
    frame = requestAnimationFrame(loop)
  }, T.wake)
  window.addEventListener('resize', onResize)
  reduced.addEventListener('change', onMotion)
  return () => {
    stop()
    window.removeEventListener('resize', onResize)
    reduced.removeEventListener('change', onMotion)
  }
}

/**
 * The website's opening, drawn from `web/public/cloudfield-logo-small.svg`.
 * A logo file the builder cannot split shows as a plain image instead: a
 * decoration is never worth a crash.
 */
export function Opening() {
  const field = useRef<HTMLCanvasElement>(null)
  const lockup = useRef<HTMLDivElement>(null)

  useLayoutEffect(() => {
    const host = lockup.current!
    let from: Element = host
    try {
      const { svg, dot } = buildLockup(logoSource)
      host.replaceChildren(svg)
      from = dot
    } catch {
      const img = document.createElement('img')
      img.src = '/cloudfield-logo-small.svg'
      img.alt = ''
      host.replaceChildren(img)
    }
    return startField(field.current!, from, host)
  }, [])

  return (
    <>
      <canvas ref={field} className="launch-splash__field" />
      <div ref={lockup} className="launch-splash__lockup" data-testid="launch-splash-lockup" />
    </>
  )
}
