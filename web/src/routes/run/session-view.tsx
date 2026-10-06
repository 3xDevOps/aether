import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { useShallow } from 'zustand/react/shallow'
import { VList, type VListHandle } from 'virtua'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import { runLabel as labelOfRun } from '@/lib/status'
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
import { loadMessagePage, messageScopeKey } from '@/store/messages'
import type { RunRecord } from '@/store/runs'
import { liveLabel, rowsOfTurn, type SessionRow } from '@/store/session-rows'
import { deliveryOf, readSessionLog, rowsForRun, type AcpSession } from '@/store/sessions'
import { loadOlderItems } from '@/store/session-stream'

const emptyRoom: RoomMessage[] = []
const emptyIDs: string[] = []
const bottomSlack = 24
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

/** The item log names no author, so a person's message is matched to the
 * room message that carried it; one not logged yet is still on its way. */
function withSteers(rows: SessionRow[], room: RoomMessage[]): SessionRow[] {
  const steers = room.filter((m) => m.kind === 'steer_request')
  const used = new Set<string>()
  const matched = rows.map((row) => {
    if (row.kind !== 'user') return row
    const steer = steers.find((m) => !used.has(m.id) && m.body.trim() === row.body.trim() &&
      Date.parse(m.created_at) <= Date.parse(row.at) + sameMessageWindow)
    if (!steer) return row
    used.add(steer.id)
    return { ...row, authorID: steer.actor_id }
  })
  const waiting = steers.filter((m) => !used.has(m.id) && m.state !== 'cancelled')
    .map((m): SessionRow => ({ kind: 'user', id: m.id, at: m.created_at, body: m.body, authorID: m.actor_id, ...deliveryOf(m), ...(m.state === 'sent' ? { delivery: 'Queued' } : {}) }))
  return byTime(matched, waiting)
}

function enhancedRows(session: AcpSession | undefined): SessionRow[] {
  if (!session) return []
  const rows = session.turns.flatMap(rowsOfTurn)
  const open = session.turns.at(-1)
  const live = session.live ? liveLabel(open) : null
  if (live) rows.push({ kind: 'live', id: 'live', at: open!.items.at(-1)!.time, label: live })
  return rows
}

