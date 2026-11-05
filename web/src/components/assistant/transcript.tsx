import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { type CacheSnapshot, VList, type VListHandle } from 'virtua'
import { ActionCard, ReadsLine, useOpenTarget } from '@/components/assistant/action-card'
import { QueuedPrompt, queuedHeading } from '@/components/assistant/cards'
import { originVia } from '@/components/assistant/labels'
import { Button } from '@/components/ui/button'
import { OpenRoute } from '@/components/ui/markdown'
import { api } from '@/lib/api'
import type { AssistantQueueEntry } from '@/lib/assistant-types'
import { assistantView, transcriptFacts, type ActionGroup, type AssistantView } from '@/lib/assistant-actions'
import { cn } from '@/lib/utils'
import { EnteringRow, rowGap, TimelineRow, useArrivals, useFlatRows, type FlatRow, type RowContext } from '@/routes/run/session-rows'
import { enhancedRows } from '@/routes/run/session-view'
import { useStore } from '@/store'
import { assistantSessionID } from '@/store/assistant'
import { loadOlderAssistant } from '@/store/assistant-stream'
import type { SessionRow } from '@/store/session-rows'
import type { AcpSession } from '@/store/sessions'
import type { Route } from '@/store/ui'
import { noAgents, turnExtras } from '@/store/subagents'

const bottomSlack = 24

/** Where the reader left the conversation when the panel closed; reopening the same conversation returns there. */
let kept: { conversation: string; cache: CacheSnapshot; offset: number; pinned: boolean } | null = null

export function assistantRows(session: AcpSession) {
  return enhancedRows(session.turns, session.live, turnExtras(noAgents, 0), null)
}

const noQueue: AssistantQueueEntry[] = []

export function useAssistantView(session: AcpSession | undefined): AssistantView | null {
  const confirmations = useStore((s) => s.assistantConfirmations)
  const queue = useStore((s) => s.assistantStatus?.queue ?? noQueue)
  return useMemo(() => (session ? assistantView(withOrigins(assistantRows(session), queue), confirmations, transcriptFacts(session.turns)) : null), [session, confirmations, queue])
}

/** Names on each prompt the door it came through, from the queue entry that sent it. */
function withOrigins(rows: SessionRow[], queue: AssistantQueueEntry[]): SessionRow[] {
  const via = new Map(queue.flatMap((e) => (e.message_id && originVia(e.origin) ? [[e.message_id, originVia(e.origin)!] as const] : [])))
  if (!via.size) return rows
  return rows.map((row) => {
    const door = row.kind === 'user' ? via.get(row.id.slice(row.id.lastIndexOf(':') + 1)) : undefined
    return door ? { ...row, via: door } : row
  })
}

/** Queued prompts follow the conversation, and so does one that ended without reaching it, until a later turn does. */
export function unsent(queue: AssistantQueueEntry[], lastAt: string | undefined): AssistantQueueEntry[] {
  const after = lastAt ? Date.parse(lastAt) : -Infinity
  return queue.filter((e) => e.state === 'queued' || ((e.state === 'failed' || e.state === 'interrupted') && !e.message_id && Date.parse(e.updated_at ?? e.created_at) >= after))
}

type Item = FlatRow | { kind: 'actions'; key: string; group: ActionGroup } | { kind: 'queued'; key: string; entry: AssistantQueueEntry; position?: number; next?: string }
  | { kind: 'queue-heading'; key: string; count: number }

/** Each row's cards follow it, after the steps an open block shows. */
function withActions(flat: FlatRow[], groups: AssistantView['groups']): Item[] {
  const out: Item[] = []
  const after = (anchor: string) => {
    for (const group of groups.get(anchor) ?? []) out.push({ kind: 'actions', key: group.id, group })
  }
  after('')
  let host: string | null = null
  for (const item of flat) {
    if (item.kind === 'row') {
      if (host !== null) after(host)
      host = item.row.id
    }
    out.push(item)
  }
  if (host !== null) after(host)
  return out
}

