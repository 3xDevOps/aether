import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { useShallow } from 'zustand/react/shallow'
import { VList, type VListHandle } from 'virtua'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import type { RoomMessage } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { Composer } from '@/routes/run/composer'
import type { RunNavigation } from '@/routes/run/header'
import type { RunRoom } from '@/routes/run/room'
import { SessionFailureCallout } from '@/routes/run/session-failure'
import { RequestDock, requestTitle } from '@/routes/run/session-requests'
import { flattenRows, rowGap, TimelineRow, type FlatRow, type RowContext } from '@/routes/run/session-rows'
import type { RunShells } from '@/routes/run/shells'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { messageScopeKey, useMessageList } from '@/store/messages'
import type { RunRecord } from '@/store/runs'
import { liveActivity, rowsOfTurn, type SessionRow } from '@/store/session-rows'
import { deliveryOf, readSessionLog, roomImages, rowsForRun, type AcpSession } from '@/store/sessions'
import { loadOlderItems } from '@/store/session-stream'

const emptyRoom: RoomMessage[] = []
const emptyIDs: string[] = []
const bottomSlack = 24
const historySlack = 240
const userScrollWindow = 600
const sameMessageWindow = 120_000

function byTime(rows: SessionRow[], extra: SessionRow[]): SessionRow[] {
  if (extra.length === 0) return rows
  const out: SessionRow[] = []
  const sorted = [...extra].sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
  let next = 0
  for (const row of rows) {
    while (next < sorted.length && Date.parse(sorted[next]!.at) < Date.parse(row.at)) out.push(sorted[next++]!)
    out.push(row)
  }
  return [...out, ...sorted.slice(next)]
}

function withSteers(rows: SessionRow[], room: RoomMessage[]): SessionRow[] {
  const steers = room.filter((m) => m.kind === 'steer_request')
  const used = new Set<string>()
  const matched = rows.map((row) => {
    if (row.kind !== 'user') return row
    const imageMessageID = row.images?.[0]?.messageID
    const sameImageMessage = imageMessageID !== undefined && row.images!.every((image) => image.messageID === imageMessageID)
    const steer = imageMessageID
      ? sameImageMessage && steers.find((m) => m.id === imageMessageID)
      : steers.find((m) => !used.has(m.id) && !m.attachments?.length && m.body.trim() === row.body.trim() &&
        Date.parse(m.created_at) <= Date.parse(row.at) + sameMessageWindow)
    const messageID = steer ? steer.id : sameImageMessage ? imageMessageID : undefined
    if (messageID) {
      if (used.has(messageID)) return null
      used.add(messageID)
    }
    if (!steer) return messageID ? { ...row, id: messageID } : row
    return { ...row, id: steer.id, body: steer.body, images: roomImages(steer) ?? row.images, authorID: steer.actor_id, ...deliveryOf(steer) }
  }).filter((row): row is SessionRow => row !== null)
  const waiting = steers.filter((m) => !used.has(m.id) && m.state !== 'cancelled')
    .map((m): SessionRow => ({ kind: 'user', id: m.id, at: m.created_at, body: m.body, images: roomImages(m), authorID: m.actor_id, ...deliveryOf(m) }))
  return byTime(matched, waiting)
}

function enhancedRows(session: AcpSession | undefined): SessionRow[] {
  if (!session) return []
  const rows = session.turns.flatMap(rowsOfTurn)
  const open = session.turns.at(-1)
  const live = session.live ? liveActivity(open) : null
  if (live) rows.push({ kind: 'live', id: 'live', at: open!.items.at(-1)!.time, ...live })
  return rows
}

