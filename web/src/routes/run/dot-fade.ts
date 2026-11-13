import { useEffect, type RefObject } from 'react'
import { useReducedMotion } from '@/lib/hooks'

const SVG_NS = 'http://www.w3.org/2000/svg'
const DOT_GAP = 6
const DOT_RADIUS = 0.8
const PART_RADIUS = 42
const WAKE_LENGTH = 84
const WAKE_OVERSHOOT = 1.02
const RING_DURATION = 680
const RING_RADIUS = 46
const CALM_FEATHER = 12
const SPRING = 0.065
const DAMPING = 0.86

type Dot = {
  x: number
  y: number
  dx: number
  dy: number
  vx: number
  vy: number
  burst: number
  neutral: SVGCircleElement
  accent: SVGCircleElement
}

type Point = { x: number; y: number }
type Pointer = Point & { time: number }
type Pulse = Point & { started: number }
type Zone = { left: number; top: number; right: number; bottom: number }

export type DotGridOptions = {
  /** Enables the low-amplitude idle opacity cycle. Off by default. */
  breathing?: boolean
  /** Enables the small idle drift used by touch-first displays. Off by default. */
  drift?: boolean
  /** Scales displacement and wake force from 0 to 1. */
  intensity?: number
}


function clamp(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value))
}

function numberVariable(element: HTMLElement, name: string, fallback: number): number {
  const value = Number.parseFloat(getComputedStyle(element).getPropertyValue(name))
  return Number.isFinite(value) ? value : fallback
}

function enabledVariable(element: HTMLElement, name: string, fallback: boolean): boolean {
  const value = getComputedStyle(element).getPropertyValue(name).trim()
  return value ? Number.parseFloat(value) > 0 : fallback
}

function smoothstep(value: number): number {
  const x = clamp(value, 0, 1)
  return x * x * (3 - 2 * x)
}

