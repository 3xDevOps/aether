import { useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import type * as React from 'react'
import { MessageSquarePlus, X } from '@/components/icons'
import { assistantTriggerID, useAssistantAvailable } from '@/components/assistant/button'
import { AssistantRequestCard, ConfirmationCard } from '@/components/assistant/cards'
import { AssistantComposer } from '@/components/assistant/composer'
import { AssistantFailed, AssistantRestart, AssistantSetup, EmptyConversation } from '@/components/assistant/states'
import { AssistantSheet, AutoApproveSuggestion } from '@/components/assistant/suggestion'
import { assistantRows, Transcript } from '@/components/assistant/transcript'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { PaneHeader } from '@/components/ui/pane-header'
import { Skeleton } from '@/components/ui/skeleton'
import { StateLine } from '@/components/ui/status-dot'
import type { Tone } from '@/components/ui/status-dot'
import { api } from '@/lib/api'
import type { AssistantState } from '@/lib/assistant-types'
import { minReadableWidth, useIsMobile } from '@/lib/breakpoints'
import { useDrag } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { splitterTarget } from '@/lib/keys'
import { cn, focusRing } from '@/lib/utils'
import { requestTitle } from '@/routes/run/session-requests'
import { useStore } from '@/store'
import { assistantWaiting, clampAssistantWidth, maxAssistantWidth, minAssistantWidth } from '@/store/assistant'
import { connectAssistant, refreshAssistant } from '@/store/assistant-stream'
import { useIdentityKnown } from '@/store/hooks'
import { assistantShareView, syncViewContext, trackViewContext } from '@/store/view-context'

const stateWord: Record<AssistantState, string> = {
  setup: 'Needs setup',
  needs_restart: 'Restart needed',
  idle: 'Ready',
  starting: 'Starting',
  running: 'Working',
  failed: 'Failed',
}

const stateTone: Record<AssistantState, Tone> = {
  setup: 'needs-you',
  needs_restart: 'needs-you',
  idle: 'neutral',
  starting: 'working',
  running: 'working',
  failed: 'failed',
}

const connectionWord: Record<string, string> = {
  connecting: 'Connecting…',
  reconnecting: 'Reconnecting…',
  offline: 'Offline. Retrying…',
}

function useAssistantState() {
  const status = useStore((s) => s.assistantStatus)
  const session = useStore((s) => s.acpSessions.assistant)
  const confirmations = useStore((s) => s.assistantConfirmations)
  const running = session?.state?.turn_in_flight ?? status?.state === 'running'
  const waiting = confirmations.length > 0 || (session?.pending.length ?? 0) > 0
  return { status, session, confirmations, running, waiting }
}

function useAnnouncement(): string {
  const { session, confirmations, running } = useAssistantState()
  const [text, setText] = useState('')
  const pending = session?.pending.at(-1)
  const confirmation = confirmations.at(-1)
  const seen = useRef({ pending: pending?.id, confirmation: confirmation?.id, running })
  useEffect(() => {
    const prior = seen.current
    seen.current = { pending: pending?.id, confirmation: confirmation?.id, running }
    if (confirmation && confirmation.id !== prior.confirmation) setText(`Approval needed: ${confirmation.summary}`)
    else if (pending && pending.id !== prior.pending) setText(`${requestTitle(pending)} ${pending.title}`)
    else if (prior.running && !running) setText('The assistant finished its turn')
  }, [pending, confirmation, running])
  return text
}

function AutoApproveLine() {
  const navigate = useStore((s) => s.navigate)
  const sheet = useContext(AssistantSheet)
  const open = () => {
    navigate('settings', {})
    if (sheet) useStore.getState().setAssistantOpen(false)
  }
  return (
    <p className="mx-3 mt-3 flex shrink-0 flex-wrap items-center gap-x-2 text-ui-sm text-muted">
      <span>Acts without asking: launches, saves, and messages or stops your own runs with no card.</span>
      <Button variant="link" size="sm" onClick={open}>Change in Settings</Button>
    </p>
  )
}

function Body({ textarea }: { textarea: React.RefObject<HTMLTextAreaElement | null> }) {
  const { status, session, confirmations, running } = useAssistantState()
  const error = useStore((s) => s.assistantStatusError)
  const setDraft = useStore((s) => s.setAssistantDraft)
  const hasRows = useMemo(() => (session ? assistantRows(session).length > 0 : false), [session])

  if (!status) {
    return error ? (
      <div className="p-4">
        <Callout tone="failed" role="alert" title="The assistant could not be reached" actions={<Button size="sm" variant="secondary" onClick={() => void refreshAssistant(useStore, api)}>Retry</Button>}>
          {error}
        </Callout>
      </div>
    ) : (
      <div className="p-4"><Skeleton className="h-20 w-full" /></div>
    )
  }
  if (status.state === 'setup') return <AssistantSetup status={status} />
  if (status.state === 'needs_restart') return <AssistantRestart />

  const failed = status.state === 'failed'
  const connection = session && connectionWord[session.stream]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {failed && <AssistantFailed error={status.error} />}
      {session?.stream === 'refused' && (
        <Callout tone="failed" role="alert" className="mx-3 mt-3 shrink-0" title="The conversation stream was refused">
          {session.streamError}
        </Callout>
      )}
      {status.auto_approve && <AutoApproveLine />}
      {session?.truncatedBefore && (
        <Callout tone="needs-you" role="status" className="mx-3 mt-3 shrink-0">
          Earlier conversation has expired. Retained items remain.
        </Callout>
      )}
      {!session ? (
        <p role="status" className="p-4 text-ui-sm text-muted">Connecting…</p>
      ) : hasRows ? (
        <Transcript session={session} />
      ) : (
        <EmptyConversation autoApprove={status.auto_approve} onPick={(text) => {
          setDraft(text)
          textarea.current?.focus()
        }} />
      )}
      {connection && session?.stream !== 'refused' && hasRows && (
        <p role="status" className="shrink-0 px-3 pb-1 text-ui-sm text-muted">{connection}</p>
      )}
      <AutoApproveSuggestion busy={running || confirmations.length > 0 || (session?.pending.length ?? 0) > 0} hasRows={hasRows} />
      {!failed && (
        <AssistantComposer
          textarea={textarea}
          running={running}
          dock={(confirmations.length > 0 || (session?.pending.length ?? 0) > 0) && (
            <div className="flex max-h-[40dvh] flex-col gap-2 overflow-y-auto">
              {confirmations.map((confirmation) => <ConfirmationCard key={confirmation.id} confirmation={confirmation} />)}
              {session?.pending.map((request) => <AssistantRequestCard key={request.id} request={request} />)}
            </div>
          )}
        />
      )}
    </div>
  )
}

