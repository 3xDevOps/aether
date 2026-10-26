import { memo, useLayoutEffect, useMemo, useRef } from 'react'
import type * as React from 'react'
import { MessageRow as AgentMessage } from '@/components/messages/message-row'
import { MessageAttachments } from '@/components/messages/message-images'
import { Button } from '@/components/ui/button'
import { CopyButton } from '@/components/ui/copy-button'
import { Markdown } from '@/components/ui/markdown'
import { RelativeTime } from '@/components/ui/relative-time'
import { RequestCard } from '@/components/ui/request-card'
import { RequestAction, RequestActions, RequestFooter } from '@/components/ui/request-choice'
import { AssistantRow, EventRow, LiveActivityRow, MessageRow } from '@/components/ui/timeline'
import { playEntrance, type Entrance } from '@/lib/motion'
import { memberName } from '@/lib/member-name'
import { inputHint } from '@/lib/run-requests'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunNavigation } from '@/routes/run/header'
import { NoteMessage } from '@/routes/run/note-thread'
import { DeliveryCountdown, requestCardID } from '@/routes/run/requests'
import { hasDetail, MoreSteps, StepDetail, StepsBlockHeader, StepView } from '@/routes/run/session-steps'
import { SubagentGroup } from '@/routes/run/subagent-cards'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'
import type { SessionRow } from '@/store/session-rows'
import type { Step, StepsRow, WorkItem } from '@/store/session-steps'

export type FlatRow =
  | { kind: 'row'; key: string; row: SessionRow; open: boolean }
  | { kind: 'step'; key: string; step: Step; first: boolean; last: boolean; live: boolean }
  | { kind: 'detail'; key: string; entry: WorkItem; last: boolean }
  | { kind: 'more'; key: string; hidden: number; open: boolean }

export const sessionColumn = 'mx-auto w-full max-w-[736px] px-4'

/** Whether a block with no choice of the viewer's is open. */
export type OpenPolicy = (row: StepsRow) => boolean

export const openWhileCurrent: OpenPolicy = (row) => row.current

const foldOver = 40
const foldHead = 5
const foldTail = 30

const foldAt = (row: StepsRow): number => (row.steps.length > foldOver ? row.steps.length - foldTail : 0)

export function flattenRows(rows: SessionRow[], expanded: Record<string, boolean> | undefined, policy: OpenPolicy = openWhileCurrent, tailAt = foldAt): FlatRow[] {
  const out: FlatRow[] = []
  for (const row of rows) {
    if (row.kind !== 'steps') {
      out.push({ kind: 'row', key: row.id, row, open: false })
      continue
    }
    const open = expanded?.[row.id] ?? policy(row)
    out.push({ kind: 'row', key: row.id, row, open })
    if (!open) continue
    const live = row.live !== undefined
    const moreKey = `${row.id}/more`
    const tail = tailAt(row)
    const fold = tail > foldHead
    const all = expanded?.[moreKey] ?? false
    row.steps.forEach((step, index) => {
      if (fold && index === foldHead) out.push({ kind: 'more', key: moreKey, hidden: tail - foldHead, open: all })
      if (fold && !all && index >= foldHead && index < tail) return
      const last = index === row.steps.length - 1
      const key = `${row.id}/${step.id}`
      out.push({ kind: 'step', key, step, first: index === 0, last, live })
      if (step.kind === 'tool' && expanded?.[step.id] && hasDetail(step.entry)) out.push({ kind: 'detail', key: `${key}/detail`, entry: step.entry, last })
    })
  }
  return out
}

/** The flat key of the first step `match` finds, with its block and, when the step is folded away, the fold to open. */
export function stepTarget(rows: SessionRow[], match: (step: Step) => boolean): { key: string; expand: string[] } | undefined {
  for (const row of rows) {
    if (row.kind !== 'steps') continue
    const index = row.steps.findIndex(match)
    if (index === -1) continue
    const folded = index >= foldHead && index < foldAt(row)
    return { key: `${row.id}/${row.steps[index]!.id}`, expand: folded ? [row.id, `${row.id}/more`] : [row.id] }
  }
  return undefined
}

/**
 * The flat list with each block's default open state and fold. Defaults
 * change on their own (a block shuts when its turn ends, a growing block folds
 * more of its middle); while the reader is scrolled up, or focus is inside a
 * block, they hold, so nothing moves under them.
 */
