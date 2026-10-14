import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { VList, type VListHandle } from 'virtua'
import { ChevronLeft, ChevronRight, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Markdown } from '@/components/ui/markdown'
import { MessageRow } from '@/components/ui/timeline'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { RunFileLinks } from '@/routes/files/path-link'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunNavigation } from '@/routes/run/header'
import { flattenRows, rowGap, TimelineRow, useRowContext, type FlatRow } from '@/routes/run/session-rows'
import { AgentDot, AgentElapsed, agentStateWord, openAgent } from '@/routes/run/subagent-cards'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'
import { loadOlderItems, loadWholeItem } from '@/store/session-stream'
import { rowsOfTurn } from '@/store/session-rows'
import {
  agentLabel,
  agentLoaded,
  agentRows,
  agentPrompt,
  agentReport,
  agentState,
  agentTurn,
  agentType,
  ancestry,
  noAgents,
  siblings,
} from '@/store/subagents'

const bottomSlack = 24
const autoPages = 5

/** The sibling button that opened the next inspector keeps focus across its remount. */
let stepped: string | null = null

type Entry = 'prompt' | 'empty' | 'report' | FlatRow

function PromptNote({ error, cut }: { error: string | undefined; cut: boolean }) {
  if (error) return <p role="alert" className="text-ui-sm text-state-failed">The prompt could not be read: {error}</p>
  return cut
    ? <p className="text-ui-sm text-muted">The launch input is longer than the session log keeps, so its prompt is not available.</p>
    : <p className="text-ui-sm text-muted">Loading the prompt…</p>
}