function Content({ textarea, onClose }: { textarea: React.RefObject<HTMLTextAreaElement | null>; onClose: () => void }) {
  const { status, waiting } = useAssistantState()
  const [confirming, setConfirming] = useState(false)
  const announcement = useAnnouncement()
  const agent = status?.agents.find((item) => item.name === status.agent)?.display_name ?? status?.agent
  const ready = status !== null && status.state !== 'setup' && status.state !== 'needs_restart'
  const onRun = useStore((s) => s.route.name === 'run')
  return (
    <>
      <PaneHeader
        title="Assistant"
        level={2}
        size={onRun ? 'run' : 'view'}
        stateLine={status && (
          <StateLine tone={waiting ? 'needs-you' : stateTone[status.state]} pulse={status.state === 'running' && !waiting}>
            {[agent, waiting ? 'Waiting for you' : stateWord[status.state]].filter(Boolean).join(' · ')}
          </StateLine>
        )}
        actionsLabel="Assistant actions"
        actions={
          <>
            {ready && (
              <Button variant="ghost" size="icon-sm" label="New conversation" onClick={() => setConfirming(true)}>
                <MessageSquarePlus />
              </Button>
            )}
            <Button variant="ghost" size="icon-sm" label="Close assistant" onClick={onClose}>
              <X />
            </Button>
          </>
        }
      />
      <Body textarea={textarea} />
      <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">{announcement}</span>
      {confirming && (
        <Confirm
          title="Start a new conversation?"
          description="This discards the current conversation. What the assistant already did stays done."
          action="Start new conversation"
          pending="Starting…"
          onConfirm={async () => {
            useStore.getState().setAssistantStatus(await api.assistantReset())
            useStore.getState().restartAssistantConversation()
          }}
          onDone={() => {}}
          onClose={() => setConfirming(false)}
        />
      )}
    </>
  )
}

function subscribeResize(notify: () => void) {
  window.addEventListener('resize', notify)
  return () => window.removeEventListener('resize', notify)
}

/** The width of `main` with nothing docked beside it. */
function useMainWidth(): number {
  const sidebar = useStore((s) => (s.sidebarCollapsed ? 0 : s.sidebarWidth))
  const innerWidth = useSyncExternalStore(subscribeResize, () => window.innerWidth)
  return innerWidth - sidebar
}

/** What a floating assistant leaves uncovered at the left of `main`. */
const uncoveredWidth = 240

function docks(main: number): boolean {
  return main - minReadableWidth >= minAssistantWidth
}

export function useAssistantDocked(): boolean {
  const supported = useAssistantAvailable()
  const open = useStore((s) => s.assistantOpen)
  const phone = useIsMobile()
  const main = useMainWidth()
  return supported && open && !phone && docks(main)
}

