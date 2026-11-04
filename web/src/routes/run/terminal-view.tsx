import { useEffect, useRef } from 'react'
import { connectionLabel } from '@/components/shell/connection'
import { TerminalPane, TerminalSpinner } from '@/components/terminal-pane'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { isRetainedRun } from '@/lib/commands'
import { message } from '@/lib/format'
import { endedStatuses, type AgentTerminal } from '@/routes/run/agent-terminal'
import { usePresenceSummary } from '@/routes/run/multiplayer-controls'
import { ShellTerminal } from '@/routes/run/shell-terminal'
import { StopShellDialog, terminalPanelID, TerminalTabs, type RunShells } from '@/routes/run/shells'
import { TerminalHistory } from '@/routes/terminal/history'
import { ControlButton } from '@/routes/terminal/control-button'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'
import { shellEnded } from '@/store/terminal'

function Presence({ run, agent }: { run: RunRecord; agent: AgentTerminal }) {
  const summary = usePresenceSummary(run, agent)
  const { state } = agent.session
  const ended = endedStatuses.includes(run.status)
  const showConnection = !agent.starting && !ended && state.connection !== 'live'
  return (
    <div role="group" aria-label="Session presence" className="control-matrix-clear flex min-w-0 items-center gap-2">
      {ended && <span className="shrink-0 text-ui-sm text-muted">{isRetainedRun(run) ? 'Container stopped' : 'Container removed'} · read-only</span>}
      {showConnection && <span className="shrink-0 text-ui-sm text-state-needs-you">{connectionLabel[state.connection]}</span>}
      {agent.steerable && <span className="min-w-0 truncate text-ui-sm text-muted">{summary}</span>}
      {agent.steerable && (
        <ControlButton
          ownsControl={agent.localControl}
          unavailable={agent.controlUnavailable}
          onTakeControl={agent.session.takeControl}
          onReleaseControl={agent.session.releaseControl}
          takeover={agent.takeover.interaction}
        />
      )}
    </div>
  )
}

function Notice({ run, agent }: { run: RunRecord; agent: AgentTerminal }) {
  const roomStatusError = useStore((s) => s.roomStatusError[run.id])
  const { state, sessionMissing, retry } = agent.session
  const ended = endedStatuses.includes(run.status)
  const lines = [
    state.steerDenied && 'You can watch this session but not type in it.',
    state.message && (state.refused && sessionMissing && ended
      ? 'This session has ended and left no recorded terminal to replay.'
      : state.message),
    roomStatusError && `Presence unavailable: ${roomStatusError}`,
  ].filter(Boolean)
  const canRetry = state.refused && !ended
  if (lines.length === 0 && !canRetry) return null
  return (
    <div className="flex shrink-0 items-center gap-2 border-b border-seam px-3 py-1 text-ui-sm text-muted">
      <p role="status" className="flex min-w-0 flex-1 flex-wrap gap-x-2 break-words whitespace-pre-wrap">
        {lines.map((line) => <span key={String(line)}>{line}</span>)}
      </p>
      {canRetry && <Button size="sm" variant="ghost" onClick={retry}>Retry</Button>}
    </div>
  )
}

/**
 * On a phone an Enhanced session has no agent terminal to land on, so the
 * first time its Terminal tab is viewed it shows a shell that is already
 * running. It never starts one: viewing a tab must not start a process or
 * take a lease.
 */
function useRunningShellOnPhone(shells: RunShells, wanted: boolean) {
  const mobile = useIsMobile()
  const asked = useRef(false)
  const { runID, visible, canOpen, show, setError } = shells
  useEffect(() => {
    if (!mobile || !wanted || !visible || !canOpen || asked.current) return
    asked.current = true
    void (async () => {
      // Decided from a list this request read itself: the poll may not have
      // listed the session's shells yet, and may supersede a refresh.
      let terminals
      try {
        terminals = (await api.devTerminalList({ run_id: runID })).terminals
      } catch (cause) {
        setError(message(cause))
        return
      }
      useStore.getState().syncShellTerminals(runID, terminals)
      const running = terminals.find((terminal) => !shellEnded(terminal))
      if (running) show(running.terminal_id)
    })()
  }, [canOpen, mobile, runID, setError, show, visible, wanted])
}

