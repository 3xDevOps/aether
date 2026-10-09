import { useEffect, useId, useRef, useState } from 'react'
import type * as React from 'react'
import { ArrowUp, CornerUpRight, ListPlus, Play, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { canReopenRun } from '@/lib/commands'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { formatKeys, shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import type { ConfigOption } from '@/lib/session-types'
import { useImplicitControl, type AgentTerminal } from '@/routes/run/agent-terminal'
import { ComposerImagePicker, ComposerImages, useComposerImages } from '@/routes/run/composer-images'
import { commandSuggestions, OptionPills, SuggestionList, triggerAt, useFileSuggestions, type Suggestion } from '@/routes/run/composer-menus'
import { composerBlock, enhancedBlock, pillFor, pillHint, type Pill } from '@/routes/run/composer-state'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { sessionLease } from '@/store/session-stream'
import { cn } from '@/lib/utils'

interface ComposerProps {
  run: RunRecord
  agent: AgentTerminal
  room: RunRoom
  textarea: React.RefObject<HTMLTextAreaElement | null>
  onFocusChange: (focused: boolean) => void
  /** The gate can keep the box unmounted, so focusing waits for it to mount. */
  autoFocus?: boolean
  dock?: React.ReactNode
  onEscape: () => void
}

function Closed({ reason, action, dock, failure }: { reason: string; action?: React.ReactNode; dock?: React.ReactNode; failure?: string }) {
  return (
    <div className="shrink-0 border-t border-seam bg-canvas pb-[var(--keyboard-inset,0px)]">
      <div className="mx-auto flex max-w-[736px] flex-col gap-2 px-4 py-3">
        {dock}
        {failure && <p role="alert" className="text-ui-sm text-state-failed">{failure}</p>}
        <div className="flex min-w-0 items-center gap-2">
          <p role="status" className="min-w-0 flex-1 text-ui-sm text-muted">{reason}</p>
          {action}
        </div>
      </div>
    </div>
  )
}

const pillLook: Record<Pill, { label: string; Icon: typeof ArrowUp; variant: 'primary' | 'danger' }> = {
  send: { label: 'Send', Icon: ArrowUp, variant: 'primary' },
  steer: { label: 'Steer', Icon: CornerUpRight, variant: 'primary' },
  queue: { label: 'Queue', Icon: ListPlus, variant: 'primary' },
  interrupt: { label: 'Interrupt', Icon: Square, variant: 'danger' },
  resume: { label: 'Resume', Icon: Play, variant: 'primary' },
}

function ComposerBox({ textarea, autoFocus, value, onChange, onFocusChange, onSend, onQueue, onEscape, placeholder, describedBy, menu, onKeyDown, combobox, onPaste, readOnly, images, imageAction }: {
  textarea: React.RefObject<HTMLTextAreaElement | null>
  autoFocus?: boolean
  value: string
  onChange: (value: string, caret: number) => void
  onFocusChange: (focused: boolean) => void
  onSend: () => void
  onQueue?: () => void
  onEscape: () => void
  placeholder: string
  describedBy: string
  menu?: React.ReactNode
  onKeyDown?: (event: React.KeyboardEvent<HTMLTextAreaElement>) => void
  combobox?: { expanded: boolean; controls: string; active?: string }
  onPaste?: (event: React.ClipboardEvent<HTMLTextAreaElement>) => void
  readOnly?: boolean
  images?: React.ReactNode
  imageAction?: React.ReactNode
}) {
  const coarse = useMediaQuery(coarsePointer)
  const [focused, setFocused] = useState(false)
  useEffect(() => {
    if (autoFocus) textarea.current?.focus()
  }, [autoFocus, textarea])
  useKeybindings('composer', focused && !coarse ? {
    'composer-send': (event) => {
      event.preventDefault()
      onSend()
    },
    ...(onQueue ? {
      'composer-queue': (event: KeyboardEvent) => {
        event.preventDefault()
        onQueue()
      },
    } : {}),
  } : {})
  return (
    <div className="relative">
      {menu}
      <div className="overflow-hidden rounded-control border border-control bg-canvas">
      {images}
      <Textarea
        ref={textarea}
        aria-label="Message the agent"
        aria-describedby={describedBy}
        role={combobox ? 'combobox' : undefined}
        aria-expanded={combobox?.expanded}
        aria-controls={combobox?.expanded ? combobox.controls : undefined}
        aria-activedescendant={combobox?.active}
        aria-autocomplete={combobox ? 'list' : undefined}
        rows={2}
        value={value}
        readOnly={readOnly}
        onPaste={onPaste}
        placeholder={placeholder}
        className="resize-none border-0 [field-sizing:content] max-md:[--composer-lines:6.5rem]"
        style={{ maxHeight: 'var(--composer-lines, 10rem)' }}
        onFocus={() => {
          setFocused(true)
          onFocusChange(true)
        }}
        onBlur={() => {
          setFocused(false)
          onFocusChange(false)
        }}
        onChange={(event) => onChange(event.target.value, event.target.selectionStart)}
        onKeyDown={(event) => {
          onKeyDown?.(event)
          if (event.defaultPrevented || event.key !== 'Escape' || event.nativeEvent.isComposing || value.trim()) return
          event.preventDefault()
          onEscape()
        }}
      />
      {imageAction && <div className="flex items-center gap-2 px-1 pb-1">{imageAction}</div>}
      </div>
    </div>
  )
}

function StandardComposer({ run, agent, room, textarea, autoFocus, onFocusChange, onEscape }: ComposerProps) {
  const self = useSelf()
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const coarse = useMediaQuery(coarsePointer)
  const [body, setBody] = useState('')
  const hintID = useId()
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const block = composerBlock(run, maySteer, maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run))
  const images = useComposerImages(run.id, !block && cap.hasMethod('terminal.image'), () => room.busy)
  const { uploading, uploadError } = images
  const control = useImplicitControl(run, agent)
  const hint = control.canAct
    ? 'Sends to the agent’s terminal.'
    : 'Delivers in 45 s unless the controller decides sooner.'
  const error = uploadError ?? (room.errorFromComposer ? room.error : undefined)

  const send = () => {
    const text = body.trim()
    if (!text || room.busy || !images.isReady()) return
    const post = async () => {
      if (await room.post({ kind: 'steer_request', body: text, attachments: images.getPaths() })) {
        setBody('')
        images.clear()
      }
    }
    if (control.canAct) control.withControl(() => void post())
    else void post()
  }

  if (block) return <Closed reason={block} />

  return (
    <div className="shrink-0 border-t border-seam bg-canvas pb-[var(--keyboard-inset,0px)]">
      <div className="mx-auto flex max-w-[736px] flex-col gap-2 px-4 py-3">
        {error && (
          <div role="alert" className="flex items-start gap-2 text-ui-sm text-state-failed">
            <span className="min-w-0 flex-1 break-words">{error}</span>
            <Button size="sm" variant="ghost" onClick={() => {
              images.clearError()
              if (room.errorFromComposer) room.clearError()
            }}>Dismiss</Button>
          </div>
        )}
        <ComposerBox
          textarea={textarea}
          autoFocus={autoFocus}
          value={body}
          onChange={setBody}
          onFocusChange={onFocusChange}
          onSend={send}
          onEscape={onEscape}
          placeholder="Message the agent"
          describedBy={hintID}
          onPaste={images.onPaste}
          images={<ComposerImages images={images} disabled={room.busy} />}
          imageAction={<ComposerImagePicker images={images} disabled={room.busy || !cap.hasMethod('terminal.image')} />}
        />
        <div className="flex min-w-0 items-center gap-2">
          <p id={hintID} className="line-clamp-2 min-w-0 flex-1 text-ui-sm text-muted">
            {uploading ? 'Uploading…' : hint}
          </p>
          <Button
            size="sm"
            hint={coarse ? undefined : `Send (${formatKeys('$mod+Enter')})`}
            disabled={!body.trim() || room.busy || !images.ready}
            onClick={send}
          >
            <ArrowUp />
            {room.busy ? 'Sending…' : 'Send'}
          </Button>
        </div>
      </div>
    </div>
  )
}

function useQueueHeld(active: boolean): boolean {
  const [held, setHeld] = useState(false)
  useEffect(() => {
    if (!active) {
      setHeld(false)
      return
    }
    const update = (event: KeyboardEvent) => setHeld((event.metaKey || event.ctrlKey) && event.shiftKey)
    const clear = () => setHeld(false)
    window.addEventListener('keydown', update)
    window.addEventListener('keyup', update)
    window.addEventListener('blur', clear)
    return () => {
      window.removeEventListener('keydown', update)
      window.removeEventListener('keyup', update)
      window.removeEventListener('blur', clear)
    }
  }, [active])
  return held
}

function EnhancedComposer({ run, agent, textarea, autoFocus, onFocusChange, dock, onEscape }: ComposerProps) {
  const self = useSelf()
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const session = useStore((s) => s.acpSessions[run.id])
  const paused = useStore((s) => s.pausedRuns[run.id] ?? run.paused ?? false)
  const control = useImplicitControl(run, agent)
  const coarse = useMediaQuery(coarsePointer)
  const [body, setBody] = useState('')
  const [caret, setCaret] = useState(0)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [error, setError] = useState<string>()
  const [focused, setFocused] = useState(false)
  const [active, setActive] = useState(0)
  const idempotency = useRef<{ identity: string; key: string } | null>(null)
  const hintID = useId()
  const listID = useId()
  const queueHeld = useQueueHeld(focused)

  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const state = session?.state
  const gate = enhancedBlock({
    run,
    maySteer,
    canReopen: maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run),
    stream: session?.stream,
    streamError: session?.streamError,
    sessionLive: session?.live ?? false,
    pending: session?.pending.length ?? 0,
    hasLease: control.canAct,
  })
  const moderated = !control.canAct
  const turnRunning = state?.turn_in_flight ?? false
  const supportsImages = state?.prompt_images === true
  const images = useComposerImages(run.id, !gate && supportsImages && cap.hasMethod('terminal.image'), () => busyRef.current)
  const hasContent = Boolean(body.trim()) || images.previews.length > 0
  const pill = moderated ? 'send' : pillFor({ paused, turnRunning, steering: state?.steering ?? false, queueHeld, empty: !hasContent })

  const [dismissed, setDismissed] = useState(false)
  const trigger = focused && !dismissed ? triggerAt(body, caret) : null
  const files = useFileSuggestions(run.workspace_id, run.id, trigger?.kind === '@' ? trigger.query : null)
  const suggestions: Suggestion[] = trigger?.kind === '/' ? commandSuggestions(state?.commands ?? [], trigger.query) : trigger?.kind === '@' ? files : []
  const current = Math.min(active, Math.max(0, suggestions.length - 1))

  const pick = (item: Suggestion) => {
    if (busyRef.current) return
    if (!trigger) return
    const next = body.slice(0, trigger.start) + item.insert + body.slice(caret)
    const at = trigger.start + item.insert.length
    setBody(next)
    setCaret(at)
    setActive(0)
    requestAnimationFrame(() => textarea.current?.setSelectionRange(at, at))
  }

  const attempt = async (action: () => Promise<unknown>) => {
    if (busyRef.current) return false
    busyRef.current = true
    setBusy(true)
    setError(undefined)
    try {
      await action()
      return true
    } catch (err) {
      setError(message(err))
      return false
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }

  const deliver = async (steer: boolean) => {
    const text = body.trim()
    const attachments = images.getPaths()
    const lease = sessionLease(useStore, run.id)
    if ((!text && !attachments.length) || busyRef.current || !images.isReady() || (!lease && !moderated)) return
    if (attachments.length && !supportsImages) {
      setError('This agent does not support image prompts.')
      return
    }
    const identity = JSON.stringify([text, attachments])
    const key = idempotency.current?.identity === identity ? idempotency.current.key : crypto.randomUUID()
    idempotency.current = { identity, key }
    const sent = await attempt(async () => {
      const result = await api.runInject(run.id, text, key, { steer, lease, attachments })
      useStore.getState().upsertRoomMessage(result.message)
      const delivery = result.receipt === 'not_sent' || result.receipt === 'uncertain' ? result.receipt : result.message.state
      if (delivery !== 'sent' && delivery !== 'queued') {
        if (delivery !== 'uncertain') idempotency.current = null
        throw new Error(result.message.failure?.message ?? (delivery === 'uncertain'
          ? 'Delivery is uncertain. Your draft was kept; check the conversation before retrying.'
          : 'The message was not sent. Your draft was kept.'))
      }
    })
    if (sent) {
      idempotency.current = null
      setBody('')
      images.clear()
    }
  }

  const act = (chosen: Pill) => {
    if (busyRef.current || images.isUploading()) return
    if (moderated) void deliver(false)
    else control.withControl(() => perform(chosen))
  }
  const perform = (chosen: Pill) => {
    if (busyRef.current || images.isUploading()) return
    const lease = sessionLease(useStore, run.id)
    switch (chosen) {
      case 'resume':
        void attempt(() => api.runResume(run.id))
        return
      case 'interrupt':
        if (lease) void attempt(() => api.runACPCancel(run.id, lease))
        return
      default:
        void deliver(chosen === 'steer')
    }
  }

  const setOption = async (option: ConfigOption, value: string) => {
    const lease = sessionLease(useStore, run.id)
    if (lease) await attempt(() => api.runACPSetOption(run.id, option.id, value, lease))
  }

  if (gate) {
    return (
      <Closed
        reason={gate.reason}
        dock={dock}
        failure={gate.interrupt ? error : undefined}
        action={gate.interrupt && turnRunning && (
          <Button size="sm" variant="danger" aria-label="Interrupt the agent" hint={pillHint.interrupt} disabled={busy} onClick={() => act('interrupt')}>
            <Square className="fill-current" />
            Interrupt
          </Button>
        )}
      />
    )
  }

  const look = pillLook[pill]
  const pillDisabled = busy || images.uploading || (pill !== 'interrupt' && pill !== 'resume' && (!images.ready || !hasContent || (images.previews.length > 0 && !supportsImages)))
  const keyHint = coarse ? undefined : pill === 'steer' ? `Steer (${shortcutLabel('composer-send')}); ${shortcutLabel('composer-queue')} queues` : `${look.label} (${shortcutLabel(pill === 'queue' && state?.steering ? 'composer-queue' : 'composer-send')})`

  return (
    <div className="shrink-0 border-t border-seam bg-canvas pb-[var(--keyboard-inset,0px)]">
      <div className="mx-auto flex max-w-[736px] flex-col gap-2 px-4 py-3">
        {dock}
        {images.uploadError && (
          <div role="alert" className="flex items-start gap-2 text-ui-sm text-state-failed">
            <span className="min-w-0 flex-1 break-words">{images.uploadError}</span>
            <Button size="sm" variant="ghost" onClick={images.clearError}>Dismiss</Button>
          </div>
        )}
        {error && (
          <div role="alert" className="flex items-start gap-2 text-ui-sm text-state-failed">
            <span className="min-w-0 flex-1 break-words">{error}</span>
            <Button size="sm" variant="ghost" onClick={() => setError(undefined)}>Dismiss</Button>
          </div>
        )}
        <ComposerBox
          textarea={textarea}
          autoFocus={autoFocus}
          value={body}
          readOnly={busy}
          onPaste={images.onPaste}
          images={<ComposerImages images={images} disabled={busy} />}
          imageAction={(
            <ComposerImagePicker
              images={images}
              disabled={busy || !supportsImages || !cap.hasMethod('terminal.image')}
              unsupported={!supportsImages ? 'This agent does not support image prompts.' : undefined}
            />
          )}
          onChange={(value, at) => {
            setBody(value)
            setCaret(at)
            setActive(0)
            setDismissed(false)
          }}
          onFocusChange={(next) => {
            setFocused(next)
            onFocusChange(next)
          }}
          onSend={() => {
            if (hasContent) act(pill)
          }}
          onQueue={() => {
            if (hasContent) act(turnRunning ? 'queue' : 'send')
          }}
          onEscape={onEscape}
          placeholder={turnRunning ? 'Message the agent while it works' : 'Message the agent, / for commands, @ for files'}
          describedBy={hintID}
          combobox={{ expanded: suggestions.length > 0, controls: listID, active: suggestions.length ? `${listID}-${current}` : undefined }}
          menu={<SuggestionList id={listID} items={suggestions} active={current} onPick={pick} />}
          onKeyDown={(event) => {
            if (busyRef.current) return
            if (suggestions.length === 0 || event.nativeEvent.isComposing) return
            if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
              event.preventDefault()
              setActive((current + (event.key === 'ArrowDown' ? 1 : -1) + suggestions.length) % suggestions.length)
            } else if ((event.key === 'Enter' && !event.metaKey && !event.ctrlKey) || event.key === 'Tab') {
              event.preventDefault()
              pick(suggestions[current]!)
            } else if (event.key === 'Escape') {
              event.preventDefault()
              setDismissed(true)
            }
          }}
        />
        <div className="flex min-w-0 flex-wrap items-center gap-1">
          <OptionPills options={state?.config_options ?? []} disabled={busy || moderated} onSet={setOption} />
          <p id={hintID} role={images.uploading ? 'status' : undefined} className="line-clamp-2 min-w-0 flex-1 basis-24 px-1 text-ui-sm text-muted">
            {images.uploading ? 'Uploading…' : moderated ? 'Delivers in 45 s unless the controller decides sooner.' : turnRunning || pill === 'resume' ? pillHint[pill] : ''}
          </p>
          <Button
            size="sm"
            variant={look.variant}
            hint={keyHint}
            aria-label={pill === 'interrupt' ? 'Interrupt the agent' : undefined}
            disabled={pillDisabled}
            onClick={() => act(pill)}
          >
            <look.Icon className={cn(pill === 'interrupt' && 'fill-current')} />
            {busy ? 'Sending…' : look.label}
          </Button>
        </div>
      </div>
    </div>
  )
}

export function Composer(props: ComposerProps) {
  return props.run.acp ? <EnhancedComposer key={props.run.id} {...props} /> : <StandardComposer key={props.run.id} {...props} />
}
