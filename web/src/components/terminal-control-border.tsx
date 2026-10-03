import { useLayoutEffect, useRef, useState } from 'react'
import { useMediaQuery } from '@/lib/hooks'

export type TerminalControlAppearance = 'active' | 'release' | 'takeover' | 'hidden'

/** Decoration only: lease/input fencing never waits for this animation. */
export function TerminalControlBorder({ appearance }: { appearance: TerminalControlAppearance }) {
  const ref = useRef<SVGSVGElement>(null)
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
    const color = style.color
    const primary = style.getPropertyValue('--primary').trim()
    const danger = style.getPropertyValue('--destructive').trim()
    const target = appearance === 'active' ? 0 : 1
    const targetColor = appearance === 'takeover' ? danger : primary
    svg.style.strokeDashoffset = String(target)
    svg.style.color = targetColor
    if (reducedMotion || appearance === 'hidden' || !svg.animate) return

    const distance = Math.abs(target - from)
    const travel = distance * 480
    const fade = appearance === 'takeover' && from < 1 ? 180 : 0
    const duration = fade + travel
    if (duration === 0) return
    const frames: Keyframe[] = [{ strokeDashoffset: from, color, offset: 0 }]
    if (fade) frames.push({ strokeDashoffset: from, color: danger, offset: fade / duration })
    frames.push({ strokeDashoffset: target, color: targetColor, offset: 1 })
    const animation = svg.animate(frames, { duration, easing: 'linear' })
    return () => {
      // Freeze the visible point before reversing or reacquiring. There are no
      // timers or completion callbacks that can outlive the current lease.
      const current = getComputedStyle(svg)
      svg.style.strokeDashoffset = current.strokeDashoffset
      svg.style.color = current.color
      animation.cancel()
    }
  }, [appearance, reducedMotion])

  const left = 0.5
  const right = Math.max(left, size.width - 0.5)
  const top = 0.5
  const bottom = Math.max(top, size.height - 0.5)
  const x = size.width / 2
  const y = size.height / 2
  return (
    <svg ref={ref} aria-hidden="true" className="terminal-control-border" data-control-appearance={appearance}>
      <path pathLength="1" d={`M ${left} ${y} V ${top} H ${x}`} />
      <path pathLength="1" d={`M ${right} ${y} V ${top} H ${x}`} />
      <path pathLength="1" d={`M ${left} ${y} V ${bottom} H ${x}`} />
      <path pathLength="1" d={`M ${right} ${y} V ${bottom} H ${x}`} />
    </svg>
  )
}