export function useFlatRows(rows: SessionRow[], expanded: Record<string, boolean> | undefined, following: boolean, focused: string | null, policy: OpenPolicy = openWhileCurrent): FlatRow[] {
  const held = useRef(new Map<string, { open: boolean; tail: number }>())
  return useMemo(() => {
    const defaults = (row: StepsRow) => {
      const prior = held.current.get(row.id)
      const hold = !following || (focused?.startsWith(`${row.id}/`) ?? false)
      const next = prior && hold ? { open: prior.open, tail: Math.min(prior.tail, foldAt(row)) } : { open: policy(row), tail: foldAt(row) }
      held.current.set(row.id, next)
      return next
    }
    return flattenRows(rows, expanded, (row) => defaults(row).open, (row) => defaults(row).tail)
  }, [rows, expanded, following, focused, policy])
}

export function rowGap(flat: FlatRow): string {
  if (flat.kind !== 'row') return 'pt-0'
  switch (flat.row.kind) {
    case 'user':
      return 'pt-6'
    case 'note':
      return 'pt-4'
    case 'steps':
    case 'assistant':
    case 'agent-message':
      return 'pt-3'
    default:
      return 'pt-2'
  }
}

export interface RowContext {
  runID: string
  task: string
  ownerID: string
  hasAgentTerminal: boolean
  openTerminal: () => void
  reveal: RunNavigation['reveal']
}

export function useRowContext(run: RunRecord, agent: AgentTerminal, nav: RunNavigation): RowContext {
  const latest = useRef({ agent, nav })
  latest.current = { agent, nav }
  return useMemo(() => ({
    runID: run.id,
    task: run.task,
    ownerID: run.member_id,
    hasAgentTerminal: agent.hasAgentTerminal,
    openTerminal: () => {
      const { agent: current, nav: to } = latest.current
      to.go('terminal')
      if (!current.localControl && !current.controlUnavailable) current.session.takeControl()
    },
    reveal: (id) => latest.current.nav.reveal(id),
  }), [run.id, run.task, run.member_id, agent.hasAgentTerminal])
}

function MailRow({ messageID, runID }: { messageID: string; runID: string }) {
  const m = useStore((s) => s.runMessages[messageID])
  if (!m) return null
  return <div className="pl-6"><AgentMessage message={m} flush label={(id) => (id === runID ? 'This run' : undefined)} /></div>
}

