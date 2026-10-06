import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { MessageRow } from '@/components/messages/message-row'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { Input } from '@/components/ui/input'
import { RequestCard } from '@/components/ui/request-card'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { api } from '@/lib/api'
import { isRetainedRun } from '@/lib/commands'
import { deletesInLabel } from '@/lib/format'
import { plainReason } from '@/lib/status'
import type { RoomMessage } from '@/lib/types'
import { modeLabel } from '@/routes/run/agent-name'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { usePrimaryAction, type RunNavigation } from '@/routes/run/header'
import { NeedsYouCards, useRunRequests } from '@/routes/run/requests'
import type { RunRoom } from '@/routes/run/room'
import type { RunView } from '@/routes/run/views'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useRunPresentation } from '@/store/hooks'
import { useShallow } from 'zustand/react/shallow'
import { loadMessagePage, messageScopeKey, useMessageList, type MessageScope } from '@/store/messages'
import { isTerminal, type RunRecord } from '@/store/runs'

const emptyMessages: RoomMessage[] = []

function Section({ id, title, count, inset, children }: { id?: string; title: string; count?: number; inset: boolean; children: React.ReactNode }) {
  return (
    <section id={id} aria-label={title} className={cn('flex flex-col gap-2 border-b border-seam py-3 last:border-b-0', inset && 'px-4')}>
      <SectionLabel as="h2" className="flex items-center gap-1.5">
        {title}
        {count !== undefined && count > 0 && <span className="text-text tabular-nums">{count}</span>}
      </SectionLabel>
      {children}
    </section>
  )
}

function Person({ id }: { id: string }) {
  const member = useStore((s) => s.members[id])
  const name = member?.display_name ?? id
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <Avatar name={name} color={member?.color} />
      <span className="min-w-0 truncate">{name}</span>
    </span>
  )
}

const recentMessages = 5

function AgentMessages({ run, inset }: { run: RunRecord; inset: boolean }) {
  const scope: MessageScope = { kind: 'run', workspaceID: run.workspace_id, runID: run.id }
  const key = messageScopeKey(scope)
  const messages = useStore(useShallow((s) => (s.messageLists[key]?.ids ?? []).map((id) => s.runMessages[id]!)))
  const olderCursor = useStore((s) => s.messageLists[key]?.nextBefore)
  const error = useStore((s) => s.messageErrors[key])
  const [all, setAll] = useState(false)
  const [loading, setLoading] = useState(false)
  useMessageList(useStore, api, scope)
  if (messages.length === 0 && !run.mission_id) return null
  const label = (id: string) => (id === run.id ? 'This run' : undefined)
  const shown = all ? messages : messages.slice(-recentMessages)
  const older = async () => {
    setLoading(true)
    await loadMessagePage(useStore, api, scope, true)
    setLoading(false)
  }
  return (
    <Section title="Agent messages" count={messages.length} inset={inset}>
      {messages.length === 0 && <p className="text-ui-sm text-muted">No messages between agents yet.</p>}
      {error && <p role="alert" className="text-ui-sm text-state-failed">{error}</p>}
      {all && olderCursor && (
        <Button size="sm" variant="ghost" disabled={loading} onClick={() => void older()}>{loading ? 'Loading…' : 'Show older'}</Button>
      )}
      <ol className="flex flex-col gap-3">
        {shown.map((m) => (
          <li key={m.id}>
            <MessageRow message={m} label={label} flush />
          </li>
        ))}
      </ol>
      {!all && (messages.length > recentMessages || olderCursor) && (
        <Button size="sm" variant="ghost" onClick={() => setAll(true)}>Show all</Button>
      )}
    </Section>
  )
}

function Notes({ run, room, inset, draft }: { run: RunRecord; room: RunRoom; inset: boolean; draft: { text: string } | null }) {
  const notes = useStore((s) => s.roomMessages[run.id] ?? emptyMessages).filter((m) => m.kind === 'comment' || m.kind === 'question')
  const [body, setBody] = useState('')
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (!draft) return
    setBody(draft.text)
    input.current?.focus()
  }, [draft])
  const send = async () => {
    const text = body.trim()
    if (!text || room.busy) return
    if (await room.post({ kind: 'comment', body: text })) setBody('')
  }
  return (
    <Section title="Notes" count={notes.length} inset={inset}>
      {notes.length > 0 && (
        <ol className="flex flex-col gap-2">
          {notes.map((note) => (
            <li key={note.id} className="flex flex-col gap-0.5 text-ui-sm">
              <span className="flex min-w-0 items-center gap-1.5 text-muted">
                <Person id={note.actor_id} />
                {note.kind === 'question' && <span className="shrink-0">· question</span>}
                <RelativeTime at={note.created_at} className="ml-auto shrink-0 tabular-nums" />
              </span>
              <span className="break-words whitespace-pre-wrap text-ui text-text">{note.body}</span>
            </li>
          ))}
        </ol>
      )}
      <form
        className="flex items-center gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          void send()
        }}
      >
        <Input ref={input} aria-label="Add a note" placeholder="Add a note for people on this run" value={body} onChange={(event) => setBody(event.target.value)} />
        <Button type="submit" size="sm" variant="secondary" disabled={!body.trim() || room.busy}>Add</Button>
      </form>
      <p className="text-ui-xs text-muted">Notes are for people. The agent never sees them.</p>
    </Section>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-2 py-0.5">
      <dt className="text-muted">{label}</dt>
      <dd className="min-w-0 break-words text-text">{children}</dd>
    </div>
  )
}

