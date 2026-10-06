import { useMemo, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { MessageSquare } from '@/components/icons'
import { MessageRow, Participants } from '@/components/messages/message-row'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import type { Api } from '@/lib/api'
import { useStore } from '@/store'
import {
  groupMessages,
  loadMessagePage,
  useMessageList,
  messageScopeKey,
  type MessageGroup,
  type MessageScope,
} from '@/store/messages'
import type { MissionDetail } from '@/store/missions'

const collapsedGroups = 6
const noIDs: string[] = []

function MessageRun({ group }: { group: Extract<MessageGroup, { kind: 'run' }> }) {
  const [open, setOpen] = useState(false)
  const last = group.messages.at(-1)!
  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 px-4 py-1.5 text-ui-sm text-muted">
        <MessageSquare aria-hidden className="size-3.5 shrink-0" />
        <Participants from={group.from} to={group.to} />
        <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
          {group.messages.length} messages
        </Button>
        <RelativeTime at={last.created_at} className="tabular-nums" />
      </div>
      {open && (
        <div className="ml-1.5 border-l border-seam pl-3">
          {group.messages.map((m) => <MessageRow key={m.id} message={m} />)}
        </div>
      )}
    </div>
  )
}

function Group({ group }: { group: MessageGroup }) {
  switch (group.kind) {
    case 'run':
      return <MessageRun group={group} />
    case 'single':
      return <MessageRow message={group.message} />
    case 'thread':
      return (
        <div className="flex min-w-0 flex-col">
          <MessageRow message={group.question} />
          {group.replies.length > 0 && (
            <div className="ml-1.5 border-l border-seam pl-3">
              {group.replies.map((m) => <MessageRow key={m.id} message={m} />)}
            </div>
          )}
        </div>
      )
  }
}

function groupKey(group: MessageGroup): string {
  switch (group.kind) {
    case 'run':
      return group.messages[0].id
    case 'single':
      return group.message.id
    case 'thread':
      return group.question.id
  }
}

export function AgentMessages({ detail, client }: { detail: MissionDetail; client: Api }) {
  const { mission } = detail
  const scope = useMemo<MessageScope>(
    () => ({ kind: 'mission', workspaceID: mission.workspace_id, missionID: mission.id }),
    [mission.workspace_id, mission.id],
  )
  const key = messageScopeKey(scope)
  const ids = useStore((s) => s.messageLists[key]?.ids ?? noIDs)
  const messages = useStore(useShallow((s) => ids.map((id) => s.runMessages[id])))
  const loaded = useStore((s) => s.messageLists[key] !== undefined)
  const hasOlder = useStore((s) => Boolean(s.messageLists[key]?.nextBefore))
  const error = useStore((s) => s.messageErrors[key])
  const [showAll, setShowAll] = useState(false)
  const [loadingOlder, setLoadingOlder] = useState(false)

  useMessageList(useStore, client, scope)

  const groups = useMemo(() => groupMessages(messages).reverse(), [messages])
  const shown = showAll ? groups : groups.slice(0, collapsedGroups)

  const older = async () => {
    setLoadingOlder(true)
    await loadMessagePage(useStore, client, scope, true)
    setLoadingOlder(false)
  }

  return (
    <section aria-label="Agent messages" className="flex flex-col gap-1">
      <SectionLabel as="h2">Agent messages</SectionLabel>
      {error && (
        <Callout
          tone="failed"
          role="alert"
          actions={<Button size="sm" variant="secondary" onClick={() => void loadMessagePage(useStore, client, scope)}>Retry</Button>}
        >
          Loading agent messages failed: {error}
        </Callout>
      )}
      {loaded && groups.length === 0 && !error && (
        <p className="text-ui text-muted">No agent messages yet. Workers and the integrator write here as they coordinate.</p>
      )}
      <ol className="flex flex-col divide-y divide-seam">
        {shown.map((group) => (
          <li key={groupKey(group)} className="[contain-intrinsic-size:auto_3rem] [content-visibility:auto]">
            <Group group={group} />
          </li>
        ))}
      </ol>
      {(groups.length > collapsedGroups || hasOlder) && (
        <div>
          {!showAll ? (
            <Button variant="link" size="sm" onClick={() => setShowAll(true)}>Show all</Button>
          ) : hasOlder && (
            <Button variant="link" size="sm" disabled={loadingOlder} onClick={() => void older()}>
              {loadingOlder ? 'Loading…' : 'Show earlier messages'}
            </Button>
          )}
        </div>
      )}
    </section>
  )
}