function UserRow({ row, ctx, name }: { row: Extract<SessionRow, { kind: 'user' }>; ctx: RowContext; name: (id: string) => string }) {
  const self = useStore((s) => s.info?.member.id)
  const body = row.body.startsWith(`${ctx.task}\n\n`) ? ctx.task : row.body
  const authorID = row.authorID ?? (row.images?.length || row.files?.length ? undefined : ctx.ownerID)
  const author = authorID === self && !row.via ? undefined : `${authorID ? name(authorID) : 'User'}${row.via ? ` via ${row.via}` : ''}`
  return (
    <MessageRow
      author={author}
      attachments={row.images?.length || row.files?.length ? <MessageAttachments runID={ctx.runID} images={row.images} files={row.files} /> : undefined}
      meta={
        <>
          {row.delivery && (
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

function AnswerRow({ row }: { row: Extract<SessionRow, { kind: 'assistant' }> }) {
  const body = useRef<HTMLDivElement>(null)
  return (
    <AssistantRow
      streaming={row.streaming}
      outcome={row.outcome && <span>{row.outcome.text}</span>}
      actions={!row.streaming && (
        <>
          <RelativeTime at={row.at} />
          <CopyButton text={row.text} label="Copy the reply" source={body} />
        </>
      )}
    >
      <div ref={body}><Markdown text={row.text} live={row.streaming} /></div>
    </AssistantRow>
  )
}

function Row({ row, open, ctx }: { row: SessionRow; open: boolean; ctx: RowContext }) {
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const name = (id: string) => memberName({ members, mergedInto }, id)
  switch (row.kind) {
    case 'user':
      return <UserRow row={row} ctx={ctx} name={name} />
    case 'note':
      return (
        <NoteMessage authorID={row.authorID} authorName={row.authorName} via={row.via} question={row.question} at={row.at} body={row.body} status={row.pending ? 'Sending…' : undefined} />
      )
    case 'assistant':
      return <AnswerRow row={row} />
    case 'steps':
      return <StepsBlockHeader row={row} open={open} runID={ctx.runID} />
    case 'subagents':
      return <div className="pl-6"><SubagentGroup runID={ctx.runID} rowID={row.id} ids={row.ids} /></div>
    case 'live':
      return <LiveActivityRow tone={row.tone}>{row.label}</LiveActivityRow>
    case 'terminal-note':
      return (
        <p className="flex min-h-6 items-center gap-1 pl-6 text-ui-sm text-muted">
          Live detail is in the Terminal.
          {ctx.hasAgentTerminal && <Button variant="link" size="sm" onClick={ctx.openTerminal}>Open terminal</Button>}
        </p>
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
          description={row.answer === 'input' ? inputHint(ctx.hasAgentTerminal) : row.body}
          footer={(row.answer !== 'input' || ctx.hasAgentTerminal) && (
            <RequestFooter>
              <RequestActions>
                {row.answer === 'input'
                  ? <RequestAction label="Open terminal" onClick={ctx.openTerminal} />
                  : <RequestAction label="Reply" onClick={() => ctx.reveal(requestCardID.question(row.id))} />}
              </RequestActions>
            </RequestFooter>
          )}
        />
      )
    case 'event':
      return <EventRow tone={row.tone} detail={row.detail} trailing={<RelativeTime at={row.at} />}>{row.text}</EventRow>
    case 'finished':
      return <EventRow tone={row.tone} trailing={<RelativeTime at={row.at} />}>{row.text}</EventRow>
    case 'agent-message':
      return <MailRow messageID={row.messageID} runID={ctx.runID} />
  }
}

function sameFields(a: FlatRow, b: FlatRow): boolean {
  const x = a as Record<string, unknown>
  const y = b as Record<string, unknown>
  const keys = Object.keys(x)
  return keys.length === Object.keys(y).length && keys.every((key) => x[key] === y[key])
}

/** A flat item is rebuilt on every flatten; it renders again only when one of its fields changed. */
export const TimelineRow = memo(function TimelineRow({ item, ctx }: { item: FlatRow; ctx: RowContext }) {
  switch (item.kind) {
    case 'row':
      return <Row row={item.row} open={item.open} ctx={ctx} />
    case 'step':
      return <StepView step={item.step} first={item.first} last={item.last} live={item.live} runID={ctx.runID} />
    case 'detail':
      return <StepDetail entry={item.entry} last={item.last} runID={ctx.runID} />
    case 'more':
      return <MoreSteps id={item.key} hidden={item.hidden} open={item.open} runID={ctx.runID} />
  }
}, (a, b) => a.ctx === b.ctx && sameFields(a.item, b.item))

const rowEnter: Entrance = { motion: { duration: '--duration-360', easing: '--ease-enter' }, rise: 4 }

/** Rows new since the last list, by key and their place in the burst, and when they were found. */
export interface Arrivals {
  at: number
  keys: Map<string, number>
}

/** A row only enters soon after it arrived: one mounted later by scrolling, or after history paged in, does not. */
const arrivalWindow = 600

/**
 * The list's row box. A row that arrived since the last list plays its
 * entrance once, on the mount that shows it.
 */
export function EnteringRow({ arrivals, itemKey, className, children, ...props }: React.ComponentProps<'div'> & { arrivals: Arrivals; itemKey: string }) {
  const node = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const index = arrivals.keys.get(itemKey)
    if (index === undefined) return
    arrivals.keys.delete(itemKey)
    if (node.current && performance.now() - arrivals.at < arrivalWindow) playEntrance(node.current, { ...rowEnter, index })
  }, [])
  return <div ref={node} className={className} {...props}>{children}</div>
}

const burstCap = 8

/**
 * The rows new since the last list. A person opening a block shows every
 * step it holds; a stream adds a few rows at a time, so a larger burst is a
 * first load or a replay, and a new first row is a history page: neither
 * enters with motion.
 */
export function useArrivals(flat: FlatRow[], expanded: Record<string, boolean> | undefined): Arrivals {
  const seen = useRef<{ flat: FlatRow[] | null; expanded: typeof expanded }>({ flat: null, expanded })
  return useMemo(() => {
    const prior = seen.current
    seen.current = { flat, expanded }
    const keys = new Map<string, number>()
    if (prior.flat && prior.flat.length > 0 && prior.flat[0]!.key === flat[0]?.key) {
      // A message moves between the answer and a block under a new key, keyed by its step id in both places; it is already on screen.
      const held = new Set(prior.flat.flatMap((item) => (item.kind === 'step' ? [item.key, item.step.id] : [item.key])))
      for (const item of flat) if (!held.has(item.key) && !(item.kind === 'step' && held.has(item.step.id))) keys.set(item.key, keys.size)
      if (expanded === prior.expanded && keys.size > burstCap) keys.clear()
    }
    return { at: performance.now(), keys }
  }, [flat])
}
