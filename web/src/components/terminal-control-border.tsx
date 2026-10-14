import { useId, useLayoutEffect, useRef, useState } from 'react'
import type { CSSProperties } from 'react'
import { useMediaQuery } from '@/lib/hooks'

export type TerminalControlAppearance = 'active' | 'release' | 'takeover' | 'hidden'

interface Pool {
  id: number
  /** Centre as a fraction of the width. */
  x: number
  peak: number
  /** Distance the pool slides over its life, as a fraction of the width. */
  drift: number
  born: number
  /** Animation delay at mount; undefined for a pool that never moves. */
  delay?: number
}

// A pool rises for 3s, holds, then fades for 3s (`control-pool` in index.css).
// A new pool starts rising each handoff, as the oldest one starts fading.
const handoffMs = 5000
const lifeMs = 13000

const stillPools: Pool[] = [
  { id: 0, x: 0.3, peak: 1, drift: 0, born: 0 },
  { id: 1, x: 0.74, peak: 1, drift: 0, born: 0 },
]

/** A spot at least a third of the width from the pool that stays, and clear of the one fading. */
function placePool(staying?: Pool, fading?: Pool): number {
  let best = 0.5
  let bestGap = -1
  for (let tries = 0; tries < 40; tries++) {
    const x = 0.08 + Math.random() * 0.84
    const gap = Math.min(staying ? Math.abs(x - staying.x) * 3 : 1, fading ? Math.abs(x - fading.x) / 0.15 : 1)
    if (gap >= 1) return x
    if (gap > bestGap) {
      best = x
      bestGap = gap
    }
  }
  return best
}

function usePools(lit: boolean, still: boolean): Pool[] {
  const [pools, setPools] = useState<Pool[]>([])
  useLayoutEffect(() => {
    if (!lit || still) return
    let alive: Pool[] = []
    let id = 0
    const birth = (born: number) => {
      alive = alive.filter((pool) => born - pool.born < lifeMs)
      const [fading, staying] = alive.length === 2 ? alive : [undefined, alive[0]]
      const x = placePool(staying, fading)
      const drift = Math.random() < 0.5 ? -0.03 : 0.03
      alive = [...alive, { id: id++, x, peak: 0.7 + Math.random() * 0.3, drift, born, delay: born - performance.now() }]
      setPools(alive)
    }
    // Two pools already lit, with the first handoff two seconds in.
    const start = performance.now()
    birth(start - 8000)
    birth(start - 3000)
    let next = start + 2000
    let timer = 0
    const tick = () => {
      // A throttled background tab replays only the last two missed handoffs.
      const missed = Math.floor((performance.now() - next) / handoffMs)
      if (missed > 1) next += (missed - 1) * handoffMs
      birth(next)
      next += handoffMs
      timer = window.setTimeout(tick, next - performance.now())
    }
    timer = window.setTimeout(tick, next - start)
    return () => {
      window.clearTimeout(timer)
      setPools([])
    }
  }, [lit, still])
  return !lit ? [] : still ? stillPools : pools
}

