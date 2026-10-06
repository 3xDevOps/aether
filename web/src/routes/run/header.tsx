import { useState } from 'react'
import { Camera, Lock, PanelRight, ScrollText } from '@/components/icons'
import { RunActions } from '@/components/run-actions'
import { ConnectionLine } from '@/components/shell/connection'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { PaneHeader } from '@/components/ui/pane-header'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useIsMobile } from '@/lib/breakpoints'
import { copyText } from '@/lib/clipboard'
import type { AgentInfo } from '@/lib/types'
import { useClock } from '@/lib/clock'
import { shortcutLabel } from '@/lib/keybindings'
import { needsYou, terminalAction } from '@/lib/needs-you'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { approveRequest } from '@/routes/board/card-action'
import { modeLabel } from '@/routes/run/agent-name'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useModeSwitch } from '@/routes/run/mode-switch'
import { requestCardID } from '@/routes/run/requests'
import { runViewLabel, type RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import { useHeaderPrimary, useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { stateContextOf } from '@/store/selectors'

export const stateTone: Record<PresentationState, Tone> = {
  'needs-you': 'needs-you',
  working: 'working',
  paused: 'paused',
  done: 'done',
  failed: 'failed',
}

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
  const approval = useStore((s) => s.approvalsByRun[run.id]?.[0])
  const room = useStore((s) => s.roomMessages[run.id])
  const navigate = useStore((s) => s.navigate)
  const selfID = useStore((s) => s.info?.member.id)
  const [busy, setBusy] = useState(false)
  if (!condition) return null

  const action = condition.action(run, approval)
  const openTerminal = (): FrameAction | null =>
    !agent.hasAgentTerminal || view === 'terminal'
      ? null
      : { label: action.label, act: () => { nav.go('terminal'); if (!agent.localControl && !agent.controlUnavailable) agent.session.takeControl() } }

  if (action.kind === 'approve' && approval) {
    return {
      label: action.label,
      act: () => {
        if (busy) return
        setBusy(true)
        void approveRequest(run.id, approval).finally(() => setBusy(false))
      },
    }
  }
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
  const { state, reason } = run.switching
    ? { state: 'working' as const, reason: `Switching to ${modeLabel[run.switching] ?? run.switching}…` }
    : presented
  const owner = useStore((s) => s.members[run.member_id])
  return (
    <div className="@container/state w-full min-w-0">
      <div className="flex min-w-0 items-center gap-1.5 overflow-hidden text-ui-sm text-muted">
        <StatusDot tone={stateTone[state]} pulse={state === 'working'} label={stateLabel[state]} />
        <span className={state === 'needs-you' ? 'min-w-0 truncate text-text' : 'min-w-0 truncate'}>{reason}</span>
        <span className="flex shrink-0 items-center gap-1.5">
          <span aria-hidden>·</span>
          <AgentGlyph agent={run.harness} />
          <span className="sr-only @3xs/state:not-sr-only">
            <span className="sr-only @xs/state:not-sr-only">{agentName} · </span>
            {modeLabel[run.mode] ?? run.mode}
          </span>
        </span>
        <span className="hidden min-w-0 shrink items-center gap-1.5 @2xl/state:flex">
          <span aria-hidden>·</span>
          <Button
            variant="link"
            size="sm"
            hint="Copy branch name"
            aria-label={`Branch ${run.branch}, copy`}
            className="min-w-0 shrink"
            onClick={(event) => void copyText(run.branch, event.currentTarget)}
          >
            <span className="max-w-56 truncate font-code text-muted">{run.branch}</span>
          </Button>
        </span>
        <span className="hidden shrink-0 @lg/state:flex">
          <Avatar name={owner?.display_name ?? run.member_id} color={owner?.color} />
        </span>
      </div>
    </div>
  )
}

export function ViewSwitch({ views }: { views: RunView[] }) {
  return (
    <TabsList look="segmented" aria-label="Run views">
      {views.map((view) => (
        <TabsTrigger key={view} value={view}>{runViewLabel[view]}</TabsTrigger>
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
}: {
  run: RunRecord
  agentName: string
  agentEntry: AgentInfo | undefined
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
                { id: 'captures', label: 'Captures…', Icon: Camera, onSelect: onCaptures },
                { id: 'events', label: 'Raw events…', Icon: ScrollText, onSelect: onEvents },
              ]}
            />
          </>
        }
      />
      {!composing && (
        <div className="flex h-8 shrink-0 items-center border-b border-seam px-2 coarse:h-11">
          <ViewSwitch views={views} />
        </div>
      )}
      {modeSwitch.dialog}
      {!mobile && <ConnectionLine className="border-b border-seam px-4 py-1" />}
    </>
  )
}