function useRows(run: RunRecord, enhanced: boolean, showMessages: boolean): { rows: SessionRow[]; session?: AcpSession } {
  const session = useStore((s) => (enhanced ? s.acpSessions[run.id] : undefined))
  const events = useStore((s) => (enhanced ? undefined : s.sessionLogs[run.id]?.events))
  const room = useStore((s) => s.roomMessages[run.id] ?? emptyRoom)
  const members = useStore((s) => s.members)
  const messageIDs = useStore((s) => s.messageLists[messageScopeKey({ kind: 'run', workspaceID: run.workspace_id, runID: run.id })]?.ids ?? emptyIDs)
  const messages = useStore(useShallow((s) => messageIDs.map((id) => s.runMessages[id]!)))
  const rows = useMemo(() => {
    const base = enhanced
      ? withSteers(enhancedRows(session), room)
      : rowsForRun({ run, events: events ?? [], room, memberName: (id) => members[id]?.display_name ?? id })
    if (!showMessages) return base
    return byTime(base, messages.map((m) => ({ kind: 'agent-message', id: `mail:${m.id}`, at: m.created_at, messageID: m.id })))
  }, [enhanced, session, room, run, events, members, messages, showMessages])
  return { rows, session }
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

export function SessionView({ run, agent, room, nav, active, textarea, onComposing, shells, switchable }: {
  run: RunRecord
  agent: AgentTerminal
  room: RunRoom
  nav: RunNavigation
  active: boolean
  textarea: React.RefObject<HTMLTextAreaElement | null>
  onComposing: (composing: boolean) => void
  shells: RunShells
  switchable: boolean
}) {
  const enhanced = run.acp === true
  const [showMessages, setShowMessages] = useState(true)
  const { rows, session } = useRows(run, enhanced, showMessages)
  const expanded = useStore((s) => s.expandedRows[run.id])
  const log = useStore((s) => (enhanced ? undefined : s.sessionLogs[run.id]))
  const roomError = useStore((s) => s.roomError[run.id])
  const roomLoading = useStore((s) => s.roomLoading[run.id] === true)
  const runs = useStore((s) => s.runs)
  const messageMap = useStore((s) => s.runMessages)
  const hasMessages = useStore((s) => (s.messageLists[messageScopeKey({ kind: 'run', workspaceID: run.workspace_id, runID: run.id })]?.ids.length ?? 0) > 0)
  const olderRoom = useStore((s) => {
    const page = s.roomPagination[run.id]
    return Boolean(page?.initialized && !page.exhausted && s.roomNextBefore[run.id] !== undefined)
  })
  const older = enhanced ? Boolean(session?.more) : olderRoom
  const flat = useMemo(() => flattenRows(rows, expanded), [rows, expanded])
  const list = useRef<VListHandle>(null)
  const pinned = useRef(true)
  const lastInput = useRef(0)
  const viewport = useRef<HTMLDivElement>(null)
  const [shift, setShift] = useState(false)
  const [focused, setFocused] = useState<number | null>(null)
  const announcement = useAnnouncement(run, rows, session)

  const ctx: RowContext = useMemo(() => ({
    runID: run.id,
    task: run.task,
    ownerID: run.member_id,
    agent,
    nav,
    expanded,
    messages: messageMap,
    runLabel: (id: string) => (id === run.id ? 'This run' : runs[id] ? labelOfRun(runs[id]) : id),
  }), [run.id, run.task, run.member_id, agent, nav, expanded, messageMap, runs])

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
  useEffect(() => {
    if (shift) setShift(false)
  }, [flat, shift])

  const live = useStore((s) => s.connection === 'live')
  useEffect(() => {
    if (live && !enhanced) void readSessionLog(useStore, api, run)
  }, [live, enhanced, run.id, run.workspace_id])
  useEffect(() => {
    void loadMessagePage(useStore, api, { kind: 'run', workspaceID: run.workspace_id, runID: run.id })
  }, [run.workspace_id, run.id])

  const pendingCount = session?.pending.length ?? 0
  const hadPending = useRef(false)
  useEffect(() => {
    if (pendingCount === 0 && hadPending.current && active && document.activeElement === document.body) textarea.current?.focus()
    hadPending.current = pendingCount > 0
  }, [pendingCount, active, textarea])

  const loadOlder = () => {
    pinned.current = false
    setShift(true)
    if (enhanced) void loadOlderItems(useStore, api, run.id)
    else void room.loadOlder()
  }
  const loadingOlder = enhanced ? Boolean(session?.olderLoading) : roomLoading
  const olderError = enhanced ? session?.olderError : undefined

  const dock = enhanced && session && session.pending.length > 0 && (
    <RequestDock
      runID={run.id}
      requests={session.pending}
      agent={agent}
      className="max-md:max-h-[40dvh]"
    />
  )

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div
        ref={viewport}
        className="relative min-h-0 flex-1"
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
        {hasMessages && (
          <div className="absolute top-2 right-4 z-10">
            <Button size="sm" variant="ghost" aria-pressed={!showMessages} onClick={() => setShowMessages(!showMessages)}>
              {showMessages ? 'Hide agent messages' : 'Show agent messages'}
            </Button>
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
          }}
        >
          {(item: FlatRow | null, index: number) => item === null ? (
            <div key="older" className="mx-auto flex w-full max-w-[736px] justify-center px-4 pt-4">
              <Button size="sm" variant="ghost" disabled={loadingOlder} onClick={loadOlder}>
                {loadingOlder ? 'Loading…' : enhanced ? 'Show earlier' : 'Show older messages'}
              </Button>
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
              <TimelineRow flat={item} ctx={ctx} />
            </div>
          )}
        </VList>
      </div>
      {enhanced && (
        <div className="mx-auto w-full max-w-[736px] px-4 pb-2 empty:hidden">
          <SessionFailureCallout run={run} session={session} switchable={switchable} shells={shells} nav={nav} />
        </div>
      )}
      <Composer run={run} agent={agent} room={room} textarea={textarea} onFocusChange={onComposing} dock={dock || undefined} />
      <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">{announcement}</span>
    </div>
  )
}

