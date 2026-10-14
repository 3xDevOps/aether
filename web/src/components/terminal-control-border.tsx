import { useId, useLayoutEffect, useRef, useState } from 'react'
import { useMediaQuery } from '@/lib/hooks'

export type TerminalControlAppearance = 'active' | 'release' | 'takeover' | 'hidden'

/** Where the light pools along the edge, as fractions of the box. */
const smudges = [
  { x: 0.22, y: 0 },
  { x: 0.7, y: 0 },
  { x: 1, y: 0.38 },
  { x: 0.58, y: 1 },
  { x: 0, y: 0.68 },
]

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
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
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
    const animation = svg.animate(frames, { duration, easing: 'linear' })
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
  const left = 0.5
  const right = Math.max(left, width - 0.5)
  const top = 0.5
  const bottom = Math.max(top, height - 0.5)
  const x = width / 2
  const y = height / 2
  const paths = (
    <>
      <path pathLength="1" d={`M ${left} ${y} V ${top} H ${x}`} />
      <path pathLength="1" d={`M ${right} ${y} V ${top} H ${x}`} />
      <path pathLength="1" d={`M ${left} ${y} V ${bottom} H ${x}`} />
      <path pathLength="1" d={`M ${right} ${y} V ${bottom} H ${x}`} />
    </>
  )
  const region = { x: 0, y: 0, width, height }
  const spots = (scale: number) => smudges.map((spot, index) => {
    const along = Math.min(280, Math.max(90, (spot.y % 1 === 0 ? width : height) * 0.24)) * scale
    const across = 96 * scale
    return (
      <ellipse
        key={index}
        cx={spot.x * width}
        cy={spot.y * height}
        rx={spot.y % 1 === 0 ? along : across}
        ry={spot.y % 1 === 0 ? across : along}
        fill={`url(#${id}-spot)`}
      />
    )
  })
  const light = (
    <>
      <g mask={`url(#${id}-pool)`}>
        <g className="control-glow" filter={`url(#${id}-glow)`}>{paths}</g>
        <g className="control-halo" filter={`url(#${id}-halo)`}>{paths}</g>
      </g>
      <g mask={`url(#${id}-rim)`}>
        <g className="control-rim">{paths}</g>
      </g>
      <g mask={`url(#${id}-hot)`}>
        <g className="control-core" filter={`url(#${id}-bloom)`}>{paths}</g>
      </g>
    </>
  )
  const progress = Math.min(1, Math.max(0, takeoverProgress ?? 0))
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
      <g>{light}</g>
      {appearance === 'active' && takeoverProgress !== undefined && (
        <g
          data-takeover-border
          style={{
            color: 'var(--state-failed)',
            strokeDashoffset: reducedMotion ? 0 : 1 - progress,
          }}
        >
          {light}
        </g>
      )}
    </svg>
  )
}
