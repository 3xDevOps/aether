import { useEffect, useRef, useState, type RefObject } from 'react'
import type * as React from 'react'
import { startCapture, type Capture } from '@/components/dictation/capture'
import { dictationHints, insertTranscript } from '@/components/dictation/text'
import { base64, encodeWav, maxSeconds, resample } from '@/components/dictation/wav'
import { LoaderCircle, Mic, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

/** The `server.info` feature of a server with dictation installed. */
const dictationFeature = 'dictation'

/** Whether the server has dictation installed and this gateway forwards it. */
export function useDictation(): boolean {
  const installed = useStore((s) => s.info?.features?.includes(dictationFeature) ?? false)
  const cap = useCapability()
  return installed && cap.hasMethod('dictation.transcribe')
}

/** A press held at least this long stops on release: hold-to-talk. Shorter is a tap that toggles. */
const holdMs = 300
const meterMs = 1000 / 15
const transcribeTimeout = 120_000
const meterBars = 5

type Phase =
  | { kind: 'idle' }
  | { kind: 'starting' }
  | { kind: 'recording'; since: number }
  | { kind: 'transcribing' }
  | { kind: 'notice'; text: string }

function micError(err: unknown): string {
  return err instanceof DOMException ? `Microphone: ${err.name}: ${err.message}` : message(err)
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
  const selection = useRef({ start: 0, end: 0 })
  const press = useRef<{ at: number; started: boolean } | null>(null)
  const latest = useRef({ textarea, onInsert, hints })
  latest.current = { textarea, onInsert, hints }

  const fail = (text: string) => {
    setPhase({ kind: 'notice', text })
    setStatus(text)
  }

  const start = async () => {
    const box = latest.current.textarea.current
    const end = box?.value.length ?? 0
    selection.current = { start: box?.selectionStart ?? end, end: box?.selectionEnd ?? end }
    const run = ++generation.current
    setPhase({ kind: 'starting' })
    setStatus('Starting microphone')
    try {
      const opened = await startCapture()
      if (run !== generation.current) return opened.cancel()
      capture.current = opened
      setMeter({ elapsed: 0, levels: [] })
      setPhase({ kind: 'recording', since: performance.now() })
      setStatus('Recording. Press again to stop, Escape to cancel.')
    } catch (err) {
      if (run === generation.current) fail(micError(err))
    }
  }

  const stop = async () => {
    const recording = capture.current
    if (!recording) return
    capture.current = null
    const run = generation.current
    setPhase({ kind: 'transcribing' })
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
      if (!words) return fail('No speech was recognized.')
      const box = latest.current.textarea.current
      const next = insertTranscript(box?.value ?? '', words, selection.current.start, selection.current.end)
      latest.current.onInsert(next.body)
      setPhase({ kind: 'idle' })
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
    setPhase({ kind: 'idle' })
    setStatus('Dictation cancelled')
  }
  const latestActions = useRef({ stop, cancel })
  latestActions.current = { stop, cancel }

  const busy = phase.kind === 'starting' || phase.kind === 'recording' || phase.kind === 'transcribing'
  useEffect(() => {
    if (!busy) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
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
  }, [])

  if (!offered) return null

  const toggle = () => {
    if (phase.kind === 'recording') void stop()
    else if (phase.kind === 'idle' || phase.kind === 'notice') void start()
  }
  const handlers = {
    onPointerDown: (event: React.PointerEvent) => {
      if (event.button !== 0) return
      press.current = { at: performance.now(), started: phase.kind === 'idle' || phase.kind === 'notice' }
      if (press.current.started) void start()
    },
    onPointerUp: () => {
      const held = press.current
      // A press that outlasted the permission prompt is a tap: the hold counts from when recording began.
      if (!held?.started || phase.kind !== 'recording') return
      if (performance.now() - Math.max(held.at, phase.since) >= holdMs) void stop()
    },
    onClick: () => {
      const pointer = press.current
      press.current = null
      if (!pointer?.started) toggle()
    },
    onContextMenu: (event: React.MouseEvent) => event.preventDefault(),
  }

  let button: React.ReactNode
  if (phase.kind === 'recording') {
    button = (
      <Button type="button" size="pill" variant="danger" aria-label="Stop dictation" aria-pressed hint={`Stop dictation, Escape cancels. Stops by itself at ${clock(maxSeconds * 1000)}.`} className="touch-none select-none gap-2 px-2.5 tabular-nums" {...handlers}>
        <Square className="fill-current" aria-hidden />
        <span aria-hidden className="flex h-3.5 items-center gap-0.5">
          {Array.from({ length: meterBars }, (_, i) => (
            <span
              key={i}
              data-slot="dictation-meter"
              className="h-full w-0.5 rounded-full bg-current transition-transform duration-75 motion-reduce:transition-none"
              style={{ transform: `scaleY(${0.15 + 0.85 * (meter.levels[i] ?? 0)})` }}
            />
          ))}
        </span>
        <span aria-hidden>{clock(meter.elapsed)}</span>
      </Button>
    )
  } else if (phase.kind === 'starting' || phase.kind === 'transcribing') {
    button = (
      <Button type="button" size="pill" variant="ghost" aria-label={phase.kind === 'starting' ? 'Starting microphone' : 'Transcribing'} aria-disabled hint="Escape cancels" className="gap-1.5 px-2.5 aria-disabled:cursor-default aria-disabled:opacity-100">
        <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden />
        <span aria-hidden>{phase.kind === 'starting' ? 'Starting mic…' : 'Transcribing…'}</span>
      </Button>
    )
  } else {
    button = (
      <Button type="button" size="icon-sm" variant="ghost" label="Start dictation" aria-pressed={false} hint="Dictate: tap to start and stop, or hold to talk" className="touch-none select-none" {...handlers}>
        <Mic />
      </Button>
    )
  }

  return (
    <Popover open={phase.kind === 'notice'} onOpenChange={(open) => { if (!open) setPhase({ kind: 'idle' }) }}>
      <PopoverAnchor asChild>
        <span className="inline-flex shrink-0">{button}</span>
      </PopoverAnchor>
      {phase.kind === 'notice' && (
        <PopoverContent side="top" align="end" className="flex w-[min(360px,calc(100vw-16px))] items-start gap-2 p-2 pl-3" onOpenAutoFocus={(event) => event.preventDefault()}>
          <p className="min-w-0 flex-1 py-0.5 text-ui-sm break-words">{phase.text}</p>
          <Button size="sm" variant="ghost" onClick={() => setPhase({ kind: 'idle' })}>Dismiss</Button>
        </PopoverContent>
      )}
      <span role="status" aria-live="polite" className="sr-only">{status}</span>
    </Popover>
  )
}
