import { useLayoutEffect, useRef, useState } from 'react'
import { ChevronLeft, ChevronRight } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { glideHeight } from '@/lib/flip'
import { prefersReducedMotion } from '@/lib/hooks'
import { type Motion, play, playEntrance } from '@/lib/motion'
import type { SessionRequest } from '@/lib/session-types'
import { cn } from '@/lib/utils'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { SessionRequestCard } from '@/routes/run/session-requests'
import type { RunRecord } from '@/store/runs'

export const dockedRequestID = 'session-request-docked'

const fadeOut: Motion = { duration: '--duration-200', easing: '--ease-standard' }
const fadeIn: Motion = { duration: '--duration-250', easing: '--ease-standard' }
const glide: Motion = { duration: '--duration-glide', easing: '--ease-glide' }

type Shown = { request?: SessionRequest; enter: boolean; from?: number }

/**
 * The open run's requests, one at a time. The card on screen is held by id,
 * so an arrival or another answer never swaps it. A request that leaves
 * `pending` fades out where it stood before the next one, if any, glides in:
 * the exit follows the server, never the click, so a refused answer keeps its
 * card. Until `caughtUp` the dock stays empty, so a run opened with requests
 * waiting shows them without an entrance.
 */
export function RequestDock({ run, requests, caughtUp, agent, className }: {
  run: RunRecord
  requests: SessionRequest[]
  caughtUp: boolean
  agent: AgentTerminal
  className?: string
}) {
  const [selected, setSelected] = useState<{ id?: string; index: number }>({ index: 0 })
  const found = requests.findIndex((request) => request.id === selected.id)
  const current = found >= 0 ? found : Math.min(selected.index, requests.length - 1)
  const target = requests[current]
  if (target?.id !== selected.id || (target && current !== selected.index)) {
    setSelected(target ? { id: target.id, index: current } : { index: 0 })
  }
  const [ready, setReady] = useState(caughtUp)
  const [shown, setShown] = useState<Shown>({ request: caughtUp ? target : undefined, enter: false })
  if (!ready && caughtUp) {
    setReady(true)
    setShown({ request: target, enter: false })
  }
  const live = shown.request && requests.find((request) => request.id === shown.request!.id)
  const leaving = shown.request !== undefined && !live
  if (ready && !leaving && shown.request?.id !== target?.id) {
    setShown({ request: target, enter: target !== undefined, from: shown.request ? undefined : 0 })
  }
  const latest = useRef(target)
  latest.current = target
  const box = useRef<HTMLDivElement>(null)
  const content = useRef<HTMLDivElement>(null)

  useLayoutEffect(() => {
    const el = content.current
    const frame = box.current
    if (!leaving || !el || !frame) return
    if (document.activeElement instanceof HTMLElement && el.contains(document.activeElement)) document.activeElement.blur()
    const height = frame.getBoundingClientRect().height
    const settle = () => {
      const next = latest.current
      setShown({ request: next, enter: next !== undefined, from: next ? height : undefined })
    }
    if (prefersReducedMotion()) {
      settle()
      return
    }
    const animations = [play(el, [{ opacity: 1 }, { opacity: 0 }], fadeOut, { fill: 'forwards' })]
    if (!latest.current) {
      frame.style.overflow = 'clip'
      animations.push(play(frame, [{ height: `${height}px` }, { height: '0px' }], glide, { fill: 'forwards' }))
    }
    let cancelled = false
    void Promise.all(animations.map((animation) => animation.finished)).then(() => {
      if (!cancelled) settle()
    }, () => {})
    return () => {
      cancelled = true
      frame.style.overflow = ''
      animations.forEach((animation) => animation.cancel())
    }
  }, [leaving, shown.request?.id])

  useLayoutEffect(() => {
    const el = content.current
    const frame = box.current
    if (!shown.enter || !el || !frame) return
    if (shown.from !== undefined) glideHeight(frame, shown.from, glide)
    const animations = playEntrance(el, { motion: fadeIn })
    return () => animations.forEach((animation) => animation.cancel())
  }, [shown])

  const request = live ?? shown.request
  if (!ready || !request) return null
  const position = leaving ? -1 : current
  const step = (by: number) => {
    const index = (position + by + requests.length) % requests.length
    setSelected({ id: requests[index]!.id, index })
  }
  return (
    <div ref={box} className={cn('max-h-[50dvh] overflow-y-auto rounded-panel border border-seam bg-raised', className)}>
      <div ref={content} key={request.id} inert={leaving || undefined}>
        <SessionRequestCard
          id={dockedRequestID}
          className="rounded-none border-0"
          run={run}
          request={request}
          agent={agent}
          meta={requests.length > 1 && position >= 0 && (
            <>
              <Button variant="ghost" size="icon-sm" label="Previous request" onClick={() => step(-1)}><ChevronLeft /></Button>
              <span>{position + 1} of {requests.length}</span>
              <Button variant="ghost" size="icon-sm" label="Next request" onClick={() => step(1)}><ChevronRight /></Button>
            </>
          )}
        />
      </div>
    </div>
  )
}
