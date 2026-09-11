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

const coarsePointerQuery = '(pointer: coarse)'
const phoneWidthQuery = '(max-width: 640px)'

/** A media query, tracked, so a rotation or a resize moves what it decides. */
function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(
    () => window.matchMedia?.(query).matches ?? false,
  )
  useEffect(() => {
    const media = window.matchMedia?.(query)
    if (!media) return
    setMatches(media.matches)
    const apply = (event: MediaQueryListEvent) => setMatches(event.matches)
    media.addEventListener('change', apply)
    return () => media.removeEventListener('change', apply)
  }, [query])
  return matches
}

/**
 * Whether a finger is the primary pointer. What a drag or a hover is the only
 * way to reach has to grow a tapped control here; see the touch density
 * section of docs/dashboard-frontend.md.
 */
export function useCoarsePointer(): boolean {
  return useMediaQuery(coarsePointerQuery)
}

/**
 * A phone-sized screen: a finger, or a window no wider than one. Layout
 * decisions that a narrow desktop window shares with a phone read this;
 * decisions about the pointer itself read `useCoarsePointer`.
 */
export function usePhoneScreen(): boolean {
  const coarse = useMediaQuery(coarsePointerQuery)
  const narrow = useMediaQuery(phoneWidthQuery)
  return coarse || narrow
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
