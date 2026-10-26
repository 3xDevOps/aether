import * as React from 'react'
import { prefersReducedMotion } from '@/lib/hooks'
import { type Entrance, type Motion, play, playEntrance } from '@/lib/motion'
import { cn } from '@/lib/utils'

type Layer = { id: number; content: React.ReactNode; phase: 'rest' | 'in' | 'out' }

const swapIn: Entrance = { motion: { duration: '--duration-200', easing: '--ease-enter' }, blur: true }
const swapOut: Motion = { duration: '--duration-200', easing: '--ease-standard' }

/**
 * Replaces one text with another in the same box. `replace` drops the old
 * copy at once and fades the new one in from a blur; `crossfade` also fades
 * the old copy out underneath; `cut` swaps with no motion, as reduced motion
 * always does. The swap runs when `swapKey` changes (default: the text
 * itself), so key it by the state word, never by a ticking value. Only the
 * incoming copy is exposed to assistive technology or takes input.
 */
export function TextSwap({
  children,
  swapKey,
  mode = 'replace',
  className,
  layerClassName,
}: {
  children: React.ReactNode
  swapKey?: string | number
  mode?: 'cut' | 'replace' | 'crossfade'
  className?: string
  layerClassName?: string
}) {
  const key = swapKey ?? (typeof children === 'string' || typeof children === 'number' ? children : undefined)
  const [layers, setLayers] = React.useState<Layer[]>([{ id: 0, content: children, phase: 'rest' }])
  const shownKey = React.useRef(key)
  const nextId = React.useRef(1)
  const nodes = React.useRef(new Map<number, HTMLElement>())

  React.useLayoutEffect(() => {
    if (key === shownKey.current) {
      setLayers((prev) => {
        const top = prev[prev.length - 1]
        return top.content === children ? prev : [...prev.slice(0, -1), { ...top, content: children }]
      })
      return
    }
    shownKey.current = key
    const id = nextId.current++
    const style = prefersReducedMotion() ? 'cut' : mode
    if (style === 'crossfade') {
      setLayers((prev) => [...prev.filter((l) => l.phase !== 'out').map((l) => ({ ...l, phase: 'out' as const })), { id, content: children, phase: 'in' }])
    } else {
      setLayers([{ id, content: children, phase: style === 'cut' ? 'rest' : 'in' }])
    }
  }, [key, children, mode])

  const started = React.useRef(new Set<string>())
  React.useLayoutEffect(() => {
    const live = new Set(layers.map((l) => `${l.id}:${l.phase}`))
    for (const tag of started.current) if (!live.has(tag)) started.current.delete(tag)
    const leaving: Promise<unknown>[] = []
    for (const layer of layers) {
      const tag = `${layer.id}:${layer.phase}`
      const el = nodes.current.get(layer.id)
      if (!el || layer.phase === 'rest' || started.current.has(tag)) continue
      started.current.add(tag)
      if (layer.phase === 'in') playEntrance(el, swapIn)
      else leaving.push(play(el, [{ opacity: 1 }, { opacity: 0 }], swapOut, { fill: 'forwards' }).finished)
    }
    if (leaving.length === 0) return
    const gone = new Set(layers.filter((l) => l.phase === 'out').map((l) => l.id))
    void Promise.allSettled(leaving).then(() => setLayers((prev) => prev.filter((l) => !gone.has(l.id))))
  }, [layers])

  return (
    <span data-slot="swap" className={cn('inline-grid', className)}>
      {layers.map((layer) => (
        <span
          key={layer.id}
          ref={(el) => {
            if (el) nodes.current.set(layer.id, el)
            else nodes.current.delete(layer.id)
          }}
          aria-hidden={layer.phase === 'out' || undefined}
          inert={layer.phase === 'out'}
          className={cn('col-start-1 row-start-1', layerClassName)}
        >
          {layer.content}
        </span>
      ))}
    </span>
  )
}

/** Icon `from`, or `to` while `swapped`; the caller swaps the accessible name for the hold. */
export function IconSwap({
  swapped,
  from,
  to,
  className,
}: {
  swapped: boolean
  from: React.ReactNode
  to: React.ReactNode
  className?: string
}) {
  return (
    <TextSwap swapKey={swapped ? 'to' : 'from'} mode="crossfade" className={className} layerClassName="inline-flex items-center justify-center">
      {swapped ? to : from}
    </TextSwap>
  )
}
