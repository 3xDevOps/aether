import { useContext, useEffect, useMemo, useState } from 'react'
import { paramRows } from '@/components/assistant/cards'
import { AssistantSheet } from '@/components/assistant/suggestion'
import { ChevronRight, ExternalLink, RotateCcw } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { ToolValue } from '@/components/ui/session-blocks'
import { StatusDot, type Tone, toneClasses } from '@/components/ui/status-dot'
import { api } from '@/lib/api'
import { actionSummary, isKnownAction, launchLine, readNoun, readSummary, watchLine, type ActionNames, type ActionState, type ActionTarget, type AssistantAction } from '@/lib/assistant-actions'
import { message } from '@/lib/format'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { assistantSessionID } from '@/store/assistant'
import { isTerminal } from '@/store/runs'
import { loadWholeItem } from '@/store/session-stream'

export const actionTone: Record<ActionState, Tone> = {
  approval: 'needs-you',
  running: 'working',
  done: 'done',
  failed: 'failed',
  declined: 'closed',
  expired: 'ended',
  withdrawn: 'ended',
  unfinished: 'interrupted',
}

export const actionStateWord: Record<ActionState, string> = {
  approval: 'Waiting for your approval',
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  declined: 'You declined',
  expired: 'No answer in 10 minutes',
  withdrawn: 'Withdrawn: the turn ended before you answered',
  unfinished: 'Did not finish: the turn ended first',
}

const targetWord: Record<ActionTarget['kind'], string> = {
  run: 'Open run',
  swarm: 'Open swarm',
  pr: 'Open pull request',
  workspace: 'Open workspace',
  environment: 'Open environment',
  watch: 'Open watch',
  link: 'Open link',
}

const goneWord: Partial<Record<ActionTarget['kind'], string>> = {
  run: 'This run no longer exists',
  workspace: 'This workspace no longer exists',
}

const launches = new Set(['aether_launch_run', 'aether_launch_swarm', 'aether_relaunch_run'])

/** States the member chose or let lapse: said quietly, with any error kept for the details. */
const quiet = new Set<ActionState>(['declined', 'expired', 'withdrawn'])

export function stateLine(action: AssistantAction, background: boolean): string {
  return [
    background ? 'Working in the background' : actionStateWord[action.state],
    action.approval === 'approved' ? 'approved by you' : action.approval === 'auto-approved' ? 'auto-approved' : undefined,
  ].filter(Boolean).join(' · ')
}

export function useActionNames(): ActionNames {
  const runs = useStore((s) => s.runs)
  const workspaces = useStore((s) => s.workspaces)
  const agents = useStore((s) => s.agentList?.agents)
  const assistantAgents = useStore((s) => s.assistantStatus?.agents)
  return useMemo(() => ({
    run: (id) => runs[id] && runLabel(runs[id]),
    workspace: (id) => workspaces[id]?.name,
    agent: (name) => agents?.find((a) => a.name === name)?.display_name ?? assistantAgents?.find((a) => a.name === name)?.display_name,
  }), [runs, workspaces, agents, assistantAgents])
}

/**
 * Whether the store, once loaded, no longer holds the target. Swarms load on
 * demand, so only runs and workspaces can tell; a launch's own run can reach
 * the transcript before the store hears of it, so it never reads as gone.
 */
export function useTargetGone(action: AssistantAction): boolean {
  const target = action.target
  return useStore((s) => {
    if (!s.hydrated || !target || launches.has(action.tool)) return false
    if (target.kind === 'run') return !s.runs[target.id] && !s.deletingRuns[target.id]
    if (target.kind === 'workspace') return !s.workspaces[target.id]
    return false
  })
}

export function useOpenTarget(): (target: ActionTarget) => void {
  const sheet = useContext(AssistantSheet)
  return (target) => {
    if (target.url) {
      window.open(target.url, '_blank', 'noopener,noreferrer')
      return
    }
    if (!target.route) return
    useStore.getState().navigate(target.route.name, target.route.params)
    if (sheet) useStore.getState().setAssistantOpen(false)
  }
}

/**
 * A launched run or swarm keeps going after the panel closes. Swarms load
 * per page, so a launched swarm the store has not read counts as working.
 */
