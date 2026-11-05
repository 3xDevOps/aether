import type * as React from 'react'
import { useShallow } from 'zustand/react/shallow'
import { useAssistantAvailable } from '@/components/assistant/button'
import { ChartGantt, ChevronLeft, FolderTree, Globe, Lock, Network, PanelRight, Search, Sparkles } from '@/components/icons'
import { RunActions, type ExtraItem } from '@/components/run-actions'
import { ConnectionLine } from '@/components/shell/connection'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/status-dot'
import { TabsList, TabsTrigger } from '@/components/ui/tabs'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { useKeyboardUp } from '@/lib/keyboard-inset'
import { cn } from '@/lib/utils'
import { runLabel, stateName, toneOf } from '@/lib/status'
import { historyDepth, redirectRoute, sessionEntry } from '@/lib/url-state'
import { modeLabel } from '@/routes/run/agent-name'
import type { RunNavigation } from '@/routes/run/header'
import { defaultView, runViewLabel, type RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { diffTotals } from '@/store/diff'
import { useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

const tabViews = ['session', 'terminal', 'changes'] as const
// A Standard session's agent runs only in its terminal, so its tabs lead with Terminal.
const standardTabs: RunView[] = ['terminal', 'session', 'changes']

type OverflowView = Exclude<RunView, (typeof tabViews)[number]>

const viewIcon: Record<OverflowView, React.ComponentType<{ className?: string }>> = {
  trace: ChartGantt,
  files: FolderTree,
  browser: Globe,
}

/** A text field, the composer or the terminal's input, has focus on a touch screen with the keyboard up. */
export function useKeyboardFold(): boolean {
  const coarse = useMediaQuery(coarsePointer)
  return useKeyboardUp('textarea') && coarse
}

/** Steps back past every entry this session pushed, a subagent's included, to the page it was opened from. */
function leave() {
  const entered = sessionEntry()
  const steps = entered === undefined ? 0 : entered - 1 - historyDepth()
  if (steps < 0) window.history.go(steps)
  else redirectRoute(useStore, { name: 'sessions', params: {} })
}

export function PhoneRunHeader({ run, view, views, nav, agentName, keyboard, primary, detailsOpen, onDetails, onWorkflow, extra, pullRequest }: {
  run: RunRecord
  view: RunView
  views: RunView[]
  nav: RunNavigation
  agentName: string
  keyboard: boolean
  primary: { label: string; act: () => void } | null
  detailsOpen: boolean
  onDetails: (opener: HTMLElement | null) => void
  onWorkflow: (opener: HTMLElement | null) => void
  extra: ExtraItem[]
  /** Kept to the Changes tab, where it cannot crowd the tabs. */
  pullRequest: React.ReactNode
}) {
  const shown = useRunPresentation(run)
  const assistant = useAssistantAvailable()
  // Chat shows an open request's own card, whose answer is the screen's one filled button.
  const asking = useStore((s) => (s.acpSessions[run.id]?.pending.length ?? 0) > 0)
  const totals = useStore(useShallow((s) => {
    const snapshot = s.diffs[run.id]?.snapshots[0] ?? s.diffs[run.id]?.persisted
    return snapshot?.truncated ? undefined : diffTotals(snapshot?.files)
  }))
  const order: readonly RunView[] = defaultView(run) === 'terminal' ? standardTabs : tabViews
  const tabs = [...order.filter((item) => views.includes(item)), ...(order.includes(view) ? [] : [view])]
  const elsewhere: ExtraItem[] = views
    .filter((item): item is OverflowView => !tabs.includes(item))
    .map((item) => ({ id: `view-${item}`, label: runViewLabel[item], Icon: viewIcon[item], onSelect: () => nav.go(item) }))
  const panels: ExtraItem[] = [
    { id: 'details', label: detailsOpen ? 'Hide details' : 'Session details', Icon: PanelRight, onSelect: onDetails },
    { id: 'workflow', label: 'Workflow', Icon: Network, onSelect: onWorkflow },
    { id: 'search', label: 'Search…', Icon: Search, onSelect: () => useStore.getState().togglePalette(true) },
    ...(assistant ? [{ id: 'assistant', label: 'Assistant', Icon: Sparkles, onSelect: () => useStore.getState().setAssistantOpen(true) }] : []),
  ]
  const state = run.switching ? `Switching to ${modeLabel[run.switching] ?? run.switching}…` : stateName(shown)

  return (
    <>
      <header data-slot="pane-header" className={keyboard ? 'flex h-11 shrink-0 items-center gap-1 border-b border-seam px-1' : 'flex h-12 shrink-0 items-center gap-1 px-1'}>
        <Button variant="ghost" size="icon" label="Back" onClick={leave}>
          <ChevronLeft />
        </Button>
        <div className={keyboard ? 'flex min-w-0 flex-1 items-center gap-2' : 'flex min-w-0 flex-1 flex-col'}>
          <h1 tabIndex={-1} className="flex min-w-0 items-center gap-1.5 text-ui font-medium outline-none">
            <span className="truncate">{runLabel(run)}</span>
            {run.protected && <Lock role="img" aria-label="Protected: only the owner or an admin can message, control or stop this session" className="size-3.5 shrink-0 text-muted" />}
          </h1>
          <p className="flex min-w-0 shrink-0 items-center gap-1.5 text-ui-sm text-muted">
            <StatusDot tone={toneOf(shown)} pulse={shown.state === 'working'} />
            <span className={keyboard ? 'whitespace-nowrap max-[399px]:sr-only' : 'min-w-0 truncate'}>
              <span role="status">{state}</span>
              {!keyboard && <span> · {agentName} · {modeLabel[run.mode] ?? run.mode}</span>}
            </span>
          </p>
        </div>
        {primary && !keyboard && <Button size="sm" variant={asking && view === 'session' ? 'secondary' : 'primary'} onClick={primary.act}>{primary.label}</Button>}
        <RunActions run={run} compact views={[...elsewhere, ...panels]} extra={extra} />
      </header>
      {/* Folded, the tabs stay mounted out of sight: each view's panel is labelled by its tab. */}
      <div className={keyboard ? 'sr-only' : 'flex h-11 shrink-0 items-stretch border-b border-seam'}>
        <TabsList aria-label="Session views" className="h-11 min-w-0 flex-1 gap-0 coarse:h-11">
          {tabs.map((item) => (
            <TabsTrigger key={item} value={item} className="min-w-0 flex-1 justify-center px-2">
              {runViewLabel[item]}
              {item === 'changes' && totals && (totals.additions > 0 || totals.deletions > 0) && (
                <span aria-hidden className={cn('text-ui-sm font-normal text-muted tabular-nums', tabs.length > 3 && 'max-[399px]:hidden')}>+{totals.additions} −{totals.deletions}</span>
              )}
            </TabsTrigger>
          ))}
        </TabsList>
      </div>
      {!keyboard && view === 'changes' && pullRequest && (
        <div className="flex h-11 shrink-0 items-center border-b border-seam px-2 [&>[role=group]]:ml-0 [&>[role=group]]:pl-0">{pullRequest}</div>
      )}
      <ConnectionLine className="border-b border-seam px-4 py-1" />
    </>
  )
}
