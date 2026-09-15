import { useEffect, useMemo, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { cn } from '@/lib/utils'
import type { ControlMetadata } from '@/routes/terminal/attach'
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

function remainingSeconds(deliverAfter: string | undefined, now: number): number {
  if (!deliverAfter) return 0
  return Math.max(0, Math.ceil((new Date(deliverAfter).valueOf() - now) / 1000))
}

export interface RunRoomProps {
  run: Run
  client?: Api
  selfID: string | null
  control?: ControlMetadata
  onTakeControl: (takeover: boolean) => void
  onReleaseControl: () => void
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

export function RunRoom({ run, client = api, selfID, control, onTakeControl, onReleaseControl }: RunRoomProps) {
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
  const initializePagination = useStore((state) => state.initializeRoomPagination)
  const setLoading = useStore((state) => state.setRoomLoading)
  const setError = useStore((state) => state.setRoomError)
  const setActionError = useStore((state) => state.setRoomActionError)
  const setPage = useStore((state) => state.setRoomPage)
  const upsert = useStore((state) => state.upsertRoomMessage)
  const setStatus = useStore((state) => state.setRoomStatus)
  const [open, setOpen] = useState(false)
  const [confirmTakeover, setConfirmTakeover] = useState(false)
  const [mode, setMode] = useState<'comment' | 'steer_request' | 'reply'>('comment')
  const [body, setBody] = useState('')
  const [correlationID, setCorrelationID] = useState<string | undefined>()
  const [attachments, setAttachments] = useState<string[]>([])
  const [uploading, setUploading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [pending, setPending] = useState<PendingPost | undefined>()
  const [initialLoadFailed, setInitialLoadFailed] = useState(false)
  const [clock, setClock] = useState(() => Date.now())
  const roomLoadKey = useRef<string | null>(null)
  const draftVersion = useRef(0)
  const composer = useRef<HTMLTextAreaElement>(null)
  const questions = useMemo(() => unansweredQuestions(messages), [messages])
  const queued = useMemo(() => queuedSteers(messages), [messages])
  const count = questions.length + Math.max(status?.queued_steers ?? 0, queued.length)
  const controller = status?.controller
  const watchers = status?.watchers ?? []
  const controllerName = controller ? memberLabel(controller.member_id, members) : null
  const ownsControl = Boolean(controller && selfID && controller.member_id === selfID && control?.has_control)
  const isLive = run.status === 'running' || run.status === 'needs-attention'
  const canLoadOlder = pagination?.initialized === true && !pagination.exhausted && nextBefore !== undefined

  const load = async () => {
    // Define the list before the first await. A room event arriving while this
    // request is in flight must see an initialized list and reconcile it.
    initializePagination(runID)
    setLoading(runID, true)
    setError(runID)
    setInitialLoadFailed(false)
    try {
      const [page, nextStatus] = await Promise.all([
        client.runRoomList({ workspace_id: workspaceID, run_id: runID, limit: 100 }),
        client.runRoomStatus({ workspace_id: workspaceID, run_id: runID }),
      ])
      setPage(runID, page.messages, page.next_before)
      setStatus(runID, nextStatus)
    } catch (cause) {
      setInitialLoadFailed(true)
      setError(runID, cause instanceof Error ? cause.message : String(cause))
    } finally {
      setLoading(runID, false)
    }
  }

  // Reconcile the newest page periodically while open. If a burst is larger
  // than one page, walk the server's cursors until this cached history is
  // reached or the server reports exhaustion. Each cursor is visited once so
  // a malformed response cannot create an unbounded loop.
  const refreshMessages = async () => {
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
        setPage(runID, page.messages, page.next_before, mergeAsOlder)
        if (overlapsCached(page.messages, cached) || !page.next_before || visited.has(page.next_before)) break
        visited.add(page.next_before)
        before = page.next_before
      }
      setError(runID)
    } catch (cause) {
      setError(runID, cause instanceof Error ? cause.message : String(cause))
    }
  }

  const loadOlder = async () => {
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
      setPage(runID, page.messages, page.next_before, true)
    } catch (cause) {
      setError(runID, cause instanceof Error ? cause.message : String(cause))
    } finally {
      setLoading(runID, false)
    }
  }
  // Control status is ephemeral and can change without a room message. Keep
  // this refresh separate from `load`: it must never reset the message list,
  // composer, attachments, or pending post.
  const refreshStatus = async () => {
    try {
      setStatus(runID, await client.runRoomStatus({ workspace_id: workspaceID, run_id: runID }))
    } catch {
      // Status is best-effort while the room remains open. The existing room
      // contents and composer remain useful if a bounded poll misses.
    }
  }

  const controlKey = control
    ? `${control.control_session_id}:${control.control_generation}:${control.has_control ? 'held' : 'mirror'}`
    : 'none'
  const previousControlKey = useRef(controlKey)

  useEffect(() => {
    if (!open) {
      previousControlKey.current = controlKey
      return
    }
    const changed = previousControlKey.current !== controlKey
    previousControlKey.current = controlKey
    if (changed) void refreshStatus()
    const timer = window.setInterval(() => void refreshStatus(), 5000)
    return () => window.clearInterval(timer)
    // The key captures the only control metadata transitions that affect room
    // status; status refresh itself intentionally does not reload messages.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, runID, workspaceID, controlKey, client])


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

  useEffect(() => {
    if (!open) return
    const timer = window.setInterval(() => setClock(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [open])

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
        ...(action.kind === 'steer_request' && control?.has_control && control.control_generation > 0
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


  return (
    <>
      {!open && (
        <button
          type="button"
          aria-label="Open Run Room"
          aria-expanded={false}
          className="absolute inset-y-0 right-0 z-30 flex w-8 items-center justify-center border-l border-border bg-toolbar text-[11px] font-medium text-muted-foreground shadow-sm hover:bg-toolbar-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          onClick={() => setOpen(true)}
        >
          <span className="[writing-mode:vertical-rl]">Run Room{count ? ` · ${count}` : ''}</span>
        </button>
      )}
      {open && (
        <aside
          aria-label="Run Room"
          className={isPhone
            ? 'fixed inset-x-0 top-[calc(var(--title-bar-height)+var(--safe-top))] bottom-0 z-40 flex min-h-0 w-full flex-col bg-background shadow-xl'
            : 'absolute inset-y-0 right-0 z-40 flex min-h-0 w-[min(420px,calc(100vw-2rem))] flex-col border-l border-border bg-background shadow-xl'}
        >
          <header className="flex min-h-12 items-center justify-between gap-2 border-b border-border bg-toolbar px-3 py-2">
            <div className="min-w-0">
              <h2 className="truncate text-[13px] font-semibold">Run Room</h2>
              <div className="flex min-w-0 items-center gap-1 text-[11px] text-muted-foreground" aria-live="polite">
                <span className="truncate">{watchers.length} watching</span>
                {watchers.slice(0, 4).map((id) => <MemberAvatar key={id} member={members[id]} fallback={id} className="size-4 text-[8px]" />)}
                {watchers.length > 4 && <span>+{watchers.length - 4}</span>}
                <span aria-hidden>·</span>
                <span className="truncate">{controllerName ? `Controller: ${controllerName}` : 'No controller'}</span>
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-1">
              {ownsControl ? <Button type="button" size="sm" variant="outline" disabled={!isLive} onClick={onReleaseControl}>Release control</Button> : <Button type="button" size="sm" disabled={!isLive} onClick={() => controller && !ownsControl ? setConfirmTakeover(true) : onTakeControl(false)}>Take control</Button>}
              <Button type="button" size="icon" variant="ghost" aria-label="Close Run Room" onClick={() => setOpen(false)}>×</Button>
            </div>
          </header>
          <div className="flex flex-wrap items-center gap-1 border-b border-border px-3 py-1.5 text-[11px] text-muted-foreground">
            <span className={status?.protected || run.protected ? 'text-state-warn' : 'text-state-success'}>{status?.protected || run.protected ? 'Protected' : 'Unprotected'}</span>
            {status?.watchers?.length ? <span className="truncate">{status.watchers.map((id) => memberLabel(id, members)).join(', ')}</span> : <span>No watchers reported</span>}
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2">
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
            {!loading && !messages.length && !error && <p className="py-8 text-center text-[12px] text-muted-foreground">No messages yet. Add a comment for the people watching this run.</p>}
            <div className="space-y-2" aria-live="polite">
              {messages.map((message) => <RoomMessageRow key={message.id} message={message} members={members} selfID={selfID} canModerate={ownsControl} now={clock} busy={busy} onAnswer={selectQuestion} onDecide={decide} />)}
            </div>
          </div>
          <footer className="border-t border-border bg-toolbar px-3 py-2">
            <div className="mb-1 flex items-center justify-between gap-2">
              <div className="flex gap-1" role="group" aria-label="Room composer mode">
                <Button type="button" size="sm" variant={mode === 'comment' || mode === 'reply' ? 'secondary' : 'ghost'} onClick={() => { markDraftEdited(); setMode('comment'); setCorrelationID(undefined) }}>Comment</Button>
                <Button type="button" size="sm" variant={mode === 'steer_request' ? 'secondary' : 'ghost'} disabled={Boolean(status?.protected || run.protected)} onClick={() => { markDraftEdited(); setMode('steer_request'); setCorrelationID(undefined) }}>Send to agent</Button>
              </div>
              {mode === 'reply' && <span className="text-[11px] text-muted-foreground">Replying to question</span>}
            </div>
            {(status?.protected || run.protected) && <p className="mb-1 text-[11px] text-state-warn">Protected runs do not accept steering requests.</p>}
            <textarea
              ref={composer}
              value={body}
              onChange={(event) => { markDraftEdited(); setBody(event.target.value) }}
              onKeyDown={(event) => {
                if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
                  event.preventDefault()
                  void submit()
                }
              }}
              rows={3}
              placeholder={mode === 'steer_request' ? 'Instruction for the agent…' : mode === 'reply' ? 'Answer the question…' : 'Write a comment…'}
              aria-label="Run Room message"
              className="w-full resize-y border border-input bg-background px-2 py-1.5 text-[13px] leading-5 outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring"
            />
            <div className="mt-1 flex items-center justify-between gap-2">
              <div className="flex min-w-0 flex-wrap items-center gap-1">
                <label
                  className={cn(
                    'inline-flex items-center',
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
                  <span className="rounded-[2px] border border-input px-2 py-1 text-[11px] hover:bg-toolbar-hover">
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
        </aside>
      )}
      <Dialog open={confirmTakeover} onOpenChange={setConfirmTakeover}>
        <DialogContent>
          <DialogHeader><DialogTitle>Take control of this run?</DialogTitle><DialogDescription>{controllerName ? `${controllerName} currently controls the run. Taking control will end their writable session and notify them.` : 'Taking control will make this tab the run controller.'}</DialogDescription></DialogHeader>
          <DialogFooter><Button type="button" variant="outline" onClick={() => setConfirmTakeover(false)}>Cancel</Button><Button type="button" disabled={!isLive} onClick={() => { setConfirmTakeover(false); onTakeControl(true) }}>Take control</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function RoomMessageRow({ message, members, selfID, canModerate, now, busy, onAnswer, onDecide }: { message: RoomMessage; members: Record<string, { display_name: string; color?: string }>; selfID: string | null; canModerate: boolean; now: number; busy: boolean; onAnswer: (message: RoomMessage) => void; onDecide: (id: string, decision: 'approve' | 'deny') => void }) {
  const remaining = remainingSeconds(message.deliver_after, now)
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
        {isQueued && <span className="text-state-warn">{remaining ? `${remaining}s before delivery` : 'Ready for delivery'}</span>}
        {isQuestion && message.state === 'sent' && <Button type="button" size="sm" variant="outline" onClick={() => onAnswer(message)}>Answer</Button>}
        {isQueued && canModerate && <><Button type="button" size="sm" onClick={() => onDecide(message.id, 'approve')} disabled={busy}>Approve now</Button><Button type="button" size="sm" variant="outline" onClick={() => onDecide(message.id, 'deny')} disabled={busy}>Deny</Button></>}
      </div>
    </article>
  )
}