function DesktopAside({ children, main, onClose }: { children: React.ReactNode; main: number; onClose: () => void }) {
  const floating = !docks(main)
  const room = main - minReadableWidth
  const preferred = useStore((s) => clampAssistantWidth(s.assistantWidth))
  const width = Math.min(preferred, floating ? Math.max(minAssistantWidth, main - uncoveredWidth) : room)
  const maxWidth = Math.min(maxAssistantWidth, room)
  const setWidth = useStore((s) => s.setAssistantWidth)
  const beginDrag = useDrag()

  const startResize = (event: React.PointerEvent) => {
    event.preventDefault()
    const startX = event.clientX
    const drag = beginDrag()
    window.addEventListener('pointermove', (ev) => setWidth(width - (ev.clientX - startX)), { signal: drag.signal })
    for (const end of ['pointerup', 'pointercancel']) {
      window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
    }
  }
  const resizeKey = (event: React.KeyboardEvent) => {
    const next = splitterTarget(event.key, { value: width, min: minAssistantWidth, max: maxWidth, grow: 'ArrowLeft', shrink: 'ArrowRight' })
    if (next === null) return
    event.preventDefault()
    setWidth(next)
  }
  return (
    <aside
      id="assistant"
      aria-label="Assistant"
      tabIndex={-1}
      style={{ width }}
      onKeyDown={(event) => {
        if (!floating || event.key !== 'Escape' || event.defaultPrevented || event.nativeEvent.isComposing || event.keyCode === 229) return
        event.preventDefault()
        onClose()
      }}
      className={cn(
        'flex flex-col border-l border-seam bg-canvas pr-[env(safe-area-inset-right)] outline-none',
        floating ? 'absolute inset-y-0 right-0 z-30 shadow-overlay animate-sheet-right motion-reduce:animate-none' : 'relative h-full min-w-0 shrink-0',
      )}
    >
      {children}
      {!floating && (
        <div
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize assistant"
          aria-controls="assistant"
          aria-valuenow={Math.round(width)}
          aria-valuemin={minAssistantWidth}
          aria-valuemax={maxWidth}
          tabIndex={0}
          onPointerDown={startResize}
          onKeyDown={resizeKey}
          className={cn(focusRing, 'absolute inset-y-0 -left-1 z-10 w-2 cursor-col-resize touch-none hover:bg-hover-chrome coarse:-left-3 coarse:w-6')}
        />
      )}
    </aside>
  )
}

export function AssistantPanel() {
  const phone = useIsMobile()
  const main = useMainWidth()
  const open = useStore((s) => s.assistantOpen)
  const setOpen = useStore((s) => s.setAssistantOpen)
  const generation = useStore((s) => s.assistantGeneration)
  const identity = useStore((s) => s.identityKey)
  const identityKnown = useIdentityKnown()
  const state = useStore((s) => s.assistantStatus?.state)
  const waiting = useStore(assistantWaiting) > 0
  const share = useStore(assistantShareView)
  const supported = useAssistantAvailable()
  const textarea = useRef<HTMLTextAreaElement>(null)
  const shown = supported && open
  const hasSession = supported && state !== undefined && state !== 'setup' && state !== 'needs_restart'
  // Closed, the stream stays up only while the assistant works or waits on an approval.
  const streamable = hasSession && (open || state === 'running' || state === 'starting' || waiting)

  const close = () => {
    setOpen(false)
    if (!phone) queueMicrotask(() => document.getElementById(assistantTriggerID)?.focus())
  }

  useKeybindings('global', supported ? {
    assistant: (event) => {
      // Stopped as well, so a focused terminal or Browser page is not sent the chord.
      event.preventDefault()
      event.stopPropagation()
      if (useStore.getState().assistantOpen) close()
      else setOpen(true)
    },
  } : {})

  // Read once on load and on each open, before the stream decides whether there is a session.
  useEffect(() => {
    if (supported && identityKnown && (open || useStore.getState().assistantStatus === null)) void refreshAssistant(useStore, api)
  }, [supported, identityKnown, open, identity])
  useEffect(() => (streamable ? connectAssistant(useStore, api) : undefined), [streamable, generation, identity])
  useEffect(() => (supported && share ? trackViewContext(useStore) : undefined), [supported, share, identity])
  useEffect(() => (shown && hasSession && share ? syncViewContext(useStore, api) : undefined), [shown, hasSession, share, identity])

  // Opening by hand moves the keyboard in; a panel restored from a reload does not take it.
  const opened = useRef(shown)
  useEffect(() => {
    const was = opened.current
    opened.current = shown
    if (shown && !was && !phone) (textarea.current ?? document.getElementById('assistant'))?.focus()
  }, [shown, phone])

  if (!shown) return null
  const content = <Content textarea={textarea} onClose={close} />
  if (phone) {
    return (
      <Dialog open onOpenChange={setOpen}>
        <DialogContent
          id="assistant"
          variant="side"
          side="right"
          showCloseButton={false}
          aria-describedby={undefined}
          className="w-[min(28rem,100vw)]"
          onOpenAutoFocus={(event) => {
            if (!textarea.current) return
            event.preventDefault()
            textarea.current.focus()
          }}
        >
          <DialogTitle className="sr-only">Assistant</DialogTitle>
          <AssistantSheet.Provider value>{content}</AssistantSheet.Provider>
        </DialogContent>
      </Dialog>
    )
  }
  return <DesktopAside main={main} onClose={close}>{content}</DesktopAside>
}