/** Interactive dot physics for the multiplayer strip. The static CSS texture remains the rest state. */
export function useDotFade(ref: RefObject<HTMLElement | null>, options?: DotGridOptions): void {
  const reducedMotion = useReducedMotion()
  const breathingOption = options?.breathing
  const driftOption = options?.drift
  const intensityOption = options?.intensity

  useEffect(() => {
    const strip = ref.current
    const matrix = strip?.querySelector<HTMLElement>('.control-matrix')
    const reach = matrix?.querySelector<HTMLElement>('.control-matrix-reach')
    if (!strip || !matrix || !reach) return
    const host = strip

    let frame = 0
    let lastFrame = 0
    let pointer: Pointer | undefined
    let activePointerID: number | undefined
    let pulse: Pulse | undefined
    let dots: Dot[] = []
    let zones: Zone[] = []
    let neutralSVG: SVGSVGElement | undefined
    let accentSVG: SVGSVGElement | undefined
    let ring: SVGCircleElement | undefined
    let width = 0
    let height = 0
    let visible = typeof document === 'undefined' || document.visibilityState !== 'hidden'
    let intersecting = true
    const intensity = clamp(intensityOption ?? numberVariable(matrix, '--dot-intensity', 1), 0, 1)
    const breathing = breathingOption ?? enabledVariable(matrix, '--dot-breathing', false)
    const drift = driftOption ?? enabledVariable(matrix, '--dot-drift', false)

    const canAnimate = () => !reducedMotion && visible && intersecting
    const updateZones = (rect: DOMRect) => {
      zones = []
      for (const element of strip.querySelectorAll<HTMLElement>('.control-matrix-clear')) {
        const zone = element.getBoundingClientRect()
        zones.push({
          left: zone.left - rect.left,
          top: zone.top - rect.top,
          right: zone.right - rect.left,
          bottom: zone.bottom - rect.top,
        })
      }
    }
    const calm = (x: number, y: number): number => {
      let result = 1
      for (const zone of zones) {
        const insideX = x >= zone.left && x <= zone.right
        const insideY = y >= zone.top && y <= zone.bottom
        if (insideX && insideY) return 0.25
        const dx = x < zone.left ? zone.left - x : x > zone.right ? x - zone.right : 0
        const dy = y < zone.top ? zone.top - y : y > zone.bottom ? y - zone.bottom : 0
        const distance = Math.hypot(dx, dy)
        if (distance < CALM_FEATHER) result = Math.min(result, 0.25 + 0.75 * smoothstep(distance / CALM_FEATHER))
      }
      return result
    }
    const cancel = () => {
      if (frame) cancelAnimationFrame(frame)
      frame = 0
      lastFrame = 0
    }
    const destroyGrid = () => {
      cancel()
      neutralSVG?.remove()
      accentSVG?.remove()
      neutralSVG = undefined
      accentSVG = undefined
      ring = undefined
      dots = []
      matrix.removeAttribute('data-dot-grid-active')
    }
    const circle = (className: string, x: number, y: number, radius: number, fill: string): SVGCircleElement => {
      const element = document.createElementNS(SVG_NS, 'circle')
      element.setAttribute('class', className)
      element.setAttribute('cx', String(x))
      element.setAttribute('cy', String(y))
      element.setAttribute('r', String(radius))
      element.setAttribute('fill', fill)
      return element
    }
    const createGrid = () => {
      if (neutralSVG) return
      const rect = strip.getBoundingClientRect()
      width = rect.width || rect.right - rect.left
      height = rect.height || rect.bottom - rect.top
      if (width <= 0 || height <= 0) return
      updateZones(rect)
      neutralSVG = document.createElementNS(SVG_NS, 'svg')
      neutralSVG.setAttribute('class', 'control-matrix-grid')
      neutralSVG.setAttribute('aria-hidden', 'true')
      neutralSVG.setAttribute('viewBox', `0 0 ${width} ${height}`)
      neutralSVG.setAttribute('preserveAspectRatio', 'none')
      accentSVG = document.createElementNS(SVG_NS, 'svg')
      accentSVG.setAttribute('class', 'control-matrix-grid control-matrix-grid-accent')
      accentSVG.setAttribute('aria-hidden', 'true')
      accentSVG.setAttribute('viewBox', `0 0 ${width} ${height}`)
      accentSVG.setAttribute('preserveAspectRatio', 'none')
      for (let y = 3; y < height; y += DOT_GAP) {
        for (let x = 3; x < width; x += DOT_GAP) {
          const neutral = circle('control-matrix-dot', x, y, DOT_RADIUS, 'currentColor')
          const accent = circle('control-matrix-dot', x, y, DOT_RADIUS, 'currentColor')
          neutralSVG.appendChild(neutral)
          accentSVG.appendChild(accent)
          dots.push({ x, y, dx: 0, dy: 0, vx: 0, vy: 0, burst: 0, neutral, accent })
        }
      }
      ring = circle('control-matrix-ring', 0, 0, 0, 'none')
      ring.setAttribute('stroke', 'var(--dot-ring-color, var(--control-light))')
      ring.setAttribute('stroke-width', '1.5')
      ring.setAttribute('vector-effect', 'non-scaling-stroke')
      neutralSVG.appendChild(ring)
      matrix.insertBefore(neutralSVG, matrix.firstChild)
      reach.appendChild(accentSVG)
      matrix.setAttribute('data-dot-grid-active', '')
    }
    const schedule = () => {
      if (!canAnimate() || frame || !neutralSVG) return
      frame = requestAnimationFrame(paint)
    }
    const settle = () => {
      if (!pointer && !pulse && !breathing && !drift) destroyGrid()
    }
    function paint(now: number) {
      frame = 0
      const dt = lastFrame ? clamp((now - lastFrame) / 16.67, 0.25, 2) : 1
      lastFrame = now
      let moving = false
      updateZones(host.getBoundingClientRect())
      const breathingAmount = breathing ? 0.92 + Math.sin(now / 3600) * 0.08 : 1
      if (neutralSVG) neutralSVG.style.opacity = String(breathingAmount)
      if (accentSVG) accentSVG.style.opacity = String(breathingAmount)
      for (const dot of dots) {
        let targetX = 0
        let targetY = 0
        const localCalm = calm(dot.x, dot.y)
        if (pointer) {
          const dx = dot.x - pointer.x
          const dy = dot.y - pointer.y
          const distance = Math.hypot(dx, dy)
          if (distance > 0.01 && distance < PART_RADIUS) {
            const force = (1 - distance / PART_RADIUS) * 13 * intensity * localCalm
            targetX = dx / distance * force
            targetY = dy / distance * force
          }
        }
        dot.vx += (targetX - dot.dx) * SPRING * dt
        dot.vy += (targetY - dot.dy) * SPRING * dt
        dot.vx *= Math.pow(DAMPING, dt)
        dot.vy *= Math.pow(DAMPING, dt)
        dot.dx += dot.vx * dt
        dot.dy += dot.vy * dt
        dot.burst *= Math.pow(0.82, dt)
        if (Math.abs(dot.dx - targetX) + Math.abs(dot.dy - targetY) + Math.abs(dot.vx) + Math.abs(dot.vy) > 0.025) moving = true
        const x = dot.x + dot.dx
        const y = dot.y + dot.dy
        dot.neutral.setAttribute('cx', String(x))
        dot.neutral.setAttribute('cy', String(y))
        dot.accent.setAttribute('cx', String(x))
        dot.accent.setAttribute('cy', String(y))
        dot.neutral.setAttribute('r', String(DOT_RADIUS + dot.burst))
        dot.accent.setAttribute('r', String(DOT_RADIUS + dot.burst))
      }
      if (pulse && !reducedMotion) {
        const progress = clamp((now - pulse.started) / RING_DURATION, 0, 1)
        const eased = 1 - Math.pow(1 - progress, 2)
        ring?.setAttribute('cx', String(pulse.x))
        ring?.setAttribute('cy', String(pulse.y))
        ring?.setAttribute('r', String(5 + eased * RING_RADIUS))
        ring?.setAttribute('opacity', String((1 - progress) * 0.72))
        if (progress >= 1) pulse = undefined
        else moving = true
      }
      if (drift && !pointer) {
        const driftX = Math.sin(now / 2800) * 1.1 * intensity
        const driftY = Math.cos(now / 3300) * 0.7 * intensity
        for (const dot of dots) {
          dot.dx += (driftX - dot.dx) * 0.01
          dot.dy += (driftY - dot.dy) * 0.01
        }
        moving = true
      }
      if (moving || pulse || breathing || drift) schedule()
      else settle()
    }
    const pulseAt = (point: Point) => {
      if (!neutralSVG) createGrid()
      else updateZones(host.getBoundingClientRect())
      pulse = { ...point, started: performance.now() }
      for (const dot of dots) {
        const dx = dot.x - point.x
        const dy = dot.y - point.y
        const distance = Math.hypot(dx, dy)
        if (distance > 0.01 && distance < RING_RADIUS) {
          const force = (1 - distance / RING_RADIUS) * 5.5 * intensity * calm(dot.x, dot.y)
          dot.vx += dx / distance * force
          dot.vy += dy / distance * force
          dot.burst = Math.max(dot.burst, (1 - distance / RING_RADIUS) * 0.7 * intensity)
        }
      }
      if (reducedMotion) {
        ring?.setAttribute('cx', String(point.x))
        ring?.setAttribute('cy', String(point.y))
        ring?.setAttribute('r', '22')
        ring?.setAttribute('opacity', '0.72')
      } else schedule()
    }
    const pointOf = (event: PointerEvent): Point | undefined => {
      const rect = host.getBoundingClientRect()
      updateZones(rect)
      const nextWidth = rect.width || rect.right - rect.left
      const nextHeight = rect.height || rect.bottom - rect.top
      const x = event.clientX - rect.left
      const y = event.clientY - rect.top
      if (x < 0 || y < 0 || x > nextWidth || y > nextHeight) return undefined
      return { x: clamp(x, 0, nextWidth), y: clamp(y, 0, nextHeight) }
    }
    const move = (event: PointerEvent) => {
      if (event.pointerType !== 'mouse' && event.pointerId !== activePointerID) return
      if (!pointer && activePointerID === undefined && event.target instanceof Node && !strip.contains(event.target)) return
      const point = pointOf(event)
      if (!point) {
        if (event.pointerType === 'mouse' && activePointerID === undefined && pointer) {
          pointer = undefined
          schedule()
        }
        return
      }
      const now = performance.now()
      if (pointer) {
        const dx = point.x - pointer.x
        const dy = point.y - pointer.y
        const distance = Math.hypot(dx, dy)
        const elapsed = Math.max(now - pointer.time, 8)
        const speed = clamp(distance / elapsed, 0, 2)
        if (distance > 0.01 && speed > 0) {
          const normalX = -dy / distance
          const normalY = dx / distance
          for (const dot of dots) {
            const along = (dot.x - point.x) * dx / distance + (dot.y - point.y) * dy / distance
            const side = (dot.x - point.x) * normalX + (dot.y - point.y) * normalY
            const trail = -along
            const branchWidth = 7 + trail * 0.2
            if (trail > 0 && trail < WAKE_LENGTH && Math.abs(side) < branchWidth) {
              const wake = speed * (1 - trail / WAKE_LENGTH) * (1 - Math.abs(side) / branchWidth) * intensity * WAKE_OVERSHOOT
              const branch = side < 0 ? -1 : 1
              const factor = calm(dot.x, dot.y)
              dot.vx += normalX * branch * wake * factor
              dot.vy += normalY * branch * wake * factor
            }
          }
        }
      }
      if (!neutralSVG) createGrid()
      pointer = { ...point, time: now }
      schedule()
    }
    const down = (event: PointerEvent) => {
      if (event.pointerType === 'mouse' && event.button !== 0) return
      if (event.target instanceof Node && !strip.contains(event.target)) return
      const point = pointOf(event)
      if (!point) return
      if (event.pointerType !== 'mouse') activePointerID = event.pointerId
      if (!neutralSVG) createGrid()
      pulseAt(point)
      pointer = { ...point, time: performance.now() }
    }
    const up = (event: PointerEvent) => {
      if (activePointerID !== undefined && event.pointerId !== activePointerID) return
      if (event.pointerType !== 'mouse') {
        activePointerID = undefined
        pointer = undefined
      }
      schedule()
    }
    const cancelPointer = (event: PointerEvent) => {
      if (activePointerID !== undefined && event.pointerId !== activePointerID) return
      activePointerID = undefined
      pointer = undefined
      schedule()
    }
    const leave = (event: PointerEvent) => {
      if (event.pointerType === 'mouse' && activePointerID === undefined) {
        pointer = undefined
        schedule()
      }
    }
    const key = (event: KeyboardEvent) => {
      if (event.target !== strip) return
      if (event.key !== 'Enter' && event.key !== ' ' && !event.key.startsWith('Arrow')) return
      event.preventDefault()
      const rect = strip.getBoundingClientRect()
      const nextWidth = rect.width || rect.right - rect.left
      const nextHeight = rect.height || rect.bottom - rect.top
      const current = pointer ?? { x: nextWidth / 2, y: nextHeight / 2 }
      if (event.key.startsWith('Arrow')) {
        const amount = 12
        const next = {
          x: clamp(current.x + (event.key === 'ArrowLeft' ? -amount : event.key === 'ArrowRight' ? amount : 0), 0, nextWidth),
          y: clamp(current.y + (event.key === 'ArrowUp' ? -amount : event.key === 'ArrowDown' ? amount : 0), 0, nextHeight),
        }
        pointer = { ...next, time: performance.now() }
        if (!neutralSVG) createGrid()
      }
      pulseAt(pointer ?? current)
    }
    const blur = (event: FocusEvent) => {
      if (event.target !== strip) return
      pointer = undefined
      schedule()
    }
    const visibility = () => {
      visible = document.visibilityState !== 'hidden'
      if (visible) schedule()
      else cancel()
    }
    const observer = typeof IntersectionObserver === 'undefined' ? undefined : new IntersectionObserver(([entry]) => {
      intersecting = entry?.isIntersecting ?? true
      if (intersecting) schedule()
      else cancel()
    })
    const resize = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(() => {
      const rect = strip.getBoundingClientRect()
      const nextWidth = rect.width || rect.right - rect.left
      const nextHeight = rect.height || rect.bottom - rect.top
      if (nextWidth === width && nextHeight === height) return
      destroyGrid()
      width = nextWidth
      height = nextHeight
      if (pointer || pulse || breathing || drift) {
        createGrid()
        schedule()
      }
    })
    strip.addEventListener('keydown', key)
    strip.addEventListener('blur', blur)
    strip.addEventListener('pointerleave', leave)
    window.addEventListener('pointermove', move, { passive: true })
    window.addEventListener('pointerdown', down, { passive: true })
    window.addEventListener('pointerup', up, { passive: true })
    window.addEventListener('pointercancel', cancelPointer, { passive: true })
    document.addEventListener('visibilitychange', visibility)
    observer?.observe(strip)
    resize?.observe(strip)
    const rect = strip.getBoundingClientRect()
    width = rect.width || rect.right - rect.left
    height = rect.height || rect.bottom - rect.top
    if ((breathing || drift) && !reducedMotion) {
      createGrid()
      schedule()
    }
    return () => {
      strip.removeEventListener('keydown', key)
      strip.removeEventListener('blur', blur)
      strip.removeEventListener('pointerleave', leave)
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerdown', down)
      window.removeEventListener('pointerup', up)
      window.removeEventListener('pointercancel', cancelPointer)
      document.removeEventListener('visibilitychange', visibility)
      observer?.disconnect()
      resize?.disconnect()
      destroyGrid()
    }
  }, [breathingOption, driftOption, intensityOption, reducedMotion, ref])
}
