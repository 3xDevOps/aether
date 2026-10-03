import { useCallback, useEffect, useRef, useState } from 'react'
import type { ControlMetadata, TakeoverAction } from '@/routes/terminal/attach'
import type { TakeoverSnapshot } from '@/routes/terminal/session'

interface TakeoverInput {
  state?: TakeoverSnapshot
  error?: string
  control?: ControlMetadata
  enabled: boolean
  occupied: boolean
  request: (action: TakeoverAction, id: string, generation?: number) => boolean
}

export interface TakeoverInteraction {
  progress?: number
  seconds: number
  phase?: 'holding' | 'review'
  canStart: boolean
  begin: () => boolean
  end: () => void
  cancel: () => void
}

export function useTakeover({ state, error, control, enabled, occupied, request }: TakeoverInput) {
  const active = useRef<{ id: string; confirmed: boolean } | null>(null)
  const [now, setNow] = useState(() => performance.now())
  const [decisionPending, setDecisionPending] = useState(false)
  const requester = enabled && state?.requester_session_id === control?.control_session_id
  const holder = enabled && control?.has_control === true &&
    state?.holder_session_id === control.control_session_id &&
    state?.holder_generation === control.control_generation
  const visible = state && (requester || holder) ? state : undefined
  const deadline = visible ? Date.parse(visible.phase === 'review'
    ? visible.decision_deadline ?? visible.server_now : visible.hold_deadline) : 0
  const remaining = visible
    ? Math.max(0, deadline - Date.parse(visible.server_now) - Math.max(0, now - visible.receivedAt)) : 0
  const duration = visible ? Date.parse(visible.hold_deadline) - Date.parse(visible.hold_started_at) : 5000
  const progress = visible ? visible.phase === 'review' ? 1 : Math.min(1, Math.max(0, 1 - remaining / duration)) : undefined

  const cancel = useCallback(() => {
    const pending = active.current
    active.current = null
    if (pending) request('cancel', pending.id)
  }, [request])

  const end = useCallback(() => {
    if (!active.current?.confirmed) cancel()
  }, [cancel])

  const begin = useCallback(() => {
    if (!enabled || !occupied || control?.has_control || active.current || visible) return false
    const id = crypto.randomUUID()
    if (!request('start', id)) return false
    active.current = { id, confirmed: false }
    return true
  }, [control?.has_control, enabled, occupied, request, visible])

  useEffect(() => {
    if (!visible) return
    if (visible.phase === 'review') {
      setNow(performance.now())
      const timer = window.setInterval(() => setNow(performance.now()), 100)
      return () => window.clearInterval(timer)
    }
    let frame = 0
    const tick = () => {
      setNow(performance.now())
      frame = requestAnimationFrame(tick)
    }
    tick()
    return () => cancelAnimationFrame(frame)
  }, [visible])

  useEffect(() => {
    const pending = active.current
    if (!requester || !state || state.phase !== 'holding' || pending?.id !== state.id || pending.confirmed) return
    const delay = Math.max(0, Date.parse(state.hold_deadline) - Date.parse(state.server_now) -
      (performance.now() - state.receivedAt)) + 40
    const timer = setTimeout(() => {
      if (active.current !== pending) return
      pending.confirmed = request('confirm', pending.id)
      if (!pending.confirmed) cancel()
    }, delay)
    return () => clearTimeout(timer)
  }, [cancel, request, requester, state])

  useEffect(() => {
    setDecisionPending(false)
    if (!state && active.current?.confirmed) active.current = null
  }, [state, error])

  useEffect(() => {
    if (!enabled) cancel()
  }, [cancel, enabled])

  useEffect(() => {
    const onBlur = () => end()
    const onVisibility = () => { if (document.hidden) end() }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && active.current) {
        event.preventDefault()
        event.stopPropagation()
        cancel()
      }
    }
    window.addEventListener('blur', onBlur)
    document.addEventListener('visibilitychange', onVisibility)
    window.addEventListener('keydown', onKey, true)
    return () => {
      window.removeEventListener('blur', onBlur)
      document.removeEventListener('visibilitychange', onVisibility)
      window.removeEventListener('keydown', onKey, true)
      cancel()
    }
  }, [cancel, end])

  const decide = (action: 'accept' | 'deny') => {
    if (!holder || !state || state.phase !== 'review' || decisionPending) return
    if (request(action, state.id, state.holder_generation)) setDecisionPending(true)
  }

  const interaction: TakeoverInteraction = {
    progress,
    seconds: Math.ceil(remaining / 1000),
    phase: visible?.phase === 'review' ? 'review' : visible ? 'holding' : undefined,
    canStart: enabled && occupied && !control?.has_control && !visible,
    begin,
    end,
    cancel,
  }
  return { interaction, holderProgress: holder ? progress : undefined,
    review: holder && visible?.phase === 'review' ? visible : undefined,
    decisionPending, decide }
}
