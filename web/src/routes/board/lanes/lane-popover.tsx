import { useRef, type ReactNode } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { MessageRow, Participants } from '@/components/messages/message-row'
import { Button } from '@/components/ui/button'
import { PopoverContent } from '@/components/ui/popover'
import { api } from '@/lib/api'
import { clockTime } from '@/lib/format'
import { unreadMail, unreadReason } from '@/lib/needs-you'
import { stateName, stateWords } from '@/lib/status'
import { CardActionButton, cardAction } from '@/routes/board/card-action'
import type { LaneEdge } from '@/routes/board/lanes/edges'
import type { RunLane, SwarmLane } from '@/routes/board/lanes/model'
import { segmentLabel } from '@/routes/board/lanes/segments'
import { elapsed } from '@/routes/missions/tree'
import { useStore } from '@/store'
import { diffTotals } from '@/store/diff'
import { messageScopeKey, scopeMessages, useMessageList, type MessageScope } from '@/store/messages'

const iso = (t: number) => new Date(t).toISOString()

function Header({ children }: { children: ReactNode }) {
  return <p className="text-ui-xs text-muted">{children}</p>
}

function Facts({ items }: { items: (string | false | undefined)[] }) {
  const shown = items.filter((item): item is string => Boolean(item))
  if (shown.length === 0) return null
  return (
    <ul className="mt-1.5 flex flex-col gap-0.5 text-ui-sm">
      {shown.map((item) => (
        <li key={item} className="relative pl-3 break-words before:absolute before:top-1.75 before:left-0.5 before:size-1 before:rounded-full before:bg-icon-faint">{item}</li>
      ))}
    </ul>
  )
}

function Actions({ children }: { children: ReactNode }) {
  return <div className="mt-3 flex flex-wrap items-center gap-2">{children}</div>
}

export function RunPopover({ lane, at, agentName, now, onReply, onAgents }: {
  lane: RunLane
  /** The time under the pointer, when the bar was clicked. */
  at?: number
  agentName?: string
  now: number
  onReply: () => void
  onAgents?: (open: boolean) => void
}) {
  const { card } = lane
  const { run } = card
  const navigate = useStore((s) => s.navigate)
  const snapshot = useStore((s) => s.diffs[run.id]?.snapshots[0])
  const overlaps = useStore((s) => s.overlaps[run.id])
  const totals = diffTotals(snapshot?.files)
  const action = cardAction(card)
  const segment = at === undefined ? undefined : lane.segments.find((s) => at >= s.start && at <= s.end)
  const unread = unreadMail(run, now)
  const pending = run.pending_inputs?.length ?? 0
  const words = stateWords(card, undefined, now)
  return (
    <>
      <Header>{[lane.handle ?? agentName ?? run.harness, stateName(card), elapsed(run.created_at, lane.end)].join(' · ')}</Header>
      <p className="mt-0.5 text-ui font-medium break-words">{lane.title}</p>
      <Facts
        items={[
          words.label !== stateName(card) && words.label,
          words.detail,
          card.next,
          run.branch && `${run.branch}${totals ? ` +${totals.additions} −${totals.deletions}` : ''}`,
          unread > 0 && unreadReason(unread, run.oldest_unacked_at!, now),
          pending > 0 && `${pending} ${pending === 1 ? 'input' : 'inputs'} waiting`,
          overlaps !== undefined && overlaps.length > 0 && `Overlaps ${overlaps.length} ${overlaps.length === 1 ? 'run' : 'runs'} on ${new Set(overlaps.flatMap((o) => o.files)).size} files`,
          segment && (segment.state === 'unloaded'
            ? `History before ${clockTime(iso(segment.end), now)} not loaded`
            : `${segmentLabel[segment.state]} ${clockTime(iso(segment.start), now)}–${clockTime(iso(segment.end), now)}`),
        ]}
      />
      <Actions>
        {action && <CardActionButton card={card} action={action} onReply={onReply} />}
        <Button variant="secondary" size="sm" onClick={() => navigate('run', { runId: run.id })}>Open run</Button>
        {run.mission_id && <Button variant="ghost" size="sm" onClick={() => navigate('missions', { missionId: run.mission_id! })}>Open swarm</Button>}
        {onAgents && <Button variant="ghost" size="sm" onClick={() => onAgents(!lane.agentsOpen)}>{lane.agentsOpen ? 'Hide agents' : 'Show agents'}</Button>}
      </Actions>
    </>
  )
}

export function SwarmPopover({ lane }: { lane: SwarmLane }) {
  const navigate = useStore((s) => s.navigate)
  const { integratorID } = lane
  return (
    <>
      <Header>Swarm · {lane.detail}</Header>
      <p className="mt-0.5 text-ui font-medium break-words">{lane.title}</p>
      <Actions>
        <Button variant="secondary" size="sm" onClick={() => navigate('missions', { missionId: lane.missionID })}>Open swarm</Button>
        {integratorID && <Button variant="ghost" size="sm" onClick={() => navigate('run', { runId: integratorID })}>Open integrator</Button>}
      </Actions>
    </>
  )
}

