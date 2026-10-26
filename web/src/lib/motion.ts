import { prefersReducedMotion } from '@/lib/hooks'

export type DurationToken = `--duration-${string}`
export type EasingToken = `--ease-${string}`
export type ScaleToken = `--scale-${string}`
export type TimeToken = DurationToken | '--stagger-row'

/** Token names, never literal values, so a script animation follows motion.css. */
export type Motion = { duration: DurationToken; easing: EasingToken; delay?: TimeToken }

const cache = new Map<string, string>()

/** A token's computed value; empty where no stylesheet defines it, as under jsdom. */
export function readToken(name: string): string {
  const hit = cache.get(name)
  if (hit !== undefined) return hit
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  if (value) cache.set(name, value)
  return value
}

/** A time token in milliseconds; 0 when it is undefined. */
export function time(token: TimeToken): number {
  const value = readToken(token)
  const n = parseFloat(value)
  if (Number.isNaN(n)) return 0
  return value.endsWith('ms') ? n : value.endsWith('s') ? n * 1000 : n
}

export function easing(token: EasingToken): string {
  return readToken(token) || 'linear'
}

export function numberToken(token: ScaleToken | `--opacity-${string}`): number {
  const n = parseFloat(readToken(token))
  return Number.isNaN(n) ? 1 : n
}

export function play(
  el: Element,
  keyframes: Keyframe[],
  motion: Motion,
  options: { extraDelay?: number; fill?: FillMode; composite?: CompositeOperation } = {},
): Animation {
  return el.animate(keyframes, {
    duration: time(motion.duration),
    easing: easing(motion.easing),
    delay: (motion.delay ? time(motion.delay) : 0) + (options.extraDelay ?? 0),
    fill: options.fill ?? 'none',
    composite: options.composite ?? 'replace',
  })
}

export type Entrance = {
  motion: Motion
  /** Rise from this many px below. */
  rise?: number
  /** Start from a blur of `--blur-fade`; for text, never rows. */
  blur?: boolean
  scale?: ScaleToken
  /** Position in a burst: each step waits `--stagger-row`, up to the eighth. */
  index?: number
}

const staggerCap = 8

/** Plays an entrance now and returns its animations; none under reduced motion. */
export function playEntrance(el: Element, entrance: Entrance): Animation[] {
  if (prefersReducedMotion()) return []
  const timing = { extraDelay: Math.min(entrance.index ?? 0, staggerCap) * time('--stagger-row'), fill: 'backwards' as const }
  const animations = [play(el, [{ opacity: 0 }, { opacity: 1 }], entrance.motion, timing)]
  if (entrance.rise) {
    animations.push(
      play(el, [{ translate: `0 ${entrance.rise}px` }, { translate: '0 0' }], entrance.motion, { ...timing, composite: 'add' }),
    )
  }
  if (entrance.scale) {
    animations.push(play(el, [{ scale: String(numberToken(entrance.scale)) }, { scale: '1' }], entrance.motion, timing))
  }
  if (entrance.blur) {
    animations.push(play(el, [{ filter: `blur(${readToken('--blur-fade')})` }, { filter: 'blur(0px)' }], entrance.motion, timing))
  }
  return animations
}
