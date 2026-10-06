import { useEffect, useMemo, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { ClipboardCheck, CornerDownRight, MessageCircleQuestion, MessageSquare, type LucideIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import type { Api } from '@/lib/api'
import type { RunMessage, RunMessageKind } from '@/lib/types'
import { ClampedText } from '@/routes/missions/clamped-text'
import { participantLabel } from '@/routes/missions/swarm'
import { useStore } from '@/store'
import {
  deliveryWord,
  groupMessages,
  loadMessagePage,
  messageScopeKey,
  type MessageGroup,
  type MessageScope,
} from '@/store/messages'
import type { MissionDetail } from '@/store/missions'

const kindIcon: Record<RunMessageKind, LucideIcon> = {
  message: MessageSquare,
  question: MessageCircleQuestion,
  reply: CornerDownRight,
  report: ClipboardCheck,
}

const kindWord: Record<RunMessageKind, string> = {
  message: 'Message',
  question: 'Question',
  reply: 'Reply',
  report: 'Report',
}

const collapsedGroups = 6
const noIDs: string[] = []

function Participant({ runID, detail }: { runID: string; detail: MissionDetail }) {
  const navigate = useStore((s) => s.navigate)
  const label = useStore((s) => participantLabel(runID, detail, s.runs))
  return (
    <Button variant="link" size="sm" className="min-w-0" title={label} onClick={() => navigate('terminal', { runId: runID })}>
      <span className="max-w-40 truncate sm:max-w-56">{label}</span>
    </Button>
  )
}

function Pair({ from, to, detail }: { from: string; to: string; detail: MissionDetail }) {
  return (
    <span className="flex min-w-0 max-w-full items-center gap-1">
      <Participant runID={from} detail={detail} />
      <span aria-label="to" className="text-muted">→</span>
      <Participant runID={to} detail={detail} />
    </span>
  )
}

function reportText(m: RunMessage): string {
  const lines = [m.outcome && m.summary ? `${m.outcome[0].toUpperCase()}${m.outcome.slice(1)}: ${m.summary}` : m.summary || m.body]
  if (m.next_action) lines.push(`Next: ${m.next_action}`)
  return lines.filter(Boolean).join('\n')
}

function MessageRow({ message: m, detail, pair = true }: { message: RunMessage; detail: MissionDetail; pair?: boolean }) {
  const Icon = kindIcon[m.kind]
  return (
    <div className="flex min-w-0 gap-2 py-1.5 text-ui" data-message-id={m.id}>
      <Icon aria-label={kindWord[m.kind]} className="mt-0.5 size-3.5 shrink-0 text-muted" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 text-ui-sm text-muted">
          {pair && <Pair from={m.from_run_id} to={m.to_run_id} detail={detail} />}
          <RelativeTime at={m.created_at} className="tabular-nums" />
          <span>{deliveryWord(m)}</span>
        </div>
        <ClampedText text={m.kind === 'report' ? reportText(m) : m.body} />
      </div>
    </div>
  )
}

function MessageRun({ group, detail }: { group: Extract<MessageGroup, { kind: 'run' }>; detail: MissionDetail }) {
  const [open, setOpen] = useState(false)
  const last = group.messages.at(-1)!
  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 py-1.5 text-ui-sm text-muted">
        <MessageSquare aria-hidden className="size-3.5 shrink-0" />
        <Pair from={group.from} to={group.to} detail={detail} />
        <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
          {group.messages.length} messages
        </Button>
        <RelativeTime at={last.created_at} className="tabular-nums" />
      </div>
      {open && (
        <div className="ml-1.5 border-l border-seam pl-3">
          {group.messages.map((m) => <MessageRow key={m.id} message={m} detail={detail} pair={false} />)}
        </div>
      )}
    </div>
  )
}

function Group({ group, detail }: { group: MessageGroup; detail: MissionDetail }) {
  switch (group.kind) {
    case 'run':
      return <MessageRun group={group} detail={detail} />
    case 'single':
      return <MessageRow message={group.message} detail={detail} />
    case 'thread':
      return (
        <div className="flex min-w-0 flex-col">
          <MessageRow message={group.question} detail={detail} />
          {group.replies.length > 0 && (
            <div className="ml-1.5 border-l border-seam pl-3">
              {group.replies.map((m) => <MessageRow key={m.id} message={m} detail={detail} />)}
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

  useEffect(() => {
    void loadMessagePage(useStore, client, scope)
  }, [client, scope])

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
      {error && <Callout tone="failed" role="alert">Loading agent messages failed: {error}</Callout>}
      {loaded && groups.length === 0 && !error && (
        <p className="text-ui text-muted">No agent messages yet. Workers and the integrator write here as they coordinate.</p>
      )}
      <ol className="flex flex-col divide-y divide-seam">
        {shown.map((group) => (
          <li key={groupKey(group)} className="[contain-intrinsic-size:auto_3rem] [content-visibility:auto]">
            <Group group={group} detail={detail} />
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
