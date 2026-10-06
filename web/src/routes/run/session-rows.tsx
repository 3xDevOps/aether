import { memo, useEffect, useState } from 'react'
import { Copy } from '@/components/icons'
import { AgentMessageRow } from '@/components/messages/message-row'
import { Button } from '@/components/ui/button'
import { Markdown } from '@/components/ui/markdown'
import { RelativeTime } from '@/components/ui/relative-time'
import { RequestCard } from '@/components/ui/request-card'
import { ChangedFiles, DiffBlock, OutputTail, PlanCard } from '@/components/ui/session-blocks'
import { AssistantRow, duration, EventRow, LiveActivityRow, MessageRow, WorkDetail, WorkEntry, WorkGroup } from '@/components/ui/timeline'
import { api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { inputHint } from '@/lib/run-requests'
import type { RunMessage } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunNavigation } from '@/routes/run/header'
import { DeliveryCountdown, requestCardID } from '@/routes/run/requests'
import { AnsweredText } from '@/routes/run/session-requests'
import { useStore } from '@/store'
import type { SessionRow, WorkItem } from '@/store/session-rows'
import { loadWholeItem } from '@/store/session-stream'

export type FlatRow =
  | { key: string; depth: 0; row: SessionRow }
  | { key: string; depth: 1; entry: WorkItem }
  | { key: string; depth: 2; entry: WorkItem }

const entryKey = (id: string) => `entry:${id}`

export function flattenRows(rows: SessionRow[], expanded: Record<string, true> | undefined): FlatRow[] {
  const out: FlatRow[] = []
  for (const row of rows) {
    out.push({ key: row.id, depth: 0, row })
    if (row.kind !== 'work' || !expanded?.[row.id]) continue
    for (const entry of row.entries) {
      out.push({ key: `${row.id}/${entry.id}`, depth: 1, entry })
      if (expanded[entryKey(entry.id)]) out.push({ key: `${row.id}/${entry.id}/detail`, depth: 2, entry })
    }
  }
  return out
}

export function rowGap(flat: FlatRow): string {
  if (flat.depth !== 0) return 'pt-0'
  switch (flat.row.kind) {
    case 'user':
    case 'note':
      return 'pt-4'
    case 'assistant':
    case 'plan':
    case 'request':
    case 'agent-message':
      return 'pt-2'
    default:
      return 'pt-1'
  }
}

export interface RowContext {
  runID: string
  task: string
  ownerID: string
  agent: AgentTerminal
  nav: RunNavigation
  expanded: Record<string, true> | undefined
  messages: Record<string, RunMessage>
  runLabel: (runID: string) => string
}

function hasDetail(entry: WorkItem): boolean {
  return Boolean(entry.command || entry.output || entry.diffs || entry.truncated)
}

function EntryDetail({ runID, entry }: { runID: string; entry: WorkItem }) {
  const [error, setError] = useState<string>()
  const missing = entry.truncated && entry.seq !== undefined
  useEffect(() => {
    if (!missing) return
    loadWholeItem(useStore, api, runID, entry.seq!).catch((err) => setError(message(err)))
  }, [missing, runID, entry.seq])
  return (
    <WorkDetail>
      {entry.command && <code className="font-code text-ui-sm break-all text-text">$ {entry.command}</code>}
      {entry.output && <OutputTail output={entry.output} exitCode={entry.exitCode} />}
      {entry.diffs?.map((diff) => <DiffBlock key={diff.path} path={diff.path} patch={diff.patch} />)}
      {missing && !error && <p className="text-ui-sm text-muted">Loading the full output…</p>}
      {error && <p role="alert" className="text-ui-sm text-state-failed">The full output could not be read: {error}</p>}
      {!entry.command && !entry.output && !entry.diffs && !missing && <p className="text-ui-sm text-muted">No output.</p>}
    </WorkDetail>
  )
}

