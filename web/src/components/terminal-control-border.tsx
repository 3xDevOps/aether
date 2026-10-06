import { useLayoutEffect, useRef, useState } from 'react'
import { useMediaQuery } from '@/lib/hooks'

export type TerminalControlAppearance = 'active' | 'release' | 'takeover' | 'hidden'

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
    const primary = style.getPropertyValue('--accent-fill').trim()
    const danger = style.getPropertyValue('--destructive').trim()
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

  const left = 0.5
  const right = Math.max(left, size.width - 0.5)
  const top = 0.5
  const bottom = Math.max(top, size.height - 0.5)
  const x = size.width / 2
  const y = size.height / 2
  const paths = (
    <>
      <path pathLength="1" d={`M ${left} ${y} V ${top} H ${x}`} />
      <path pathLength="1" d={`M ${right} ${y} V ${top} H ${x}`} />
      <path pathLength="1" d={`M ${left} ${y} V ${bottom} H ${x}`} />
      <path pathLength="1" d={`M ${right} ${y} V ${bottom} H ${x}`} />
    </>
  )
  const progress = Math.min(1, Math.max(0, takeoverProgress ?? 0))
  return (
    <svg ref={ref} aria-hidden="true" className="terminal-control-border" data-control-appearance={appearance}>
      <g>{paths}</g>
      {appearance === 'active' && takeoverProgress !== undefined && (
        <g
          data-takeover-border
          style={{
            color: 'var(--destructive)',
            strokeDashoffset: reducedMotion ? 0 : 1 - progress,
          }}
        >
          {paths}
        </g>
      )}
    </svg>
  )
}