export function useBackground(action: AssistantAction): boolean {
  return useStore((s) => {
    if (action.state !== 'done' || !launches.has(action.tool) || !action.target) return false
    if (action.target.kind === 'run') {
      const run = s.runs[action.target.id]
      return run !== undefined && !isTerminal(run.status)
    }
    if (action.target.kind !== 'swarm') return false
    const mission = s.missions[action.target.id]
    return mission === undefined || (mission.phase !== 'completed' && mission.phase !== 'cancelled')
  })
}

function useSwarm(action: AssistantAction): string | undefined {
  const id = action.state === 'done' && launches.has(action.tool) && action.target?.kind === 'swarm' ? action.target.id : undefined
  const known = useStore((s) => id !== undefined && s.missions[id] !== undefined)
  const [error, setError] = useState<string>()
  useEffect(() => {
    if (!id || known) return
    api.missionShow(id).then((result) => useStore.getState().upsertMission(result.mission), (err) => setError(`Could not read swarm ${id}: ${message(err)}`))
  }, [id, known])
  return error
}

export function retryDraft(action: AssistantAction, names: ActionNames): void {
  const retry = `Retry: ${actionSummary({ ...action, state: 'failed' }, names)}`
  const typed = useStore.getState().assistantDraft.trimEnd()
  useStore.getState().setAssistantDraft(typed ? `${typed}\n\n${retry}` : retry)
  document.querySelector<HTMLTextAreaElement>('#assistant textarea')?.focus()
}

export function ActionCard({ action }: { action: AssistantAction }) {
  const names = useActionNames()
  const open = useOpenTarget()
  const gone = useTargetGone(action)
  const swarmError = useSwarm(action)
  const background = useBackground(action) && !swarmError
  const details = useStore((s) => s.expandedRows[assistantSessionID]?.[action.id] ?? false)
  const setRow = useStore((s) => s.setSessionRow)
  const summary = actionSummary(action, names)
  const tone = background ? 'working' : actionTone[action.state]
  const state = stateLine(action, background)
  if (action.state === 'approval') {
    return (
      <div data-slot="action-card" data-state={action.state} role="group" aria-label={`${summary}. ${state}`} className="flex min-w-0 items-start gap-2 px-3 text-ui-sm">
        <span className="flex h-5 shrink-0 items-center"><StatusDot tone={tone} pulse /></span>
        <p className="min-w-0 flex-1 break-words"><span className="text-text">{summary}</span><span className={toneClasses(tone).text}> · {state} below</span></p>
      </div>
    )
  }
  const known = isKnownAction(action.tool)
  const facts = launchLine(action, names) ?? watchLine(action)
  const retry = action.state === 'failed' || action.state === 'declined' || action.state === 'expired' || action.state === 'unfinished'
  const link = action.target && !gone && (action.target.route || action.target.url) ? action.target : undefined
  const shownError = action.error && !quiet.has(action.state) ? action.error : undefined
  return (
    <div
      data-slot="action-card"
      data-state={action.state}
      role="group"
      aria-label={`${summary}. ${state}`}
      className="flex min-w-0 flex-col gap-1.5 rounded-panel border border-seam bg-raised px-3 py-2.5 text-ui"
    >
      <div className="flex min-w-0 items-start gap-2">
        <span className="flex h-5 shrink-0 items-center"><StatusDot tone={tone} pulse={tone === 'working'} /></span>
        <p className="min-w-0 flex-1 font-medium break-words">{summary}</p>
        <span className="flex h-5 shrink-0 items-center text-ui-sm text-muted tabular-nums"><RelativeTime at={action.at} /></span>
      </div>
      <p className={cn('pl-5 text-ui-sm', toneClasses(tone).text)}>{state}</p>
      {facts && <p className="pl-5 text-ui-sm break-words text-muted">{facts}</p>}
      {!known && <InputRows input={action.input} />}
      {shownError && (
        <p role="alert" className="ml-5 rounded-control bg-state-failed-soft px-2 py-1 font-code text-ui-sm break-words whitespace-pre-wrap text-text">
          {shownError}
        </p>
      )}
      {swarmError && <p className="pl-5 text-ui-sm break-words text-muted">{swarmError}</p>}
      {gone && goneWord[action.target!.kind] && <p className="pl-5 text-ui-sm text-muted">{goneWord[action.target!.kind]}</p>}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 pl-5">
        {link && (
          <Button variant="link" size="sm" onClick={() => open(link)}>
            {targetWord[link.kind]}
            {link.url && <ExternalLink aria-hidden className="size-3.5" />}
          </Button>
        )}
        {retry && (
          <Button variant="secondary" size="sm" onClick={() => retryDraft(action, names)}>
            <RotateCcw aria-hidden />
            Retry
          </Button>
        )}
        <Button variant="quiet" size="sm" aria-expanded={details} onClick={() => setRow(assistantSessionID, action.id, !details)}>
          {details ? 'Hide details' : 'Details'}
        </Button>
      </div>
      {details && (
        <div className="flex min-w-0 flex-col gap-2 pl-5">
          <WholeItem seq={action.seq} />
          <ToolValue label={`Input to ${action.tool}`} value={action.input} />
          {action.error && !shownError && <ToolValue label="Error" value={action.error} />}
          {action.result !== undefined && !action.error && <ToolValue label="Result" value={action.result} />}
        </div>
      )}
    </div>
  )
}

