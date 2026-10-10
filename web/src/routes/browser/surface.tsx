import { useCallback, useEffect, useImperativeHandle, useRef, useState } from 'react'
import type { KeyboardEvent, PointerEvent as ReactPointerEvent, Ref } from 'react'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Spinner } from '@/components/ui/spinner'
import { api, browserStreamURL } from '@/lib/api'
import { message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { DevBrowserActionParams, DevBrowserFrameMetadata, DevBrowserPage, DevControlFence, DevStreamResponse } from '@/lib/types'
import { decodeBrowserFrame, framePoint, sameFrameTarget, type BrowserFrame } from './frames'

export interface BrowserSurfaceHandle {
  /** Gives the page the keyboard; on a phone this opens its keyboard. */
  focus: () => void
  /** False while input is queued or was sent in the last half second, a
   * pointer is down, or text is being composed. */
  idle: () => boolean
}

interface BrowserSurfaceProps {
  ref?: Ref<BrowserSurfaceHandle>
  runID: string
  page: DevBrowserPage
  control: DevControlFence | null
  /** Nobody else holds the lease, so input may take it through `acquire`. */
  free: boolean
  acquire: () => Promise<DevControlFence | null>
  /** The toolbar is changing the page; input is dropped until it is done. */
  paused: boolean
  onBlocked: () => void
  onError: (error: string) => void
  /** A painted frame is of a newer revision than `page`. */
  onNavigated: () => void
  onPage: (page: DevBrowserPage) => void
}
type BrowserInput = Omit<DevBrowserActionParams, 'run_id' | 'session_id' | 'page_id' | 'page_revision' | 'control_session_id' | 'control_generation'>
interface PendingInput { input: BrowserInput; frame: DevBrowserFrameMetadata; time: number; epoch: number }
const inputSentinel = '\u200b'
const retryDelays = [500, 1000, 2000, 4000, 8000]
interface PointerGesture {
  kind: string
  button: string
  touchID: number
  clickCount: number
  modifiers: string[]
  frame: DevBrowserFrameMetadata
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
  const repaintParked = useRef(() => {})
  const composing = useRef(false)
  const compositionFrame = useRef<DevBrowserFrameMetadata | null>(null)
  const pointers = useRef(new Map<number, PointerGesture>())
  const lastClick = useRef({ time: 0, x: 0, y: 0, button: -1, count: 0 })
  const controlled = useRef(false)
  const lastInput = useRef(-Infinity)
  const failures = useRef(0)
  const [live, setLive] = useState(false)
  const [failure, setFailure] = useState('')
  const [connection, setConnection] = useState(0)
  const connecting = useDelayed(!live && !failure, 400)
  const watching = props.control === null && !props.free

  const clearInput = useCallback(() => {
    inputEpoch.current++
    queue.current = []
    pointers.current.clear()
    compositionFrame.current = null
    composing.current = false
    if (keyboard.current) {
      keyboard.current.value = inputSentinel
      keyboard.current.setSelectionRange(1, 1)
    }
  }, [])
  useImperativeHandle(props.ref, () => ({
    focus: () => keyboard.current?.focus({ preventScroll: true }),
    idle: () => !sending.current && queue.current.length === 0 && pointers.current.size === 0 && !composing.current && performance.now() - lastInput.current > 500,
  }), [])
  useEffect(() => {
    // Input queued while a free lease is being taken belongs to that lease.
    if (controlled.current) clearInput()
    controlled.current = props.control !== null
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
    return () => { active.current = false }
  }, [])
  useEffect(() => {
    let disposed = false
    let acknowledged = false
    let failed = false
    let latest: BrowserFrame | null = null
    let parked: BrowserFrame | null = null
    let painted = 0
    let decoding = false
    let sequence = 0
    let retry: ReturnType<typeof setTimeout> | undefined
    const target = { run_id: props.runID, session_id: props.page.session_id, page_id: props.page.page_id, page_revision: current.current.page.page_revision }
    const socket = new WebSocket(browserStreamURL(target))
    socket.binaryType = 'arraybuffer'
    // The last painted frame stays up while the stream reconnects.
    const fail = (text: string) => {
      if (disposed || failed) return
      failed = true
      latest = null
      parked = null
      setLive(false)
      const delay = retryDelays[failures.current++]
      if (delay === undefined) setFailure(text)
      else retry = setTimeout(() => setConnection((value) => value + 1), delay)
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
            if (!element || !context || frame.metadata.page_revision < observed.page_revision) continue
            // A frame can outrun the RPC that reports its new viewport, and the
            // screencast sends nothing more for a still page.
            if (frame.metadata.page_revision === observed.page_revision && frame.metadata.viewport_id !== observed.viewport_id) {
              parked = frame
              continue
            }
            if (frame.metadata.sequence <= painted) continue
            parked = null
            painted = frame.metadata.sequence
            element.width = frame.metadata.width
            element.height = frame.metadata.height
            context.drawImage(bitmap, 0, 0, element.width, element.height)
            displayed.current = frame.metadata
            failures.current = 0
            setLive(true)
            if (frame.metadata.page_revision > observed.page_revision) current.current.onNavigated()
          } finally { bitmap.close() }
        }
      } catch (cause) {
        fail(`Browser image failed: ${message(cause)}`)
        socket.close()
      } finally { decoding = false }
    }
    repaintParked.current = () => {
      if (!parked) return
      if (!latest && parked.metadata.sequence > painted) latest = parked
      parked = null
      void paint()
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
    socket.onerror = () => fail('The page stream could not connect.')
    socket.onclose = (event) => fail(`The page stream closed${event.reason ? `: ${event.reason}` : '.'}`)
    return () => {
      disposed = true
      clearTimeout(retry)
      latest = null
      parked = null
      repaintParked.current = () => {}
      socket.close()
    }
    // Revisions arrive in the stream; they are not new observation sessions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.runID, props.page.session_id, props.page.page_id, connection])
  useEffect(() => repaintParked.current(), [props.page.page_revision, props.page.viewport_id])

  const send = (input: BrowserInput) => {
    const frame = displayed.current
    if (!frame || !active.current || current.current.paused || failure) return
    const observed = current.current.page
    if (frame.page_revision < observed.page_revision || (frame.page_revision === observed.page_revision && frame.viewport_id !== observed.viewport_id)) {
      current.current.onError('Input discarded: wait for the current page frame.')
      return
    }
    const entry: PendingInput = { input, frame, time: performance.now(), epoch: inputEpoch.current }
    lastInput.current = entry.time
    const previous = queue.current.at(-1)
    if (input.phase === 'move' && previous?.input.action === input.action && previous.input.phase === 'move' && previous.input.touch_id === input.touch_id) queue.current[queue.current.length - 1] = entry
    else if (queue.current.length < 32) queue.current.push(entry)
    else {
      queue.current = []
      current.current.onError('Input discarded: browser input is congested.')
      return
    }
    if (sending.current) return
    sending.current = true
    void (async () => {
      try {
        while (active.current && queue.current.length) {
          let control = current.current.control
          if (!control) {
            control = await current.current.acquire()
            if (!control) {
              clearInput()
              break
            }
            // Taking the lease is not time the input spent going stale.
            const now = performance.now()
            for (const waiting of queue.current) waiting.time = now
          }
          const next = queue.current.shift()
          if (!next) break
          if (next.epoch !== inputEpoch.current) continue
          const now = current.current
          const visible = displayed.current
          const observedChanged = now.page.session_id !== next.frame.session_id || now.page.page_id !== next.frame.page_id || now.page.page_revision > next.frame.page_revision || (now.page.page_revision === next.frame.page_revision && now.page.viewport_id !== next.frame.viewport_id)
          if (!visible || !sameFrameTarget(visible, next.frame) || observedChanged || performance.now() - next.time > 1000) {
            clearInput()
            now.onError('Input discarded: the displayed frame changed or input expired. Nothing was replayed.')
            break
          }
          try {
            const result = await api.devBrowserAction({ ...next.input, run_id: next.frame.run_id, session_id: next.frame.session_id, page_id: next.frame.page_id, page_revision: next.frame.page_revision, viewport_id: next.frame.viewport_id, ...control })
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
      const now = current.current
      if (!frame) return
      event.preventDefault()
      // A wheel also reaches a window that is not the focused one, which must
      // not take the lease on its behalf.
      if (now.paused || !document.hasFocus()) return
      if (!now.control && !now.free) {
        now.onBlocked()
        return
      }
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
    // React's text-control fallback does not deliver every mobile deletion;
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
    if (watching || event.nativeEvent.isComposing || composing.current || event.key === 'Process' || event.key === 'Unidentified' || event.key === 'Dead') return
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'v') return
    event.preventDefault()
    if (['Shift', 'Control', 'Alt', 'Meta'].includes(event.key)) return
    send({ action: 'key', key: event.key === ' ' ? 'Space' : event.key, modifiers: [event.altKey && 'Alt', event.ctrlKey && 'Control', event.metaKey && 'Meta', event.shiftKey && 'Shift'].filter((value): value is string => Boolean(value)) })
  }
  const onPointer = (event: ReactPointerEvent<HTMLCanvasElement>, phase: 'down' | 'move' | 'up' | 'cancel') => {
    if (!displayed.current) return
    if (props.paused) {
      // A press that was down when the pause began is forgotten here, so its
      // late release is not sent on its own; the next press starts clean.
      if (phase === 'up' || phase === 'cancel') pointers.current.delete(event.pointerId)
      return
    }
    if (watching) {
      if (phase === 'down') props.onBlocked()
      return
    }
    const existing = pointers.current.get(event.pointerId)
    // Hovering a page nobody drives does not take it; the first press does.
    if (!props.control && !existing && phase !== 'down') return
    if (existing && !sameFrameTarget(existing.frame, displayed.current)) {
      pointers.current.delete(event.pointerId)
      current.current.onError('Gesture discarded: the displayed page changed.')
      return
    }
    const point = framePoint(displayed.current, event.currentTarget.getBoundingClientRect(), event.clientX, event.clientY)
    if (!point && phase !== 'up' && phase !== 'cancel') {
      // A press beside a page that does not fill the pane still takes the
      // lease, which is what lets the viewport follow the pane.
      if (phase === 'down' && !props.control) void props.acquire()
      return
    }
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
      pointers.current.set(event.pointerId, { kind: event.pointerType, button: ['left', 'middle', 'right'][event.button] ?? 'left', modifiers, touchID, clickCount: count, point: coordinates, frame: displayed.current })
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

  return <div className="relative h-full min-h-0 min-w-0 overflow-hidden bg-chrome focus-within:outline-1 focus-within:-outline-offset-1 focus-within:outline-accent">
    <canvas ref={canvas} aria-label="Shared browser page" aria-busy={!live} tabIndex={watching ? -1 : 0}
      className="h-full w-full touch-none object-scale-down outline-none"
      onFocus={() => keyboard.current?.focus({ preventScroll: true })}
      onKeyDown={onKey} onContextMenu={(event) => event.preventDefault()}
      onPointerDown={(event) => onPointer(event, 'down')} onPointerMove={(event) => onPointer(event, 'move')}
      onPointerUp={(event) => onPointer(event, 'up')} onPointerCancel={(event) => onPointer(event, 'cancel')}
      onLostPointerCapture={(event) => { if (pointers.current.has(event.pointerId)) onPointer(event, 'cancel') }}
      />
    <textarea ref={keyboard} aria-label="Remote browser keyboard" autoCapitalize="off" autoCorrect="off" spellCheck={false} defaultValue={inputSentinel}
      className="absolute bottom-0 left-0 h-px w-px resize-none opacity-0" tabIndex={-1} disabled={watching}
      onKeyDown={onKey}
      onFocus={(event) => event.currentTarget.setSelectionRange(event.currentTarget.value.length, event.currentTarget.value.length)}
      onPaste={(event) => {
        event.preventDefault()
        const text = event.clipboardData.getData('text/plain')
        if (text) send({ action: 'text', text })
      }}
      onCompositionStart={() => {
        composing.current = true
        compositionFrame.current = displayed.current
      }}
      onCompositionEnd={(event) => {
        composing.current = false
        const frame = compositionFrame.current
        compositionFrame.current = null
        const text = event.data
        if (text && frame) {
          if (displayed.current && sameFrameTarget(frame, displayed.current)) send({ action: 'text', text })
          else current.current.onError('Composed text discarded: the displayed page changed.')
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
    {connecting && <div className="pointer-events-none absolute inset-0 grid place-items-center"><Spinner label="Connecting to the page" className="size-5" /></div>}
    {failure && (
      <div role="alert" className="absolute inset-0 grid place-items-center overflow-y-auto bg-canvas">
        <EmptyState
          title="The page stream stopped"
          action={<Button variant="secondary" onClick={() => {
            failures.current = 0
            setFailure('')
            setConnection((value) => value + 1)
          }}>Retry</Button>}
        >
          <span className="break-words">{failure}</span>
        </EmptyState>
      </div>
    )}
  </div>
}
