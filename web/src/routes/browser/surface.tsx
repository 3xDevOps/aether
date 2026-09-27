import { useCallback, useEffect, useRef, useState } from 'react'
import type { KeyboardEvent, PointerEvent as ReactPointerEvent } from 'react'
import { Button } from '@/components/ui/button'
import { api, browserStreamURL } from '@/lib/api'
import { message } from '@/lib/format'
import type { DevBrowserActionParams, DevBrowserFrameMetadata, DevBrowserPage, DevControlFence, DevStreamResponse } from '@/lib/types'
import { decodeBrowserFrame, framePoint, sameFrameTarget, type BrowserFrame } from './frames'

interface BrowserSurfaceProps {
  runID: string
  page: DevBrowserPage
  control: DevControlFence | null
  connection: number
  expanded: boolean
  onExpandedChange: (expanded: boolean) => void
  onError: (error: string) => void
  onPage: (page: DevBrowserPage) => void
}
interface PendingInput { request: DevBrowserActionParams; frame: DevBrowserFrameMetadata; time: number; epoch: number }
type BrowserInput = Omit<DevBrowserActionParams, 'run_id' | 'session_id' | 'page_id' | 'page_revision' | 'control_session_id' | 'control_generation'>
const inputSentinel = '\u200b'
interface PointerGesture {
  kind: string
  button: string
  touchID: number
  clickCount: number
  modifiers: string[]
  frame: DevBrowserFrameMetadata
  control: DevControlFence
  point: { x: number; y: number }
}

