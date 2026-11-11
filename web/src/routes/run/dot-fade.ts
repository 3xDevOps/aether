import { useEffect, type RefObject } from 'react'
import { useIsMobile } from '@/lib/breakpoints'
import { useMediaQuery, useReducedMotion } from '@/lib/hooks'

const RADIUS_PX = 54
/** The share of the radius over which opacity climbs back to full; the rest is held at MIN_OPACITY. */
const FALLOFF = 1
const MIN_OPACITY = 0
const RETURN_MS = 1200

export const dotFadeShape = {
  '--dot-radius': `${RADIUS_PX}px`,
  '--dot-inner': `${(1 - FALLOFF) * 100}%`,
  '--dot-min': String(MIN_OPACITY),
  '--dot-return': `${RETURN_MS}ms`,
}

/** Fades the strip's `.control-matrix` dots around a fine pointer; static on touch, phones and reduced motion. */
export function useDotFade(ref: RefObject<HTMLElement | null>): boolean {
  const finePointer = useMediaQuery('(hover: hover) and (pointer: fine)')
  const phoneWidth = useIsMobile()
  const reducedMotion = useReducedMotion()
  const enabled = finePointer && !phoneWidth && !reducedMotion
  useEffect(() => {
    const strip = ref.current
    if (!enabled || !strip) return
    let frame = 0
    let x = 0
    let y = 0
    const paint = () => {
      frame = 0
      const rect = strip.getBoundingClientRect()
      // ControlButton captures the pointer during a hold, so moves keep arriving once it has left the strip.
      const inside = x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom
      strip.style.setProperty('--dot-px', `${x - rect.left}px`)
      strip.style.setProperty('--dot-py', `${y - rect.top}px`)
      strip.style.setProperty('--dot-fade', inside ? '1' : '0')
    }
    const move = (event: PointerEvent) => {
      if (event.pointerType !== 'mouse') return
      x = event.clientX
      y = event.clientY
      if (!frame) frame = requestAnimationFrame(paint)
    }
    const leave = () => {
      cancelAnimationFrame(frame)
      frame = 0
      strip.style.setProperty('--dot-fade', '0')
    }
    strip.addEventListener('pointermove', move)
    strip.addEventListener('pointerleave', leave)
    return () => {
      strip.removeEventListener('pointermove', move)
      strip.removeEventListener('pointerleave', leave)
      cancelAnimationFrame(frame)
      for (const name of ['--dot-px', '--dot-py', '--dot-fade']) strip.style.removeProperty(name)
    }
  }, [enabled, ref])
  return enabled
}
