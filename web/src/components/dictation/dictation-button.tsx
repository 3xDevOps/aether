import { useEffect, useRef, useState, type RefObject } from 'react'
import type * as React from 'react'
import { startCapture, type Capture } from '@/components/dictation/capture'
import { setDictationState, type DictationState } from '@/components/dictation/state'
import { dictationHints, insertTranscript } from '@/components/dictation/text'
import { base64, encodeWav, maxSeconds, resample } from '@/components/dictation/wav'
import { LoaderCircle, Mic, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

/** The `server.info` feature of a server with dictation installed. */
const dictationFeature = 'dictation'

/** Whether the server has dictation installed and this gateway forwards it. */
function useDictation(): boolean {
  const installed = useStore((s) => s.info?.features?.includes(dictationFeature) ?? false)
  const cap = useCapability()
  return installed && cap.hasMethod('dictation.transcribe')
}

/** A press held at least this long stops on release: hold-to-talk. Shorter is a tap that toggles. */
const holdMs = 300
const meterMs = 1000 / 15
// The local gateway's bound on dictation.transcribe: the server's 3-minute dictation timeout plus 60 s.
const transcribeTimeout = 240_000
const meterBars = 7

type Phase =
  | { kind: 'idle' }
  | { kind: 'starting' }
  | { kind: 'recording'; since: number }
  | { kind: 'transcribing' }

function micError(err: unknown): { text: string; hint?: string } {
  if (!(err instanceof DOMException)) return { text: message(err) }
  const text = `Microphone: ${err.name}: ${err.message}`
  return err.name === 'NotAllowedError' ? { text, hint: "Allow the microphone for this site in your browser's settings, then press the mic again." } : { text }
}

function clock(ms: number): string {
  const seconds = Math.floor(ms / 1000)
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}

/** Records speech, transcribes it with `dictation.transcribe` and puts the text at the caret the box had when recording started. It never sends. */
export function DictationButton({ textarea, onInsert, hints }: {
  textarea: RefObject<HTMLTextAreaElement | null>
  onInsert: (next: string) => void
  hints?: string[]
}) {
  const offered = useDictation()
  const [phase, setPhase] = useState<Phase>({ kind: 'idle' })
  const [status, setStatus] = useState('')
  const [meter, setMeter] = useState<{ elapsed: number; levels: number[] }>({ elapsed: 0, levels: [] })
  const capture = useRef<Capture | null>(null)
  const request = useRef<AbortController | null>(null)
  const generation = useRef(0)
  const selection = useRef({ value: '', start: 0, end: 0 })
  const press = useRef<{ at: number; started: boolean; held: boolean } | null>(null)
  const mine = useRef<HTMLSpanElement>(null)
  const latest = useRef({ textarea, onInsert, hints })
  latest.current = { textarea, onInsert, hints }

  const show = (next: Phase, notice: DictationState['notice'] = null) => {
    setPhase(next)
    setDictationState(latest.current.textarea, { active: next.kind !== 'idle', notice })
  }
  const fail = (text: string, error = true, hint?: string) => {
    show({ kind: 'idle' }, { text, error, hint })
    setStatus('')
  }

  const start = async () => {
    const box = latest.current.textarea.current
    // A read-only box is sending; the sent draft would replace whatever dictation put in it.
    if (box?.readOnly) return fail('The message is still sending; dictate again once it is sent.', false)
    const value = box?.value ?? ''
    selection.current = { value, start: box?.selectionStart ?? value.length, end: box?.selectionEnd ?? value.length }
    const run = ++generation.current
    show({ kind: 'starting' })
    setStatus('Starting microphone')
    try {
      const opened = await startCapture()
      if (run !== generation.current) return opened.cancel()
      capture.current = opened
      setMeter({ elapsed: 0, levels: [] })
      show({ kind: 'recording', since: performance.now() })
      setStatus('Recording. Press again to stop, Escape to cancel.')
    } catch (err) {
      if (run !== generation.current) return
      const { text, hint } = micError(err)
      fail(text, true, hint)
    }
  }

  const stop = async () => {
    const recording = capture.current
    if (!recording) return
    capture.current = null
    const run = generation.current
    show({ kind: 'transcribing' })
    setStatus('Transcribing')
    const controller = new AbortController()
    request.current = controller
    const timer = setTimeout(() => controller.abort(new DOMException(`dictation.transcribe got no answer within ${transcribeTimeout / 1000} seconds`, 'TimeoutError')), transcribeTimeout)
    try {
      const { samples, rate } = await recording.stop()
      if (run !== generation.current) return
      const audio = base64(encodeWav(resample(samples, rate)))
      const { text } = await api.dictationTranscribe(audio, latest.current.hints ?? dictationHints(useStore.getState()), controller.signal)
      if (run !== generation.current) return
      const words = text.trim()
      if (!words) return fail('No speech was recognized.', false)
      const box = latest.current.textarea.current
      const value = box?.value ?? ''
      // Text edited while recording moves the words: the caret the member left wins, else the end.
      const at = value === selection.current.value
        ? selection.current
        : box && document.activeElement === box ? { start: box.selectionStart, end: box.selectionEnd } : { start: value.length, end: value.length }
      const next = insertTranscript(value, words, at.start, at.end)
      latest.current.onInsert(next.body)
      show({ kind: 'idle' })
      setStatus('Dictation inserted')
      requestAnimationFrame(() => {
        const field = latest.current.textarea.current
        field?.focus()
        field?.setSelectionRange(next.caret, next.caret)
      })
    } catch (err) {
      if (run === generation.current) fail(message(err))
    } finally {
      clearTimeout(timer)
      if (request.current === controller) request.current = null
    }
  }

  const cancel = () => {
    generation.current++
    capture.current?.cancel()
    capture.current = null
    request.current?.abort()
    show({ kind: 'idle' })
    setStatus('Dictation cancelled')
  }
  const latestActions = useRef({ stop, cancel })
  latestActions.current = { stop, cancel }

  const busy = phase.kind === 'starting' || phase.kind === 'recording' || phase.kind === 'transcribing'
  useEffect(() => {
    if (!busy) return
    // Only an Escape meant for this box: one in a dialog, menu or another composer goes on to its owner.
    const onKey = (event: KeyboardEvent) => {
      const target = event.target as Element | null
      if (event.key !== 'Escape' || (target !== latest.current.textarea.current && target?.closest('[data-dictation]') !== mine.current)) return
      event.preventDefault()
      event.stopPropagation()
      latestActions.current.cancel()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [busy])

  const since = phase.kind === 'recording' ? phase.since : null
  useEffect(() => {
    if (since === null) return
    const tick = setInterval(() => {
      const elapsed = performance.now() - since
      if (elapsed >= maxSeconds * 1000) {
        void latestActions.current.stop()
        return
      }
      const level = capture.current?.level() ?? 0
      setMeter((current) => ({ elapsed, levels: [...current.levels, level].slice(-meterBars) }))
    }, meterMs)
    return () => clearInterval(tick)
  }, [since])

  useEffect(() => () => {
    generation.current++
    capture.current?.cancel()
    request.current?.abort()
    setDictationState(latest.current.textarea, { active: false, notice: null })
  }, [])

  if (!offered) return null

  const toggle = () => {
    if (phase.kind === 'recording') void stop()
    else if (phase.kind === 'idle') void start()
  }
  const release = () => {
    const held = press.current
    // A press that outlasted the permission prompt is a tap: the hold counts from when recording began.
    if (!held?.started || phase.kind !== 'recording') return
    if (performance.now() - Math.max(held.at, phase.since) >= holdMs) {
      press.current = { ...held, started: false, held: true }
      void stop()
    }
  }
  const handlers = {
    onPointerDown: (event: React.PointerEvent) => {
      if (event.button !== 0) return
      // Captured, so a hold released off the button still stops.
      event.currentTarget.setPointerCapture(event.pointerId)
      press.current = { at: performance.now(), started: phase.kind === 'idle', held: false }
      if (press.current.started) void start()
    },
    onPointerUp: release,
    onPointerCancel: release,
    onLostPointerCapture: release,
    onClick: (event: React.MouseEvent) => {
      const pointer = press.current
      press.current = null
      // A keyboard click has no detail and no press before it.
      if (event.detail === 0 || !(pointer?.started || pointer?.held)) toggle()
    },
    // Focus stays in the box, so a phone keeps its keyboard up while recording.
    onMouseDown: (event: React.MouseEvent) => event.preventDefault(),
    onContextMenu: (event: React.MouseEvent) => event.preventDefault(),
  }

  const pill = 'mr-2 min-w-28 justify-center gap-2 px-3 tabular-nums coarse:min-w-32'
  return (
    <span ref={mine} data-dictation className="inline-flex shrink-0">
      {phase.kind === 'recording' ? (
        <Button type="button" size="pill" variant="danger" aria-label="Dictate" aria-pressed hint={`Stop dictation, Escape cancels. Stops by itself at ${clock(maxSeconds * 1000)}.`} className={cn(pill, 'touch-none select-none')} {...handlers}>
          <Square className="fill-current" aria-hidden />
          <span aria-hidden className="flex h-4 items-center gap-0.5">
            {Array.from({ length: meterBars }, (_, i) => (
              <span
                key={i}
                data-slot="dictation-meter"
                className="h-full w-[3px] rounded-full bg-current transition-transform duration-75 motion-reduce:transition-none"
                style={{ transform: `scaleY(${0.35 + 0.65 * (meter.levels[i] ?? 0)})` }}
              />
            ))}
          </span>
          <span aria-hidden>{clock(meter.elapsed)}</span>
        </Button>
      ) : phase.kind === 'starting' || phase.kind === 'transcribing' ? (
        <Button type="button" size="pill" variant="secondary" aria-label="Dictate" aria-pressed={phase.kind === 'starting'} aria-disabled hint="Escape cancels" className={cn(pill, 'aria-disabled:cursor-default aria-disabled:opacity-100')} {...handlers}>
          <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden />
          <span aria-hidden>{phase.kind === 'starting' ? 'Starting mic…' : 'Transcribing…'}</span>
        </Button>
      ) : (
        <Button type="button" size="icon-sm" variant="ghost" label="Dictate" aria-pressed={false} hint="Dictate: tap to start and stop, or hold to talk" className="touch-none text-text select-none" {...handlers}>
          <Mic />
        </Button>
      )}
      <span role="status" aria-live="polite" className="sr-only">{status}</span>
    </span>
  )
}
