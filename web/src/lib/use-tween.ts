import { useEffect, useRef, useState } from 'react'
import { prefersReducedMotion } from '@/lib/hooks'
import { time } from '@/lib/motion'

/**
 * A number that glides to each new target, starting a retarget from where it
 * is. A reading that is not finite counts as none. The first target shows at once, so nothing counts up on mount, and
 * reduced motion snaps.
 */
export function useTween(reading: number | undefined): number | undefined {
  const target = reading !== undefined && Number.isFinite(reading) ? reading : undefined
  const [shown, setShown] = useState(target)
  const current = useRef(target)
  useEffect(() => {
    const from = current.current
    const duration = time('--duration-glide')
    if (target === undefined || from === undefined || from === target || duration === 0 || prefersReducedMotion()) {
      current.current = target
      setShown(target)
      return
    }
    const start = performance.now()
    let frame = requestAnimationFrame(function step(now) {
      const progress = Math.min(1, (now - start) / duration)
      current.current = from + (target - from) * (1 - (1 - progress) ** 4)
      setShown(current.current)
      if (progress < 1) frame = requestAnimationFrame(step)
    })
    return () => cancelAnimationFrame(frame)
  }, [target])
  return shown
}