function useRows(run: RunRecord, enhanced: boolean, showMessages: boolean): { rows: SessionRow[]; session?: AcpSession; missingRoomImages: boolean } {
  const session = useStore((s) => (enhanced ? s.acpSessions[run.id] : undefined))
  const events = useStore((s) => (enhanced ? undefined : s.sessionLogs[run.id]?.events))
  const room = useStore((s) => s.roomMessages[run.id] ?? emptyRoom)
  const members = useStore((s) => s.members)
  const paused = useStore((s) => s.pausedRuns[run.id] ?? run.paused ?? false)
  const messageIDs = useStore((s) => s.messageLists[messageScopeKey({ kind: 'run', workspaceID: run.workspace_id, runID: run.id })]?.ids ?? emptyIDs)
  const messages = useStore(useShallow((s) => messageIDs.map((id) => s.runMessages[id]!)))
  const rows = useMemo(() => {
    const base = enhanced
      ? withSteers(enhancedRows(session), room)
      : rowsForRun({ run, events: events ?? [], room, paused, memberName: (id) => members[id]?.display_name ?? id })
    if (!showMessages) return base
    const reported = messages.some((m) => m.kind === 'report')
    return byTime(reported ? base.filter((row) => row.kind !== 'event' || !row.report) : base, messages.map((m) => ({ kind: 'agent-message', id: `mail:${m.id}`, at: m.created_at, messageID: m.id })))
  }, [enhanced, session, room, run, events, members, messages, showMessages, paused])
  const roomIDs = new Set(room.map((m) => m.id))
  const missingRoomImages = enhanced && rows.some((row) => row.kind === 'user' && row.images?.some((image) => !roomIDs.has(image.messageID)))
  return { rows, session, missingRoomImages }
}

function useAnnouncement(run: RunRecord, rows: SessionRow[], session: AcpSession | undefined): string {
  const [text, setText] = useState('')
  const finished = [...rows].reverse().find((row) => row.kind === 'finished')
  const pending = session?.pending.at(-1)
  const seen = useRef({ finished: finished?.id, pending: pending?.id, status: run.status })
  useEffect(() => {
    const prior = seen.current
    seen.current = { finished: finished?.id, pending: pending?.id, status: run.status }
    if (pending && pending.id !== prior.pending) setText(`${requestTitle(pending)} ${pending.title}`)
    else if (finished && finished.id !== prior.finished && finished.kind === 'finished') setText(`The agent’s turn ended: ${finished.text}`)
    else if (run.status !== prior.status) setText(`Run is now ${run.status.replace('-', ' ')}`)
  }, [finished, pending, run.status])
  return text
}

