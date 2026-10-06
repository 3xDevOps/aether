import { useEffect, useLayoutEffect, useMemo, useRef } from 'react'
import type * as React from 'react'
import { VList, type VListHandle } from 'virtua'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { RelativeTime } from '@/components/ui/relative-time'
import { RequestCard } from '@/components/ui/request-card'
import { EventRow, MessageRow, WorkEntry, WorkGroup } from '@/components/ui/timeline'
import { api } from '@/lib/api'
import type { RoomMessage } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { Composer } from '@/routes/run/composer'
import type { RunNavigation } from '@/routes/run/header'
import { DeliveryCountdown, requestCardID } from '@/routes/run/requests'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { readSessionLog, rowsForRun, type SessionRow } from '@/store/sessions'
import type { RunRecord } from '@/store/runs'

const emptyMessages: RoomMessage[] = []
const bottomSlack = 24

function Row({ row, agent, nav }: { row: SessionRow; agent: AgentTerminal; nav: RunNavigation }) {
  const members = useStore((s) => s.members)
  const name = (id: string) => members[id]?.display_name ?? id
  switch (row.kind) {
    case 'user':
      return (
        <MessageRow
          author={name(row.authorID)}
          meta={
            <>
              <span className={row.delivery === 'Not sent' || row.delivery === 'Denied' ? 'text-state-failed' : undefined}>
                {row.delivery === 'Queued' ? <DeliveryCountdown deliverAfter={row.deliverAfter} /> : row.delivery}
              </span>
              <RelativeTime at={row.at} />
            </>
          }
        >
          {row.body}
          {row.failure && <span className="mt-1 block text-ui-sm text-muted">{row.failure}</span>}
        </MessageRow>
      )
    case 'note':
      return (
        <MessageRow variant="note" author={`${name(row.authorID)} · note`} meta={<RelativeTime at={row.at} />}>
          {row.body}
        </MessageRow>
      )
    case 'work':
      return (
        <WorkGroup summary={row.summary} trailing={<RelativeTime at={row.at} />}>
          {row.entries.map((entry) => (
            <WorkEntry key={entry.id} status={entry.status}>{entry.label}</WorkEntry>
          ))}
        </WorkGroup>
      )
    case 'request':
      if (row.reply) {
        return (
          <EventRow trailing={<RelativeTime at={row.at} />}>
            {row.title}: {row.body} · {name(row.reply.authorID)} replied: {row.reply.body}
          </EventRow>
        )
      }
      return (
        <RequestCard
          title={row.title}
          meta={<RelativeTime at={row.at} />}
          actions={row.answer === 'terminal'
            ? agent.hasAgentTerminal && (
              <Button size="sm" onClick={() => {
                nav.go('terminal')
                if (!agent.localControl && !agent.controlUnavailable) agent.session.takeControl()
              }}>Open terminal</Button>
            )
            : <Button size="sm" variant="secondary" onClick={() => nav.reveal(requestCardID.question(row.id))}>Reply</Button>}
        >
          {row.body}
        </RequestCard>
      )
    case 'event':
      return <EventRow trailing={<RelativeTime at={row.at} />}>{row.text}</EventRow>
    case 'finished':
      return <EventRow tone={row.tone} trailing={<RelativeTime at={row.at} />}>{row.text}</EventRow>
  }
}

export function SessionView({ run, agent, room, nav, active, textarea, onComposing }: {
  run: RunRecord
  agent: AgentTerminal
  room: RunRoom
  nav: RunNavigation
  active: boolean
  textarea: React.RefObject<HTMLTextAreaElement | null>
  onComposing: (composing: boolean) => void
}) {
  const log = useStore((s) => s.sessionLogs[run.id])
  const messages = useStore((s) => s.roomMessages[run.id] ?? emptyMessages)
  const members = useStore((s) => s.members)
  const events = log?.events
  const rows = useMemo(() => rowsForRun({
    run,
    events: events ?? [],
    room: messages,
    memberName: (id) => members[id]?.display_name ?? id,
  }), [run, events, messages, members])
  const list = useRef<VListHandle>(null)
  const pinned = useRef(true)

  useLayoutEffect(() => {
    if (active && pinned.current && rows.length > 0) list.current?.scrollToIndex(rows.length - 1, { align: 'end' })
  }, [active, rows.length])

  const live = useStore((s) => s.connection === 'live')
  useEffect(() => {
    if (live) void readSessionLog(useStore, api, run)
  }, [live, run.id, run.workspace_id])

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div role="log" aria-live="off" aria-label="Session" className="relative min-h-0 flex-1">
        {log?.error && (
          <div className="mx-auto max-w-[736px] px-4 pt-3">
            <Callout
              tone="failed"
              title="The session history could not be read"
              actions={<Button size="sm" variant="secondary" onClick={() => void readSessionLog(useStore, api, run)}>Retry</Button>}
            >
              {log.error}
            </Callout>
          </div>
        )}
        <VList
          ref={list}
          className="h-full"
          onScroll={(offset) => {
            const handle = list.current
            if (handle) pinned.current = offset + handle.viewportSize >= handle.scrollSize - bottomSlack
          }}
        >
          {rows.map((row, index) => (
            <div
              key={row.id}
              role="article"
              aria-posinset={index + 1}
              aria-setsize={rows.length}
              className="mx-auto w-full max-w-[736px] px-4 py-2 first:pt-4 last:pb-4"
            >
              <Row row={row} agent={agent} nav={nav} />
            </div>
          ))}
        </VList>
      </div>
      <Composer run={run} agent={agent} room={room} textarea={textarea} onFocusChange={onComposing} />
    </div>
  )
}
