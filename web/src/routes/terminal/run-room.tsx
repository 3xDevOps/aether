import { Fragment, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { Bot, MessageSquare, Shield, Users, X } from 'lucide-react'
import { Dialog as DialogPrimitive } from 'radix-ui'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/tooltip'
import { Textarea } from '@/components/ui/textarea'
import { DialogOverlay, DialogPortal } from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { inModal } from '@/lib/keys'
import { shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { cn } from '@/lib/utils'
import type { ControlMetadata } from '@/routes/terminal/attach'
import { ControlButton } from '@/routes/terminal/control-button'
import type { TakeoverInteraction } from '@/routes/terminal/use-takeover'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import type { RoomMessage, RoomMessageKind, Run } from '@/lib/types'
import { MemberAvatar } from '@/routes/board/member-avatar'
import { useStore } from '@/store'
const emptyMessages: RoomMessage[] = []
const maxRoomAttachments = 8

function dateLabel(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function memberLabel(id: string, members: Record<string, { display_name: string }>, snapshot?: string): string {
  return members[id]?.display_name || snapshot || id
}

function actionKey(): string {
  return crypto.randomUUID()
}

function remainingSeconds(deliverAfter: string | undefined): number {
  if (!deliverAfter) return 0
  return Math.max(0, Math.ceil((new Date(deliverAfter).valueOf() - Date.now()) / 1000))
}

/** Ticks once a second until delivery; only this text re-renders. */
function DeliveryCountdown({ deliverAfter }: { deliverAfter?: string }) {
  const [remaining, setRemaining] = useState(() => remainingSeconds(deliverAfter))
  useEffect(() => {
    setRemaining(remainingSeconds(deliverAfter))
    if (remainingSeconds(deliverAfter) === 0) return
    const timer = window.setInterval(() => {
      const next = remainingSeconds(deliverAfter)
      setRemaining(next)
      if (next === 0) window.clearInterval(timer)
    }, 1000)
    return () => window.clearInterval(timer)
  }, [deliverAfter])
  return <span className="text-state-warn">{remaining ? `${remaining}s before delivery` : 'Ready for delivery'}</span>
}

export interface RunRoomProps {
  run: Run
  client?: Api
  selfID: string | null
  control?: ControlMetadata
  onTakeControl: () => void
  onReleaseControl: () => void
  evidenceAnswer?: { fact: string }
  controlUnavailable?: boolean
  takeover?: TakeoverInteraction
}

type PendingPost = {
  key: string
  kind: RoomMessageKind
  body: string
  attachments?: string[]
  correlation_id?: string
}
function sameAttachments(left?: string[], right?: string[]): boolean {
  const a = left ?? []
  const b = right ?? []
  return a.length === b.length && a.every((value, index) => value === b[index])
}

function overlapsCached(messages: RoomMessage[], cached: RoomMessage[]): boolean {
  if (!messages.length || !cached.length) return false
  const cachedIDs = new Set(cached.map((message) => message.id))
  return messages.some((message) => cachedIDs.has(message.id))
}

export function RunRoom({ run, client = api, selfID, control, onTakeControl, onReleaseControl, evidenceAnswer, controlUnavailable = false, takeover }: RunRoomProps) {
  const runID = run.id
  const workspaceID = run.workspace_id
  const isPhone = useMediaQuery(phoneScreen)
  const members = useStore((state) => state.members)
  const messages = useStore((state) => state.roomMessages[runID] ?? emptyMessages)
  const nextBefore = useStore((state) => state.roomNextBefore[runID])
  const pagination = useStore((state) => state.roomPagination[runID])
  const loading = useStore((state) => state.roomLoading[runID] === true)
  const error = useStore((state) => state.roomError[runID])
  const actionError = useStore((state) => state.roomActionError[runID])
  const status = useStore((state) => state.roomStatus[runID])
  const statusControl = useStore((state) => state.roomStatusControl[runID])
  const statusError = useStore((state) => state.roomStatusError[runID])
  const initializePagination = useStore((state) => state.initializeRoomPagination)
  const setLoading = useStore((state) => state.setRoomLoading)
  const setError = useStore((state) => state.setRoomError)
  const setActionError = useStore((state) => state.setRoomActionError)
  const setPage = useStore((state) => state.setRoomPage)
  const upsert = useStore((state) => state.upsertRoomMessage)
  const setStatus = useStore((state) => state.setRoomStatus)
  const setStatusError = useStore((state) => state.setRoomStatusError)
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<'comment' | 'steer_request' | 'reply'>('comment')
  const [body, setBody] = useState('')
  const [correlationID, setCorrelationID] = useState<string | undefined>()
  const [attachments, setAttachments] = useState<string[]>([])
  const [uploading, setUploading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [pending, setPending] = useState<PendingPost | undefined>()
  const [initialLoadFailed, setInitialLoadFailed] = useState(false)
  const roomLoadKey = useRef<string | null>(null)
  const draftVersion = useRef(0)
  const composer = useRef<HTMLTextAreaElement>(null)
  const opener = useRef<HTMLButtonElement>(null)
  const invoker = useRef<HTMLElement | null>(null)
  const focusComposer = useRef(false)
  const consumedEvidenceAnswer = useRef<RunRoomProps['evidenceAnswer']>(undefined)
  const restoreFocus = useRef(false)
  const roomID = useId()
  const hintID = useId()
  const scope = useRef({ active: true })
  const questions = useMemo(() => unansweredQuestions(messages), [messages])
  const queued = useMemo(() => queuedSteers(messages), [messages])
  const count = questions.length + Math.max(status?.queued_steers ?? 0, queued.length)
  const controller = status?.controller
  const watchers = status?.watchers ?? []
  const staleController = statusControl !== control || Boolean(statusError)
  const ownsControl = Boolean(selfID && control?.has_control)
  const controllerID = ownsControl ? selfID : controller?.member_id
  const controllerName = controllerID ? memberLabel(controllerID, members) : null
  const isLive = run.status === 'running' || run.status === 'needs-attention'
  const canLoadOlder = pagination?.initialized === true && !pagination.exhausted && nextBefore !== undefined

  useEffect(() => {
    const current = { active: true }
    scope.current = current
    return () => {
      current.active = false
      setLoading(runID, false)
    }
  }, [runID, workspaceID, client, setLoading])

  const closeRoom = useCallback(() => {
    restoreFocus.current = true
    setOpen(false)
  }, [])

  useEffect(() => {
    if (!evidenceAnswer || consumedEvidenceAnswer.current === evidenceAnswer) return
    consumedEvidenceAnswer.current = evidenceAnswer
    draftVersion.current += 1
    setMode('comment')
    setCorrelationID(undefined)
    setBody(evidenceAnswer.fact)
    if (!composer.current) {
      invoker.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    }
    focusComposer.current = true
    setOpen(true)
  }, [evidenceAnswer])

  useEffect(() => {
    if (open && focusComposer.current) {
      focusComposer.current = false
      composer.current?.focus()
    } else if (!open && restoreFocus.current) {
      restoreFocus.current = false
      const target = invoker.current
      if (target?.isConnected && target !== document.body) target.focus()
      else opener.current?.focus()
    }
  }, [open, evidenceAnswer])

  useKeybindings('run', {
    'run-room': (event) => {
      const ownDialog = isPhone && event.target instanceof Element &&
        event.target.closest('[role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"]')?.id === roomID
      if (event.defaultPrevented || (inModal(event.target) && !ownDialog)) return
      const state = useStore.getState()
      if (state.paletteOpen || state.paletteDialog) return
      // Claim the chord before xterm's target handler can turn it into bytes.
      event.preventDefault()
      event.stopPropagation()
      if (event.repeat) return
      if (open) closeRoom()
      else {
        invoker.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
        focusComposer.current = true
        setOpen(true)
      }
    },
  })

  const load = async () => {
    const current = scope.current
    // Define the list before the first await. A room event arriving while this
    // request is in flight must see an initialized list and reconcile it.
    initializePagination(runID)
    setLoading(runID, true)
    setError(runID)
    setInitialLoadFailed(false)
    try {
      const page = await client.runRoomList({ workspace_id: workspaceID, run_id: runID, limit: 100 })
      if (!current.active) return
      setPage(runID, page.messages, page.next_before)
    } catch (cause) {
      if (!current.active) return
      setInitialLoadFailed(true)
      setError(runID, cause instanceof Error ? cause.message : String(cause))
    } finally {
      if (current.active) setLoading(runID, false)
    }
  }

  // Reconcile the newest page periodically while open. If a burst is larger
  // than one page, walk the server's cursors until this cached history is
  // reached or the server reports exhaustion. Each cursor is visited once so
  // a malformed response cannot create an unbounded loop.
  const refreshMessages = async () => {
    const current = scope.current
    const cached = useStore.getState().roomMessages[runID] ?? []
    const mergeAsOlder = cached.length === 0
    const visited = new Set<string>()
    let before: string | undefined
    try {
      while (true) {
        const page = await client.runRoomList({
          workspace_id: workspaceID,
          run_id: runID,
          ...(before === undefined ? {} : { before }),
          limit: 100,
        })
        if (!current.active) return
        setPage(runID, page.messages, page.next_before, mergeAsOlder)
        if (overlapsCached(page.messages, cached) || !page.next_before || visited.has(page.next_before)) break
        visited.add(page.next_before)
        before = page.next_before
      }
      setError(runID)
    } catch (cause) {
      if (!current.active) return
      setError(runID, cause instanceof Error ? cause.message : String(cause))
    }
  }

  const loadOlder = async () => {
    const current = scope.current
    const state = useStore.getState()
    const before = state.roomNextBefore[runID]
    const currentPagination = state.roomPagination[runID]
    if (
      currentPagination?.initialized !== true ||
      currentPagination.exhausted ||
      before === undefined ||
      state.roomLoading[runID] === true
    ) return
    setLoading(runID, true)
    setError(runID)
    setInitialLoadFailed(false)
    try {
      const page = await client.runRoomList({
        workspace_id: workspaceID,
        run_id: runID,
        before,
        limit: 100,
      })
      if (!current.active) return
      setPage(runID, page.messages, page.next_before, true)
    } catch (cause) {
      if (!current.active) return
      setError(runID, cause instanceof Error ? cause.message : String(cause))
    } finally {
      if (current.active) setLoading(runID, false)
    }
  }
  // Presence stays live even with the room collapsed. It must never gate
  // history, reset a draft, or let an older response replace newer metadata.
  useEffect(() => {
    let active = true
    let pending: AbortController | undefined
    let deadline: number | undefined
    const refresh = async () => {
      if (pending) return
      const controller = new AbortController()
      pending = controller
      // Race cancellation as well as aborting fetch: injected clients may
      // ignore the signal, but must not hold the polling guard forever.
      const aborted = new Promise<never>((_, reject) => {
        controller.signal.addEventListener('abort', () => reject(controller.signal.reason), { once: true })
      })
      deadline = window.setTimeout(() => {
        controller.abort(new Error('Presence request timed out after 15 seconds'))
      }, 15_000)
      try {
        const next = await Promise.race([
          client.runRoomStatus({ workspace_id: workspaceID, run_id: runID }, controller.signal),
          aborted,
        ])
        if (!active) return
        setStatus(runID, next, control)
        setStatusError(runID)
      } catch (cause) {
        if (!active) return
        setStatusError(runID, cause instanceof Error ? cause.message : String(cause))
      } finally {
        window.clearTimeout(deadline)
        pending = undefined
      }
    }
    void refresh()
    const timer = window.setInterval(() => void refresh(), 10_000)
    return () => {
      active = false
      window.clearInterval(timer)
      window.clearTimeout(deadline)
      pending?.abort()
    }
  }, [runID, workspaceID, control, client, setStatus, setStatusError])


  useEffect(() => {
    if (!open) return
    const timer = window.setInterval(() => void refreshMessages(), 10_000)
    return () => window.clearInterval(timer)
    // Reconciliation merges the newest page and never resets room history or
    // local composer state.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, runID, workspaceID, client])

  useEffect(() => {
    const key = `${workspaceID}:${runID}`
    if (!open) {
      roomLoadKey.current = null
      return
    }
    if (roomLoadKey.current === key || loading) return
    roomLoadKey.current = key
    void load()
    // Loading is intentionally tied to opening the room, not the terminal.
    // Retry invokes load directly after a failed initial read.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, runID, workspaceID])

  const markDraftEdited = () => {
    draftVersion.current += 1
  }

  const selectQuestion = (question: RoomMessage) => {
    markDraftEdited()
    setMode('reply')
    setCorrelationID(question.id)
    setBody('')
    composer.current?.focus()
  }

  const upload = async (file: File) => {
    // Keep the client-side cap identical to the server contract. The input is
    // disabled at the same boundary, but this guard also covers a queued
    // change event that lands before React commits the disabled state.
    if (uploading || attachments.length >= maxRoomAttachments) return
    markDraftEdited()
    setUploading(true)
    setActionError(runID)
    try {
      const result = await client.uploadTerminalImage(file, runID)
      setAttachments((current) =>
        current.length >= maxRoomAttachments ? current : [...current, result.path],
      )
    } catch (cause) {
      setActionError(runID, cause instanceof Error ? cause.message : String(cause))
    } finally {
      setUploading(false)
    }
  }

  const clearAttachments = () => {
    if (!attachments.length) return
    markDraftEdited()
    setAttachments([])
  }


  const submit = async () => {
    const text = body.trim()
    if (!text || busy || uploading) return
    if (mode === 'steer_request' && (status?.protected || run.protected)) return
    const kind = mode
    const actionAttachments = attachments.length ? [...attachments] : undefined
    const existing =
      pending &&
      pending.body === text &&
      pending.kind === kind &&
      pending.correlation_id === correlationID &&
      sameAttachments(pending.attachments, actionAttachments)
        ? pending
        : undefined
    const action: PendingPost = existing ?? {
      key: actionKey(),
      kind,
      body: text,
      attachments: actionAttachments,
      correlation_id: correlationID,
    }
    const submittedDraftVersion = draftVersion.current
    setPending(action)
    setBusy(true)
    setActionError(runID)
    try {
      const result = await client.runRoomPost({
        workspace_id: workspaceID,
        run_id: runID,
        kind: action.kind,
        body: action.body,
        attachments: action.attachments,
        correlation_id: action.correlation_id,
        idempotency_key: action.key,
        ...(action.kind === 'steer_request' && isLive && ownsControl && control && control.control_generation > 0
          ? { control_session_id: control.control_session_id, control_generation: control.control_generation }
          : {}),
      })
      upsert(result.message)
      setPending(undefined)
      setActionError(runID)
      if (draftVersion.current === submittedDraftVersion) {
        setBody('')
        setAttachments([])
        setCorrelationID(undefined)
        setMode('comment')
      }
    } catch (cause) {
      setActionError(runID, cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }

  const decide = async (messageID: string, decision: 'approve' | 'deny') => {
    setBusy(true)
    setActionError(runID)
    try {
      const decisionParams = {
        message_id: messageID,
        decision,
        control_session_id: control?.control_session_id ?? '',
        control_generation: control?.control_generation ?? 0,
      }
      const result = await client.runRoomDecide(decisionParams)
      upsert(result.message)
      setActionError(runID)
    } catch (cause) {
      setActionError(runID, cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }


  const RoomPortal = isPhone ? DialogPortal : Fragment
  const RoomPanel = isPhone ? DialogPrimitive.Content : 'aside'
  const RoomTitle = isPhone ? DialogPrimitive.Title : 'h2'

  return (
    <DialogPrimitive.Root open={isPhone && open} onOpenChange={(next) => { if (!next) closeRoom() }}>
      {!open && (
        <Tooltip side="left" content={`Toggle Run Room · ${shortcutLabel('run-room')}`}>
          <button
            ref={opener}
            type="button"
            aria-label="Open Run Room"
            aria-expanded={false}
            aria-controls={roomID}
            aria-keyshortcuts="Control+Shift+M Meta+Shift+M"
            className="flex w-8 shrink-0 items-center justify-center border-l border-border bg-toolbar text-[12px] font-medium text-muted-foreground hover:bg-toolbar-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring coarse:w-11"
            onClick={() => { invoker.current = opener.current; setOpen(true) }}
          >
            <span className="flex items-center gap-2 [writing-mode:vertical-rl]">
              <MessageSquare className="size-3.5 text-[var(--accent-soft-foreground)]" aria-hidden />
              Run Room{count ? ` · ${count}` : ''}
            </span>
          </button>
        </Tooltip>
      )}
      <RoomPortal>
      {isPhone && <DialogOverlay />}
      {open && (
        <RoomPanel
          {...(isPhone ? {
            'aria-describedby': undefined,
            onOpenAutoFocus: (event: Event) => {
              if (!focusComposer.current) return
              event.preventDefault()
              focusComposer.current = false
              composer.current?.focus()
            },
            onCloseAutoFocus: (event: Event) => {
              event.preventDefault()
              composer.current?.focus()
            },
          } : undefined)}
          id={roomID}
          aria-label="Run Room"
          className={isPhone
            ? 'fixed inset-x-0 top-[calc(var(--title-bar-height)+var(--safe-top))] bottom-0 z-50 flex min-h-0 w-full flex-col overflow-y-auto bg-background'
            : 'flex min-h-0 w-[min(420px,40%)] shrink-0 flex-col overflow-y-auto border-l border-border bg-background'}
        >
          <header className="flex min-h-9 shrink-0 flex-wrap items-center justify-between gap-2 border-b border-border bg-toolbar px-3 py-1.5">
            <RoomTitle className="flex items-center gap-1.5 text-[13px] font-semibold">
              <MessageSquare className="size-3.5 text-[var(--accent-soft-foreground)]" aria-hidden />
              Run Room
            </RoomTitle>
            <div className="flex items-center gap-2">
              <kbd className="text-ui-xs text-muted-foreground">{shortcutLabel('run-room')}</kbd>
              <Button type="button" size="icon" variant="ghost" label="Close Run Room" onClick={closeRoom}><X className="size-4" aria-hidden /></Button>
            </div>
          </header>
          {isPhone && <div className="shrink-0 space-y-2 border-b border-border px-3 py-2 text-[12px]">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex min-w-0 flex-1 items-center gap-1.5">
                {controllerID && <MemberAvatar member={members[controllerID]} fallback={controllerName ?? controllerID} className="size-5 shrink-0 text-[9px]" />}
                <span className="min-w-0 break-words">{controllerName ? <>Controller: {controllerName}{!ownsControl && staleController && <span className="text-muted-foreground"> (last known)</span>}</> : status ? staleController ? 'Controller unknown' : 'No controller' : statusError ? 'Controller unavailable' : 'Loading presence…'}</span>
              </div>
              <ControlButton ownsControl={ownsControl} unavailable={!isLive || controlUnavailable}
                onTakeControl={onTakeControl} onReleaseControl={onReleaseControl} takeover={takeover} />
            </div>
            <div className="flex items-start gap-1.5 text-muted-foreground" aria-live="polite">
              <Users className="mt-0.5 size-3.5 shrink-0" aria-hidden />
              <span className="min-w-0 break-words">{watchers.length ? `Viewing: ${watchers.map((id) => memberLabel(id, members)).join(', ')}` : status ? 'No viewers reported' : 'Viewers not yet available'}</span>
            </div>
            {statusError && <p role="status" className="break-words text-warning-soft-foreground">{status ? 'Presence is stale' : 'Presence unavailable'}: {statusError}</p>}
          </div>}
          {isPhone && <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-1.5 text-[12px] text-muted-foreground">
            <span className={cn('flex items-center gap-1', (status?.protected || run.protected) && 'text-warning-soft-foreground')}><Shield className="size-3.5" aria-hidden />{status?.protected || run.protected ? 'Protected' : 'Unprotected'}</span>
          </div>}
          <div className="min-h-20 flex-1 overflow-y-auto px-3 py-2">
            {error && (
              <div role="alert" aria-label="Room history error" className="mb-2 flex items-start justify-between gap-2 border-b border-state-failed/30 bg-state-failed/10 px-1 py-2 text-[12px] text-state-failed">
                <span className="break-words">{error}</span>
                {initialLoadFailed && <Button type="button" size="sm" variant="ghost" disabled={loading} onClick={() => void load()}>Retry room</Button>}
              </div>
            )}
            {actionError && (
              <div role="alert" aria-label="Run Room action error" className="mb-2 flex items-start justify-between gap-2 border-b border-state-failed/30 bg-state-failed/10 px-1 py-2 text-[12px] text-state-failed">
                <span className="break-words">{actionError}</span>
                <Button type="button" size="sm" variant="ghost" onClick={() => setActionError(runID)}>Dismiss</Button>
              </div>
            )}
            {loading && <p className="py-2 text-[12px] text-muted-foreground">Loading room…</p>}
            {canLoadOlder && <Button type="button" variant="ghost" size="sm" className="mb-2" disabled={loading} onClick={() => void loadOlder()}>Load older</Button>}
            {!loading && !messages.length && !error && (
              <div className="flex items-start gap-2 py-3 text-[12px]">
                <MessageSquare className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
                <div className="space-y-1">
                  <p className="font-medium">No messages yet</p>
                  <p className="text-muted-foreground">Leave a comment for collaborators, or choose Send to agent to queue an instruction. Replies and delivery receipts stay here.</p>
                </div>
              </div>
            )}
            <div className="space-y-2" aria-live="polite">
              {messages.map((message) => <RoomMessageRow key={message.id} message={message} members={members} selfID={selfID} canModerate={isLive && ownsControl} busy={busy} onAnswer={selectQuestion} onDecide={decide} />)}
            </div>
          </div>
          <footer className="shrink-0 border-t border-border bg-toolbar px-3 py-2">
            <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
              <div className="flex max-w-full gap-0.5 rounded-[2px] border border-border p-0.5" role="group" aria-label="Room composer mode">
                <Button type="button" size="sm" variant="ghost" aria-pressed={mode !== 'steer_request'} className={cn(mode !== 'steer_request' && 'bg-selection text-selection-foreground shadow-[inset_0_-2px_0_currentColor]')} onClick={() => { markDraftEdited(); setMode('comment'); setCorrelationID(undefined) }}><MessageSquare className="size-3.5" aria-hidden />Comment</Button>
                <Button type="button" size="sm" variant="ghost" aria-pressed={mode === 'steer_request'} className={cn('text-[var(--accent-soft-foreground)]', mode === 'steer_request' && 'bg-primary/10 shadow-[inset_0_-2px_0_currentColor]')} disabled={Boolean(status?.protected || run.protected)} onClick={() => { markDraftEdited(); setMode('steer_request'); setCorrelationID(undefined) }}><Bot className="size-3.5" aria-hidden />Send to agent</Button>
              </div>
              {mode === 'reply' && <span className="text-[11px] text-muted-foreground">Replying to question</span>}
            </div>
            {(status?.protected || run.protected) && <p className="mb-1 text-[11px] text-state-warn">Protected runs do not accept steering requests.</p>}
            <p id={hintID} className={cn('mb-1 text-[12px]', mode === 'steer_request' ? 'text-[var(--accent-soft-foreground)]' : 'text-muted-foreground')}>
              {mode === 'steer_request' ? 'Queues an instruction for the agent; the controller can approve or deny it.' : mode === 'reply' ? 'Answers the selected question in this room.' : 'Shared with collaborators. Does not send input to the agent.'}
            </p>
            <Textarea
              ref={composer}
              value={body}
              onChange={(event) => { markDraftEdited(); setBody(event.target.value) }}
              onKeyDown={(event) => {
                if (!event.nativeEvent.isComposing && !event.defaultPrevented && (event.metaKey || event.ctrlKey) && event.key === 'Enter') {
                  event.preventDefault()
                  void submit()
                }
              }}
              rows={3}
              placeholder={mode === 'steer_request' ? 'Instruction for the agent…' : mode === 'reply' ? 'Answer the question…' : 'Write a comment…'}
              aria-label="Run Room message"
              aria-describedby={hintID}
              className={cn('w-full max-h-40', mode === 'steer_request' && 'border-primary')}
            />
            <div className="mt-1 flex items-center justify-between gap-2">
              <div className="flex min-w-0 flex-wrap items-center gap-1">
                <label
                  className={cn(
                    'inline-flex items-center rounded-[2px] focus-within:outline-2 focus-within:outline-ring',
                    attachments.length < maxRoomAttachments && !uploading && 'cursor-pointer',
                    (uploading || attachments.length >= maxRoomAttachments) && 'cursor-not-allowed opacity-60',
                  )}
                >
                  <span className="sr-only">Attach image</span>
                  <input
                    type="file"
                    accept="image/png,image/jpeg,image/gif,image/webp"
                    aria-label="Attach image"
                    className="sr-only"
                    disabled={uploading || attachments.length >= maxRoomAttachments}
                    onChange={(event) => {
                      const file = event.target.files?.[0]
                      if (file) void upload(file)
                      event.currentTarget.value = ''
                    }}
                  />
                  <span className="inline-flex min-h-[26px] items-center rounded-[2px] border border-input px-2 text-[12px] hover:bg-toolbar-hover coarse:min-h-10">
                    {uploading ? 'Uploading…' : 'Attach image'}
                  </span>
                </label>
                <span aria-live="polite" className="text-[11px] text-muted-foreground">
                  Attachments: {attachments.length}/{maxRoomAttachments}
                </span>
                {attachments.length >= maxRoomAttachments && (
                  <span className="text-[11px] text-state-warn">Attachment limit reached</span>
                )}
                {attachments.length > 0 && (
                  <Button type="button" size="sm" variant="ghost" onClick={clearAttachments}>
                    Clear attachments
                  </Button>
                )}
              </div>
              <Button type="button" size="sm" disabled={!body.trim() || busy || uploading || (mode === 'steer_request' && Boolean(status?.protected || run.protected))} onClick={() => void submit()}>{busy ? 'Sending…' : pending ? 'Retry' : mode === 'steer_request' ? 'Queue steer' : 'Send'}</Button>
            </div>
          </footer>
        </RoomPanel>
      )}
      </RoomPortal>
    </DialogPrimitive.Root>
  )
}

function RoomMessageRow({ message, members, selfID, canModerate, busy, onAnswer, onDecide }: { message: RoomMessage; members: Record<string, { display_name: string; color?: string }>; selfID: string | null; canModerate: boolean; busy: boolean; onAnswer: (message: RoomMessage) => void; onDecide: (id: string, decision: 'approve' | 'deny') => void }) {
  const isQuestion = message.kind === 'question'
  const isQueued = message.kind === 'steer_request' && message.state === 'queued'
  const anchor = message.anchor
  return (
    <article className="border-l-2 border-border pl-2" data-message-state={message.state}>
      <div className="flex items-baseline justify-between gap-2 text-[11px] text-muted-foreground">
        <span className="truncate font-medium text-foreground">{memberLabel(message.actor_id, members, message.actor_display_name)}{message.actor_id === selfID ? ' (you)' : ''}</span>
        <time dateTime={message.created_at}>{dateLabel(message.created_at)}</time>
      </div>
      <p className="whitespace-pre-wrap break-words text-[13px] leading-5">{message.body}</p>
      {anchor && <div className="mt-1 text-[11px] text-muted-foreground"><span>Anchor: </span><code>{anchor.path || 'transcript'}{anchor.start_line !== undefined ? `:${anchor.start_line}${anchor.end_line && anchor.end_line !== anchor.start_line ? `-${anchor.end_line}` : ''}` : anchor.transcript_offset !== undefined ? ` @${anchor.transcript_offset}` : ''}</code></div>}
      {message.attachments?.length ? <div className="mt-1 space-y-1 text-[11px] text-muted-foreground">{message.attachments.map((attachment) => <div key={attachment}>Image attached: <code className="break-all">{attachment}</code></div>)}</div> : null}
      <div className="mt-1 flex flex-wrap items-center gap-1 text-[11px]">
        <span className={message.state === 'sent' ? 'text-state-success' : message.state === 'uncertain' ? 'text-state-warn' : message.state === 'not_sent' || message.state === 'denied' ? 'text-state-failed' : 'text-muted-foreground'}>{message.state === 'sent' && message.kind !== 'steer_request' ? 'Posted' : message.state === 'uncertain' ? 'Delivery uncertain' : message.state === 'not_sent' ? 'Not sent' : message.state === 'sent' ? 'Sent' : message.state}</span>
        {message.failure?.message && <span className="text-muted-foreground">{message.failure.message}</span>}
        {isQueued && <DeliveryCountdown deliverAfter={message.deliver_after} />}
        {isQuestion && message.state === 'sent' && <Button type="button" size="sm" variant="secondary" onClick={() => onAnswer(message)}>Answer</Button>}
        {isQueued && canModerate && <><Button type="button" size="sm" onClick={() => onDecide(message.id, 'approve')} disabled={busy}>Approve now</Button><Button type="button" size="sm" variant="secondary" onClick={() => onDecide(message.id, 'deny')} disabled={busy}>Deny</Button></>}
      </div>
    </article>
  )
}