export function TerminalView({ run, agent, shells, onCaptures }: {
  run: RunRecord
  agent: AgentTerminal
  shells: RunShells
  onCaptures: (returnTo: HTMLElement | null) => void
}) {
  const shellShown = shells.dock.shellShown && shells.dock.activeTab !== null && shells.canOpen
  const tabs = <TerminalTabs shells={shells} agent={agent.hasAgentTerminal} />
  const tabbed = shells.canOpen && shells.dock.tabs.length > 0
  const { controller, session, takeover } = agent
  const { state, replaying, controlMetadata } = session
  useRunningShellOnPhone(shells, !agent.hasAgentTerminal && !shellShown)
  const mobile = useIsMobile()

  return (
    <div className="relative flex h-full min-h-0 flex-col">
      {shells.error && (
        <p role="alert" className="shrink-0 border-b border-seam px-3 py-1 text-ui-sm text-state-failed">{shells.error}</p>
      )}
      <div className="relative min-h-0 flex-1">
      {shellShown && (
        <div
          data-slot="shell-terminal"
          id={terminalPanelID.shell}
          role={tabbed ? 'tabpanel' : undefined}
          aria-label={tabbed ? 'Shell' : undefined}
          className="absolute inset-0 flex flex-col"
        >
          <ShellTerminal shells={shells} tabs={tabs} onCaptures={onCaptures} />
        </div>
      )}
      {agent.hasAgentTerminal ? (
        <div
          id={terminalPanelID.agent}
          role={tabbed ? 'tabpanel' : undefined}
          aria-label={tabbed ? 'Agent' : undefined}
          inert={shellShown}
          className={cn('absolute inset-0', shellShown && 'invisible')}
        >
          <TerminalPane
            controller={controller}
            tabs={tabs}
            toolbarEnd={<Presence run={run} agent={agent} />}
            notice={<Notice run={run} agent={agent} />}
            onCaptures={onCaptures}
            writable={state.write && !agent.starting && !replaying}
            controlAppearance={agent.liveWritable ? 'active'
              : state.connection !== 'live' || state.steerDenied || !agent.steerable || replaying || agent.readingHistory
                ? 'hidden' : controlMetadata?.loss ?? 'hidden'}
            takeoverProgress={takeover.holderProgress}
            readingSurface={agent.readingHistory ? agent.historyTools : undefined}
            surface={
              <TerminalHistory
                controller={controller}
                cache={agent.historyCache}
                enabled={!agent.starting && !replaying}
                beforeDispose={agent.beforeDispose}
                tools={agent.historyTools}
                onReadingChange={agent.setReadingHistory}
              />
            }
            imageTarget={run.id}
            imageUploadEnabled={!agent.starting && state.connection === 'live' && state.write && !state.steerDenied}
            replaying={replaying}
          >
            {agent.starting && <TerminalSpinner label="Starting the session's container" />}
          </TerminalPane>
        </div>
      ) : !shellShown && (
        <div className="absolute inset-0 flex flex-col">
          <div role="toolbar" aria-label="Terminal toolbar" className="flex h-8 shrink-0 items-center gap-1 border-b border-seam bg-chrome px-1 coarse:h-11">
            {tabs}
          </div>
          <EmptyState
            title="No agent terminal"
            action={mobile && shells.canOpen && (
              <Button disabled={!shells.canAdd} onClick={() => void shells.open()}>Open shell</Button>
            )}
          >
            An Enhanced session talks to its agent in the Chat view. Open a shell to work in the session’s container.
          </EmptyState>
        </div>
      )}
      </div>
      <StopShellDialog shells={shells} />
    </div>
  )
}
