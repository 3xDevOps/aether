import { useEffect, useMemo, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { Ellipsis } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Menu, MenuCheckboxItem, MenuContent, MenuTrigger } from '@/components/ui/menu'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import { EventFeed, feedItems } from '@/routes/activity/event-feed'
import { ActivityFilter, agentMessages, type FilterField, type Option } from '@/routes/activity/filter'
import { MessageHistory, type MessageFilters, noMessageFilters } from '@/routes/activity/message-history'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { openFeed, useLiveFeed } from '@/routes/team/sync'
import { useStore } from '@/store'
import { useRunIDs } from '@/store/hooks'
import { scopeMessages } from '@/store/messages'

function threadTitle(body: string): string {
  const line = body.split('\n')[0] ?? ''
  return line.length > 48 ? `${line.slice(0, 48)}…` : line || 'Untitled'
}

export function ActivityRoute({ params, client = api }: RouteProps & { client?: Api }) {
  const workspaces = useStore(useShallow((s) => Object.values(s.workspaces)))
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const members = useStore(useShallow((s) => Object.values(s.members)))
  const filters = useStore((s) => s.feedFilters)
  const setFilters = useStore((s) => s.setFeedFilters)
  const [messages, setMessages] = useState(false)
  const [messageFilters, setMessageFilters] = useState<MessageFilters>(noMessageFilters)
  const [raw, setRaw] = useState(false)
  const count = useStore((s) => feedItems(s.feed, raw).length)

  useEffect(() => {
    if (filters.workspaceID) return
    const first = params.workspaceId || activeWorkspace || workspaces[0]?.id
    if (first) setFilters({ workspaceID: first })
  }, [filters.workspaceID, params.workspaceId, activeWorkspace, workspaces, setFilters])

  useEffect(() => {
    if (!messages) void openFeed(useStore, client)
  }, [filters, client, messages])

  useLiveFeed(!messages, client)

  const runIDs = useRunIDs(filters.workspaceID)
  const runLabels = useStore(useShallow((s) => runIDs.map((id) => (s.runs[id] ? runLabel(s.runs[id]) : id))))
  const runOptions = useMemo(() => runIDs.map((id, i): Option => [id, runLabels[i]]), [runIDs, runLabels])
  const workspaceMessages = useStore(useShallow((s) => (messages ? scopeMessages(s, { kind: 'workspace', workspaceID: filters.workspaceID }) : [])))
  const threads = useMemo(
    () => workspaceMessages.filter((m) => m.kind === 'question' || m.kind === 'report').map((m): Option => [m.correlation_id || m.id, threadTitle(m.body)]),
    [workspaceMessages],
  )
  const setMessageFilter = (patch: Partial<MessageFilters>) => setMessageFilters((current) => ({ ...current, ...patch }))

  const workspaceField: FilterField | undefined = workspaces.length > 1
    ? {
        label: 'Workspace',
        value: filters.workspaceID,
        onChange: (workspaceID) => {
          // A run belongs to one workspace, so a kept run filter would match nothing.
          setFilters({ workspaceID, runID: '' })
          setMessageFilters(noMessageFilters)
        },
        options: workspaces.map((w): Option => [w.id, w.name]),
      }
    : undefined

  const fields: FilterField[] = messages
    ? [
        { label: 'Sender', value: messageFilters.sender, onChange: (sender) => setMessageFilter({ sender }), options: [['', 'Any run'], ...runOptions] },
        { label: 'Recipient', value: messageFilters.recipient, onChange: (recipient) => setMessageFilter({ recipient }), options: [['', 'Any run'], ...runOptions] },
        { label: 'Thread', value: messageFilters.thread, onChange: (thread) => setMessageFilter({ thread }), options: [['', 'Every thread'], ...threads] },
      ]
    : [
        { label: 'Run', value: filters.runID, onChange: (runID) => setFilters({ runID }), options: [['', 'Every run'], ...runOptions] },
        { label: 'Member', value: filters.memberID, onChange: (memberID) => setFilters({ memberID }), options: [['', 'Everyone'], ...members.map((m): Option => [m.id, m.display_name])] },
      ]

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ViewHeader
        title="Activity"
        subtitle={messages ? 'Agent messages' : `${count} ${count === 1 ? 'entry' : 'entries'}`}
        actions={
          <>
            <ActivityFilter
              workspace={workspaceField}
              kind={messages ? agentMessages : filters.type}
              onKind={(kind) => {
                setMessages(kind === agentMessages)
                if (kind !== agentMessages) setFilters({ type: kind })
              }}
              fields={fields}
              onClear={() => {
                setMessages(false)
                setMessageFilters(noMessageFilters)
                setFilters({ runID: '', memberID: '', type: '' })
              }}
            />
            {!messages && (
              <Menu>
                <MenuTrigger asChild>
                  <Button variant="ghost" size="icon" label="More activity options">
                    <Ellipsis />
                  </Button>
                </MenuTrigger>
                <MenuContent align="end">
                  <MenuCheckboxItem checked={raw} onCheckedChange={setRaw}>Raw events</MenuCheckboxItem>
                </MenuContent>
              </Menu>
            )}
          </>
        }
      />
      {messages && filters.workspaceID ? (
        <MessageHistory
          client={client}
          workspaceID={filters.workspaceID}
          filters={messageFilters}
          onThread={(thread) => setMessageFilters({ ...noMessageFilters, thread })}
        />
      ) : (
        <EventFeed client={client} raw={raw} />
      )}
    </div>
  )
}

registerRoute('timeline', ActivityRoute)