function WholeItem({ seq }: { seq: number | undefined }) {
  const [error, setError] = useState<string>()
  useEffect(() => {
    if (seq === undefined) return
    loadWholeItem(useStore, api, assistantSessionID, seq).catch((err) => setError(message(err)))
  }, [seq])
  if (seq === undefined) return null
  return <p role={error ? 'alert' : 'status'} className={cn('text-ui-sm', error ? 'text-state-failed' : 'text-muted')}>{error ?? 'Loading the whole call…'}</p>
}

function InputRows({ input }: { input: Record<string, unknown> }) {
  const rows = paramRows(input)
  if (rows.length === 0) return null
  return (
    <dl className="flex min-w-0 flex-col pl-5 text-ui-sm">
      {rows.map(([key, value]) => (
        <div key={key} className="flex min-w-0 gap-3 py-0.5">
          <dt className="w-20 shrink-0 text-muted">{key}</dt>
          <dd className="line-clamp-3 min-w-0 flex-1 break-words whitespace-pre-wrap">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

export function ReadsLine({ id, actions }: { id: string; actions: AssistantAction[] }) {
  const open = useStore((s) => s.expandedRows[assistantSessionID]?.[id] ?? false)
  const setRow = useStore((s) => s.setSessionRow)
  const failed = actions.filter((action) => action.state === 'failed').length
  const running = actions.some((action) => action.state === 'running')
  return (
    <div data-slot="action-reads" className="flex min-w-0 flex-col text-ui-sm text-muted">
      <Button
        variant="quiet"
        aria-expanded={open}
        onClick={() => setRow(assistantSessionID, id, !open)}
        className="min-h-7 w-full min-w-0 justify-start gap-1 text-left font-normal whitespace-normal hover:not-aria-disabled:no-underline coarse:min-h-11"
      >
        <span className="flex w-5 shrink-0 justify-center"><StatusDot tone={failed ? 'failed' : running ? 'working' : 'neutral'} pulse={running} /></span>
        <span className="min-w-0 flex-1 break-words">
          {readSummary(actions)}
          {failed > 0 && <span className="text-state-failed"> · {failed} failed</span>}
        </span>
        <ChevronRight aria-hidden className={cn('size-3.5 transition-transform', open && 'rotate-90')} />
      </Button>
      {open && (
        <ul className="flex min-w-0 flex-col gap-1 pb-1 pl-5">
          {actions.map((action) => <ReadEntry key={action.id} action={action} />)}
        </ul>
      )}
    </div>
  )
}

function ReadEntry({ action }: { action: AssistantAction }) {
  const params = paramRows(action.input).map(([key, value]) => `${key} ${value}`).join(' · ')
  return (
    <li className="flex min-w-0 flex-col gap-1">
      <WholeItem seq={action.seq} />
      <span className="flex min-w-0 items-center gap-1.5">
        <StatusDot tone={actionTone[action.state]} />
        <span className="min-w-0 break-words text-text">{readNoun(action.tool)[0]}</span>
        {params && <span className="min-w-0 break-all">{params}</span>}
      </span>
      {action.error
        ? <span className="pl-4 font-code break-words whitespace-pre-wrap text-state-failed">{action.error}</span>
        : action.result !== undefined && <div className="pl-4"><ToolValue label="Result" value={action.result} /></div>}
    </li>
  )
}
