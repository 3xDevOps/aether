import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Menu, MenuCheckboxItem, MenuContent, MenuTrigger } from '@/components/ui/menu'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { SlidingIndicator } from '@/components/ui/sliding-indicator'
import { coarsePointer, useDelayed, useElementSize, useMediaQuery } from '@/lib/hooks'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { useAnnouncement } from '@/lib/use-announcement'
import { cn, focusRingInset } from '@/lib/utils'
import { maxSpan, minSpan, presetOf, tickInterval, widerWindow, windowSpan } from '@/routes/board/lanes/axis'
import { laneEdges, mailOf } from '@/routes/board/lanes/edges'
import { trackGutter } from '@/routes/board/lanes/lane-row'
import { LanesView } from '@/routes/board/lanes/lanes'
import { Legend } from '@/routes/board/lanes/legend'
import { buildLanes, coarseMetrics, fineMetrics, reuseLanes, type LanesModel, type RunLane } from '@/routes/board/lanes/model'
import { statusChanges } from '@/routes/board/lanes/segments'
import { useLaneAgents, useLaneHistory } from '@/routes/board/lanes/use-lane-data'
import { useMapRuns, type BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { useStateContext } from '@/store/hooks'
import { laneEdgeKinds, laneWindows, type LaneEdgeKind, type LaneWindow } from '@/store/lanes'

const phone = '(max-width: 767px)'
const minute = 60_000

const edgeWords: Record<LaneEdgeKind, string> = {
  message: 'Agent messages',
  overlap: 'Overlaps',
}

const announced = new Set<PresentationState>(['needs-you', 'failed', 'interrupted', 'ready', 'completed', 'merged'])
const urgent = new Set<PresentationState>(['needs-you', 'failed', 'interrupted'])
const word = (state: PresentationState) => stateLabel[state]
const canShowAgents = (card: BoardCard) => card.run.acp === true

function useNow(interval: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), interval)
    return () => clearInterval(timer)
  }, [interval])
  return now
}

function WindowControl({ value, custom, onChange }: { value: LaneWindow; custom: boolean; onChange: (window: LaneWindow) => void }) {
  return (
    <div
      role="radiogroup"
      aria-label="Time window"
      className="relative flex h-7 items-center gap-0.5 rounded-control bg-chrome p-0.75 coarse:h-11"
      onKeyDown={(event) => {
        if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
        event.preventDefault()
        const next = laneWindows[(laneWindows.indexOf(value) + (event.key === 'ArrowRight' ? 1 : -1) + laneWindows.length) % laneWindows.length]
        onChange(next)
        event.currentTarget.querySelector<HTMLElement>(`[data-indicator-key="${next}"]`)?.focus()
      }}
    >
      <SlidingIndicator activeKey={custom ? undefined : value} className="rounded-chip bg-raised ring-1 ring-seam" />
      {laneWindows.map((key) => {
        const checked = !custom && key === value
        return (
          <Button
            key={key}
            variant="ghost"
            size="sm"
            role="radio"
            aria-checked={checked}
            tabIndex={checked || (custom && key === value) ? 0 : -1}
            data-indicator-key={key}
            className={cn(focusRingInset, 'relative h-full rounded-chip px-2 shadow-none hover:bg-transparent', checked ? 'text-text' : 'text-muted')}
            onClick={() => onChange(key)}
          >
            {key}
          </Button>
        )
      })}
      {custom && <span className="relative px-2 text-ui text-muted">Custom</span>}
    </div>
  )
}

