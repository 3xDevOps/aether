import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { type CacheSnapshot, VList, type VListHandle } from 'virtua'
import { ActionCard, ReadsLine } from '@/components/assistant/action-card'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { assistantView, transcriptFacts, type ActionGroup, type AssistantView } from '@/lib/assistant-actions'
import { cn } from '@/lib/utils'
import { EnteringRow, rowGap, TimelineRow, useArrivals, useFlatRows, type FlatRow, type RowContext } from '@/routes/run/session-rows'
import { enhancedRows } from '@/routes/run/session-view'
import { useStore } from '@/store'
import { assistantSessionID } from '@/store/assistant'
import { loadOlderAssistant } from '@/store/assistant-stream'
import type { AcpSession } from '@/store/sessions'
import { noAgents, turnExtras } from '@/store/subagents'

const bottomSlack = 24

/** Where the reader left the conversation when the panel closed; reopening the same conversation returns there. */
let kept: { conversation: string; cache: CacheSnapshot; offset: number; pinned: boolean } | null = null

export function assistantRows(session: AcpSession) {
  return enhancedRows(session.turns, session.live, turnExtras(noAgents, 0), null)
}

export function useAssistantView(session: AcpSession | undefined): AssistantView | null {
  const confirmations = useStore((s) => s.assistantConfirmations)
  return useMemo(() => (session ? assistantView(assistantRows(session), confirmations, transcriptFacts(session.turns)) : null), [session, confirmations])
}

type Item = FlatRow | { kind: 'actions'; key: string; group: ActionGroup }

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
  const steps = useFlatRows(rows, expanded, following, focused)
  const arrivals = useArrivals(steps, expanded)
  const flat = useMemo(() => withActions(steps, view.groups), [steps, view.groups])
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
          className={cn('w-full px-3', item.kind === 'actions' ? 'pt-2' : rowGap(item), index === count - 1 && 'pb-3', index === 0 && 'pt-3')}
        >
          {item.kind !== 'actions'
            ? <TimelineRow item={item} ctx={ctx} />
            : item.group.kind === 'action'
              ? <ActionCard action={item.group.action} />
              : <ReadsLine id={item.group.id} actions={item.group.actions} />}
        </EnteringRow>
      )}
    </VList>
  )
}
