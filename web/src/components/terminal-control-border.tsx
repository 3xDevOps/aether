import { useId, useLayoutEffect, useRef, useState } from 'react'
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
  const [settled, setSettled] = useState(false)
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const steady = settled && appearance === 'active' && takeoverProgress === undefined
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
    setSettled(target === 0)
    if (reducedMotion || appearance === 'hidden' || !svg.animate) return

    const distance = Math.abs(target - from)
    // The curve starts at 1.5x speed, so 720ms matches a 480ms linear start velocity.
    const closingCurve = 'cubic-bezier(0.333333, 0.5, 0.666667, 1)'
    const travel = distance * (appearance === 'takeover' ? 1440 : 720)
    const fade = appearance === 'takeover' && from < 1 ? 540 : 0
    const duration = fade + travel
    if (duration === 0) return
    setSettled(false)
    const frames: Keyframe[] = [{
      strokeDashoffset: from, color, offset: 0, easing: fade ? 'linear' : closingCurve,
    }]
    if (fade) frames.push({
      strokeDashoffset: from, color: danger, offset: fade / duration, easing: closingCurve,
    })
    frames.push({ strokeDashoffset: target, color: targetColor, offset: 1 })
    const animation = svg.animate(frames, { duration, easing: 'linear' })
    if (target === 0) animation.onfinish = () => setSettled(true)
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

  // Full strength while the light moves or a hold turns it red; once it
  // settles, it dims to half and breathes. Opacity on the <svg> itself
  // animates on the compositor, without repainting the blurred strokes.
  useLayoutEffect(() => {
    const svg = ref.current
    if (!svg) return
    const level = steady ? 0.5 : 0.7
    if (reducedMotion || !svg.animate) {
      svg.style.opacity = String(level)
      return
    }
    const from = Number(getComputedStyle(svg).opacity)
    const settle = svg.animate([{ opacity: from }, { opacity: level }], {
      duration: steady ? 1200 : 300, easing: 'ease-in-out', fill: 'forwards',
    })
    const breathe = steady ? svg.animate([
      { opacity: 0.5, easing: 'ease-in-out' },
      { opacity: 0.6, offset: 0.25, easing: 'ease-in-out' },
      { opacity: 0.42, offset: 0.75, easing: 'ease-in-out' },
      { opacity: 0.5 },
    ], { duration: 8000, delay: 1200, iterations: Infinity }) : undefined
    return () => {
      svg.style.opacity = getComputedStyle(svg).opacity
      settle.cancel()
      breathe?.cancel()
    }
  }, [steady, reducedMotion])

  const { width, height } = size
  // Drawn from the right edge, where Take control and Release sit, so the
  // light pours out of the button and drains back into it.
  const edge = <path pathLength="1" d={`M ${width} 0.5 H 0`} />
  const region = { x: 0, y: 0, width, height }
  const light = (
    <>
      <g className="control-glow" filter={`url(#${id}-glow)`}>{edge}</g>
      <g className="control-halo" filter={`url(#${id}-halo)`}>{edge}</g>
      <g className="control-rim">{edge}</g>
    </>
  )
  const holding = appearance === 'active' && takeoverProgress !== undefined
  const progress = reducedMotion ? 1 : Math.min(1, Math.max(0, takeoverProgress ?? 0))
  return (
    <svg ref={ref} aria-hidden="true" className="terminal-control-border" data-control-appearance={appearance}>
      <defs>
        <filter id={`${id}-glow`} filterUnits="userSpaceOnUse" {...region}>
          <feGaussianBlur stdDeviation="12.6" />
        </filter>
        <filter id={`${id}-halo`} filterUnits="userSpaceOnUse" {...region}>
          <feGaussianBlur stdDeviation="2.1" />
        </filter>
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
