import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { Camera, Copy, Eye, GitBranch, Lock, PanelRight, ScrollText } from '@/components/icons'
import { RunChildNudge } from '@/components/child-nudge'
import { RunActions } from '@/components/run-actions'
import { RunRetention } from '@/components/run-retention'
import { ConnectionLine } from '@/components/shell/connection'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { PaneHeader } from '@/components/ui/pane-header'
import { TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useIsMobile } from '@/lib/breakpoints'
import { copyText } from '@/lib/clipboard'
import { memberName, resolveMember } from '@/lib/member-name'
import { extraRepoCount, repoLabel } from '@/lib/repos'
import type { AgentInfo } from '@/lib/types'
import { useClock } from '@/lib/clock'
import { shortcutLabel } from '@/lib/keybindings'
import { needsYou, terminalAction } from '@/lib/needs-you'
import { runLabel, stateLabel, stateTitle, stateWords } from '@/lib/status'
import { cn } from '@/lib/utils'
import { watchHint, watchRoute } from '@/lib/watches'
import { modeLabel } from '@/routes/run/agent-name'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { RunLineage, useChildRunsItems } from '@/routes/run/child-runs'
import { useModeSwitch } from '@/routes/run/mode-switch'
import { useOpenInEditor } from '@/routes/run/open-in-editor'
import { useRunPullRequest } from '@/routes/run/pull-request'
import { requestCardID } from '@/routes/run/requests'
import { runViewLabel, type RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import { useHeaderPrimary, useNeedsYouRuns, useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { agentState } from '@/store/subagents'
import { stateContextOf } from '@/store/selectors'
import { runWatch } from '@/store/watches'

export interface RunNavigation {
  go: (view: RunView) => void
  reveal: (cardID: string) => void
  focusComposer: () => void
  focusRequest: () => void
}

interface FrameAction {
  label: string
  act: () => void
}

/** The needs-you table names the action; the frame performs it in place. */
export function usePrimaryAction(run: RunRecord, view: RunView, agent: AgentTerminal, nav: RunNavigation): FrameAction | null {
  const now = useClock()
  const condition = useStore((s) => needsYou(run, stateContextOf(s, now)))
  const room = useStore((s) => s.roomMessages[run.id])
  const navigate = useStore((s) => s.navigate)
  const selfID = useStore((s) => s.info?.member.id)
  if (!condition) return null

  const action = condition.action(run)
  const openTerminal = (): FrameAction | null =>
    !agent.hasAgentTerminal || view === 'terminal'
      ? null
      : { label: action.label, act: () => { nav.go('terminal'); if (!agent.localControl && !agent.controlUnavailable) agent.session.takeControl() } }

  if (action.kind === 'reply') return { label: action.label, act: nav.focusComposer }
  switch (condition.target) {
    case 'changes':
      return view === 'changes' ? null : { label: action.label, act: () => nav.go('changes') }
    case 'swarm':
      return run.mission_id ? { label: action.label, act: () => navigate('missions', { missionId: run.mission_id! }) } : null
    case 'notes': {
      const question = unansweredQuestions(room ?? []).find((m) => m.actor_id !== run.member_id)
      return { label: action.label, act: () => nav.reveal(question ? requestCardID.question(question.id) : 'details-needs-you') }
    }
    case 'request': {
      if (action.label === terminalAction.label) return openTerminal()
      const steer = condition.id === 'queued-message' ? queuedSteers(room ?? []).find((m) => m.actor_id !== selfID) : undefined
      const input = run.pending_inputs?.[0]
      if (!steer && input && run.acp) return { label: action.label, act: () => nav.focusRequest() }
      const card = steer ? requestCardID.steer(steer.id) : input ? requestCardID.input(input.id) : 'details-needs-you'
      return { label: action.label, act: () => nav.reveal(card) }
    }
    case 'terminal':
      return openTerminal()
    case 'run':
      return null
  }
}

function StateLine({ run, agentName }: { run: RunRecord; agentName: string }) {
  const presented = useRunPresentation(run)
  const asks = useNeedsYouRuns([run.id]).has(run.id)
  const presentation = run.switching
    ? { state: 'working' as const, reason: `Switching to ${modeLabel[run.switching] ?? run.switching}…` }
    : presented
  const { state } = presentation
  const subagents = useStore((s) =>
    state === 'waiting-agents'
      ? Object.values(s.acpSessions[run.id]?.agents.agents ?? {}).filter((agent) => agentState(agent) === 'running').length
      : 0,
  )
  const since = state === 'ready' && run.review_changed_at ? run.review_changed_at : run.stateChangedAt
  const words = stateWords(presentation, since, useClock())
  // The Workflow chip under the header shows the state's own label.
  const detail = [
    subagents > 0 && `${subagents} ${subagents === 1 ? 'subagent' : 'subagents'} running`,
    words.label !== stateLabel[state] && words.label,
    words.detail,
  ]
    .filter(Boolean)
    .join(' · ')
  const owner = useStore((s) => resolveMember(s, run.member_id).member)
  const ownerName = useStore((s) => memberName(s, run.member_id))
  const accountID = run.requested_account_member_id ? run.account_member_id ?? '' : ''
  const accountOwner = useStore((s) => memberName(s, accountID))
  const mode = modeLabel[run.mode] ?? run.mode
  const watch = useStore((s) => runWatch(s, run.id))
  const navigate = useStore((s) => s.navigate)
  return (
    <div className="@container/state flex w-full min-w-0 items-center gap-1.5 overflow-hidden whitespace-nowrap text-ui-sm text-muted">
      <span role="status" className="sr-only">{stateLabel[state]}</span>
      {run.branch && (
        <>
          <Button
            variant="quiet"
            size="sm"
            hint="Copy branch name"
            aria-label={`Copy branch ${run.branch}`}
            className="max-w-full"
            onClick={(event) => void copyText(run.branch, event.currentTarget)}
          >
            <GitBranch aria-hidden />
            <span className="min-w-0 truncate font-code">{run.branch}</span>
            <Copy aria-hidden />
          </Button>
          <span aria-hidden>·</span>
        </>
      )}
      {extraRepoCount(run) > 0 && (
        <>
          <span className="min-w-0 truncate" title={run.repos!.map((repo) => repo.name).join(', ')}>{repoLabel(run.repos![0]!.name, run)}</span>
          <span aria-hidden>·</span>
        </>
      )}
      <span className="flex min-w-0 items-center gap-1.5" title={[agentName, mode, run.model].filter(Boolean).join(' · ')}>
        <AgentGlyph agent={run.harness} />
        <span className="sr-only">{agentName} · </span>
        <span className="min-w-0 truncate">{mode}</span>
      </span>
      {accountID && (
        <>
          <span aria-hidden>·</span>
          <span className="min-w-0 truncate">On {accountOwner}&apos;s account (fallback)</span>
        </>
      )}
      {run.via && (
        <>
          <span aria-hidden>·</span>
          <span className="min-w-0 truncate">Launched via {run.via}</span>
        </>
      )}
      {watch && (
        <>
          <span aria-hidden>·</span>
          <Button
            variant="quiet"
            size="sm"
            hint={watchHint(watch)}
            className="shrink-0"
            onClick={() => {
              const route = watchRoute(watch.id)
              navigate(route.name, route.params)
            }}
          >
            <Eye aria-hidden />
            <span className="sr-only @lg/state:not-sr-only">{watch.state === 'paused' ? 'Watch paused' : watch.state === 'error' ? 'Watch failing' : 'Watch on'}</span>
          </Button>
        </>
      )}
      <span className="flex min-w-0 flex-1 basis-0 items-center gap-1.5 self-stretch overflow-hidden" title={stateTitle(presentation, since)}>
        {detail && (
          <>
            <span aria-hidden>·</span>
            <span className={asks && !run.switching ? 'min-w-0 truncate text-text' : 'min-w-0 truncate'}>{detail}</span>
          </>
        )}
      </span>
      <span className="hidden shrink-0 @lg/state:flex">
        <Avatar name={ownerName} color={owner?.color} />
      </span>
    </div>
  )
}

/** The switch scrolls sideways when the row is too narrow for every view, a
 * phone with a pull request beside it for instance. It keeps the shown view's
 * tab in sight as the view or the room for it changes, and fades its right
 * edge while more tabs lie past it. */
export function ViewSwitch({ view, views }: { view: RunView; views: RunView[] }) {
  const strip = useRef<HTMLDivElement>(null)
  const [more, setMore] = useState(false)
  const measure = () => {
    const list = strip.current
    if (list) setMore(list.scrollLeft + list.clientWidth < list.scrollWidth - 1)
  }
  useEffect(() => {
    const list = strip.current
    if (!list) return
    const observer = new ResizeObserver(() => {
      list.querySelector('[data-state=active]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
      measure()
    })
    observer.observe(list)
    return () => observer.disconnect()
  }, [view])
  return (
    <TabsList
      ref={strip}
      look="segmented"
      aria-label="Run views"
      onScroll={measure}
      className={cn('overflow-x-auto [scrollbar-width:none]', more && '[mask-image:linear-gradient(to_right,black_calc(100%-1.5rem),transparent)]')}
    >
      {views.map((item) => (
        <TabsTrigger key={item} value={item} className="scroll-mx-6">{runViewLabel[item]}</TabsTrigger>
      ))}
    </TabsList>
  )
}

export function RunHeader({
  run,
  view,
  views,
  agent,
  nav,
  composing,
  detailsOpen,
  onDetails,
  onCaptures,
  onEvents,
  agentName,
  agentEntry,
  workflow,
}: {
  run: RunRecord
  agentName: string
  agentEntry: AgentInfo | undefined
  /** The Workflow chip, which sits beside the view switch. */
  workflow?: React.ReactNode
  view: RunView
  views: RunView[]
  agent: AgentTerminal
  nav: RunNavigation
  composing: boolean
  detailsOpen: boolean
  onDetails: (returnTo: HTMLElement) => void
  onCaptures: (returnTo: HTMLElement | null) => void
  onEvents: (returnTo: HTMLElement | null) => void
}) {
  const mobile = useIsMobile()
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const primary = usePrimaryAction(run, view, agent, nav)
  const modeSwitch = useModeSwitch(run, agent, agentEntry)
  const pullRequest = useRunPullRequest(run, agent, agentName)
  const editor = useOpenInEditor(run)
  const childRunsItems = useChildRunsItems(run)
  useHeaderPrimary(Boolean(primary))

  return (
    <>
      <PaneHeader
        size={composing ? 'view' : 'run'}
        title={
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="truncate">{runLabel(run)}</span>
            {run.protected && <Lock role="img" aria-label="Protected: only the owner or an admin can message, control or stop this run" className="size-3.5 shrink-0 text-muted" />}
          </span>
        }
        stateLine={!composing && <StateLine run={run} agentName={agentName} />}
        onOpenSidebar={!mobile && collapsed ? toggleSidebar : undefined}
        actionsLabel="Run actions"
        actions={
          <>
            {primary && <Button size="sm" onClick={primary.act}>{primary.label}</Button>}
            <Button
              variant="ghost"
              size="icon"
              label={detailsOpen ? 'Hide details' : 'Show details'}
              hint={mobile ? 'Details' : `Details (${shortcutLabel('run-details')})`}
              aria-expanded={detailsOpen}
              aria-controls="run-details"
              onClick={(event) => onDetails(event.currentTarget)}
            >
              <PanelRight />
            </Button>
            <RunActions
              run={run}
              compact={mobile}
              extra={[
                ...(modeSwitch.item ? [modeSwitch.item] : []),
                ...(editor.item ? [editor.item] : []),
                ...childRunsItems,
                { id: 'captures', label: 'Captures…', Icon: Camera, onSelect: onCaptures },
                { id: 'events', label: 'Raw events…', Icon: ScrollText, onSelect: onEvents },
              ]}
            />
          </>
        }
      />
      <RunLineage run={run} />
      <RunRetention run={run} className="border-b border-seam px-4 py-2" />
      <RunChildNudge run={run} />
      {!composing && (
        <div className="@container/views flex h-8 shrink-0 items-center border-b border-seam px-2 coarse:h-11">
          <ViewSwitch view={view} views={views} />
          {workflow}
          {pullRequest.status}
        </div>
      )}
      {modeSwitch.dialog}
      {pullRequest.panel}
      {editor.dialog}
      {!mobile && <ConnectionLine className="border-b border-seam px-4 py-1" />}
    </>
  )
}
