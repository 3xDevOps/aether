import {
  useCallback,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from 'react'

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

/** The CSS variant of the same name, asked from JavaScript. */
export const coarsePointer = '(pointer: coarse)'

/**
 * Tailwind's own breakpoints, asked from JavaScript. `sm` starts at 640px
 * and `md` at 768px, so being below one is being a pixel short of it. Every
 * JavaScript branch reads the edge from here, so a layout that stacks in CSS
 * and a layout that stacks in JavaScript cannot disagree about where.
 */
export const belowSm = '(max-width: 639px)'
export const belowMd = '(max-width: 767px)'

/** A finger on a screen narrower than `sm`: a phone, not a touch laptop. */
export const phoneScreen = `${coarsePointer} and ${belowSm}`

/**
 * Answers a media query, and keeps answering it. CSS is where a layout that
 * only changes size belongs; this is for the ones that mount different
 * elements for a finger than for a mouse, which a class cannot express.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const media = window.matchMedia?.(query)
      if (!media) return () => {}
      media.addEventListener('change', onChange)
      return () => media.removeEventListener('change', onChange)
    },
    [query],
  )
  return useSyncExternalStore(
    subscribe,
    () => window.matchMedia?.(query).matches ?? false,
  )
}
