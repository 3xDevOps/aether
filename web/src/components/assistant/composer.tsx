import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { ArrowUp, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

export function AssistantComposer({ textarea, running, dock }: {
  textarea: React.RefObject<HTMLTextAreaElement | null>
  running: boolean
  dock?: React.ReactNode
}) {
  const draft = useStore((s) => s.assistantDraft)
  const setDraft = useStore((s) => s.setAssistantDraft)
  const coarse = useMediaQuery(coarsePointer)
  const [focused, setFocused] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const attempt = useRef<{ text: string; key: string }>(null)
  const generation = useStore((s) => s.assistantGeneration)
  useEffect(() => {
    attempt.current = null
  }, [generation])

  const send = async () => {
    const text = draft.trim()
    if (!text || busy || running) return
    // A retry of the same text reuses its key, so a lost reply cannot send it twice.
    if (attempt.current?.text !== text) attempt.current = { text, key: crypto.randomUUID() }
    setBusy(true)
    setError(undefined)
    try {
      await api.assistantPrompt(text, attempt.current.key)
      attempt.current = null
      if (useStore.getState().assistantDraft.trim() === text) setDraft('')
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  const stop = async () => {
    setError(undefined)
    try {
      await api.assistantCancel()
    } catch (err) {
      setError(message(err))
    }
  }

  useKeybindings('composer', focused && !coarse ? {
    'composer-send': (event) => {
      event.preventDefault()
      void send()
    },
  } : {})

  return (
    <div className="shrink-0 border-t border-seam bg-canvas pb-[var(--keyboard-inset,0px)]">
      <div className="flex flex-col gap-2 px-3 py-3">
        {dock}
        {error && <p role="alert" className="text-ui-sm break-words text-state-failed">{error}</p>}
        <div className={cn('overflow-hidden rounded-control border border-control bg-canvas', focused && 'outline-1 -outline-offset-1 outline-accent shadow-field-focus')}>
          <Textarea
            variant="embedded"
            ref={textarea}
            aria-label="Message the assistant"
            rows={2}
            value={draft}
            readOnly={busy}
            placeholder="Ask Aether"
            className="resize-none [field-sizing:content] max-md:[--composer-lines:6.5rem]"
            style={{ maxHeight: 'var(--composer-lines, 10rem)' }}
            onFocus={() => setFocused(true)}
            onBlur={() => setFocused(false)}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== 'Enter' || event.shiftKey || event.metaKey || event.ctrlKey || event.nativeEvent.isComposing || coarse) return
              event.preventDefault()
              void send()
            }}
          />
        </div>
        <div className="flex items-center justify-end gap-2">
          {running ? (
            <Button size="sm" variant="danger" onClick={() => void stop()}>
              <Square className="fill-current" />
              Stop
            </Button>
          ) : (
            <Button size="sm" disabled={busy || !draft.trim()} hint={coarse ? undefined : `Send (${shortcutLabel('composer-send')} or Enter)`} onClick={() => void send()}>
              <ArrowUp />
              {busy ? 'Sending…' : 'Send'}
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