function Row({ row, ctx }: { row: SessionRow; ctx: RowContext }) {
  const members = useStore((s) => s.members)
  const name = (id: string) => members[id]?.display_name ?? id
  const toggle = (id: string) => () => useStore.getState().toggleSessionRow(ctx.runID, id)
  switch (row.kind) {
    case 'user': {
      const body = row.body.startsWith(`${ctx.task}\n\n`) ? ctx.task : row.body
      return (
        <MessageRow
          author={name(row.authorID ?? ctx.ownerID)}
          meta={
            <>
              {row.delivery && row.delivery !== 'Sent' && (
                <span className={row.delivery === 'Not sent' || row.delivery === 'Denied' ? 'text-state-failed' : undefined}>
                  {row.delivery === 'Queued' && row.deliverAfter ? <DeliveryCountdown deliverAfter={row.deliverAfter} /> : row.delivery}
                </span>
              )}
              <RelativeTime at={row.at} />
            </>
          }
        >
          {body}
          {row.failure && <span className="mt-1 block text-ui-sm text-muted">{row.failure}</span>}
        </MessageRow>
      )
    }
    case 'note':
      return (
        <MessageRow variant="note" author={`${name(row.authorID)} · ${row.question ? 'question' : 'note'}`} meta={<RelativeTime at={row.at} />}>
          {row.body}
        </MessageRow>
      )
    case 'assistant':
      return (
        <AssistantRow
          meta={!row.streaming && (
            <>
              <Button variant="ghost" size="icon-sm" label="Copy the reply" onClick={(event) => void copyText(row.text, event.currentTarget)}>
                <Copy />
              </Button>
              <RelativeTime at={row.at} />
            </>
          )}
        >
          <Markdown text={row.text} />
        </AssistantRow>
      )
    case 'thinking':
      return <EventRow detail={row.text}>Thought</EventRow>
    case 'work': {
      const running = row.entries.some((entry) => entry.status === 'running')
      const failed = row.entries.filter((entry) => entry.status === 'failed').length
      return (
        <WorkGroup
          summary={failed ? `${row.summary} · ${failed} failed` : row.summary}
          trailing={running ? undefined : <RelativeTime at={row.at} />}
          expanded={Boolean(ctx.expanded?.[row.id])}
          onToggle={toggle(row.id)}
        />
      )
    }
    case 'live':
      return <LiveActivityRow>{row.label}</LiveActivityRow>
    case 'plan':
      return <PlanCard entries={row.entries} />
    case 'changed-files':
      return <ChangedFiles files={row.files} onOpen={() => ctx.nav.go('changes')} />
    case 'answered':
      return (
        <EventRow tone={row.request.status === 'cancelled' ? 'neutral' : 'done'} trailing={<RelativeTime at={row.at} />}>
          <AnsweredText request={row.request} command={row.command} />
        </EventRow>
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
          actions={row.answer === 'input'
            ? ctx.agent.hasAgentTerminal && (
              <Button size="sm" variant="secondary" onClick={() => {
                ctx.nav.go('terminal')
                if (!ctx.agent.localControl && !ctx.agent.controlUnavailable) ctx.agent.session.takeControl()
              }}>Open terminal</Button>
            )
            : <Button size="sm" variant="secondary" onClick={() => ctx.nav.reveal(requestCardID.question(row.id))}>Reply</Button>}
        >
          {row.answer === 'input' ? inputHint(ctx.agent.hasAgentTerminal) : row.body}
        </RequestCard>
      )
    case 'event':
      return <EventRow tone={row.tone} detail={row.detail} trailing={<RelativeTime at={row.at} />}>{row.text}</EventRow>
    case 'finished':
      return <EventRow tone={row.tone} trailing={<RelativeTime at={row.at} />}>{row.text}</EventRow>
    case 'agent-message': {
      const m = ctx.messages[row.messageID]
      if (!m) return null
      return (
        <AgentMessageRow
          message={m}
          label={ctx.runLabel}
          onOpenRun={(runID) => runID === ctx.runID ? undefined : () => useStore.getState().navigate('run', { runId: runID })}
        />
      )
    }
  }
}

export const TimelineRow = memo(function TimelineRow({ flat, ctx }: { flat: FlatRow; ctx: RowContext }) {
  if (flat.depth === 0) return <Row row={flat.row} ctx={ctx} />
  const { entry } = flat
  if (flat.depth === 2) return <EntryDetail runID={ctx.runID} entry={entry} />
  const detail = hasDetail(entry)
  return (
    <WorkEntry
      tool={entry.tool}
      status={entry.status}
      trailing={duration(entry.durationMs)}
      expanded={detail ? Boolean(ctx.expanded?.[entryKey(entry.id)]) : undefined}
      onToggle={detail ? () => useStore.getState().toggleSessionRow(ctx.runID, entryKey(entry.id)) : undefined}
    >
      {entry.label}
    </WorkEntry>
  )
})