/** Decoration only: lease/input fencing never waits for this animation. */
export function TerminalControlBorder({
  appearance,
  takeoverProgress,
}: {
  appearance: TerminalControlAppearance
  takeoverProgress?: number
}) {
  const ref = useRef<SVGSVGElement>(null)
  const previousTakeoverProgress = useRef<number | undefined>(undefined)
  const [size, setSize] = useState({ width: 0, height: 0 })
  const [lit, setLit] = useState(false)
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const pools = usePools(lit, reducedMotion)
  const id = useId()

  useLayoutEffect(() => {
    const svg = ref.current
    if (!svg) return
    const observer = new ResizeObserver(([entry]) => {
      setSize({ width: entry.contentRect.width, height: entry.contentRect.height })
    })
    observer.observe(svg)
    return () => observer.disconnect()
  }, [])

  useLayoutEffect(() => {
    const svg = ref.current
    if (!svg) return
    const style = getComputedStyle(svg)
    const from = Number.parseFloat(style.strokeDashoffset) || 0
    const primary = style.getPropertyValue('--control-light').trim()
    const danger = style.getPropertyValue('--state-failed').trim()
    const color = appearance === 'takeover' && previousTakeoverProgress.current === 1
      ? danger : from === 1 ? primary : style.color
    const target = appearance === 'active' ? 0 : 1
    const targetColor = appearance === 'takeover' ? danger : primary
    svg.style.strokeDashoffset = String(target)
    svg.style.color = targetColor
    setLit(target === 0)
    if (reducedMotion || appearance === 'hidden' || !svg.animate) return

    const distance = Math.abs(target - from)
    // The curve starts at 1.5x speed, so 720ms matches a 480ms linear start velocity.
    const closingCurve = 'cubic-bezier(0.333333, 0.5, 0.666667, 1)'
    const travel = distance * (appearance === 'takeover' ? 1440 : 720)
    const fade = appearance === 'takeover' && from < 1 ? 540 : 0
    const duration = fade + travel
    if (duration === 0) return
    const frames: Keyframe[] = [{
      strokeDashoffset: from, color, offset: 0, easing: fade ? 'linear' : closingCurve,
    }]
    if (fade) frames.push({
      strokeDashoffset: from, color: danger, offset: fade / duration, easing: closingCurve,
    })
    frames.push({ strokeDashoffset: target, color: targetColor, offset: 1 })
    setLit(true)
    const animation = svg.animate(frames, { duration, easing: 'linear' })
    if (target === 1) animation.onfinish = () => setLit(false)
    return () => {
      // Freeze the visible point before reversing or reacquiring.
      const current = getComputedStyle(svg)
      svg.style.strokeDashoffset = current.strokeDashoffset
      svg.style.color = current.color
      animation.cancel()
    }
  }, [appearance, reducedMotion])

  useLayoutEffect(() => {
    previousTakeoverProgress.current = takeoverProgress
  }, [takeoverProgress])

  const { width, height } = size
  // Drawn from the right edge, where Take control and Release sit, so the
  // light pours out of the button and drains back into it.
  const edge = <path pathLength="1" d={`M ${width} 0.5 H 0`} />
  const region = { x: 0, y: 0, width, height }
  const along = Math.min(280, Math.max(90, width * 0.24))
  const spots = (scale: number) => pools.map((pool) => (
    <ellipse
      key={pool.id}
      cx={pool.x * width}
      cy={0}
      rx={along * scale}
      ry={96 * scale}
      fill={`url(#${id}-spot)`}
      style={pool.delay === undefined ? undefined : {
        '--peak': pool.peak,
        '--drift': `${pool.drift * width}px`,
        animation: `control-pool ${lifeMs}ms ${pool.delay}ms both`,
      } as CSSProperties}
    />
  ))
  const light = (
    <>
      <g mask={`url(#${id}-pool)`}>
        <g className="control-glow" filter={`url(#${id}-glow)`}>{edge}</g>
        <g className="control-halo" filter={`url(#${id}-halo)`}>{edge}</g>
      </g>
      <g mask={`url(#${id}-rim)`}>
        <g className="control-rim">{edge}</g>
      </g>
      <g mask={`url(#${id}-hot)`}>
        <g className="control-core" filter={`url(#${id}-bloom)`}>{edge}</g>
      </g>
    </>
  )
  const holding = appearance === 'active' && takeoverProgress !== undefined
  const progress = reducedMotion ? 1 : Math.min(1, Math.max(0, takeoverProgress ?? 0))
  return (
    <svg ref={ref} aria-hidden="true" className="terminal-control-border" data-control-appearance={appearance}>
      <defs>
        <filter id={`${id}-glow`} filterUnits="userSpaceOnUse" {...region}>
          <feGaussianBlur stdDeviation="18" />
        </filter>
        <filter id={`${id}-halo`} filterUnits="userSpaceOnUse" {...region}>
          <feGaussianBlur stdDeviation="3" />
        </filter>
        <filter id={`${id}-bloom`} filterUnits="userSpaceOnUse" {...region}>
          <feGaussianBlur stdDeviation="1.5" />
        </filter>
        <radialGradient id={`${id}-spot`}>
          <stop offset="0" stopColor="white" />
          <stop offset="1" stopColor="white" stopOpacity="0" />
        </radialGradient>
        <mask id={`${id}-pool`} maskUnits="userSpaceOnUse" {...region}>
          <rect {...region} fill="white" fillOpacity="0.1" />
          {spots(1)}
        </mask>
        <mask id={`${id}-rim`} maskUnits="userSpaceOnUse" {...region}>
          <rect {...region} fill="white" fillOpacity="0.45" />
          {spots(1)}
        </mask>
        <mask id={`${id}-hot`} maskUnits="userSpaceOnUse" {...region}>
          {spots(0.5)}
        </mask>
      </defs>
      {/* Red replaces the teal it reaches rather than mixing over it. */}
      <g style={holding ? { strokeDasharray: `0 ${progress} ${1 - progress} 1` } : undefined}>{light}</g>
      {holding && (
        <g
          data-takeover-border
          style={{
            color: 'var(--state-failed)',
            strokeDashoffset: 1 - progress,
          }}
        >
          {light}
        </g>
      )}
    </svg>
  )
}
