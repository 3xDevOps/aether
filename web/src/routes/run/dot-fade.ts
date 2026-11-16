import { useEffect, type RefObject } from 'react'
import { useReducedMotion } from '@/lib/hooks'

const SVG_NS = 'http://www.w3.org/2000/svg'
const DOT_GAP = 6
// The CSS texture's 0.75px dot with a 0.5px feather carries the ink of a 1px circle.
const DOT_RADIUS = 1
const PART_RADIUS = 42
const WAKE_LENGTH = 84
const WAKE_OVERSHOOT = 1.02
const RING_DURATION = 680
const RING_RADIUS = 46
const CALM_FEATHER = 12
const SPRING = 0.065
const DAMPING = 0.86

type Point = { x: number; y: number }
type Layers = [SVGSVGElement, SVGSVGElement, SVGSVGElement]
type Dot = Point & {
  dx: number
  dy: number
  vx: number
  vy: number
  burst: number
  circles: SVGCircleElement[]
}
type Zone = { left: number; top: number; right: number; bottom: number }

function clamp(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value))
}

/**
 * Springs SVG copies of the strip's `.control-matrix` dots away from the pointer; the static
 * CSS texture is the rest state. Reduced motion leaves the texture still.
 */
export function useDotFade(ref: RefObject<HTMLElement | null>) {
  const reducedMotion = useReducedMotion()

  useEffect(() => {
    const strip = ref.current
    const matrix = strip?.querySelector<HTMLElement>('.control-matrix')
    const light = matrix?.querySelector<HTMLElement>('.control-matrix-light')
    const reach = light?.querySelector<HTMLElement>('.control-matrix-reach')
    if (reducedMotion || !strip || !matrix || !light || !reach) return

    let frame = 0
    let lastFrame = 0
    let client: Point | undefined
    let tap: Point | undefined
    let pointer: (Point & { time: number }) | undefined
    let activePointerID: number | undefined
    let pulse: (Point & { started: number }) | undefined
    let layers: Layers | undefined
    let ring: SVGCircleElement | undefined
    let dots: Dot[] = []
    let zones: Zone[] = []

    const calm = (x: number, y: number): number => {
      let result = 1
      for (const zone of zones) {
        const dx = x < zone.left ? zone.left - x : x > zone.right ? x - zone.right : 0
        const dy = y < zone.top ? zone.top - y : y > zone.bottom ? y - zone.bottom : 0
        const t = Math.min(Math.hypot(dx, dy) / CALM_FEATHER, 1)
        result = Math.min(result, 0.25 + 0.75 * t * t * (3 - 2 * t))
      }
      return result
    }
    // ControlMatrix mounts its red hold layer only while someone holds to take over.
    const attachHold = () => {
      const hold = light.querySelector('.control-matrix-hold')
      if (layers && hold && layers[2].parentNode !== hold) hold.append(layers[2])
    }
    const build = (width: number, height: number) => {
      const layer = () => {
        const svg = document.createElementNS(SVG_NS, 'svg')
        svg.setAttribute('class', 'control-matrix-grid')
        return svg
      }
      const built: Layers = [layer(), layer(), layer()]
      for (let y = DOT_GAP / 2; y < height; y += DOT_GAP) {
        for (let x = DOT_GAP / 2; x < width; x += DOT_GAP) {
          const circles = built.map((svg) => {
            const circle = svg.appendChild(document.createElementNS(SVG_NS, 'circle'))
            circle.setAttribute('cx', String(x))
            circle.setAttribute('cy', String(y))
            circle.setAttribute('r', String(DOT_RADIUS))
            return circle
          })
          dots.push({ x, y, dx: 0, dy: 0, vx: 0, vy: 0, burst: 0, circles })
        }
      }
      ring = built[0].appendChild(document.createElementNS(SVG_NS, 'circle'))
      ring.setAttribute('class', 'control-matrix-ring')
      matrix.prepend(built[0])
      reach.append(built[1])
      layers = built
      attachHold()
      matrix.setAttribute('data-dot-grid-active', '')
    }
    const destroy = () => {
      cancelAnimationFrame(frame)
      frame = 0
      lastFrame = 0
      layers?.forEach((svg) => svg.remove())
      layers = undefined
      ring = undefined
      dots = []
      matrix.removeAttribute('data-dot-grid-active')
    }
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(paint)
    }
    const paint = (now: number) => {
      frame = 0
      if (!layers && !client && !tap && !pointer) return
      const rect = matrix.getBoundingClientRect()
      if (!rect.width || !rect.height) return
      if (!layers) build(rect.width, rect.height)
      const dt = lastFrame ? clamp((now - lastFrame) / 16.67, 0.25, 2) : 1
      lastFrame = now
      zones = [...strip.querySelectorAll('.control-matrix-clear')].map((element) => {
        const box = element.getBoundingClientRect()
        return { left: box.left - rect.left, top: box.top - rect.top, right: box.right - rect.left, bottom: box.bottom - rect.top }
      })
      if (tap) {
        pulse = { x: tap.x - rect.left, y: tap.y - rect.top, started: now }
        tap = undefined
        for (const dot of dots) {
          const dx = dot.x - pulse.x
          const dy = dot.y - pulse.y
          const distance = Math.hypot(dx, dy)
          if (distance > 0.01 && distance < RING_RADIUS) {
            const force = (1 - distance / RING_RADIUS) * 5.5 * calm(dot.x, dot.y)
            dot.vx += dx / distance * force
            dot.vy += dy / distance * force
            dot.burst = Math.max(dot.burst, (1 - distance / RING_RADIUS) * 0.7)
          }
        }
      }
      if (client) {
        const point = { x: client.x - rect.left, y: client.y - rect.top }
        client = undefined
        if (point.x < 0 || point.y < 0 || point.x > rect.width || point.y > rect.height) pointer = undefined
        else {
          const distance = pointer ? Math.hypot(point.x - pointer.x, point.y - pointer.y) : 0
          if (pointer && distance > 0.01) {
            const speed = clamp(distance / Math.max(now - pointer.time, 8), 0, 2)
            const alongX = (point.x - pointer.x) / distance
            const alongY = (point.y - pointer.y) / distance
            for (const dot of dots) {
              const trail = -((dot.x - point.x) * alongX + (dot.y - point.y) * alongY)
              const side = (dot.x - point.x) * -alongY + (dot.y - point.y) * alongX
              const branchWidth = 7 + trail * 0.2
              if (trail > 0 && trail < WAKE_LENGTH && Math.abs(side) < branchWidth) {
                const wake = speed * (1 - trail / WAKE_LENGTH) * (1 - Math.abs(side) / branchWidth) * WAKE_OVERSHOOT
                  * (side < 0 ? -1 : 1) * calm(dot.x, dot.y)
                dot.vx += -alongY * wake
                dot.vy += alongX * wake
              }
            }
          }
          pointer = { ...point, time: now }
        }
      }
      let moving = false
      for (const dot of dots) {
        let targetX = 0
        let targetY = 0
        if (pointer) {
          const dx = dot.x - pointer.x
          const dy = dot.y - pointer.y
          const distance = Math.hypot(dx, dy)
          if (distance > 0.01 && distance < PART_RADIUS) {
            const force = (1 - distance / PART_RADIUS) * 13 * calm(dot.x, dot.y)
            targetX = dx / distance * force
            targetY = dy / distance * force
          }
        }
        // Dots at rest keep the position they were last drawn at, so a frame writes only the dots in motion.
        if (!targetX && !targetY && !dot.dx && !dot.dy && !dot.vx && !dot.vy && !dot.burst) continue
        dot.vx = (dot.vx + (targetX - dot.dx) * SPRING * dt) * Math.pow(DAMPING, dt)
        dot.vy = (dot.vy + (targetY - dot.dy) * SPRING * dt) * Math.pow(DAMPING, dt)
        dot.dx += dot.vx * dt
        dot.dy += dot.vy * dt
        dot.burst *= Math.pow(0.82, dt)
        const restless = Math.abs(dot.dx - targetX) + Math.abs(dot.dy - targetY) + Math.abs(dot.vx) + Math.abs(dot.vy) + dot.burst
        if (restless > 0.025) moving = true
        else if (!targetX && !targetY) Object.assign(dot, { dx: 0, dy: 0, vx: 0, vy: 0, burst: 0 })
        for (const circle of dot.circles) {
          circle.setAttribute('cx', String(dot.x + dot.dx))
          circle.setAttribute('cy', String(dot.y + dot.dy))
          circle.setAttribute('r', String(DOT_RADIUS + dot.burst))
        }
      }
      if (pulse && ring) {
        const progress = clamp((now - pulse.started) / RING_DURATION, 0, 1)
        ring.setAttribute('cx', String(pulse.x))
        ring.setAttribute('cy', String(pulse.y))
        ring.setAttribute('r', String(5 + (1 - Math.pow(1 - progress, 2)) * RING_RADIUS))
        ring.setAttribute('opacity', String((1 - progress) * 0.72))
        if (progress < 1) moving = true
        else pulse = undefined
      }
      if (moving) schedule()
      else if (pointer) lastFrame = 0
      else destroy()
    }

    const move = (event: PointerEvent) => {
      if (event.pointerType !== 'mouse' && event.pointerId !== activePointerID) return
      client = { x: event.clientX, y: event.clientY }
      schedule()
    }
    const down = (event: PointerEvent) => {
      if (event.pointerType === 'mouse' && event.button !== 0) return
      if (event.pointerType !== 'mouse') activePointerID = event.pointerId
      client = tap = { x: event.clientX, y: event.clientY }
      schedule()
    }
    const end = (event: PointerEvent) => {
      if (event.pointerId !== activePointerID) return
      activePointerID = undefined
      client = undefined
      pointer = undefined
      schedule()
    }
    const leave = (event: PointerEvent) => {
      if (event.pointerType !== 'mouse') return
      client = undefined
      pointer = undefined
      schedule()
    }

    // Moves are read on the strip itself: ControlButton captures the pointer during a hold, and a
    // touch is captured by the element it started on, so a drag's moves still arrive here.
    const listening = new AbortController()
    const { signal } = listening
    strip.addEventListener('pointermove', move, { signal })
    strip.addEventListener('pointerdown', down, { signal })
    strip.addEventListener('pointerup', end, { signal })
    strip.addEventListener('pointercancel', end, { signal })
    strip.addEventListener('pointerleave', leave, { signal })
    const holdObserver = new MutationObserver(attachHold)
    holdObserver.observe(light, { childList: true })
    const resize = new ResizeObserver(() => {
      if (!layers) return
      destroy()
      schedule()
    })
    resize.observe(matrix)
    return () => {
      listening.abort()
      holdObserver.disconnect()
      resize.disconnect()
      destroy()
    }
  }, [reducedMotion, ref])
}
