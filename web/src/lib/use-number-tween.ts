import { useEffect, useRef, useState } from 'react'
import { useReducedMotion } from '@/lib/hooks'
import { easing, time } from '@/lib/motion'

/**
 * `target`, eased from the value on screen to each new one over
 * `--duration-500` on `--ease-glide`; a change mid-tween restarts from what is shown. Shows
 * the target at once under reduced motion.
 */
export function useNumberTween(target: number): number {
  const reduced = useReducedMotion()
  const [shown, setShown] = useState(target)
  const current = useRef(target)

  useEffect(() => {
    const duration = time('--duration-500')
    if (reduced || duration <= 0) {
      current.current = target
      setShown(target)
      return
    }
    const from = current.current
    // An animation with no target, scrubbed by hand, evaluates the token's curve.
    const timing = new Animation(new KeyframeEffect(null, null, { duration, easing: easing('--ease-glide'), fill: 'both' }))
    const start = performance.now()
    let frame = 0
    const step = (now: number) => {
      const elapsed = Math.min(duration, now - start)
      timing.currentTime = elapsed
      current.current = from + (target - from) * (timing.effect!.getComputedTiming().progress ?? 1)
      setShown(current.current)
      if (elapsed < duration) frame = requestAnimationFrame(step)
    }
    frame = requestAnimationFrame(step)
    return () => {
      cancelAnimationFrame(frame)
      timing.cancel()
    }
  }, [target, reduced])

  return reduced ? target : shown
}
