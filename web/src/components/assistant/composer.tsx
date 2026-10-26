import { useEffect, useState } from 'react'
import type * as React from 'react'
import { conflictChoiceID, ContextChips } from '@/components/assistant/context-chips'
import { ArrowUp, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { promptKey, settlePromptKey } from '@/store/assistant'
import { assistantShareView, draftConflict, draftViewContext, flushViewSync, unsharedPart, viewContextKey } from '@/store/view-context'

/** How long Stop waits on assistant.cancel before it gives the button back. */
export const stopTimeout = 10_000

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
  const stopping = useStore((s) => s.assistantStopping)
  const error = useStore((s) => s.assistantError)
  const focus = useStore((s) => s.assistantFocus)

  useEffect(() => {
    if (!focus) return
    // A frame later, so a menu that opened the panel has finished returning focus.
    const frame = requestAnimationFrame(() => {
      const box = textarea.current
      box?.focus()
      box?.setSelectionRange(box.value.length, box.value.length)
      useStore.getState().takeAssistantFocus()
    })
    return () => cancelAnimationFrame(frame)
  }, [focus, textarea])

  const send = async () => {
    const state = useStore.getState()
    const text = state.assistantDraft.trim()
    if (!text || state.assistantSending || running) return
    if (draftConflict(state)) {
      document.getElementById(conflictChoiceID)?.focus()
      return
    }
    const generation = state.assistantGeneration
    state.setAssistantSending(true)
    state.setAssistantError(null)
    try {
      // A retry of the same text and context reuses its key and context, so a lost reply cannot send it twice.
      let attempt = state.assistantAttempt?.text === text ? state.assistantAttempt : null
      if (!attempt) {
        const viewContext = draftViewContext(state)
        const fingerprint = viewContext && viewContextKey(viewContext)
        attempt = { text, viewContext, key: await promptKey(JSON.stringify([state.identityKey, text, fingerprint])) }
        const now = useStore.getState()
        const next = draftViewContext(now)
        const changed = now.assistantDraft.trim() !== text || (next && viewContextKey(next)) !== fingerprint
        // The member edited the draft or a chip while the key was made: send nothing they no longer see.
        if (now.assistantGeneration !== generation || changed) {
          settlePromptKey(attempt.key)
          return
        }
        now.setAssistantAttempt(attempt)
      }
      // The prompt keeps the held page; only the sync forgets one the member removed.
      if (useStore.getState().assistantPageRemoved) await flushViewSync(useStore)
      try {
        await api.assistantPrompt(text, attempt.key, assistantShareView(useStore.getState()) ? attempt.viewContext : unsharedPart(attempt.viewContext))
      } finally {
        settlePromptKey(attempt.key)
      }
      const now = useStore.getState()
      if (now.assistantGeneration !== generation) return
      if (now.assistantDraft.trim() === text) now.clearAssistantDraft()
      else now.setAssistantAttempt(null)
    } catch (err) {
      if (useStore.getState().assistantGeneration === generation) useStore.getState().setAssistantError(message(err))
    } finally {
      if (useStore.getState().assistantGeneration === generation) useStore.getState().setAssistantSending(false)
    }
  }
  // Blocks a second press only while assistant.cancel is in flight: an agent that ignores it can be stopped again.
  const stop = async () => {
    const state = useStore.getState()
    if (state.assistantStopping) return
    state.setAssistantStopping(true)
    state.setAssistantError(null)
    try {
      await api.assistantCancel(AbortSignal.timeout(stopTimeout))
    } catch (err) {
      const timedOut = err instanceof DOMException && err.name === 'TimeoutError'
      useStore.getState().setAssistantError(timedOut ? `assistant.cancel got no answer within ${stopTimeout / 1000} seconds` : message(err))
    } finally {
      useStore.getState().setAssistantStopping(false)
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
        <ContextChips textarea={textarea} />
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
            <Button size="sm" variant="danger" disabled={stopping} onClick={() => void stop()}>
              <Square className="fill-current" />
              {stopping ? 'Stopping…' : 'Stop'}
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