export function BrowserSurface(props: BrowserSurfaceProps) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const keyboard = useRef<HTMLTextAreaElement>(null)
  const current = useRef(props)
  current.current = props
  const displayed = useRef<DevBrowserFrameMetadata | null>(null)
  const queue = useRef<PendingInput[]>([])
  const sending = useRef(false)
  const active = useRef(true)
  const inputEpoch = useRef(0)
  const sendInput = useRef<(input: BrowserInput) => void>(() => {})
  const composing = useRef(false)
  const compositionTarget = useRef<{ frame: DevBrowserFrameMetadata; control: DevControlFence } | null>(null)
  const pointers = useRef(new Map<number, PointerGesture>())
  const lastClick = useRef({ time: 0, x: 0, y: 0, button: -1, count: 0 })
  const [live, setLive] = useState(false)
  const [dimensions, setDimensions] = useState('')

  const clearInput = useCallback(() => {
    inputEpoch.current++
    queue.current = []
    pointers.current.clear()
    compositionTarget.current = null
    composing.current = false
    if (keyboard.current) {
      keyboard.current.value = inputSentinel
      keyboard.current.setSelectionRange(1, 1)
    }
  }, [])
  useEffect(() => {
    clearInput()
  }, [props.control?.control_session_id, props.control?.control_generation, clearInput])
  useEffect(() => {
    const visibility = () => { if (document.hidden) clearInput() }
    window.addEventListener('blur', clearInput)
    document.addEventListener('visibilitychange', visibility)
    return () => {
      window.removeEventListener('blur', clearInput)
      document.removeEventListener('visibilitychange', visibility)
    }
  }, [clearInput])
  useEffect(() => {
    active.current = true
    clearInput()
    let disposed = false
    let acknowledged = false
    let failed = false
    let latest: BrowserFrame | null = null
    let decoding = false
    let sequence = 0
    const target = { run_id: props.runID, session_id: props.page.session_id, page_id: props.page.page_id, page_revision: props.page.page_revision }
    const socket = new WebSocket(browserStreamURL(target))
    socket.binaryType = 'arraybuffer'
    const clear = () => {
      displayed.current = null
      clearInput()
      canvas.current?.getContext('2d')?.clearRect(0, 0, canvas.current.width, canvas.current.height)
      setLive(false)
    }
    clear()
    const fail = (text: string) => {
      if (disposed || failed) return
      failed = true
      clear()
      current.current.onError(text)
    }
    const paint = async () => {
      if (decoding || disposed || failed) return
      decoding = true
      try {
        while (latest && !disposed) {
          const frame = latest
          latest = null
          const bitmap = await createImageBitmap(frame.image)
          try {
            if (disposed || failed) return
            // Keep one pending compressed image and one decode, but paint
            // completed decodes even when the producer is faster than us.
            const element = canvas.current
            const context = element?.getContext('2d')
            const observed = current.current.page
            if (!element || !context || frame.metadata.page_revision < observed.page_revision ||
              (frame.metadata.page_revision === observed.page_revision && frame.metadata.viewport_id !== observed.viewport_id)) continue
            element.width = frame.metadata.width
            element.height = frame.metadata.height
            context.drawImage(bitmap, 0, 0, element.width, element.height)
            displayed.current = frame.metadata
            setDimensions(`${frame.metadata.width} × ${frame.metadata.height}`)
            setLive(true)
          } finally { bitmap.close() }
        }
      } catch (cause) {
        fail(`Browser image failed: ${message(cause)}`)
        socket.close()
      } finally { decoding = false }
    }
    socket.onopen = () => socket.send(JSON.stringify(target))
    socket.onmessage = (event: MessageEvent<unknown>) => {
      try {
        if (typeof event.data === 'string') {
          const response = JSON.parse(event.data) as DevStreamResponse
          if (!response.ok) throw new Error(response.error || 'Browser stream refused')
          acknowledged = true
          return
        }
        if (!acknowledged || !(event.data instanceof ArrayBuffer)) throw new Error('Unexpected browser stream message')
        const frame = decodeBrowserFrame(event.data)
        if (frame.metadata.run_id !== target.run_id || frame.metadata.session_id !== target.session_id || frame.metadata.page_id !== target.page_id) throw new Error('Browser frame identity changed')
        if (frame.metadata.sequence <= sequence) return
        sequence = frame.metadata.sequence
        latest = frame
        void paint()
      } catch (cause) {
        fail(message(cause))
        socket.close()
      }
    }
    socket.onerror = () => fail('Browser stream connection failed. Reconnect to observe the surviving session.')
    socket.onclose = (event) => fail(`Browser stream ended${event.reason ? `: ${event.reason}` : ''}. Reconnect does not open or reset a session.`)
    return () => {
      disposed = true
      active.current = false
      latest = null
      displayed.current = null
      clearInput()
      socket.close()
    }
    // Revisions arrive in the stream; they are not new observation sessions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.runID, props.page.session_id, props.page.page_id, props.connection])

  const send = (input: BrowserInput) => {
    const frame = displayed.current
    const control = current.current.control
    if (!frame || !control || !active.current) return
    const observed = current.current.page
    if (frame.page_revision < observed.page_revision || (frame.page_revision === observed.page_revision && frame.viewport_id !== observed.viewport_id)) {
      current.current.onError('Input discarded: wait for the current page frame.')
      return
    }
    const entry: PendingInput = {
      request: { ...input, run_id: frame.run_id, session_id: frame.session_id, page_id: frame.page_id, page_revision: frame.page_revision, viewport_id: frame.viewport_id, ...control },
      frame,
      time: performance.now(),
      epoch: inputEpoch.current,
    }
    const previous = queue.current.at(-1)
    if (input.phase === 'move' && previous?.request.action === input.action && previous.request.phase === 'move' && previous.request.touch_id === input.touch_id) queue.current[queue.current.length - 1] = entry
    else if (queue.current.length < 32) queue.current.push(entry)
    else {
      queue.current = []
      current.current.onError('Input discarded: browser input is congested. Release control before continuing.')
      return
    }
    if (sending.current) return
    sending.current = true
    void (async () => {
      try {
        while (active.current && queue.current.length) {
          const next = queue.current.shift()!
          if (next.epoch !== inputEpoch.current) continue
          const now = current.current
          const visible = displayed.current
          const observedChanged = now.page.session_id !== next.frame.session_id || now.page.page_id !== next.frame.page_id || now.page.page_revision > next.frame.page_revision || (now.page.page_revision === next.frame.page_revision && now.page.viewport_id !== next.frame.viewport_id)
          if (!visible || !sameFrameTarget(visible, next.frame) || observedChanged || !now.control || now.control.control_session_id !== next.request.control_session_id || now.control.control_generation !== next.request.control_generation || performance.now() - next.time > 1000) {
            clearInput()
            now.onError('Input discarded: the displayed frame or control changed, or input expired. Nothing was replayed.')
            break
          }
          try {
            const result = await api.devBrowserAction(next.request)
            if (active.current && next.epoch === inputEpoch.current) current.current.onPage(result.page)
          } catch (cause) {
            if (active.current && next.epoch === inputEpoch.current) {
              clearInput()
              current.current.onError(`Input rejected: ${message(cause)}`)
            }
          }
        }
      } finally { sending.current = false }
    })()
  }
  sendInput.current = send

  useEffect(() => {
    const element = canvas.current
    if (!element) return
    const wheel = (event: WheelEvent) => {
      const frame = displayed.current
      if (!current.current.control || !frame) return
      event.preventDefault()
      const point = framePoint(frame, element.getBoundingClientRect(), event.clientX, event.clientY)
      if (!point) return
      const factor = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? frame.height : 1
      sendInput.current({ action: 'scroll', ...point, delta_x: Math.max(-10000, Math.min(10000, event.deltaX * factor)), delta_y: Math.max(-10000, Math.min(10000, event.deltaY * factor)) })
    }
    element.addEventListener('wheel', wheel, { passive: false })
    return () => element.removeEventListener('wheel', wheel)
  }, [])
  useEffect(() => {
    const element = keyboard.current
    if (!element) return
    // React's text-input fallback does not deliver every mobile deletion;
    // listen to the actual beforeinput event and keep a deletable sentinel.
    const beforeInput = (event: InputEvent) => {
      if (composing.current || event.isComposing) return
      const type = event.inputType
      const backward = type === 'deleteContentBackward' || type === 'deleteWordBackward'
      const forward = type === 'deleteContentForward' || type === 'deleteWordForward'
      const enter = type === 'insertLineBreak' || type === 'insertParagraph'
      if (!backward && !forward && !enter) return
      event.preventDefault()
      sendInput.current({ action: 'key', key: enter ? 'Enter' : backward ? 'Backspace' : 'Delete', modifiers: type.startsWith('deleteWord') ? ['Control'] : undefined })
    }
    element.addEventListener('beforeinput', beforeInput)
    return () => element.removeEventListener('beforeinput', beforeInput)
  }, [])

  const onKey = (event: KeyboardEvent<HTMLElement>) => {
    if (!props.control || event.nativeEvent.isComposing || composing.current || event.key === 'Process' || event.key === 'Unidentified' || event.key === 'Dead') return
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'v') return
    event.preventDefault()
    if (['Shift', 'Control', 'Alt', 'Meta'].includes(event.key)) return
    send({ action: 'key', key: event.key === ' ' ? 'Space' : event.key, modifiers: [event.altKey && 'Alt', event.ctrlKey && 'Control', event.metaKey && 'Meta', event.shiftKey && 'Shift'].filter((value): value is string => Boolean(value)) })
  }
  const onPointer = (event: ReactPointerEvent<HTMLCanvasElement>, phase: 'down' | 'move' | 'up' | 'cancel') => {
    if (!props.control || !displayed.current) return
    const existing = pointers.current.get(event.pointerId)
    if (existing && (!sameFrameTarget(existing.frame, displayed.current) || existing.control.control_session_id !== props.control.control_session_id || existing.control.control_generation !== props.control.control_generation)) {
      pointers.current.delete(event.pointerId)
      current.current.onError('Gesture discarded: the displayed page or control changed.')
      return
    }
    const point = framePoint(displayed.current, event.currentTarget.getBoundingClientRect(), event.clientX, event.clientY)
    if (!point && phase !== 'up' && phase !== 'cancel') return
    event.preventDefault()
    const coordinates = point ?? existing?.point
    if (!coordinates) return
    if (phase === 'down') {
      const used = new Set([...pointers.current.values()].map((gesture) => gesture.touchID))
      const touchID = Array.from({ length: 10 }, (_, index) => index + 1).find((id) => !used.has(id))
      if (touchID === undefined) {
        current.current.onError('At most ten simultaneous touch contacts are supported.')
        return
      }
      if (event.pointerType !== 'touch') keyboard.current?.focus({ preventScroll: true })
      event.currentTarget.setPointerCapture(event.pointerId)
      const modifiers = event.pointerType === 'touch' ? [] : [event.altKey && 'Alt', event.ctrlKey && 'Control', event.metaKey && 'Meta', event.shiftKey && 'Shift'].filter((value): value is string => Boolean(value))
      const previous = lastClick.current
      const count = event.pointerType !== 'touch' && performance.now() - previous.time < 500 && previous.button === event.button && Math.hypot(previous.x - coordinates.x, previous.y - coordinates.y) < 5 ? previous.count % 3 + 1 : 1
      lastClick.current = { time: performance.now(), ...coordinates, button: event.button, count }
      pointers.current.set(event.pointerId, { kind: event.pointerType, button: ['left', 'middle', 'right'][event.button] ?? 'left', modifiers, touchID, clickCount: count, point: coordinates, frame: displayed.current, control: props.control })
      for (const key of modifiers) send({ action: 'key', phase: 'down', key })
    }
    const gesture = pointers.current.get(event.pointerId)
    if (gesture) gesture.point = coordinates
    if (event.pointerType === 'touch') {
      if (!gesture) return
      send({ action: 'touch', phase, touch_id: gesture.touchID, ...coordinates })
    } else {
      if (!gesture && phase !== 'move') return
      send({ action: 'pointer', phase: phase === 'cancel' ? 'up' : phase, button: gesture?.button ?? 'left', click_count: gesture?.clickCount ?? 1, ...coordinates })
    }
    if ((phase === 'up' || phase === 'cancel') && gesture) {
      for (const key of gesture.modifiers) send({ action: 'key', phase: 'up', key })
      // Chromium touchCancel cancels every active contact.
      if (phase === 'cancel' && gesture.kind === 'touch') pointers.current.clear()
      else pointers.current.delete(event.pointerId)
      if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
    }
  }

  return <div className="flex min-h-0 flex-1 flex-col gap-2">
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <span role="status">{live ? `Live frame · ${dimensions}` : 'Waiting for browser frame'}</span>
      <Button size="sm" variant="outline" disabled={!props.control || !live} onClick={() => keyboard.current?.focus()}>Keyboard</Button>
      <Button size="sm" variant="outline" aria-label={props.expanded ? 'Restore browser controls' : 'Expand browser'} onClick={() => props.onExpandedChange(!props.expanded)}>{props.expanded ? 'Restore' : 'Expand'}</Button>
      <span>{props.control ? 'Click or touch the page. Keyboard opens phone input.' : props.expanded ? 'Watch only — restore browser controls to acquire control.' : 'Watch only — acquire control to interact.'}</span>
    </div>
    <div className="relative min-h-48 flex-1 overflow-hidden bg-black">
      <canvas ref={canvas} aria-label="Shared browser page" tabIndex={props.control ? 0 : -1}
        className="h-full w-full touch-none object-contain outline-none focus-visible:ring-2 focus-visible:ring-primary"
        onFocus={() => keyboard.current?.focus({ preventScroll: true })}
        onKeyDown={onKey} onContextMenu={(event) => event.preventDefault()}
        onPointerDown={(event) => onPointer(event, 'down')} onPointerMove={(event) => onPointer(event, 'move')}
        onPointerUp={(event) => onPointer(event, 'up')} onPointerCancel={(event) => onPointer(event, 'cancel')}
        onLostPointerCapture={(event) => { if (pointers.current.has(event.pointerId)) onPointer(event, 'cancel') }}
        />
      <textarea ref={keyboard} aria-label="Remote browser keyboard" autoCapitalize="off" autoCorrect="off" spellCheck={false} defaultValue={inputSentinel}
        className="absolute bottom-0 left-0 h-px w-px resize-none opacity-0" tabIndex={-1} disabled={!props.control}
        onKeyDown={onKey}
        onFocus={(event) => event.currentTarget.setSelectionRange(event.currentTarget.value.length, event.currentTarget.value.length)}
        onPaste={(event) => {
          event.preventDefault()
          const text = event.clipboardData.getData('text/plain')
          if (text) send({ action: 'text', text })
        }}
        onCompositionStart={() => {
          composing.current = true
          compositionTarget.current = displayed.current && props.control ? { frame: displayed.current, control: props.control } : null
        }}
        onCompositionEnd={(event) => {
          composing.current = false
          const target = compositionTarget.current
          compositionTarget.current = null
          const text = event.data
          if (text && target) {
            if (displayed.current && props.control && sameFrameTarget(target.frame, displayed.current) && target.control.control_session_id === props.control.control_session_id && target.control.control_generation === props.control.control_generation) send({ action: 'text', text })
            else current.current.onError('Composed text discarded: the displayed page or control changed.')
          }
          event.currentTarget.value = inputSentinel
          event.currentTarget.setSelectionRange(1, 1)
        }}
        onInput={(event) => {
          const input = event.nativeEvent as InputEvent
          if (composing.current || input.isComposing) return
          // Some keyboards emit the final composition input after
          // compositionend. That text was already committed or fenced there.
          if (input.inputType !== 'insertCompositionText' && input.inputType !== 'insertFromComposition') {
            const text = event.currentTarget.value.startsWith(inputSentinel) ? event.currentTarget.value.slice(1) : event.currentTarget.value
            if (text) send({ action: 'text', text })
          }
          event.currentTarget.value = inputSentinel
          event.currentTarget.setSelectionRange(1, 1)
        }} />
    </div>
  </div>
}
