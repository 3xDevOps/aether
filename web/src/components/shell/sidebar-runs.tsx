import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { ChevronDown, ChevronRight } from '@/components/icons'
import { ListRow } from '@/components/ui/list-row'
import { StatusDot } from '@/components/ui/status-dot'
import { useDelayed } from '@/lib/hooks'
import { needsYou, type StateContext } from '@/lib/needs-you'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { cn } from '@/lib/utils'
import { approveRequest, targetRoute } from '@/routes/board/card-action'
import { isRunRoute } from '@/routes/run/views'
import { useStore } from '@/store'
import { useRun, useSidebarGroups, useStateContext } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { stateContextOf, type RunTree, type SidebarGroup, type SwarmSummary } from '@/store/selectors'
import type { Route } from '@/store/ui'

export const runRowSelector = '#sidebar-runs [data-run-row]'

const needsYouShown = 5

export function needsYouRoute(run: RunRecord, ctx: StateContext): Route {
  return targetRoute(run, needsYou(run, ctx)?.target)
}

function swarmCounts({ counts }: SwarmSummary): string {
  return [
    counts.working > 0 && `${counts.working} working`,
    counts.needsYou > 0 && `${counts.needsYou} needs you`,
    counts.failed > 0 && `${counts.failed} failed`,
    counts.done > 0 && `${counts.done} done`,
  ].filter(Boolean).join(' · ')
}

function useRovingRows(region: React.RefObject<HTMLElement | null>) {
  const last = useRef<Element | null>(null)
  useLayoutEffect(() => {
    const rows = [...(region.current?.querySelectorAll<HTMLElement>('[data-run-row]') ?? [])]
    const active =
      rows.find((row) => row === last.current) ??
      rows.find((row) => row.getAttribute('aria-current') === 'page') ??
      rows[0]
    for (const row of rows) row.tabIndex = row === active ? 0 : -1
  })
  const onFocus = useCallback((event: React.FocusEvent) => {
    if (event.target instanceof HTMLElement && event.target.dataset.runRow !== undefined) {
      last.current = event.target
      for (const row of region.current?.querySelectorAll<HTMLElement>('[data-run-row]') ?? []) {
        row.tabIndex = row === event.target ? 0 : -1
      }
    }
  }, [region])
  const onKeyDown = useCallback((event: React.KeyboardEvent) => {
    const rows = [...(region.current?.querySelectorAll<HTMLElement>('[data-run-row]') ?? [])]
    const index = rows.indexOf(event.target as HTMLElement)
    if (index === -1) return
    const next =
      event.key === 'ArrowDown' ? Math.min(rows.length - 1, index + 1)
        : event.key === 'ArrowUp' ? Math.max(0, index - 1)
          : event.key === 'Home' ? 0
            : event.key === 'End' ? rows.length - 1
              : null
    if (next === null) return
    event.preventDefault()
    rows[next]?.focus()
  }, [region])
  return { onFocus, onKeyDown }
}

export function SidebarRuns() {
  const groups = useSidebarGroups()
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const loading = useDelayed(!hydrated && error === null && groups.length === 0)
  const region = useRef<HTMLElement>(null)
  const roving = useRovingRows(region)
  const [moreBelow, setMoreBelow] = useState(false)
  const measure = useCallback(() => {
    const el = region.current
    if (el) setMoreBelow(el.scrollHeight - el.scrollTop - el.clientHeight > 1)
  }, [])
  useEffect(() => {
    const el = region.current
    if (!el) return
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    if (el.firstElementChild) observer.observe(el.firstElementChild)
    return () => observer.disconnect()
  }, [measure])

  return (
    <section
      ref={region}
      id="sidebar-runs"
      aria-label="Runs"
      className={cn(
        'min-h-0 flex-1 overflow-y-auto px-2 pb-2',
        moreBelow && '[mask-image:linear-gradient(to_bottom,black_calc(100%-2rem),transparent)]',
      )}
      onScroll={measure}
      onFocus={roving.onFocus}
      onKeyDown={roving.onKeyDown}
    >
      <div>
        {groups.length === 0 ? (
          error !== null ? (
            <p className="px-2 py-1 text-ui-sm text-muted">{dead ? error : 'Cannot reach the server. Retrying.'}</p>
          ) : loading ? (
            <p className="px-2 py-1 text-ui-sm text-muted">Loading runs</p>
          ) : hydrated && (
            <p className="px-2 py-1 text-ui-sm text-muted">No runs yet.</p>
          )
        ) : (
          groups.map((group) => {
            const open = expanded[group.key] ?? group.key !== 'finished'
            return (
              <Group
                key={group.key}
                group={group}
                expanded={open}
                onToggle={() => setExpanded((current) => ({ ...current, [group.key]: !open }))}
              />
            )
          })
        )}
      </div>
    </section>
  )
}

