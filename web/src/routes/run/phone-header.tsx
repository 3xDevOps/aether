import { useEffect, useState } from 'react'
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
import { runLabel, stateName, toneOf } from '@/lib/status'
import { historyDepth } from '@/lib/url-state'
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

const inTabs = (item: RunView): boolean => (tabViews as readonly RunView[]).includes(item)

/** The terminal's own input has focus on a touch screen, so the soft keyboard is up. */
export function useTerminalTyping(): boolean {
  const coarse = useMediaQuery(coarsePointer)
  const [typing, setTyping] = useState(false)
  useEffect(() => {
    if (!coarse) return
    // focusout lands on the body before the next focusin, so the check waits for both.
    const update = () => setTimeout(() => setTyping(document.activeElement?.matches('.xterm-helper-textarea') === true))
    document.addEventListener('focusin', update)
    document.addEventListener('focusout', update)
    return () => {
      document.removeEventListener('focusin', update)
      document.removeEventListener('focusout', update)
      setTyping(false)
    }
  }, [coarse])
  return typing
}

function leave() {
  if (historyDepth() > 0) window.history.back()
  else useStore.getState().navigate('sessions')
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
  onWorkflow?: (opener: HTMLElement | null) => void
  extra: ExtraItem[]
  pullRequest: React.ReactNode
}) {
  const shown = useRunPresentation(run)
  const assistant = useAssistantAvailable()
  const totals = useStore(useShallow((s) => {
    const snapshot = s.diffs[run.id]?.snapshots[0] ?? s.diffs[run.id]?.persisted
    return snapshot?.truncated ? undefined : diffTotals(snapshot?.files)
  }))
  const order = defaultView(run) === 'terminal' ? standardTabs : tabViews
  const tabs = [...order.filter((item) => views.includes(item)), ...(inTabs(view) ? [] : [view])]
  const elsewhere: ExtraItem[] = views
    .filter((item): item is OverflowView => !tabs.includes(item))
    .map((item) => ({ id: `view-${item}`, label: runViewLabel[item], Icon: viewIcon[item], onSelect: () => nav.go(item) }))
  const panels: ExtraItem[] = [
    { id: 'details', label: detailsOpen ? 'Hide details' : 'Session details', Icon: PanelRight, onSelect: onDetails },
    ...(onWorkflow ? [{ id: 'workflow', label: 'Workflow', Icon: Network, onSelect: onWorkflow }] : []),
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
              {!keyboard && <span aria-hidden> · {agentName} · {modeLabel[run.mode] ?? run.mode}</span>}
            </span>
          </p>
        </div>
        {primary && !keyboard && <Button size="sm" onClick={primary.act}>{primary.label}</Button>}
        <RunActions run={run} compact views={[...elsewhere, ...panels]} extra={extra} />
      </header>
      {!keyboard && (
        <div className="flex h-11 shrink-0 items-stretch border-b border-seam">
          <TabsList aria-label="Session views" className="h-11 min-w-0 flex-1 gap-0 coarse:h-11">
            {tabs.map((item) => (
              <TabsTrigger key={item} value={item} className="min-w-0 flex-1 justify-center px-2">
                {runViewLabel[item]}
                {item === 'changes' && totals && (totals.additions > 0 || totals.deletions > 0) && (
                  <span aria-hidden className="text-ui-sm font-normal text-muted tabular-nums">+{totals.additions} −{totals.deletions}</span>
                )}
              </TabsTrigger>
            ))}
          </TabsList>
          {pullRequest}
        </div>
      )}
      <ConnectionLine className="border-b border-seam px-4 py-1" />
    </>
  )
}