export function BoardLanes({ agentNames }: { agentNames: Record<string, string> }) {
  const workspace = useStore((s) => s.activeWorkspace)
  const workspaceName = useStore((s) => s.workspaces[s.activeWorkspace]?.name ?? s.activeWorkspace)
  const allWorkspaces = useStore((s) => s.boardMapAllWorkspaces)
  const setAllWorkspaces = useStore((s) => s.setBoardMapAllWorkspaces)
  const mineOnly = useStore((s) => s.mineOnly)
  const setMineOnly = useStore((s) => s.setMineOnly)
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const prefs = useStore(useShallow((s) => ({ window: s.laneWindow, group: s.laneGroup, hidden: s.laneHidden })))
  const { setLaneWindow, setLaneGroup, setLaneHidden } = useStore.getState()
  const overlaps = useStore((s) => s.overlaps)
  const ctx = useStateContext()
  const scope = allWorkspaces ? '' : workspace
  const { cards, archivedCards } = useMapRuns(scope)
  const [showArchived, setShowArchived] = useState(false)
  useEffect(() => { setShowArchived(false) }, [scope])
  useEffect(() => { if (!archivedCards.length) setShowArchived(false) }, [archivedCards.length])
  const visible = useMemo(
    () => (showArchived ? [...cards.filter((card) => card.group !== 'finished'), ...archivedCards] : cards),
    [showArchived, cards, archivedCards],
  )

  const preset = prefs.window ?? (cards.some((card) => card.group === 'working') ? '1h' : '6h')
  const [custom, setCustom] = useState<number | null>(null)
  const [end, setEnd] = useState<number | null>(null)
  const span = custom ?? windowSpan[preset]
  const [sizeRef, size] = useElementSize<HTMLElement>()
  const compact = useMediaQuery(phone)
  const metrics = useMediaQuery(coarsePointer) ? coarseMetrics : fineMetrics
  const labelWidth = compact ? 48 : 240
  const width = Math.max(120, size.width - labelWidth - trackGutter)
  const now = useNow(tickInterval(span, width))
  const following = end === null || end >= now
  const timeWindow = useMemo(() => {
    const stop = following ? now : end!
    return { start: stop - span, end: stop }
  }, [following, now, end, span])

  const workspaces = useMemo(() => [...new Set(visible.map((card) => card.run.workspace_id))].sort(), [visible])
  const history = useLaneHistory(workspaces, Math.floor(timeWindow.start / minute) * minute)
  const { agents, toggle: toggleAgents, retry: retryAgents } = useLaneAgents()
  const [open, setOpen] = useState<ReadonlyMap<string, boolean>>(() => new Map())

  const changes = useMemo(() => statusChanges(history.events), [history.events])
  const floor = useCallback((id: string) => {
    const h = history.byWorkspace.get(id)
    return !h || h.error ? -Infinity : h.floor
  }, [history.byWorkspace])
  const previousModel = useRef<LanesModel | null>(null)
  const model = useMemo(() => {
    previousModel.current = reuseLanes(buildLanes({
      cards: visible,
      ctx,
      changes,
      floor,
      window: timeWindow,
      now,
      group: prefs.group,
      open,
      agents,
      canShowAgents,
      archived: showArchived,
    }), previousModel.current)
    return previousModel.current
  }, [visible, ctx, changes, floor, timeWindow, now, prefs.group, open, agents, showArchived])
  const g = useMemo(() => ({ window: timeWindow, width, labelWidth, compact, metrics }), [timeWindow, width, labelWidth, compact, metrics])

  const hidden = useMemo(() => new Set<LaneEdgeKind>(prefs.hidden), [prefs.hidden])
  const mail = useMemo(() => mailOf(history.events), [history.events])
  const edgeInput = useMemo(() => ({
    drawn: model.drawn,
    mail,
    window: timeWindow,
    now,
    bucket: (24 * span) / width,
    overlaps,
  }), [model.drawn, mail, timeWindow, now, span, width, overlaps])
  const edges = useMemo(() => laneEdges({ ...edgeInput, hidden }), [edgeInput, hidden])

  const announceItems = useMemo(
    () => new Map(visible.map((card) => [card.run.id, { state: card.group === 'needs-you' ? 'needs-you' as const : card.state, label: runLabel(card.run) }])),
    [visible],
  )
  const announcement = useAnnouncement(announceItems, announced, urgent, word)

  const total = cards.length + archivedCards.length
  const loading = useDelayed(!hydrated && error === null && total === 0)
  const wider = widerWindow(span)
  const windowWord = custom === null ? `the last ${preset}` : 'this window'
  const choose = (key: LaneWindow) => {
    setLaneWindow(key)
    setCustom(null)
  }
  const hiddenByMine = mineOnly && Object.values(ctx.runs).some((run) => (!scope || run.workspace_id === scope) && run.member_id !== ctx.viewerID)

  let body
  if (error !== null && total === 0) {
    body = <div className="p-4"><Callout role="alert" tone="failed" title="Cannot reach the server">{dead ? error : 'Retrying.'}</Callout></div>
  } else if (loading) {
    body = (
      <div className="flex flex-col gap-2 p-4">
        <p role="status" className="text-ui-sm text-muted">Loading runs…</p>
        {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-6" />)}
      </div>
    )
  } else if (visible.length === 0) {
    body = (
      <div className="flex flex-col items-start gap-2 p-4 text-ui-sm text-muted">
        <p>No runs in this view.</p>
        {hiddenByMine && <Button variant="link" size="sm" onClick={() => setMineOnly(false)}>Show everyone&apos;s runs</Button>}
      </div>
    )
  } else {
    body = (
      <>
        {model.inWindow === 0 && (
          <div className="flex shrink-0 flex-wrap items-center gap-2 px-4 pt-3 text-ui-sm text-muted">
            <p>Nothing ran in {windowWord}.</p>
            {wider && <Button variant="link" size="sm" onClick={() => choose(wider)}>Show {wider}</Button>}
          </div>
        )}
        <LanesView
          model={model}
          edges={edges}
          edgeInput={edgeInput}
          overlapsShown={!hidden.has('overlap')}
          g={g}
          now={now}
          agentNames={agentNames}
          widen={wider}
          onWiden={() => wider && choose(wider)}
          onToggle={(key, value) => setOpen((current) => new Map(current).set(key, value))}
          onAgents={toggleAgents}
          onRetryAgents={(runID) => void retryAgents(runID)}
          canShowAgents={(lane: RunLane) => canShowAgents(lane.card)}
          onPan={(ms) => setEnd((current) => {
            const next = (current ?? Date.now()) + ms
            return next >= Date.now() ? null : next
          })}
          onZoom={(factor, anchor) => {
            const next = Math.min(maxSpan, Math.max(minSpan, span * factor))
            const stop = anchor + ((timeWindow.end - anchor) * next) / span
            const matched = presetOf(next)
            if (matched) choose(matched)
            else setCustom(next)
            setEnd(stop >= Date.now() ? null : stop)
          }}
          onNow={() => setEnd(null)}
        />
      </>
    )
  }

  return (
    <section ref={sizeRef} aria-label="Run lanes view" className="flex min-h-0 min-w-0 flex-1 flex-col">
      <header className="flex flex-wrap items-center gap-2 border-b border-seam px-4 py-2">
        <h2 className="text-ui-sm font-medium">Runs <span className="text-muted tabular-nums">{model.inWindow}</span></h2>
        <Select value={scope ? 'workspace' : 'all'} onValueChange={(value) => setAllWorkspaces(value === 'all')}>
          <SelectTrigger aria-label="Lanes workspace scope" className="w-40"><SelectValue /></SelectTrigger>
          <SelectContent>
            {workspace && <SelectItem value="workspace">{workspaceName}</SelectItem>}
            <SelectItem value="all">All workspaces</SelectItem>
          </SelectContent>
        </Select>
        <Button variant={mineOnly ? 'secondary' : 'ghost'} size="sm" aria-pressed={mineOnly} onClick={() => setMineOnly(!mineOnly)} hint="Mine filters Working and Finished. Needs you always includes every workspace.">Mine</Button>
        {(archivedCards.length > 0 || showArchived) && (
          <Button variant="ghost" size="sm" aria-pressed={showArchived} onClick={() => setShowArchived(!showArchived)}>
            {showArchived ? 'Back to Finished' : `Archived (${archivedCards.length})`}
          </Button>
        )}
        <WindowControl value={preset} custom={custom !== null} onChange={choose} />
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="sm">Show</Button>
          </MenuTrigger>
          <MenuContent align="start">
            {laneEdgeKinds.map((kind) => (
              <MenuCheckboxItem
                key={kind}
                checked={!hidden.has(kind)}
                onSelect={(event) => event.preventDefault()}
                onCheckedChange={(checked) => setLaneHidden(checked ? prefs.hidden.filter((k) => k !== kind) : [...prefs.hidden, kind])}
              >
                {edgeWords[kind]}
              </MenuCheckboxItem>
            ))}
          </MenuContent>
        </Menu>
        <Select value={prefs.group} onValueChange={(value) => setLaneGroup(value === 'owner' ? 'owner' : 'status')}>
          <SelectTrigger aria-label="Group lanes by" className="w-28"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="status">By status</SelectItem>
            <SelectItem value="owner">By owner</SelectItem>
          </SelectContent>
        </Select>
        <Legend />
        {!following && <Button variant="secondary" size="sm" onClick={() => setEnd(null)}>Now</Button>}
        {history.loading && <span role="status" className="text-ui-sm text-muted">Loading history…</span>}
        {!history.loading && history.capped && <span className="text-ui-sm text-muted">Older history not loaded</span>}
      </header>
      {history.error && (
        <div className="shrink-0 px-4 pt-3">
          <Callout role="alert" tone="failed" title="Run history" actions={<Button variant="secondary" size="sm" onClick={history.retry}>Retry</Button>}>
            {history.error}
          </Callout>
        </div>
      )}
      {body}
      <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">{announcement}</span>
    </section>
  )
}
