import { useEffect, useId, useRef, useState } from 'react'
import type * as React from 'react'
import { ArrowUp, CornerUpRight, ListPlus, Play, RefreshCw, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Tooltip } from '@/components/ui/tooltip'
import { api } from '@/lib/api'
import { canReopenRun } from '@/lib/commands'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { isPress, shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import type { ConfigOption, SessionUsage } from '@/lib/session-types'
import { useImplicitControl, type AgentTerminal } from '@/routes/run/agent-terminal'
import { ComposerImagePicker, ComposerImages, hasImages, useComposerImages } from '@/routes/run/composer-images'
import { commandSuggestions, OptionPills, SuggestionList, triggerAt, useFileSuggestions, type Suggestion } from '@/routes/run/composer-menus'
import { beginDraftRequest, composerBlock, composerDraft, draftSendKey, enhancedBlock, pillFor, pillHint, saveComposerDraft, sentDraft, useComposerDraft, type ComposerDraft, type Pill } from '@/routes/run/composer-state'
import type { RunRoom } from '@/routes/run/room'
import { TypingIndicator, useTypingSignal } from '@/routes/run/typing'
import { sessionColumn } from '@/routes/run/session-rows'
import { sameIdentity, useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'
import type { RunRecord } from '@/store/runs'
import { sessionLease } from '@/store/session-stream'
import { backgroundWait } from '@/store/sessions'
import { cn, focusRing } from '@/lib/utils'

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

function ComposerFrame({ children }: { children: React.ReactNode }) {
  return (
    <div className="shrink-0 bg-canvas pb-[var(--keyboard-inset,0px)]">
      <div className={cn(sessionColumn, 'flex flex-col gap-2 pb-4')}>{children}</div>
    </div>
  )
}

function Closed({ runID, reason, action, dock, failure }: { runID: string; reason: string; action?: React.ReactNode; dock?: React.ReactNode; failure?: string }) {
  return (
    <ComposerFrame>
      {dock}
      {failure && <p role="alert" className="text-ui-sm text-state-failed">{failure}</p>}
      <TypingIndicator runID={runID} />
      <div className="flex min-h-14 min-w-0 items-center gap-2 rounded-composer border border-seam bg-chrome py-2 pl-4 pr-2">
        <p role="status" className="min-w-0 flex-1 text-ui-sm text-muted">{reason}</p>
        {action}
      </div>
    </ComposerFrame>
  )
}

function ClosedRun({ run, reason, dock }: { run: RunRecord; reason: string; dock?: React.ReactNode }) {
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string>()
  const relaunch = async () => {
    const same = sameIdentity()
    setBusy(true)
    setFailure(undefined)
    try {
      const relaunched = await api.runRelaunch(run.id)
      if (same()) useStore.getState().upsertRun(relaunched)
    } catch (err) {
      setFailure(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Closed
      runID={run.id}
      reason={reason}
      dock={dock}
      failure={failure}
      action={<Button variant="primary" disabled={busy} onClick={() => void relaunch()}><RefreshCw aria-hidden="true" />{busy ? 'Relaunching…' : 'Relaunch'}</Button>}
    />
  )
}

const usageFormat = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 })

function ContextMeter({ usage }: { usage: SessionUsage }) {
  if (usage.size <= 0) return null
  const percent = Math.min(100, Math.round((usage.used / usage.size) * 100))
  return (
    <Tooltip content={`Context window: ${usageFormat.format(usage.used)} of ${usageFormat.format(usage.size)} tokens used`}>
      <span
        role="meter"
        tabIndex={0}
        aria-label="Context window used"
        aria-valuemin={0}
        aria-valuemax={usage.size}
        aria-valuenow={Math.min(usage.used, usage.size)}
        aria-valuetext={`${percent}%`}
        className={cn(focusRing, 'flex h-6 shrink-0 items-center gap-1.5 rounded-control px-1.5 text-ui-sm text-muted tabular-nums coarse:h-11')}
      >
        <span aria-hidden="true" className="block h-1.5 w-4 overflow-hidden rounded-full bg-hover-chrome">
          <span className={cn('block h-full', percent >= 90 ? 'bg-state-needs-you' : 'bg-icon-faint')} style={{ width: `${percent}%` }} />
        </span>
        {percent}%
      </span>
    </Tooltip>
  )
}

const pillLook: Record<Pill, { label: string; Icon: typeof ArrowUp; variant: 'primary' | 'danger' }> = {
  send: { label: 'Send', Icon: ArrowUp, variant: 'primary' },
  steer: { label: 'Steer', Icon: CornerUpRight, variant: 'primary' },
  queue: { label: 'Queue', Icon: ListPlus, variant: 'primary' },
  interrupt: { label: 'Interrupt', Icon: Square, variant: 'danger' },
  'stop-waiting': { label: 'Stop waiting', Icon: Square, variant: 'danger' },
  resume: { label: 'Resume', Icon: Play, variant: 'primary' },
}

function ComposerBox({ textarea, autoFocus, value, onChange, onCaret, onFocusChange, onSend, onQueue, onEscape, placeholder, describedBy, menu, onKeyDown, combobox, onPaste, readOnly, images, footer }: {
  textarea: React.RefObject<HTMLTextAreaElement | null>
  autoFocus?: boolean
  value: string
  onChange: (value: string, caret: number) => void
  onCaret?: (caret: number) => void
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
  footer: React.ReactNode
}) {
  const coarse = useMediaQuery(coarsePointer)
  const [focused, setFocused] = useState(false)
  const caretPlaced = useRef(false)
  useEffect(() => {
    if (autoFocus) textarea.current?.focus()
  }, [autoFocus, textarea])
  // The table stands plain keys down inside a field, so the textarea answers
  // Enter itself; this registration keeps Send listed in the shortcuts reference.
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
      <div className={cn('overflow-hidden rounded-composer border border-seam bg-chrome', focused && 'border-control')}>
      {images}
      <Textarea
        variant="embedded"
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
        className="resize-none [field-sizing:content] max-md:[--composer-lines:7.5rem]"
        style={{ maxHeight: 'var(--composer-lines, 12rem)' }}
        onFocus={(event) => {
          // A textarea that mounts with a draft starts its caret before the text.
          if (!caretPlaced.current) {
            caretPlaced.current = true
            event.target.setSelectionRange(value.length, value.length)
            onCaret?.(value.length)
          }
          setFocused(true)
          onFocusChange(true)
        }}
        onSelect={(event) => onCaret?.(event.currentTarget.selectionStart)}
        onBlur={() => {
          setFocused(false)
          onFocusChange(false)
        }}
        onChange={(event) => onChange(event.target.value, event.target.selectionStart)}
        onKeyDown={(event) => {
          onKeyDown?.(event)
          if (event.defaultPrevented || event.nativeEvent.isComposing) return
          if (!coarse && isPress(event.nativeEvent, 'composer-send')) {
            event.preventDefault()
            onSend()
          } else if (event.key === 'Escape' && !value.trim()) {
            event.preventDefault()
            onEscape()
          }
        }}
      />
      <div className="flex min-w-0 flex-wrap items-center gap-1 px-2 pb-2">{footer}</div>
      </div>
    </div>
  )
}

function ComposerHint({ id, uploading, description }: { id: string; uploading: boolean; description: string }) {
  return uploading
    ? <p id={id} role="status" className="line-clamp-2 px-4 text-ui-sm text-muted">Uploading…</p>
    : <p id={id} className="sr-only">{description}</p>
}

function SendActions({ pill, label, hint, description, disabled, onClick, onCancel }: {
  pill: Pill
  label: string
  hint?: string
  description: string
  disabled: boolean
  onClick: () => void
  onCancel?: () => void
}) {
  const look = pillLook[pill]
  const tip = [hint ? `${look.label} (${hint})` : look.label, description].filter(Boolean).join('. ')
  const icon = <look.Icon className={cn((pill === 'interrupt' || pill === 'stop-waiting') && 'fill-current')} />
  return (
    <div className="ml-auto flex shrink-0 items-center gap-1">
      {onCancel && <Button size="sm" variant="ghost" onClick={onCancel}>Cancel</Button>}
      <Tooltip content={tip}>
        <span className="inline-flex">
          {pill === 'send' && label === look.label ? (
            <Button size="round" variant={look.variant} label={look.label} hint={false} disabled={disabled} onClick={onClick}>
              {icon}
            </Button>
          ) : (
            <Button
              size="pill"
              variant={look.variant}
              hint={false}
              aria-label={pill === 'interrupt' ? 'Interrupt the agent' : pill === 'stop-waiting' ? 'Stop waiting for background work' : undefined}
              disabled={disabled}
              onClick={onClick}
            >
              {icon}
              {label}
            </Button>
          )}
        </span>
      </Tooltip>
    </div>
  )
}

function StandardComposer({ run, agent, room, textarea, autoFocus, onFocusChange, onEscape }: ComposerProps) {
  const self = useSelf()
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const coarse = useMediaQuery(coarsePointer)
  const draft = useComposerDraft(run.id)
  const { body } = draft
  useTypingSignal(run.id, body)
  const busy = Boolean(draft.busy)
  const late = usePastDeadline(busy)
  const hintID = useId()
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const block = composerBlock(run, maySteer, maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run))
  const images = useComposerImages(run.id, !block && cap.hasMethod('terminal.file'))
  const { uploading, uploadError } = images
  const control = useImplicitControl(run, agent)
  const hint = control.canAct
    ? 'Sends to the agent’s terminal.'
    : 'Delivers in 45 s unless the controller decides sooner.'
  const error = uploadError ?? (room.errorFromComposer ? room.error : undefined)

  const send = () => {
    if (!body.trim() || busy || !images.isReady()) return
    const post = async () => {
      const latest = composerDraft(run.id)
      const text = latest.body.trim()
      const attachments = images.getPaths()
      if (!text || latest.busy || !images.isReady()) return
      const idempotency = draftSendKey(latest, text, attachments)
      const request = beginDraftRequest(run.id, 'busy', { idempotency, error: undefined })
      const posted = await room.post({ kind: 'steer_request', body: text, attachments, key: idempotency.key })
      request.settle(posted ? sentDraft : {})
    }
    if (control.canAct) control.withControl(() => void post())
    else void post()
  }

  if (block) return <Closed runID={run.id} reason={block} />

  return (
    <ComposerFrame>
      {error && (
        <div role="alert" className="flex items-start gap-2 text-ui-sm text-state-failed">
          <span className="min-w-0 flex-1 break-words">{error}</span>
          <Button size="sm" variant="ghost" onClick={() => {
            images.clearError()
            if (room.errorFromComposer) room.clearError()
          }}>Dismiss</Button>
        </div>
      )}
      <TypingIndicator runID={run.id} />
      <ComposerBox
        textarea={textarea}
        autoFocus={autoFocus}
        value={body}
        readOnly={Boolean(draft.busy)}
        onChange={(next) => saveComposerDraft(run.id, { body: next })}
        onFocusChange={onFocusChange}
        onSend={send}
        onEscape={onEscape}
        placeholder="Message the agent"
        describedBy={hintID}
        onPaste={images.onPaste}
        images={<ComposerImages images={images} disabled={busy} />}
        footer={(
          <>
            <ComposerImagePicker images={images} disabled={busy || !cap.hasMethod('terminal.file')} />
            <SendActions
              pill="send"
              label={late ? stillWaiting : busy ? 'Sending…' : 'Send'}
              hint={coarse ? undefined : shortcutLabel('composer-send')}
              description={hint}
              disabled={!body.trim() || busy || !images.ready}
              onClick={send}
              onCancel={late ? () => saveComposerDraft(run.id, { busy: undefined }) : undefined}
            />
          </>
        )}
      />
      <ComposerHint id={hintID} uploading={uploading} description={hint} />
    </ComposerFrame>
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
  const draft = useComposerDraft(run.id)
  const { body, error } = draft
  useTypingSignal(run.id, body)
  const busy = Boolean(draft.busy)
  const late = usePastDeadline(busy)
  const setBody = (next: string) => saveComposerDraft(run.id, { body: next })
  const [caret, setCaret] = useState(0)
  const [focused, setFocused] = useState(false)
  const [active, setActive] = useState(0)
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
  const waiting = backgroundWait(run, session) !== null
  const supportsImages = state?.prompt_images === true
  const images = useComposerImages(run.id, !gate && cap.hasMethod('terminal.file'))
  const refusedImages = !supportsImages && hasImages(images.previews)
  const hasContent = Boolean(body.trim()) || images.previews.length > 0
  const pill = moderated ? 'send' : pillFor({ paused, turnRunning, backgroundWait: waiting, steering: state?.steering ?? false, queueHeld, empty: !hasContent })

  const [dismissed, setDismissed] = useState(false)
  const trigger = focused && !dismissed ? triggerAt(body, caret) : null
  const files = useFileSuggestions(run.workspace_id, run.id, trigger?.kind === '@' ? trigger.query : null)
  const suggestions: Suggestion[] = trigger?.kind === '/' ? commandSuggestions(state?.commands ?? [], trigger.query) : trigger?.kind === '@' ? files : []
  const current = Math.min(active, Math.max(0, suggestions.length - 1))

  const pick = (item: Suggestion) => {
    if (composerDraft(run.id).busy) return
    if (!trigger) return
    const next = body.slice(0, trigger.start) + item.insert + body.slice(caret)
    const at = trigger.start + item.insert.length
    setBody(next)
    setCaret(at)
    setActive(0)
    requestAnimationFrame(() => textarea.current?.setSelectionRange(at, at))
  }

  const attempt = async (action: () => Promise<unknown>) => {
    if (composerDraft(run.id).busy) return
    const request = beginDraftRequest(run.id, 'busy', { error: undefined, busyPrompt: false })
    try {
      await action()
      request.settle()
    } catch (err) {
      request.settle({ error: message(err) })
    }
  }

  const deliver = async (steer: boolean) => {
    const latest = composerDraft(run.id)
    const text = latest.body.trim()
    const attachments = images.getPaths()
    const lease = sessionLease(useStore, run.id)
    if ((!text && !attachments.length) || latest.busy || !images.isReady() || (!lease && !moderated)) return
    if (!supportsImages && hasImages(latest.images)) {
      saveComposerDraft(run.id, { error: 'This agent does not support image prompts.' })
      return
    }
    const idempotency = draftSendKey(latest, text, attachments)
    const request = beginDraftRequest(run.id, 'busy', { idempotency, error: undefined, busyPrompt: true })
    // After Cancel a failure still shows on the draft it was sent from; a success stays silent.
    const settle = (settled: Partial<ComposerDraft>) => {
      const draft = composerDraft(run.id)
      if (request.current()) request.settle(settled)
      else if (settled.error && !draft.busy && draft.idempotency?.key === idempotency.key) saveComposerDraft(run.id, settled)
    }
    try {
      const result = await api.runInject(run.id, text, idempotency.key, { steer, lease, attachments })
      useStore.getState().upsertRoomMessage(result.message)
      const delivery = result.receipt === 'not_sent' || result.receipt === 'uncertain' ? result.receipt : result.message.state
      if (delivery === 'sent' || delivery === 'queued') settle(sentDraft)
      else if (delivery === 'uncertain') settle({ error: result.message.failure?.message ?? 'Delivery is uncertain. Your draft was kept; check the conversation before retrying.' })
      else settle({ error: result.message.failure?.message ?? 'The message was not sent. Your draft was kept.', idempotency: undefined })
    } catch (err) {
      settle({ error: message(err) })
    }
  }

  const act = (chosen: Pill) => {
    if (composerDraft(run.id).busy || images.isUploading()) return
    if (moderated) void deliver(false)
    else control.withControl(() => perform(chosen))
  }
  const perform = (chosen: Pill) => {
    if (composerDraft(run.id).busy || images.isUploading()) return
    const lease = sessionLease(useStore, run.id)
    switch (chosen) {
      case 'resume':
        void attempt(() => api.runResume(run.id))
        return
      case 'interrupt':
      case 'stop-waiting':
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
        runID={run.id}
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

  const pillDisabled = busy || images.uploading || (pill !== 'interrupt' && pill !== 'stop-waiting' && pill !== 'resume' && (!images.ready || !hasContent || refusedImages))
  const hintText = moderated ? 'Delivers in 45 s unless the controller decides sooner.' : turnRunning || pill === 'stop-waiting' || pill === 'resume' ? pillHint[pill] : ''
  const keyHint = coarse || pill === 'stop-waiting' ? undefined : pill === 'steer' ? `${shortcutLabel('composer-send')}; ${shortcutLabel('composer-queue')} queues` : shortcutLabel(pill === 'queue' && state?.steering ? 'composer-queue' : 'composer-send')

  return (
    <ComposerFrame>
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
          <Button size="sm" variant="ghost" onClick={() => saveComposerDraft(run.id, { error: undefined })}>Dismiss</Button>
        </div>
      )}
      <TypingIndicator runID={run.id} />
      <ComposerBox
        textarea={textarea}
        autoFocus={autoFocus}
        value={body}
        readOnly={busy}
        onPaste={images.onPaste}
        images={<ComposerImages images={images} disabled={busy} />}
        footer={(
          <>
            <ComposerImagePicker
              images={images}
              disabled={busy || !cap.hasMethod('terminal.file')}
              unsupported={refusedImages ? 'This agent does not support image prompts.' : undefined}
            />
            <OptionPills options={state?.config_options ?? []} disabled={busy || moderated} onSet={setOption} />
            {state?.usage && <ContextMeter usage={state.usage} />}
            <SendActions
              pill={pill}
              label={late ? stillWaiting : busy ? 'Sending…' : pillLook[pill].label}
              hint={keyHint}
              description={hintText}
              disabled={pillDisabled}
              onClick={() => act(pill)}
              onCancel={late && draft.busyPrompt ? () => saveComposerDraft(run.id, { busy: undefined }) : undefined}
            />
          </>
        )}
        onChange={(value, at) => {
          setBody(value)
          setCaret(at)
          setActive(0)
          setDismissed(false)
        }}
        onCaret={setCaret}
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
        placeholder={turnRunning || waiting ? 'Message the agent while it works' : 'Message the agent, / for commands, @ for files'}
        describedBy={hintID}
        combobox={{ expanded: suggestions.length > 0, controls: listID, active: suggestions.length ? `${listID}-${current}` : undefined }}
        menu={<SuggestionList id={listID} items={suggestions} active={current} onPick={pick} />}
        onKeyDown={(event) => {
          if (busy) return
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
      <ComposerHint id={hintID} uploading={images.uploading} description={hintText} />
    </ComposerFrame>
  )
}

export function Composer(props: ComposerProps) {
  const { run, dock } = props
  const self = useSelf()
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const canReopen = maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run)
  if (canReopen) return <ClosedRun key={run.id} run={run} reason={composerBlock(run, maySteer, true)!} dock={dock} />
  return props.run.acp ? <EnhancedComposer key={props.run.id} {...props} /> : <StandardComposer key={props.run.id} {...props} />
}