function container(run: RunRecord): string {
  if (run.status === 'queued' || run.status === 'provisioning') return 'Starting'
  if (!isTerminal(run.status)) return 'Running'
  return isRetainedRun(run) ? 'Stopped, kept for reopening' : 'Removed'
}

function Facts({ run, agent, agentName, inset }: { run: RunRecord; agent: AgentTerminal; agentName: string; inset: boolean }) {
  const status = useStore((s) => s.roomStatus[run.id])
  const selfID = useStore((s) => s.info?.member.id)
  const controllerID = agent.localControl ? selfID : status?.controller?.member_id
  return (
    <Section title="Details" inset={inset}>
      {run.task.trim() && <p className="max-h-48 overflow-y-auto break-words whitespace-pre-wrap text-ui text-text">{run.task}</p>}
      <dl className="text-ui-sm">
        <Row label="Owner"><Person id={run.member_id} /></Row>
        {run.account_member_id && run.account_member_id !== run.member_id && <Row label="Agent account"><Person id={run.account_member_id} /></Row>}
        <Row label="Agent">{agentName} · {modeLabel[run.mode] ?? run.mode}</Row>
        <Row label="Branch"><Code>{run.branch}</Code></Row>
        <Row label="Created"><RelativeTime at={run.created_at} /></Row>
        <Row label="Changed"><RelativeTime at={run.stateChangedAt} /></Row>
        {run.last_commit && run.last_commit_at && (
          <Row label="Last commit"><Code title={run.last_commit}>{run.last_commit.slice(0, 8)}</Code> <span className="text-muted"><RelativeTime at={run.last_commit_at} /></span></Row>
        )}
        <Row label="In control">{controllerID ? <Person id={controllerID} /> : 'Nobody'}</Row>
        <Row label="Watching">
          {status && status.watchers.length > 0
            ? <span className="flex flex-wrap gap-2">{status.watchers.map((id) => <Person key={id} id={id} />)}</span>
            : 'Nobody else'}
        </Row>
        <Row label="Container">{container(run)}</Row>
        {run.archived_at && run.deletes_at && <Row label="Archived">{deletesInLabel(run.deletes_at)}</Row>}
        {run.reason && <Row label="Last reason">{plainReason(run.reason)}</Row>}
      </dl>
    </Section>
  )
}

function ConditionCard({ run, view, agent, nav }: { run: RunRecord; view: RunView; agent: AgentTerminal; nav: RunNavigation }) {
  const presented = useRunPresentation(run)
  const primary = usePrimaryAction(run, view, agent, nav)
  if (presented.state !== 'needs-you') return <p className="text-ui-sm text-muted">Nothing is waiting on you.</p>
  return (
    <RequestCard
      title={presented.reason}
      actions={primary && <Button size="sm" variant="secondary" onClick={primary.act}>{primary.label}</Button>}
    />
  )
}

export function RunDetails({ run, view, agent, agentName, room, nav, inset, noteDraft }: {
  run: RunRecord
  view: RunView
  agentName: string
  agent: AgentTerminal
  room: RunRoom
  nav: RunNavigation
  inset: boolean
  noteDraft: { text: string } | null
}) {
  const requests = useRunRequests(run)
  const waiting = requests.inputs.length + requests.sessionRequests.length + requests.approvals.length + requests.steers.length + requests.questions.length
  return (
    <div className="flex flex-col">
      {room.error && !room.errorFromComposer && (
        <div role="alert" className={cn('flex items-start gap-2 border-b border-seam py-2 text-ui-sm text-state-failed', inset && 'px-4')}>
          <span className="min-w-0 flex-1 break-words">{room.error}</span>
          <Button size="sm" variant="ghost" onClick={room.clearError}>Dismiss</Button>
        </div>
      )}
      <Section id="details-needs-you" title="Needs you" count={waiting} inset={inset}>
        {waiting > 0
          ? <NeedsYouCards run={run} agent={agent} room={room} nav={nav} docked={view === 'session'} />
          : <ConditionCard run={run} view={view} agent={agent} nav={nav} />}
      </Section>
      <AgentMessages run={run} inset={inset} />
      <Notes run={run} room={room} inset={inset} draft={noteDraft} />
      <Facts run={run} agent={agent} agentName={agentName} inset={inset} />
    </div>
  )
}
