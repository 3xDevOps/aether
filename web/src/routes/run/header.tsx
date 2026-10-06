import { useState } from 'react'
import { toast } from 'sonner'
import { Camera, Lock, PanelRight, ScrollText } from '@/components/icons'
import { RunActions } from '@/components/run-actions'
import { ConnectionLine } from '@/components/shell/connection'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { PaneHeader } from '@/components/ui/pane-header'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { TabsList, TabsTrigger } from '@/components/ui/tabs'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { copyText } from '@/lib/clipboard'
import { useClock } from '@/lib/clock'
import { message } from '@/lib/format'
import { needsYou } from '@/lib/needs-you'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { modeLabel, useAgentName } from '@/routes/run/agent-name'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { requestCardID } from '@/routes/run/requests'
import { runViewLabel, type RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import { useRunPresentation } from '@/store/hooks'
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
}

interface PrimaryAction {
  label: string
  act: () => void
}

function usePrimaryAction(run: RunRecord, view: RunView, agent: AgentTerminal, nav: RunNavigation): PrimaryAction | null {
  const now = useClock()
  const condition = useStore((s) => needsYou(run, stateContextOf(s, now))?.id)
  const approval = useStore((s) => s.approvalsByRun[run.id]?.[0])
  const room = useStore((s) => s.roomMessages[run.id])
  const navigate = useStore((s) => s.navigate)
  const selfID = useStore((s) => s.info?.member.id)
  const [busy, setBusy] = useState(false)

  const openTerminal: PrimaryAction | null = !agent.hasAgentTerminal
    ? null
    : view === 'terminal'
      ? null
      : { label: 'Open terminal', act: () => { nav.go('terminal'); if (!agent.localControl && !agent.controlUnavailable) agent.session.takeControl() } }

  switch (condition) {
    case 'permission':
      if (approval) {
        return {
          label: 'Approve',
          act: () => {
            if (busy) return
            setBusy(true)
            api.approvalDecide(run.id, approval.id, true).then(
              (decided) => useStore.getState().decideApproval(decided.workspace_id, decided.id, decided.decision, decided.decided_by ?? '', decided.decided_at ?? ''),
              (err) => toast.error(`Approve failed: ${message(err)}`),
            ).finally(() => setBusy(false))
          },
        }
      }
      return run.mode === 'acp' ? { label: 'Review', act: () => nav.reveal(requestCardID.input(run.pending_inputs?.[0]?.id ?? '')) } : openTerminal
    case 'question':
      return run.mode === 'acp' ? { label: 'Reply', act: nav.focusComposer } : openTerminal
    case 'queued-message': {
      const queued = queuedSteers(room ?? []).find((m) => m.actor_id !== selfID)
      return { label: 'Approve', act: () => nav.reveal(queued ? requestCardID.steer(queued.id) : 'details-needs-you') }
    }
    case 'room-question': {
      const question = unansweredQuestions(room ?? []).find((m) => m.actor_id !== run.member_id)
      return { label: 'Reply', act: () => nav.reveal(question ? requestCardID.question(question.id) : 'details-needs-you') }
    }
    case 'unreviewed-finish':
      return view === 'changes' ? null : { label: 'Review changes', act: () => nav.go('changes') }
    case 'swarm-question':
    case 'integrator-down':
      return run.mission_id ? { label: 'Open swarm', act: () => navigate('missions', { missionId: run.mission_id! }) } : null
    case undefined:
      return null
    default:
      return openTerminal
  }
}

function StateLine({ run }: { run: RunRecord }) {
  const { state, reason } = useRunPresentation(run)
  const owner = useStore((s) => s.members[run.member_id])
  const agentName = useAgentName(run.harness)
  const meta = [agentName, modeLabel[run.mode] ?? run.mode].join(' · ')
  return (
    <div className="@container/state w-full min-w-0">
      <div className="flex min-w-0 items-center gap-1.5 overflow-hidden text-ui-sm text-muted">
        <StatusDot tone={stateTone[state]} pulse={state === 'working'} label={stateLabel[state]} />
        <span className={state === 'needs-you' ? 'min-w-0 truncate text-text' : 'min-w-0 truncate'}>{reason}</span>
        <span className="hidden shrink-0 items-center gap-1.5 @sm/state:flex">
          <span aria-hidden>·</span>
          <AgentGlyph agent={run.harness} />
          {meta}
        </span>
        <span className="hidden min-w-0 shrink items-center gap-1.5 @lg/state:flex">
          <span aria-hidden>·</span>
          <Button
            variant="link"
            size="sm"
            hint="Copy branch name"
            aria-label={`Branch ${run.branch}, copy`}
            onClick={(event) => void copyText(run.branch, event.currentTarget)}
          >
            <span className="max-w-56 truncate font-code text-muted">{run.branch}</span>
          </Button>
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
  narrow,
  detailsOpen,
  onDetails,
  onCaptures,
  onEvents,
}: {
  run: RunRecord
  view: RunView
  views: RunView[]
  agent: AgentTerminal
  nav: RunNavigation
  composing: boolean
  narrow: boolean
  detailsOpen: boolean
  onDetails: (returnTo: HTMLElement) => void
  onCaptures: (returnTo: HTMLElement | null) => void
  onEvents: (returnTo: HTMLElement | null) => void
}) {
  const mobile = useIsMobile()
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const primary = usePrimaryAction(run, view, agent, nav)

  return (
    <>
      <PaneHeader
        size={composing ? 'view' : 'run'}
        title={
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="truncate">{runLabel(run)}</span>
            {run.protected && <Lock role="img" aria-label="Protected: only the owner or an admin can steer or stop this run" className="size-3.5 shrink-0 text-muted" />}
          </span>
        }
        stateLine={!composing && <StateLine run={run} />}
        onOpenSidebar={!mobile && collapsed ? toggleSidebar : undefined}
        viewSwitch={!narrow && <ViewSwitch views={views} />}
        actionsLabel="Run actions"
        actions={
          <>
            {primary && <Button size="sm" onClick={primary.act}>{primary.label}</Button>}
            <Button
              variant="ghost"
              size="icon"
              label={detailsOpen ? 'Hide details' : 'Show details'}
              hint={mobile ? 'Details' : 'Details (Ctrl+.)'}
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
                { id: 'captures', label: 'Captures…', Icon: Camera, onSelect: onCaptures },
                { id: 'events', label: 'Raw events…', Icon: ScrollText, onSelect: onEvents },
              ]}
            />
          </>
        }
      />
      {narrow && !composing && (
        <div className="flex shrink-0 items-center border-b border-seam px-2 py-1">
          <ViewSwitch views={views} />
        </div>
      )}
      {!mobile && <ConnectionLine className="border-b border-seam px-4 py-1" />}
    </>
  )
}