export function Transcript({ session }: { session: AcpSession }) {
  const self = useStore((s) => s.info?.member.id ?? '')
  const expanded = useStore((s) => s.expandedRows[assistantSessionID])
  const view = useAssistantView(session)!
  const rows = view.rows
  const list = useRef<VListHandle>(null)
  const generation = useStore((s) => `${s.identityKey}:${s.assistantGeneration}`)
  const conversation = useRef('')
  // The first row changes when earlier messages load, which moves every offset and cached size.
  conversation.current = `${generation}:${session.epoch}:${session.historyGeneration}:${session.more}:${rows[0]?.id}`
  const [restored] = useState(() => (kept?.conversation === conversation.current ? kept : null))
  const pinned = useRef(restored?.pinned ?? true)
  const [following, setFollowing] = useState(pinned.current)
  const pin = (value: boolean) => {
    pinned.current = value
    setFollowing(value)
  }
  const [focused, setFocused] = useState<string | null>(null)
  const openTarget = useOpenTarget()
  const openRoute = (route: Route) => openTarget({ kind: 'link', id: route.name, route })
  const steps = useFlatRows(rows, expanded, following, focused)
  const arrivals = useArrivals(steps, expanded)
  const queue = useStore((s) => s.assistantStatus?.queue ?? noQueue)
  const flat: Item[] = useMemo(() => {
    // Prompts that never started come before the ones still waiting.
    const shown = unsent(queue, rows.at(-1)?.at)
    const waiting = [...shown.filter((e) => e.state !== 'queued'), ...shown.filter((e) => e.state === 'queued')]
    const queuedCount = waiting.filter((e) => e.state === 'queued').length
    let position = 0
    const item = (entry: AssistantQueueEntry, i: number) =>
      ({ kind: 'queued' as const, key: `queued:${entry.id}`, entry, next: waiting[i + 1]?.id ?? waiting[i - 1]?.id, ...(entry.state === 'queued' ? { position: ++position } : {}) })
    const ended = waiting.length - queuedCount
    return [
      ...withActions(steps, view.groups),
      ...waiting.slice(0, ended).map(item),
      ...(queuedCount > 0 ? [{ kind: 'queue-heading' as const, key: 'queue-heading', count: queuedCount }] : []),
      ...waiting.slice(ended).map((entry, i) => item(entry, ended + i)),
    ]
  }, [steps, view.groups, queue, rows])
  const firstKey = useRef(flat[0]?.key)
  const shift = firstKey.current !== undefined && flat[0]?.key !== firstKey.current
  const older = session.more
  const count = flat.length + (older ? 1 : 0)
  const focusedIndex = focused === null ? -1 : flat.findIndex((item) => item.key === focused)
  useLayoutEffect(() => {
    firstKey.current = flat[0]?.key
    if (pinned.current && count > 0) list.current?.scrollToIndex(count - 1, { align: 'end' })
  })
  useLayoutEffect(() => {
    const handle = list.current
    if (!handle) return
    if (restored && !restored.pinned) handle.scrollTo(restored.offset)
    return () => {
      kept = { conversation: conversation.current, cache: handle.cache, offset: handle.scrollOffset, pinned: pinned.current }
    }
  }, [])

  const ctx: RowContext = useMemo(() => ({
    runID: assistantSessionID,
    task: '',
    ownerID: self,
    hasAgentTerminal: false,
    openTerminal: () => {},
    reveal: () => {},
  }), [self])

  return (
    <OpenRoute.Provider value={openRoute}>
      <VList
        ref={list}
        role="log"
        aria-live="off"
        aria-label="Assistant conversation"
        className="min-h-0 flex-1"
        data={older ? [null, ...flat] : flat}
        cache={restored?.cache}
        shift={shift}
        keepMounted={focusedIndex >= 0 ? [focusedIndex + Number(older)] : undefined}
        onScroll={(offset) => {
          const handle = list.current
          if (handle) pin(offset + handle.viewportSize >= handle.scrollSize - bottomSlack)
        }}
      >
        {(item: Item | null, index: number) => item === null ? (
          <div key="older" className="flex justify-center px-3 pt-3">
            <Button size="sm" variant="ghost" disabled={session.olderLoading} onClick={() => {
              pin(false)
              void loadOlderAssistant(useStore, api)
            }}>
              {session.olderLoading ? 'Loading…' : 'Show earlier messages'}
            </Button>
            {session.olderError && <p role="alert" className="text-ui-sm text-state-failed">{session.olderError}</p>}
          </div>
        ) : (
          <EnteringRow
            key={item.key}
            itemKey={item.key}
            arrivals={arrivals}
            role="article"
            aria-posinset={index + (older ? 0 : 1)}
            aria-setsize={flat.length}
            onFocus={() => setFocused(item.key)}
            onBlur={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget)) setFocused(null)
            }}
            className={cn('w-full px-3', item.kind === 'actions' || item.kind === 'queued' || item.kind === 'queue-heading' ? 'pt-2' : rowGap(item), index === count - 1 && 'pb-3', index === 0 && 'pt-3')}
          >
            {item.kind === 'queue-heading'
              ? <p className="ml-6 text-ui-xs text-muted">{queuedHeading(item.count)}</p>
              : item.kind === 'queued'
              ? <QueuedPrompt entry={item.entry} position={item.position} next={item.next} />
              : item.kind !== 'actions'
              ? <TimelineRow item={item} ctx={ctx} />
              : item.group.kind === 'action'
                ? <ActionCard action={item.group.action} />
                : <ReadsLine id={item.group.id} actions={item.group.actions} />}
          </EnteringRow>
        )}
      </VList>
    </OpenRoute.Provider>
  )
}
