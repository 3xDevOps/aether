import { useMemo, useRef, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { Search } from '@/components/icons'
import { MessageRow } from '@/components/messages/message-row'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import type { Api } from '@/lib/api'
import type { RunMessage } from '@/lib/types'
import { VirtualList } from '@/routes/activity/virtual-list'
import { useStore } from '@/store'
import { loadMessagePage, type MessageScope, messageScopeKey, scopeMessages, useMessageList } from '@/store/messages'

export interface MessageFilters {
  sender: string
  recipient: string
  thread: string
}

export const noMessageFilters: MessageFilters = { sender: '', recipient: '', thread: '' }

/** Pages one "Show all" may read, so a huge history cannot pin the tab. */
const showAllPages = 20

function messageScope(workspaceID: string, filters: MessageFilters): MessageScope {
  if (filters.thread) return { kind: 'thread', workspaceID, correlationID: filters.thread }
  const runID = filters.sender || filters.recipient
  return runID ? { kind: 'run', workspaceID, runID } : { kind: 'workspace', workspaceID }
}

function matches(message: RunMessage, filters: MessageFilters, query: string): boolean {
  if (filters.sender && message.from_run_id !== filters.sender) return false
  if (filters.recipient && message.to_run_id !== filters.recipient) return false
  if (!query) return true
  return [message.body, message.summary, message.outcome, message.next_action].some((text) => text?.toLowerCase().includes(query))
}

export function MessageHistory({
  client,
  workspaceID,
  filters,
  onThread,
}: {
  client: Api
  workspaceID: string
  filters: MessageFilters
  onThread: (thread: string) => void
}) {
  const scope = useMemo(() => messageScope(workspaceID, filters), [workspaceID, filters])
  const key = messageScopeKey(scope)
  const messages = useStore(useShallow((s) => scopeMessages(s, scope)))
  const loaded = useStore((s) => Boolean(s.messageLists[key]))
  const more = useStore((s) => Boolean(s.messageLists[key]?.nextBefore))
  const error = useStore((s) => s.messageErrors[key])
  const [query, setQuery] = useState('')
  const [reading, setReading] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)

  useMessageList(useStore, client, { kind: 'workspace', workspaceID })
  useMessageList(useStore, client, scope)

  const needle = query.trim().toLowerCase()
  const shown = useMemo(
    () => messages.filter((message) => matches(message, filters, needle)).reverse(),
    [messages, filters, needle],
  )

  const showAll = async () => {
    setReading(true)
    for (let page = 0; page < showAllPages && useStore.getState().messageLists[key]?.nextBefore; page++) {
      await loadMessagePage(useStore, client, scope, true)
      if (useStore.getState().messageErrors[key]) break
    }
    setReading(false)
  }

  return (
    <>
      <div className="flex shrink-0 items-center gap-2 border-b border-seam px-4 py-2">
        <label className="relative flex min-w-0 flex-1 items-center">
          <Search aria-hidden className="pointer-events-none absolute left-2 size-3.5 text-muted" />
          <Input
            type="search"
            aria-label="Search agent messages"
            placeholder="Search messages"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="pl-7"
          />
        </label>
        <span className="shrink-0 text-ui-sm text-muted tabular-nums">
          {shown.length} of {messages.length}{more ? '+' : ''}
        </span>
      </div>
      {error && <Callout tone="failed" role="alert" className="m-3">{error}</Callout>}
      <div ref={scroller} role="region" aria-label="Agent messages" className="min-h-0 flex-1 overflow-y-auto">
        {loaded && shown.length === 0 && !error && (
          <EmptyState title={messages.length === 0 ? 'No agent messages yet' : 'No messages match'}>
            {messages.length === 0
              ? 'Runs in a swarm message each other here: questions, replies and reports.'
              : 'Change the search or the filters.'}
          </EmptyState>
        )}
        <VirtualList data={shown} scrollRef={scroller}>
          {(message) => <MessageRow key={message.id} message={message} onThread={filters.thread ? undefined : onThread} />}
        </VirtualList>
        {more && (
          <div className="flex flex-col items-start gap-2 px-4 py-3">
            {needle && <p className="text-ui-sm text-muted">The search covers the messages loaded so far.</p>}
            <Button variant="secondary" size="sm" disabled={reading} onClick={() => void showAll()}>
              {reading ? 'Loading…' : 'Show all'}
            </Button>
          </div>
        )}
      </div>
    </>
  )
}