function Group({ group, expanded, onToggle }: { group: SidebarGroup; expanded: boolean; onToggle: () => void }) {
  const listID = `sidebar-group-${group.key}`
  const [showAll, setShowAll] = useState(false)
  const capped = group.key === 'needs-you' && !showAll && group.runs.length > needsYouShown
  const runs = capped ? group.runs.slice(0, needsYouShown) : group.runs
  return (
    <div className="pt-2">
      <div className="flex items-center gap-1">
        <h2 className="min-w-0 flex-1">
          <Button
            variant="ghost"
            size="sm"
            aria-expanded={expanded}
            aria-controls={listID}
            onClick={onToggle}
            className="w-full justify-start"
          >
            {expanded ? <ChevronDown /> : <ChevronRight />}
            <span className="truncate">{group.label}</span>
            <span className="ml-auto tabular-nums">{group.count}</span>
          </Button>
        </h2>
        {group.key === 'working' && <MineToggle />}
      </div>
      <ul id={listID} hidden={!expanded}>
        {expanded && runs.map((tree) => (
          <li key={tree.run.id}>
            <RunRowItem tree={tree} />
            {tree.children.length > 0 && (
              <ul aria-label={`Waiting in ${runLabel(tree.run)}`}>
                {tree.children.map((child) => (
                  <li key={child.run.id}>
                    <RunRowItem tree={{ ...child, children: [] }} nested />
                  </li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ul>
      {expanded && capped && (
        <Button variant="ghost" size="sm" className="w-full justify-start text-muted" onClick={() => setShowAll(true)}>
          Show all {group.count}
        </Button>
      )}
    </div>
  )
}

function MineToggle() {
  const mineOnly = useStore((s) => s.mineOnly)
  const setMineOnly = useStore((s) => s.setMineOnly)
  return (
    <Button
      variant={mineOnly ? 'secondary' : 'ghost'}
      size="sm"
      hint="Show only your runs under Working and Finished"
      aria-pressed={mineOnly}
      onClick={() => setMineOnly(!mineOnly)}
    >
      Mine
    </Button>
  )
}

function RunRowItem({ tree, nested = false }: { tree: RunTree; nested?: boolean }) {
  return (
    <RunRow
      runID={tree.run.id}
      state={tree.state}
      reason={tree.reason}
      workspaceName={tree.workspaceName}
      swarm={tree.swarm}
      unread={tree.unread}
      nested={nested}
    />
  )
}

const RunRow = memo(function RunRow({ runID, ...shown }: {
  runID: string
  state: PresentationState
  reason: string
  workspaceName?: string
  swarm?: SwarmSummary
  unread?: number
  nested: boolean
}) {
  const run = useRun(runID)
  return run ? <RunRowButton run={run} {...shown} /> : null
})

function RunRowButton({ run, state, reason, workspaceName, swarm, unread, nested }: {
  run: RunRecord
  state: PresentationState
  reason: string
  workspaceName?: string
  swarm?: SwarmSummary
  unread?: number
  nested: boolean
}) {
  const navigate = useStore((s) => s.navigate)
  const self = useStore((s) => s.info?.member.id)
  const mission = useStore((s) => (swarm && run.mission_id ? s.missions[run.mission_id] : undefined))
  const selected = useStore((s) =>
    swarm ? s.route.name === 'missions' && s.route.params.missionId === run.mission_id : isRunRoute(s.route, run.id))
  const title = swarm ? mission?.objective.split('\n')[0] || runLabel(run) : runLabel(run)
  const counts = swarm ? (unread ? `${unread} unread` : swarmCounts(swarm)) : ''
  const open = () => {
    if (state === 'needs-you') {
      const route = needsYouRoute(run, stateContextOf(useStore.getState(), Date.now()))
      navigate(route.name, route.params)
    } else if (swarm && run.mission_id) navigate('missions', { missionId: run.mission_id })
    else navigate('run', { runId: run.id })
  }
  const recedes = state !== 'needs-you' && (state !== 'working' || (!swarm && run.member_id !== self))
  const label = [stateLabel[state], workspaceName, title, swarm && !unread ? counts : reason].filter(Boolean).join(' · ')
  return (
    <ListRow
      data-run-row=""
      aria-label={label}
      title={label}
      aria-current={selected ? 'page' : undefined}
      selected={selected}
      onClick={open}
      leading={
        <>
          {nested && <span aria-hidden className="w-3 shrink-0" />}
          <StatusDot tone={state} />
        </>
      }
      trailing={swarm ? counts : <AgentGlyph agent={run.harness} />}
      hoverAction={state === 'needs-you' && <AnswerButton run={run} />}
    >
      {workspaceName && <span className="text-muted">{workspaceName} · </span>}
      <span className={cn(recedes && !selected && 'text-muted')}>{title}</span>
    </ListRow>
  )
}

function AnswerButton({ run }: { run: RunRecord }) {
  const navigate = useStore((s) => s.navigate)
  const ctx = useStateContext()
  const condition = needsYou(run, ctx)
  if (!condition) return null
  const approval = ctx.approvalsByRun[run.id]?.[0]
  const action = condition.action(run, approval)
  return (
    <Button
      variant="ghost"
      size="sm"
      tabIndex={-1}
      onClick={() => {
        if (action.kind === 'approve' && approval) void approveRequest(run.id, approval)
        else if (action.kind === 'reply') navigate('run', { runId: run.id, view: 'session', focus: 'composer' })
        else {
          const route = needsYouRoute(run, ctx)
          navigate(route.name, route.params)
        }
      }}
    >
      {action.label}
    </Button>
  )
}
