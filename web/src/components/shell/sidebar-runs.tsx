import { memo, useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { RunDetails, RunDetailsGroup } from '@/components/shell/run-details'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { ChevronDown, ChevronRight, Waypoints } from '@/components/icons'
import { ListRow } from '@/components/ui/list-row'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot } from '@/components/ui/status-dot'
import { useClock } from '@/lib/clock'
import { timeAgo } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import { needsYou, type StateContext } from '@/lib/needs-you'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { cn } from '@/lib/utils'
import { approveRequest, targetRoute } from '@/routes/board/card-action'
import { isRunRoute } from '@/routes/run/views'
import { useStore } from '@/store'
import { useRun, useRunPeople, useSidebarGroups, useStateContext } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'
import { stateContextOf, type RunTree, type SidebarGroup, type SwarmSummary } from '@/store/selectors'
import type { Route } from '@/store/ui'

export const runRowSelector = '#sidebar-runs [data-run-row]'

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
      <RunDetailsGroup>
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
            const open = expanded[group.key] ?? true
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
      </RunDetailsGroup>
    </section>
  )
}

// --spacing(3.25) is the centre of ListRow's status dot: px-2 plus half of size-2.5.
const connectors = cn(
  'relative pl-6.5',
  'before:absolute before:top-0 before:left-[calc(--spacing(3.25)-0.5px)] before:h-[calc(50%+0.5px)] before:w-[calc(--spacing(3.25)+0.5px)] before:border-b before:border-l before:border-icon-faint',
  'after:absolute after:top-[calc(50%+0.5px)] after:bottom-0 after:left-[calc(--spacing(3.25)-0.5px)] after:border-l after:border-icon-faint last:after:hidden',
)

