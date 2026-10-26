import {
  useCallback,
  useEffect,
  useLayoutEffect,
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

/** The element's content box in whole CSS pixels; zero until it is laid out. */
export function useElementSize<T extends HTMLElement>(): [(node: T | null) => void, { width: number; height: number }] {
  const [node, setNode] = useState<T | null>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })
  useLayoutEffect(() => {
    if (!node) return
    const measure = (rect: { width: number; height: number }) => {
      const width = Math.floor(rect.width)
      const height = Math.floor(rect.height)
      setSize((previous) => (previous.width === width && previous.height === height ? previous : { width, height }))
    }
    // The observer's first report lands after a paint; a layout chosen from the size must not flash.
    measure(node.getBoundingClientRect())
    const observer = new ResizeObserver(([entry]) => measure(entry.contentRect))
    observer.observe(node)
    return () => observer.disconnect()
  }, [node])
  return [setNode, size]
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

const reducedMotion = '(prefers-reduced-motion: reduce)'

export function useReducedMotion(): boolean {
  return useMediaQuery(reducedMotion)
}

/** For code outside render, such as an animation started from an effect. */
export function prefersReducedMotion(): boolean {
  return window.matchMedia?.(reducedMotion).matches ?? false
}

/** True for `holdMs` after each `trigger`; a new trigger restarts the hold. */
export function useTimedFlag(holdMs: number): [on: boolean, trigger: () => void] {
  const [on, setOn] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const trigger = useCallback(() => {
    clearTimeout(timer.current)
    setOn(true)
    timer.current = setTimeout(() => setOn(false), holdMs)
  }, [holdMs])
  return [on, trigger]
}

let handedOver: HTMLElement | null = null

/** For a dialog that closes as it opens another, like the palette opening New run: the next useReturnFocus dialog returns to `target`. */
export function handOverFocus(target: HTMLElement | null) {
  handedOver = target
}

function openerOf(active: Element | null): HTMLElement | null {
  const menu = active?.closest('[role="menu"]')
  // A menu item is gone once its dialog closes; its menu's trigger is still there.
  const target = handedOver ?? (menu
    ? [...document.querySelectorAll('[aria-controls]')].find((el) => el.getAttribute('aria-controls') === menu.id)
    : active)
  return target instanceof HTMLElement ? target : null
}

/** Focus handlers for a dialog that has no Radix trigger, so closing it returns focus to whatever opened it. */
export function useReturnFocus() {
  // Read while rendering: an autoFocus field inside the dialog takes focus
  // before any effect runs, and Radix then skips onOpenAutoFocus.
  const [atMount] = useState(() => (typeof document === 'undefined' ? null : openerOf(document.activeElement)))
  const opener = useRef(atMount)
  useEffect(() => {
    handedOver = null
  }, [])
  return {
    onOpenAutoFocus: () => {
      opener.current = openerOf(document.activeElement)
      handedOver = null
    },
    onCloseAutoFocus: (event: Event) => {
      if (!opener.current?.isConnected) return
      event.preventDefault()
      opener.current.focus()
    },
  }
}

/** `expires in m:ss` until `until`, then `expired`, ticking each second. */
export function useCountdown(until: string | undefined): string {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!until) return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [until])
  if (!until) return ''
  const left = Math.max(0, Math.floor((Date.parse(until) - now) / 1000))
  return left === 0 ? 'expired' : `expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`
}

function secondsUntil(at: string | undefined): number {
  return at ? Math.max(0, Math.ceil((Date.parse(at) - Date.now()) / 1000)) : 0
}

/** Seconds left until `at`, ticking each second down to zero. */
export function useSecondsUntil(at: string | undefined): number {
  const [remaining, setRemaining] = useState(() => secondsUntil(at))
  useEffect(() => {
    setRemaining(secondsUntil(at))
    if (secondsUntil(at) === 0) return
    const timer = window.setInterval(() => {
      const next = secondsUntil(at)
      setRemaining(next)
      if (next === 0) window.clearInterval(timer)
    }, 1000)
    return () => window.clearInterval(timer)
  }, [at])
  return remaining
}
