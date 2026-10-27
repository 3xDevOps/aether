import { useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { useReducedMotion } from '@/lib/hooks'

export type ControlAppearance = 'active' | 'release' | 'takeover' | 'hidden'

/** Decoration only: lease/input fencing never waits for this animation. */
export function ControlMatrix({
  appearance,
  takeoverProgress,
}: {
  appearance: ControlAppearance
  takeoverProgress?: number
}) {
  const ref = useRef<HTMLDivElement>(null)
  const previousTakeoverProgress = useRef<number | undefined>(undefined)
  const [settled, setSettled] = useState(false)
  const reducedMotion = useReducedMotion()
  const steady = settled && appearance === 'active' && takeoverProgress === undefined

  useLayoutEffect(() => {
    const light = ref.current
    if (!light) return
    const style = getComputedStyle(light)
    const from = Number.parseFloat(style.getPropertyValue('--control-reach')) || 0
    const primary = style.getPropertyValue('--control-light').trim()
    const danger = style.getPropertyValue('--state-failed').trim()
    const color = appearance === 'takeover' && previousTakeoverProgress.current === 1
      ? danger : from === 1 ? primary : style.color
    const target = appearance === 'active' ? 0 : 1
    const targetColor = appearance === 'takeover' ? danger : primary
    light.style.setProperty('--control-reach', String(target))
    light.style.color = targetColor
    setSettled(target === 0)
    if (reducedMotion || appearance === 'hidden' || !light.animate) return

    const distance = Math.abs(target - from)
    // The curve starts at 1.5x speed, so 720ms matches a 480ms linear start velocity.
    const closingCurve = 'cubic-bezier(0.333333, 0.5, 0.666667, 1)'
    const travel = distance * (appearance === 'takeover' ? 1440 : 720)
    const fade = appearance === 'takeover' && from < 1 ? 540 : 0
    const duration = fade + travel
    if (duration === 0) return
    setSettled(false)
    const frames: Keyframe[] = [{
      '--control-reach': from, color, offset: 0, easing: fade ? 'linear' : closingCurve,
    }]
    if (fade) frames.push({
      '--control-reach': from, color: danger, offset: fade / duration, easing: closingCurve,
    })
    frames.push({ '--control-reach': target, color: targetColor, offset: 1 })
    const animation = light.animate(frames, { duration, easing: 'linear' })
    if (target === 0) animation.onfinish = () => setSettled(true)
    return () => {
      // Freeze the visible point before reversing or reacquiring.
      const current = getComputedStyle(light)
      light.style.setProperty('--control-reach', current.getPropertyValue('--control-reach'))
      light.style.color = current.color
      animation.cancel()
    }
  }, [appearance, reducedMotion])

  useLayoutEffect(() => {
    previousTakeoverProgress.current = takeoverProgress
  }, [takeoverProgress])

  useLayoutEffect(() => {
    const light = ref.current
    if (!light) return
    const level = steady ? 0.5 : 0.7
    if (reducedMotion || !light.animate) {
      light.style.opacity = String(level)
      return
    }
    const from = Number(getComputedStyle(light).opacity)
    const settle = light.animate([{ opacity: from }, { opacity: level }], {
      duration: steady ? 1200 : 300, easing: 'ease-in-out', fill: 'forwards',
    })
    const breathe = steady ? light.animate([
      { opacity: 0.5, easing: 'ease-in-out' },
      { opacity: 0.6, offset: 0.25, easing: 'ease-in-out' },
      { opacity: 0.42, offset: 0.75, easing: 'ease-in-out' },
      { opacity: 0.5 },
    ], { duration: 8000, delay: 1200, iterations: Infinity }) : undefined
    return () => {
      light.style.opacity = getComputedStyle(light).opacity
      settle.cancel()
      breathe?.cancel()
    }
  }, [steady, reducedMotion])

  const holding = appearance === 'active' && takeoverProgress !== undefined
  const progress = reducedMotion ? 1 : Math.min(1, Math.max(0, takeoverProgress ?? 0))
  return (
    <div aria-hidden="true" className="control-matrix" data-control-appearance={appearance}>
      <div ref={ref} className="control-matrix-light" style={holding ? { '--control-hold': progress } as CSSProperties : undefined}>
        <div className="control-matrix-reach" />
        {holding && <div data-takeover-hold className="control-matrix-hold" />}
      </div>
    </div>
  )
}
