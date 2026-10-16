import { useState } from 'react'
import type * as React from 'react'
import { ContextChip } from '@/components/assistant/context-chip'
import { ArrowUp, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { assistantShareView, buildViewContext } from '@/store/view-context'

export function AssistantComposer({ textarea, running, dock }: {
  textarea: React.RefObject<HTMLTextAreaElement | null>
  running: boolean
  dock?: React.ReactNode
}) {
  const draft = useStore((s) => s.assistantDraft)
  const setDraft = useStore((s) => s.setAssistantDraft)
  const coarse = useMediaQuery(coarsePointer)
  const [focused, setFocused] = useState(false)
  const busy = useStore((s) => s.assistantSending)
  const [error, setError] = useState<string>()

  const send = async () => {
    const text = draft.trim()
    if (!text || busy || running) return
    const state = useStore.getState()
    // A retry of the same text reuses its key and view context, so a lost reply cannot send it twice.
    const attempt = state.assistantAttempt?.text === text
      ? state.assistantAttempt
      : { text, key: crypto.randomUUID(), viewContext: assistantShareView(state) ? buildViewContext(state) : undefined }
    const generation = state.assistantGeneration
    state.setAssistantAttempt(attempt)
    state.setAssistantSending(true)
    setError(undefined)
    try {
      await api.assistantPrompt(text, attempt.key, assistantShareView(useStore.getState()) ? attempt.viewContext : undefined)
      const now = useStore.getState()
      if (now.assistantGeneration !== generation) return
      now.setAssistantAttempt(null)
      if (now.assistantDraft.trim() === text) setDraft('')
    } catch (err) {
      setError(message(err))
    } finally {
      if (useStore.getState().assistantGeneration === generation) useStore.getState().setAssistantSending(false)
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
        <ContextChip />
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