export function SessionView({ run, agent, room, nav, active, textarea, focusComposer, onComposing, shells, switchable }: {
  run: RunRecord
  agent: AgentTerminal
  room: RunRoom
  nav: RunNavigation
  active: boolean
  textarea: React.RefObject<HTMLTextAreaElement | null>
  focusComposer: boolean
  onComposing: (composing: boolean) => void
  shells: RunShells
  switchable: boolean
}) {
  const enhanced = run.acp === true
  const [showMessages, setShowMessages] = useState(true)
  const { rows, session, missingRoomImages } = useRows(run, enhanced, showMessages)
  const expanded = useStore((s) => s.expandedRows[run.id])
  const log = useStore((s) => (enhanced ? undefined : s.sessionLogs[run.id]))
  const roomError = useStore((s) => s.roomError[run.id])
  const roomLoading = useStore((s) => s.roomLoading[run.id] === true)
  const hasMessages = useStore((s) => (s.messageLists[messageScopeKey({ kind: 'run', workspaceID: run.workspace_id, runID: run.id })]?.ids.length ?? 0) > 0)
  const olderRoom = useStore((s) => {
    const page = s.roomPagination[run.id]
    return Boolean(page?.initialized && !page.exhausted && s.roomNextBefore[run.id] !== undefined)
  })
  const older = olderRoom || (enhanced && Boolean(session?.more))
  useEffect(() => {
    if (missingRoomImages && olderRoom && !roomLoading && !roomError) void room.loadOlder()
  }, [missingRoomImages, olderRoom, roomLoading, roomError, room.loadOlder])
  const flat = useMemo(() => flattenRows(rows, expanded), [rows, expanded])
  const list = useRef<VListHandle>(null)
  const pinned = useRef(true)
  const lastInput = useRef(-Infinity)
  const viewport = useRef<HTMLDivElement>(null)
  const previous = useRef({ flat, older })
  const before = previous.current
  const prepended = before.flat.length > 0 && flat.length > before.flat.length &&
    flat[flat.length - before.flat.length]?.key === before.flat[0]!.key
  const shift = prepended || (before.older && !older && flat.length === before.flat.length)
  useLayoutEffect(() => {
    previous.current = { flat, older }
  })
  const [focused, setFocused] = useState<number | null>(null)
  const announcement = useAnnouncement(run, rows, session)

  const latest = useRef({ agent, nav })
  latest.current = { agent, nav }
  const ctx: RowContext = useMemo(() => ({
    runID: run.id,
    task: run.task,
    ownerID: run.member_id,
    hasAgentTerminal: agent.hasAgentTerminal,
    openTerminal: () => {
      const { agent: current, nav: to } = latest.current
      to.go('terminal')
      if (!current.localControl && !current.controlUnavailable) current.session.takeControl()
    },
    go: (view) => latest.current.nav.go(view),
    reveal: (id) => latest.current.nav.reveal(id),
  }), [run.id, run.task, run.member_id, agent.hasAgentTerminal])

  const count = flat.length + (older ? 1 : 0)
  const markInput = () => {
    lastInput.current = performance.now()
  }
  const follow = useRef(() => {})
  follow.current = () => {
    if (active && pinned.current && count > 0) list.current?.scrollToIndex(count - 1, { align: 'end' })
  }
  useLayoutEffect(() => follow.current(), [active, count])
  useEffect(() => {
    const node = viewport.current
    if (!node) return
    let frame = 0
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => follow.current())
    })
    observer.observe(node)
    return () => {
      observer.disconnect()
      cancelAnimationFrame(frame)
    }
  }, [])

  const live = useStore((s) => s.connection === 'live')
  useEffect(() => {
    if (live && !enhanced) void readSessionLog(useStore, api, run)
  }, [live, enhanced, run.id, run.workspace_id])
  useMessageList(useStore, api, { kind: 'run', workspaceID: run.workspace_id, runID: run.id })

  const pendingCount = session?.pending.length ?? 0
  const hadPending = useRef(false)
  useEffect(() => {
    if (pendingCount === 0 && hadPending.current && active && document.activeElement === document.body) textarea.current?.focus()
    hadPending.current = pendingCount > 0
  }, [pendingCount, active, textarea])

  const loadOlder = () => {
    pinned.current = false
    if (enhanced && session?.more) void loadOlderItems(useStore, api, run.id)
    if (olderRoom) void room.loadOlder()
  }
  const loadingOlder = roomLoading || (enhanced && Boolean(session?.olderLoading))
  const olderError = enhanced ? session?.olderError : undefined
  const loadEarlier = () => {
    const handle = list.current
    if (!active || !enhanced || !older || loadingOlder || olderError || roomError || !handle || handle.viewportSize === 0) return
    if (handle.scrollOffset <= historySlack && (!pinned.current || handle.scrollSize <= handle.viewportSize + bottomSlack)) loadOlder()
  }
  useEffect(() => {
    const frame = requestAnimationFrame(loadEarlier)
    return () => cancelAnimationFrame(frame)
  }, [active, enhanced, older, loadingOlder, olderError, roomError, flat])

  const dock = enhanced && session && session.pending.length > 0 && (
    <RequestDock
      run={run}
      requests={session.pending}
      agent={agent}
      className="max-md:max-h-[40dvh]"
    />
  )

  return (
    <div className="flex h-full min-h-0 flex-col">
      {hasMessages && (
        <div className="mx-auto flex w-full max-w-[736px] shrink-0 justify-end px-4 pt-1">
          <Button size="sm" variant="ghost" aria-pressed={!showMessages} onClick={() => setShowMessages(!showMessages)}>
            {showMessages ? 'Hide agent messages' : 'Show agent messages'}
          </Button>
        </div>
      )}
      {session?.truncatedBefore && (
        <Callout tone="needs-you" role="status" className="mx-4 mt-2 shrink-0">
          Earlier Enhanced history has expired. Retained items and the current session remain available.
        </Callout>
      )}
      <div
        ref={viewport}
        tabIndex={-1}
        className="relative min-h-0 flex-1 outline-none"
        onWheelCapture={markInput}
        onTouchMoveCapture={markInput}
        onKeyDownCapture={markInput}
        onPointerDownCapture={markInput}
      >
        {(log?.error || roomError || olderError) && (
          <div className="mx-auto flex max-w-[736px] flex-col gap-2 px-4 pt-3">
            {log?.error && (
              <Callout tone="failed" title="The session history could not be read" actions={<Button size="sm" variant="secondary" onClick={() => void readSessionLog(useStore, api, run)}>Retry</Button>}>
                {log.error}
              </Callout>
            )}
            {olderError && (
              <Callout tone="failed" title="Earlier items could not be read" actions={<Button size="sm" variant="secondary" onClick={loadOlder}>Retry</Button>}>
                {olderError}
              </Callout>
            )}
            {roomError && (
              <Callout tone="failed" title="Messages could not be read" actions={<Button size="sm" variant="secondary" onClick={() => void room.refresh()}>Retry</Button>}>
                {roomError}
              </Callout>
            )}
          </div>
        )}
        <VList
          ref={list}
          role="log"
          aria-live="off"
          aria-label="Session"
          className="h-full"
          data={older ? [null, ...flat] : flat}
          shift={shift}
          keepMounted={focused !== null && focused < count ? [focused] : undefined}
          onScroll={(offset) => {
            const handle = list.current
            if (!handle) return
            // Only the person's own scrolling unpins; resizes and row measurements move the offset too.
            if (offset + handle.viewportSize >= handle.scrollSize - bottomSlack) pinned.current = true
            else if (performance.now() - lastInput.current < userScrollWindow) pinned.current = false
            loadEarlier()
          }}
          onResize={loadEarlier}
        >
          {(item: FlatRow | null, index: number) => item === null ? (
            <div key="older" className="mx-auto flex w-full max-w-[736px] justify-center px-4 pt-4">
              {enhanced ? (
                <span role="status" className="flex h-7 items-center text-ui-sm text-muted">
                  {loadingOlder ? 'Loading earlier…' : ''}
                </span>
              ) : (
                <Button size="sm" variant="ghost" disabled={loadingOlder} onClick={loadOlder}>
                  {loadingOlder ? 'Loading…' : 'Show older messages'}
                </Button>
              )}
            </div>
          ) : (
            <div
              key={item.key}
              role="article"
              aria-posinset={index + (older ? 0 : 1)}
              aria-setsize={flat.length}
              onFocus={() => setFocused(index)}
              className={cn('mx-auto w-full max-w-[736px] px-4', rowGap(item), index === count - 1 && 'pb-4', index === 0 && 'pt-4')}
            >
              <TimelineRow depth={item.depth} row={item.depth === 0 ? item.row : undefined} entry={item.depth === 0 ? undefined : item.entry} ctx={ctx} />
            </div>
          )}
        </VList>
      </div>
      {enhanced && (
        <div className="mx-auto w-full max-w-[736px] px-4 pb-2 empty:hidden">
          <SessionFailureCallout run={run} session={session} switchable={switchable} shells={shells} nav={nav} />
        </div>
      )}
      <Composer run={run} agent={agent} room={room} textarea={textarea} autoFocus={focusComposer} onFocusChange={onComposing} dock={dock || undefined} onEscape={() => viewport.current?.focus()} />
      <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">{announcement}</span>
    </div>
  )
}