function MessageList({ scope, ids }: { scope: MessageScope; ids: ReadonlySet<string> }) {
  useMessageList(useStore, api, scope)
  const key = messageScopeKey(scope)
  const messages = useStore(useShallow((s) => scopeMessages(s, scope))).filter((m) => ids.has(m.id))
  const error = useStore((s) => s.messageErrors[key])
  const loaded = useStore((s) => Boolean(s.messageLists[key]))
  if (error) return <p role="alert" className="mt-2 text-ui-sm text-state-failed">{error}</p>
  if (!loaded) return <p className="mt-2 text-ui-sm text-muted">Loading messages…</p>
  if (messages.length === 0) return <p className="mt-2 text-ui-sm text-muted">These messages are older than the newest page this run's mail list reads.</p>
  return (
    <div className="-mx-3 mt-1 flex flex-col">
      {messages.map((m) => <MessageRow key={m.id} message={m} />)}
    </div>
  )
}

/** A bundle on a folded lane can hold several senders' mail; without a shared thread each sender's list is read. */
function MessageThread({ messages }: { messages: NonNullable<LaneEdge['messages']> }) {
  const workspaces = useStore(useShallow((s) => messages.senders.map((run) => s.runs[run]?.workspace_id ?? s.activeWorkspace)))
  const ids = new Set(messages.ids)
  if (messages.correlation) return <MessageList scope={{ kind: 'thread', workspaceID: workspaces[0], correlationID: messages.correlation }} ids={ids} />
  return messages.senders.map((runID, i) => <MessageList key={runID} scope={{ kind: 'run', workspaceID: workspaces[i], runID }} ids={ids} />)
}

const kindWord: Record<LaneEdge['kind'], string> = {
  orchestration: 'Launched by its integrator',
  dependency: 'Task dependency',
  submission: 'Submission',
  message: 'Agent message',
  overlap: 'File overlap',
}

function edgeHead(edge: LaneEdge, now: number): string {
  const count = edge.messages?.ids.length ?? 1
  return [
    edge.kind === 'message' && count > 1 ? `${count} agent messages` : kindWord[edge.kind],
    edge.submission?.state,
    clockTime(iso(edge.from.t), now),
  ].filter(Boolean).join(' · ')
}

/** A lane's links, so the keyboard reaches what a pointer opens by clicking a link. */
export function LaneLinks({ edges, now, onPick }: { edges: LaneEdge[]; now: number; onPick: (edge: LaneEdge) => void }) {
  if (edges.length === 0) return null
  return (
    <section aria-label="Links" className="mt-3 border-t border-seam pt-2">
      <Header>Links</Header>
      <ul className="mt-1 flex flex-col gap-1">
        {edges.map((edge) => (
          <li key={edge.key} className="flex min-w-0 flex-wrap items-center gap-x-2">
            <Button variant="link" size="sm" onClick={() => onPick(edge)}>{edgeHead(edge, now)}</Button>
            <Participants from={edge.from.run} to={edge.to.run} />
          </li>
        ))}
      </ul>
    </section>
  )
}

export function EdgePopover({ edge, now }: { edge: LaneEdge; now: number }) {
  return (
    <>
      <Header>{edgeHead(edge, now)}</Header>
      <div className="mt-1 text-ui">
        <Participants from={edge.from.run} to={edge.to.run} />
      </div>
      {edge.messages && <MessageThread messages={edge.messages} />}
      {edge.kind === 'submission' && edge.submission && (
        <Facts
          items={[
            edge.submission.decided_at ? `Decided ${clockTime(edge.submission.decided_at, now)}` : 'Waiting for the integrator',
            (edge.submission.scope_violations?.length ?? 0) > 0 && `${edge.submission.scope_violations!.length} scope violations`,
          ]}
        />
      )}
      {edge.kind === 'dependency' && <Facts items={['The second task started after the first finished.']} />}
      {edge.kind === 'overlap' && <Facts items={edge.files ?? []} />}
    </>
  )
}

/** Escape returns focus to the lane; a click elsewhere leaves it where the click put it. */
export function LanePopoverContent({ label, children, returnFocus }: { label: string; children: ReactNode; returnFocus: () => void }) {
  const leftOutside = useRef(false)
  return (
    <PopoverContent
      aria-label={label}
      className="w-[min(340px,calc(100vw-16px))]"
      onInteractOutside={() => {
        leftOutside.current = true
      }}
      onCloseAutoFocus={(event) => {
        event.preventDefault()
        if (!leftOutside.current) returnFocus()
      }}
    >
      {children}
    </PopoverContent>
  )
}
