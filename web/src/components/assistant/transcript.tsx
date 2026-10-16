import { useLayoutEffect, useMemo, useRef } from 'react'
import { VList, type VListHandle } from 'virtua'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { flattenRows, rowGap, TimelineRow, type FlatRow, type RowContext } from '@/routes/run/session-rows'
import { enhancedRows } from '@/routes/run/session-view'
import { useStore } from '@/store'
import { assistantSessionID } from '@/store/assistant'
import { loadOlderAssistant } from '@/store/assistant-stream'
import type { AcpSession } from '@/store/sessions'
import { noAgents, turnExtras } from '@/store/subagents'

const bottomSlack = 24

/** An agent without embedded context gets the view context as delimited text after the prompt. */
const viewContextText = /\s*<aether-view-context>[\s\S]*?<\/aether-view-context>\s*$/

export function assistantRows(session: AcpSession) {
  return enhancedRows(session.turns, session.live, turnExtras(noAgents, 0), null)
    .map((row) => (row.kind === 'user' ? { ...row, body: row.body.replace(viewContextText, '') } : row))
}

export function Transcript({ session }: { session: AcpSession }) {
  const self = useStore((s) => s.info?.member.id ?? '')
  const expanded = useStore((s) => s.expandedRows[assistantSessionID])
  const rows = useMemo(() => assistantRows(session), [session])
  const flat = useMemo(() => flattenRows(rows, expanded), [rows, expanded])
  const list = useRef<VListHandle>(null)
  const pinned = useRef(true)
  const firstKey = useRef(flat[0]?.key)
  const shift = firstKey.current !== undefined && flat[0]?.key !== firstKey.current
  const older = session.more
  const count = flat.length + (older ? 1 : 0)
  useLayoutEffect(() => {
    firstKey.current = flat[0]?.key
    if (pinned.current && count > 0) list.current?.scrollToIndex(count - 1, { align: 'end' })
  })

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
      shift={shift}
      onScroll={(offset) => {
        const handle = list.current
        if (handle) pinned.current = offset + handle.viewportSize >= handle.scrollSize - bottomSlack
      }}
    >
      {(item: FlatRow | null, index: number) => item === null ? (
        <div key="older" className="flex justify-center px-3 pt-3">
          <Button size="sm" variant="ghost" disabled={session.olderLoading} onClick={() => {
            pinned.current = false
            void loadOlderAssistant(useStore, api)
          }}>
            {session.olderLoading ? 'Loading…' : 'Show earlier messages'}
          </Button>
          {session.olderError && <p role="alert" className="text-ui-sm text-state-failed">{session.olderError}</p>}
        </div>
      ) : (
        <div
          key={item.key}
          role="article"
          aria-posinset={index + (older ? 0 : 1)}
          aria-setsize={flat.length}
          className={cn('w-full px-3', rowGap(item), index === count - 1 && 'pb-3', index === 0 && 'pt-3')}
        >
          <TimelineRow depth={item.depth} row={item.depth === 0 ? item.row : undefined} entry={item.depth === 0 ? undefined : item.entry} ctx={ctx} />
        </div>
      )}
    </VList>
  )
}
