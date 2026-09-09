import { useCallback, useEffect, useRef, useState } from 'react'

/**
 * Styleguide rule: match in-flight feedback to perceived duration - a spinner
 * that flashes for 60ms is worse than none. True only once `active` has held
 * for `delayMs`.
 */
export function useDelayed(active: boolean, delayMs = 200): boolean {
  const [shown, setShown] = useState(false)
  useEffect(() => {
    if (!active) {
      setShown(false)
      return
    }
    const timer = setTimeout(() => setShown(true), delayMs)
    return () => clearTimeout(timer)
  }, [active, delayMs])
  return active && shown
}

/**
 * The viewport height, tracked. The dock derives its ceiling from it, and a
 * value read once at mount leaves both the clamp and the bound it announces
 * wrong after the window is resized.
 */
export function useWindowHeight(): number {
  const [height, setHeight] = useState(() => window.innerHeight)
  useEffect(() => {
    const onResize = () => setHeight(window.innerHeight)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])
  return height
}

/**
 * Starts a pointer drag. Every listener the caller registers on the returned
 * signal is dropped when the drag ends or when the component goes away, which
 * a `pointerup` handler alone cannot promise.
 */
export function useDrag(): () => AbortController {
  const drag = useRef<AbortController | null>(null)
  useEffect(() => () => drag.current?.abort(), [])
  return useCallback(() => {
    drag.current?.abort()
    drag.current = new AbortController()
    return drag.current
  }, [])
}
