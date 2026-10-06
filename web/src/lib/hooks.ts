import {
  useCallback,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from 'react'

/** True only once `active` has held for `delayMs`, so a fast operation never flashes a spinner. */
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

export function useWindowHeight(): number {
  const [height, setHeight] = useState(() => window.innerHeight)
  useEffect(() => {
    const onResize = () => setHeight(window.innerHeight)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])
  return height
}

/** Listeners registered on the returned signal are dropped when the drag ends or the component unmounts. */
export function useDrag(): () => AbortController {
  const drag = useRef<AbortController | null>(null)
  useEffect(() => () => drag.current?.abort(), [])
  return useCallback(() => {
    drag.current?.abort()
    drag.current = new AbortController()
    return drag.current
  }, [])
}

export const coarsePointer = '(pointer: coarse)'

export const belowSm = '(max-width: 639px)'

/** A finger on a screen narrower than `sm`: a phone, not a touch laptop. */
export const phoneScreen = `${coarsePointer} and ${belowSm}`

/** For layouts that mount different elements for touch; size-only changes belong in CSS. */
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
