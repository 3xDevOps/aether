import { useEffect, type RefObject } from 'react'
import { useMediaQuery, useReducedMotion } from '@/lib/hooks'

export const RADIUS_PX = 100
/** The share of the radius over which opacity climbs back to full; the rest is held at MIN_OPACITY. */
export const FALLOFF = 0.75
export const MIN_OPACITY = 0
export const RETURN_MS = 400

export function fadesDots({ finePointer, phoneWidth, reducedMotion }: {
  finePointer: boolean
  phoneWidth: boolean
  reducedMotion: boolean
}): boolean {
  return finePointer && !phoneWidth && !reducedMotion
}

export function dotFadeVars(clientX: number, clientY: number, rect: Pick<DOMRect, 'left' | 'top'>): Record<'--dot-px' | '--dot-py', string> {
  return { '--dot-px': `${clientX - rect.left}px`, '--dot-py': `${clientY - rect.top}px` }
}

export const dotFadeShape = {
  '--dot-radius': `${RADIUS_PX}px`,
  '--dot-inner': `${(1 - FALLOFF) * 100}%`,
  '--dot-min': String(MIN_OPACITY),
  '--dot-return': `${RETURN_MS}ms`,
}

/** Fades the strip's `.control-matrix` dots around a fine pointer; static on touch, phones and reduced motion. */
export function useDotFade(ref: RefObject<HTMLElement | null>): boolean {
  const enabled = fadesDots({
    finePointer: useMediaQuery('(hover: hover) and (pointer: fine)'),
    phoneWidth: useMediaQuery('(max-width: 639px)'),
    reducedMotion: useReducedMotion(),
  })
  useEffect(() => {
    const strip = ref.current
    if (!enabled || !strip) return
    let frame = 0
    let x = 0
    let y = 0
    const paint = () => {
      frame = 0
      for (const [name, value] of Object.entries(dotFadeVars(x, y, strip.getBoundingClientRect()))) strip.style.setProperty(name, value)
      strip.style.setProperty('--dot-fade', '1')
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
      strip.style.removeProperty('--dot-fade')
    }
  }, [enabled, ref])
  return enabled
}