function Group({ group, expanded, onToggle }: { group: SidebarGroup; expanded: boolean; onToggle: () => void }) {
  const listID = `sidebar-group-${group.key}`
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
        {expanded && group.runs.map((tree) => (
          <li key={tree.run.id}>
            <RunRowItem tree={tree} />
            {tree.children.length > 0 && (
              <ul aria-label={`Runs in ${runLabel(tree.run)}`}>
                {tree.children.map((child) => (
                  <li key={child.run.id} className={connectors}>
                    <RunRowItem tree={{ ...child, children: [] }} />
                  </li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ul>
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

function RunRowItem({ tree }: { tree: RunTree }) {
  return (
    <RunRow
      runID={tree.run.id}
      state={tree.state}
      reason={tree.reason}
      since={tree.since}
      workspaceName={tree.workspaceName}
      swarm={tree.swarm}
      unread={tree.unread}
    />
  )
}

const RunRow = memo(function RunRow({ runID, ...shown }: {
  runID: string
  state: PresentationState
  reason: string
  since: string
  workspaceName?: string
  swarm?: SwarmSummary
  unread?: number
}) {
  const run = useRun(runID)
  return run ? <RunRowButton run={run} {...shown} /> : null
})

const names = new Intl.ListFormat(undefined, { type: 'conjunction' })

function RunRowButton({ run, state, reason, since, workspaceName, swarm, unread }: {
  run: RunRecord
  state: PresentationState
  reason: string
  since: string
  workspaceName?: string
  swarm?: SwarmSummary
  unread?: number
}) {
  const navigate = useStore((s) => s.navigate)
  const self = useStore((s) => s.info?.member.id)
  const selected = useStore((s) => isRunRoute(s.route, run.id))
  const swarmSelected = useStore((s) => !!swarm && s.route.name === 'missions' && s.route.params.missionId === run.mission_id)
  const title = runLabel(run)
  const counts = swarm ? swarmCounts(swarm) : ''
  const open = () => {
    if (swarm) navigate('run', { runId: run.id })
    else if (state === 'needs-you') {
      const route = needsYouRoute(run, stateContextOf(useStore.getState(), Date.now()))
      navigate(route.name, route.params)
    } else navigate('run', { runId: run.id })
  }
  const unopened = run.finish_unopened === true && (state === 'done' || state === 'failed')
  const recedes = state !== 'needs-you' && !unopened && (state !== 'working' || (!swarm && run.member_id !== self))
  const members = useStore((s) => s.members)
  const people = useRunPeople(run)
  const exactTime = useId()
  const name = (id: string) => members[id]?.display_name ?? id
  const controller = run.controller_member_id
  const watching = people.filter((id) => id !== controller).map((id) => (id === self ? 'you' : name(id)))
  useClock()
  const label = [
    stateLabel[state],
    workspaceName,
    title,
    swarm && !unread ? counts : reason,
    unopened && 'Not opened yet',
    timeAgo(since),
    run.member_id === self ? 'your run' : `${name(run.member_id)}'s run`,
    controller
      ? controller === self ? 'you control' : `${name(controller)} controls`
      : controller === '' && people.length > 0 && !isTerminal(run.status) && 'nobody controls',
    watching.length > 0 && `${names.format(watching)} watching`,
  ].filter(Boolean).join(' · ')
  const indicators = (
    <span className={cn('flex items-center gap-2 text-ui-sm tabular-nums', selected ? 'text-text' : 'text-muted')}>
      <RelativeTime at={since} compact title={undefined} />
      <AgentGlyph agent={run.harness} />
      {people.length > 0 && <PeopleStack people={people} />}
    </span>
  )
  return (
    <RunDetails run={run} state={state} reason={reason} since={since} counts={counts} people={people} label={label}>
      <ListRow
        data-run-row=""
        aria-label={label}
        aria-describedby={exactTime}
        aria-current={selected ? 'page' : undefined}
        selected={selected}
        onClick={open}
        leading={
          <>
            <StatusDot tone={state} />
            <span
              data-slot="owner-mark"
              aria-hidden
              style={{ backgroundColor: members[run.member_id]?.color }}
              className="-mx-1 h-1.75 w-1.5 shrink-0 bg-icon-faint [clip-path:polygon(0_0,100%_50%,0_100%)]"
            />
          </>
        }
        trailing={!swarm ? indicators : undefined}
        action={swarm && run.mission_id && (
          <>
            <Button
              variant={swarmSelected ? 'secondary' : 'ghost'}
              size="icon-sm"
              label={`Open swarm controls for ${title}`}
              hint={counts ? `Open swarm controls · ${counts}` : 'Open swarm controls'}
              aria-current={swarmSelected ? 'page' : undefined}
              onClick={() => navigate('missions', { missionId: run.mission_id! })}
            >
              <Waypoints />
            </Button>
            {indicators}
          </>
        )}
        hoverAction={state === 'needs-you' && <AnswerButton run={run} />}
      >
        {workspaceName && <span className="text-muted">{workspaceName} · </span>}
        <span className={cn(recedes && !selected && 'text-muted')}>{title}</span>
        <span id={exactTime} className="sr-only">{new Date(since).toLocaleString()}</span>
      </ListRow>
    </RunDetails>
  )
}

const peopleShown = 3

/** The first member sits whole at the right edge; each one after shows from behind it, to its left. */
function PeopleStack({ people }: { people: string[] }) {
  const members = useStore((s) => s.members)
  const faces = people.length > peopleShown ? people.slice(0, peopleShown - 1) : people
  return (
    <span data-slot="run-people" className="pointer-events-none flex items-center">
      {people.length > faces.length && <span className="mr-1 text-ui-xs">+{people.length - faces.length}</span>}
      {[...faces].reverse().map((id, behind) => (
        <span
          key={id}
          className={cn(
            'flex rounded-full ring-[1.5px] ring-chrome group-hover/row:ring-hover-chrome group-data-[selected]/row:ring-selection',
            behind > 0 && '-ml-1.5',
          )}
        >
          <Avatar name={members[id]?.display_name ?? id} color={members[id]?.color} />
        </span>
      ))}
    </span>
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