export function SubagentInspector({ run, agentID, agent: terminal, nav, onClose }: {
  run: RunRecord
  agentID: string
  agent: AgentTerminal
  nav: RunNavigation
  onClose: () => void
}) {
  const ctx = useRowContext(run, terminal, nav)
  const ready = useStore((s) => s.acpSessions[run.id] !== undefined)
  const index = useStore((s) => s.acpSessions[run.id]?.agents ?? noAgents)
  const oldestSeq = useStore((s) => s.acpSessions[run.id]?.oldestSeq ?? 0)
  const more = useStore((s) => s.acpSessions[run.id]?.more ?? false)
  const olderLoading = useStore((s) => s.acpSessions[run.id]?.olderLoading ?? false)
  const olderError = useStore((s) => s.acpSessions[run.id]?.olderError)
  const expanded = useStore((s) => s.expandedRows[run.id])
  const sub = index.agents[agentID]
  const state = sub ? agentState(sub) : undefined
  const finished = state === 'done' || state === 'failed'
  const prompt = sub ? agentPrompt(sub) : undefined
  const cut = typeof sub?.call?.raw_input === 'string'
  const report = sub ? agentReport(sub) : undefined
  const callSeq = sub?.callSeq
  const truncated = sub?.callTruncated === true && callSeq !== undefined
  const incomplete = !sub || !agentLoaded(sub, oldestSeq)

  const root = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const button = stepped && root.current?.querySelector<HTMLElement>(`[aria-label="${stepped}"]:not(:disabled)`)
    stepped = null
    ;(button || root.current?.querySelector<HTMLElement>('h2') || root.current)?.focus()
  }, [])

  const missing = truncated && (!prompt || (finished && !report))
  const [wholeError, setWholeError] = useState<string>()
  useEffect(() => {
    setWholeError(undefined)
    if (missing) loadWholeItem(useStore, api, run.id, callSeq!).catch((err) => setWholeError(message(err)))
  }, [missing, run.id, callSeq])

  const paged = useRef(0)
  const known = sub !== undefined
  const loadOlder = () => void loadOlderItems(useStore, api, run.id)
  useEffect(() => {
    if (!ready || !known || !incomplete || !more || olderLoading || olderError || paged.current >= autoPages) return
    paged.current++
    loadOlder()
  }, [ready, known, incomplete, more, olderLoading, olderError, run.id])

  const flat = useMemo(() => (sub ? flattenRows(agentRows(sub, rowsOfTurn(agentTurn(sub))), expanded) : []), [sub, expanded])
  const entries = useMemo<Entry[]>(() => {
    if (!sub) return []
    return [
      ...(prompt || truncated || cut ? ['prompt' as const] : []),
      ...(flat.length === 0 ? ['empty' as const] : flat),
      ...(finished && (report || (truncated && !wholeError)) ? ['report' as const] : []),
    ]
  }, [sub, prompt, truncated, cut, flat, finished, report, wholeError])

  const list = useRef<VListHandle>(null)
  const pinned = useRef(state === 'running')
  useLayoutEffect(() => {
    if (pinned.current && entries.length > 0) list.current?.scrollToIndex(entries.length - 1, { align: 'end' })
  }, [entries])

  const path = ancestry(index, agentID)
  const peers = sub ? siblings(index, sub) : []
  const position = peers.indexOf(agentID)
  const step = (delta: number) => {
    stepped = delta < 0 ? 'Previous subagent' : 'Next subagent'
    openAgent(run.id, peers[position + delta]!, true)
  }

  let notice: React.ReactNode
  if (!ready) notice = <p role="status" className="text-ui-sm text-muted">Loading the session…</p>
  else if (incomplete && olderError) {
    notice = (
      <Callout tone="failed" title="Earlier items could not be read" actions={<Button size="sm" variant="secondary" onClick={loadOlder}>Retry</Button>}>
        {olderError}
      </Callout>
    )
  } else if (incomplete && olderLoading) notice = <p role="status" className="text-ui-sm text-muted">Loading earlier history…</p>
  else if (incomplete) {
    const text = !sub ? `The loaded history has no subagent ${agentID}.` : more ? 'This subagent started before the loaded history.' : 'This subagent started before the retained history.'
    notice = (
      <div className="flex flex-wrap items-center gap-2 text-ui-sm text-muted">
        <p>{text}</p>
        {more && <Button size="sm" variant="secondary" onClick={loadOlder}>Load earlier history</Button>}
      </div>
    )
  }

  return (
    <div ref={root} tabIndex={-1} className="flex min-h-0 flex-1 flex-col outline-none">
      <div className="flex shrink-0 items-center gap-1 border-b border-seam bg-chrome px-2 py-1">
        <nav aria-label="Subagent path" className="flex min-w-0 flex-1 items-center gap-1 overflow-hidden text-ui-sm text-muted">
          <Button variant="quiet" className="shrink-0" onClick={onClose}>Session</Button>
          {path.map((crumb) => (
            <span key={crumb.id} className={cn('flex min-w-0 items-center gap-1', crumb.id === agentID && 'max-w-[50%] shrink-0')}>
              <span aria-hidden className="shrink-0 px-0.5">›</span>
              {crumb.id === agentID
                ? <span aria-current="page" className="min-w-0 truncate text-text">{agentLabel(crumb)}</span>
                : <Button variant="quiet" className="min-w-0 shrink" onClick={() => openAgent(run.id, crumb.id)}><span className="truncate">{agentLabel(crumb)}</span></Button>}
            </span>
          ))}
        </nav>
        {peers.length > 1 && (
          <>
            <Button variant="ghost" size="icon-sm" label="Previous subagent" disabled={position <= 0} onClick={() => step(-1)}><ChevronLeft /></Button>
            <span className="text-ui-sm text-muted tabular-nums">{position + 1} of {peers.length}</span>
            <Button variant="ghost" size="icon-sm" label="Next subagent" disabled={position >= peers.length - 1} onClick={() => step(1)}><ChevronRight /></Button>
          </>
        )}
        <Button variant="ghost" size="icon-sm" label="Close subagent" onClick={onClose}><X /></Button>
      </div>
      {sub && state && (
        <div className="flex shrink-0 items-center gap-2 px-4 pt-3 pb-2 text-ui-sm">
          <AgentDot state={state} />
          <h2 tabIndex={-1} className="min-w-0 truncate outline-none text-ui font-medium text-text">{agentLabel(sub)}</h2>
          {agentType(sub) && <span className="shrink-0 text-muted">{agentType(sub)}</span>}
          <span className="ml-auto flex shrink-0 items-center gap-2 text-muted tabular-nums">
            <span>{agentStateWord(state)}</span>
            <AgentElapsed agent={sub} state={state} />
          </span>
        </div>
      )}
      {notice && <div className="shrink-0 px-4 py-2">{notice}</div>}
      {sub && (
        <VList
          ref={list}
          role="log"
          aria-live="off"
          aria-label="Subagent transcript"
          className="min-h-0 flex-1"
          data={entries}
          onScroll={(offset) => {
            const handle = list.current
            if (handle) pinned.current = offset + handle.viewportSize >= handle.scrollSize - bottomSlack
          }}
        >
          {(entry: Entry, at: number) => {
            if (entry === 'prompt') {
              return (
                <div key="prompt" className="px-4 pt-1">
                  {prompt
                    ? <MessageRow author="Prompt">{prompt}</MessageRow>
                    : <PromptNote error={wholeError} cut={cut && !truncated} />}
                </div>
              )
            }
            if (entry === 'empty') {
              return <p key="empty" className="px-4 pt-3 text-ui-sm text-muted">{state === 'running' ? 'No activity yet.' : 'No tool calls were reported.'}</p>
            }
            if (entry === 'report') {
              return (
                <div key="report" className="px-4 pt-3 pb-4">
                  <h3 className={cn('mb-1 text-ui-sm font-medium', state === 'failed' ? 'text-state-failed' : 'text-muted')}>Report</h3>
                  {report ? <Markdown text={report} /> : <p className="text-ui-sm text-muted">Loading the report…</p>}
                </div>
              )
            }
            return (
              <div key={entry.key} role="article" aria-posinset={at + 1} aria-setsize={entries.length} className={cn('w-full px-4', rowGap(entry), at === entries.length - 1 && 'pb-4')}>
                <RunFileLinks run={run}>
                  <TimelineRow depth={entry.depth} row={entry.depth === 0 ? entry.row : undefined} entry={entry.depth === 0 ? undefined : entry.entry} ctx={ctx} />
                </RunFileLinks>
              </div>
            )
          }}
        </VList>
      )}
    </div>
  )
}
