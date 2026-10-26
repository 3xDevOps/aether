import { useContext, useRef } from 'react'
import type * as React from 'react'
import { Bot, Copy, ExternalLink, MessageSquare, X, ZoomIn } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { StatusDot } from '@/components/ui/status-dot'
import { Tooltip } from '@/components/ui/tooltip'
import { copyText } from '@/lib/clipboard'
import { clockTime } from '@/lib/format'
import { stepTarget } from '@/routes/run/session-rows'
import { openAgent } from '@/routes/run/subagent-cards'
import { kindLabel, spanDuration, spanIcon, statusLabel, statusTone, TraceClock } from '@/routes/run/trace-row'
import { useStore } from '@/store'
import { rowsOfTurn } from '@/store/session-rows'
import type { Span } from '@/store/trace'
import { tickLabel } from '@/store/trace-axis'

function kindLine(span: Span): string {
  switch (span.kind) {
    case 'tool':
      return [kindLabel.tool, span.sub ?? span.tool].filter(Boolean).join(' · ')
    case 'agent':
      return [kindLabel.agent, span.sub].filter(Boolean).join(' · ')
    case 'turn':
      return [`Turn ${span.turn}`, span.autonomous && 'autonomous'].filter(Boolean).join(' · ')
    case 'wait':
      return [kindLabel.wait, span.request].filter(Boolean).join(' · ')
    default:
      return kindLabel[span.kind]
  }
}

const tokens = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 })

function Fact({ label, children, wide }: { label: string; children: React.ReactNode; wide?: boolean }) {
  return (
    <div className={wide ? 'col-span-full min-w-0' : 'min-w-0'}>
      <dt className="text-ui-xs text-muted">{label}</dt>
      <dd className="truncate text-ui text-text tabular-nums">{children}</dd>
    </div>
  )
}

/** The Session row key that shows this span, and the blocks to unfold on the way. A pending request has no row; it is docked. */
function sessionTarget(runID: string, span: Span): { key: string; expand: string[] } | undefined {
  const turn = useStore.getState().acpSessions[runID]?.turns.find((held) => held.turn === span.turn)
  if (!turn || span.agentID) return undefined
  const rows = rowsOfTurn(turn)
  if (span.kind === 'turn') return rows[0] && { key: rows[0].id, expand: [] }
  if (span.kind === 'wait') return span.requestStatus === 'pending' ? undefined : stepTarget(rows, (step) => step.id === `req:${turn.turn}:${span.requestID}`)
  const entry = `${turn.turn}:${span.callID}`
  return stepTarget(rows, (step) => (step.kind === 'tool' && step.entry.id === entry) || (step.kind === 'lookup' && step.entries.some((item) => item.id === entry)))
}

export function TraceDetail({
  span,
  runID,
  runStart,
  onZoom,
  onClose,
  onSession,
  onRequest,
  onRun,
}: {
  span: Span
  runID: string
  runStart: number
  onZoom: () => void
  onClose: () => void
  onSession: () => void
  /** Shows the docked request card in Session. */
  onRequest: () => void
  onRun: (runID: string) => void
}) {
  const { now } = useContext(TraceClock)
  const Icon = spanIcon(span)
  const command = useRef<HTMLElement>(null)
  const target = span.kind === 'turn' || span.kind === 'tool' || span.kind === 'wait' ? sessionTarget(runID, span) : undefined
  const error = span.kind === 'turn' && span.status !== 'done' && span.status !== 'running'
    ? span.stop
    : span.kind === 'tool' && span.exitCode
      ? `Exited with code ${span.exitCode}`
      : undefined
  const elapsed = (t: number) => (
    <Tooltip content={clockTime(new Date(t).toISOString())}>
      <span>+{tickLabel(Math.max(0, t - runStart))}</span>
    </Tooltip>
  )
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex items-start gap-2.5">
        <span aria-hidden className="flex size-7.5 shrink-0 items-center justify-center rounded-control border border-seam bg-chrome text-muted">
          <Icon className="size-4" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="truncate text-ui font-medium text-text" title={span.label}>{span.label}</p>
          <p className="truncate text-ui-sm text-muted">{kindLine(span)}</p>
        </div>
        <span className="flex h-5 shrink-0 items-center gap-1.5 rounded-chip bg-chrome px-1.5 text-ui-xs text-text">
          <StatusDot tone={statusTone[span.status]} pulse={span.status === 'running'} />
          {statusLabel[span.status]}
        </span>
        <Button variant="ghost" size="icon-sm" label="Close" className="-mt-0.5 -mr-1" onClick={onClose}>
          <X />
        </Button>
      </div>
      <dl className="grid grid-cols-3 gap-x-3 gap-y-2 rounded-control bg-chrome p-2.5">
        {span.kind !== 'notice' && <Fact label="Duration">{spanDuration(span, now)}</Fact>}
        <Fact label="Started">{span.unknownStart ? 'Not loaded' : elapsed(span.start)}</Fact>
        <Fact label={span.end === null ? 'Running for' : 'Ended'}>
          {span.end === null ? spanDuration(span, now) : elapsed(span.end)}
        </Fact>
        {span.exitCode !== undefined && <Fact label="Exit code">{span.exitCode}</Fact>}
        {span.size > 0 && <Fact label="Spans">{span.size}</Fact>}
        {span.diffs !== undefined && <Fact label="Files changed">{span.diffs}</Fact>}
        {span.context && <Fact label="Context">{tokens.format(span.context.used)} of {tokens.format(span.context.size)} tokens</Fact>}
        {span.answer && <Fact label="Answer" wide>{span.answer}</Fact>}
        {span.path && <Fact label="Path" wide><span className="font-code" title={span.path}>{span.path}</span></Fact>}
        {span.command && (
          <Fact label="Command" wide>
            <span ref={command} className="font-code" title={span.command}>{span.command}</span>
          </Fact>
        )}
      </dl>
      {error && <Callout tone="failed">{error}</Callout>}
      <div className="flex flex-wrap gap-2">
        {span.agentID && (
          <Button size="sm" variant="secondary" onClick={() => openAgent(runID, span.agentID!)}>
            <Bot />
            Open subagent
          </Button>
        )}
        {span.status === 'waiting' && (
          <Button size="sm" variant="secondary" onClick={onRequest}>
            <MessageSquare />
            Show in Session
          </Button>
        )}
        {target && (
          <Button
            size="sm"
            variant="secondary"
            onClick={() => {
              useStore.getState().revealSessionRow(runID, target.key, target.expand)
              onSession()
            }}
          >
            <MessageSquare />
            Show in Session
          </Button>
        )}
        {span.runID && (
          <Button size="sm" variant="secondary" onClick={() => onRun(span.runID!)}>
            <ExternalLink />
            Open worker run
          </Button>
        )}
        {span.kind !== 'run' && span.kind !== 'notice' && (
          <Button size="sm" variant="secondary" hint="Shift+Enter" onClick={onZoom}>
            <ZoomIn />
            Zoom to this span
          </Button>
        )}
        {span.command && (
          <Button size="sm" variant="secondary" onClick={() => void copyText(span.command!, command.current)}>
            <Copy />
            Copy command
          </Button>
        )}
      </div>
    </div>
  )
}
