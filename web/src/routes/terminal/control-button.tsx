import { useCallback, useEffect, useRef } from 'react'
import { Button } from '@/components/ui/button'
import type { TakeoverInteraction } from '@/routes/terminal/use-takeover'
import './control-button.css'

export interface ControlButtonProps {
  ownsControl: boolean
  unavailable: boolean
  onTakeControl: () => void
  onReleaseControl: () => void
  takeover?: TakeoverInteraction
}

// A brief click remains an ordinary acquisition. Only a deliberate continuous
// press starts the server's five-second hold; never synthesize a click after it.
const holdIntentMs = 180

export function ControlButton({ ownsControl, unavailable, onTakeControl, onReleaseControl, takeover }: ControlButtonProps) {
  const latest = useRef({ takeover, unavailable, ownsControl, onTakeControl, onReleaseControl })
  const gesture = useRef<{ pointer?: number; key?: string; started: boolean; timer: number } | null>(null)
  latest.current = { takeover, unavailable, ownsControl, onTakeControl, onReleaseControl }
  const suppressClick = useRef(false)
  const finish = useCallback((cancelled: boolean) => {
    const current = gesture.current
    if (!current) return false
    gesture.current = null
    clearTimeout(current.timer)
    suppressClick.current = cancelled || current.started
    if (current.started) latest.current.takeover?.end()
    return !cancelled && !current.started
  }, [])

  const begin = (input: { pointer?: number; key?: string }) => {
    if (gesture.current || unavailable) return
    suppressClick.current = false
    const current = { ...input, started: false, timer: window.setTimeout(() => {
      if (gesture.current !== current || !latest.current.takeover?.canStart) return
      current.started = latest.current.takeover.begin()
      if (current.started) suppressClick.current = true
    }, holdIntentMs) }
    gesture.current = current
  }

  const click = () => {
    if (unavailable || takeover?.phase === 'review' && !ownsControl) return
    if (ownsControl) onReleaseControl()
    else onTakeControl()
  }

  useEffect(() => {
    const cancel = () => { finish(true) }
    const onVisibility = () => { if (document.hidden) cancel() }
    window.addEventListener('blur', cancel)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      window.removeEventListener('blur', cancel)
      document.removeEventListener('visibilitychange', onVisibility)
      cancel()
    }
  }, [finish])

  useEffect(() => {
    if (unavailable) finish(true)
  }, [finish, unavailable])

  const label = ownsControl ? 'Release' : 'Take control'
  const text = takeover?.phase === 'review' && !ownsControl
    ? `Requested · ${takeover.seconds}s`
    : takeover?.phase ? `${label} · ${takeover.seconds}s` : label
  const progress = takeover?.progress
  // A forward-slash leading edge; background and covered text clip separately.
  const edge = (progress ?? 0) * 120 - 20
  const clipPath = `polygon(0 0, ${edge + 20}% 0, ${edge}% 100%, 0 100%)`

  return (
    <Button
      hint={ownsControl ? 'Release terminal control' : takeover?.phase === 'review'
        ? `Waiting for the controller's decision · ${takeover.seconds}s`
        : 'Click to take free control. Hold for 5 seconds to request an occupied terminal. Release early or press Escape to cancel.'}
      type="button"
      size="sm"
      variant={ownsControl ? 'primary' : 'secondary'}
      aria-label={label}
      aria-disabled={unavailable || !ownsControl && takeover?.phase === 'review'}
      className="terminal-control-button shrink-0"
      onPointerDown={(event) => {
        if (event.button !== 0 || !event.isPrimary || unavailable) return
        begin({ pointer: event.pointerId })
        event.currentTarget.setPointerCapture(event.pointerId)
      }}
      onPointerUp={(event) => {
        if (gesture.current?.pointer !== event.pointerId) return
        finish(false)
        if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
      }}
      onPointerMove={(event) => {
        if (gesture.current?.pointer !== event.pointerId) return
        const rect = event.currentTarget.getBoundingClientRect()
        if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) finish(true)
      }}
      onPointerCancel={() => finish(true)}
      onLostPointerCapture={() => finish(true)}
      onContextMenu={(event) => event.preventDefault()}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          if (gesture.current) { event.preventDefault(); event.stopPropagation(); finish(true) }
          takeover?.cancel()
          return
        }
        if (event.key !== ' ' && event.key !== 'Enter') return
        event.preventDefault()
        if (!event.repeat) begin({ key: event.key })
      }}
      onKeyUp={(event) => {
        if (gesture.current?.key !== event.key) return
        event.preventDefault()
        const short = finish(false)
        suppressClick.current = true
        if (short) click()
      }}
      onBlur={() => finish(true)}
      onClick={(event) => {
        if (suppressClick.current) { event.preventDefault(); return }
        click()
      }}
    >
      <span className="terminal-control-label" aria-hidden="true">{text}</span>
      {progress !== undefined && progress > 0 && <>
        <span className="terminal-control-fill" style={{ clipPath }} aria-hidden="true" />
        <span className="terminal-control-covered" style={{ clipPath }} aria-hidden="true">{text}</span>
      </>}
    </Button>
  )
}
