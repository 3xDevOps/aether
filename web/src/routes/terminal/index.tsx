import { useEffect, useRef } from 'react'
import { RunActions } from '@/components/run-actions'
import { useXterm } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { type Attachment, connectAttach } from '@/routes/terminal/attach'
import { RunDock } from '@/routes/terminal/run-dock'
import { RunTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { initialTerminal } from '@/store/terminal'
const connectionLabel: Record<string, string> = {
  connecting: 'Connecting',
  live: 'Attached',
  reconnecting: 'Reconnecting',
  offline: 'Offline',
}

function TerminalView({ params }: RouteProps) {
  const runID = params.runId
  const run = useStore((s) => s.runs[runID])
  const state = useStore((s) => s.terminals[runID] ?? initialTerminal)
  const setTerminal = useStore((s) => s.setTerminal)

  const known = run !== undefined
  const attachRef = useRef<Attachment | null>(null)
  // Read at connect time by the attachment, so a toggle takes effect on the
  // reattach without re-running the terminal's own effect.
  const writeRef = useRef(state.write)
  writeRef.current = state.write
  const { hostRef, terminal } = useXterm({
    enabled: known,
    onData: (data) => attachRef.current?.send(data),
    onResize: (cols, rows) => attachRef.current?.resize(cols, rows),
  })

  useEffect(() => {
    if (!terminal) return

    // A new attach answers for itself: the last one's refusal and steer denial
    // must not decide what this one shows, or a run whose steer was granted in
    // between stays greyed out until a reload.
    setTerminal(runID, initialTerminal)
    writeRef.current = initialTerminal.write

    const attachment = connectAttach(() => api.attachSocket(runID), {
      // Every attach starts with the server's transcript replay, so the pane
      // is never blank - and clearing first keeps a reconnect from stacking a
      // second copy of the scrollback under the first.
      onAttached: () => {
        terminal.reset()
        setTerminal(runID, { message: null, refused: false })
      },
      onState: (connection) => setTerminal(runID, { connection }),
      onRefused: (message) => setTerminal(runID, { message, refused: true }),
      onWriteDenied: () => setTerminal(runID, { steerDenied: true, write: false }),
      geometry: () => ({ cols: terminal.cols, rows: terminal.rows }),
      wantsWrite: () => writeRef.current,
    })
    attachRef.current = attachment

    return () => {
      attachment.close()
      attachRef.current = null
    }
  }, [runID, setTerminal, terminal])

  if (!run) {
    return <p className="p-4 text-sm text-muted-foreground">Unknown run.</p>
  }

  const toggleWrite = () => {
    writeRef.current = !state.write
    setTerminal(runID, { write: writeRef.current })
    attachRef.current?.reopen()
  }

  const retry = () => {
    setTerminal(runID, { message: null, refused: false })
    attachRef.current?.reopen()
  }

  return (
    <div className="flex h-full flex-col">
      <ViewHeader
        title={runLabel(run)}
        subtitle={run.branch}
        actions={<RunActions run={run} />}
      />
      <RunTabs runID={runID} active="terminal" />
      <div className="flex items-center gap-3 border-b px-4 py-1.5 text-xs">
        <span
          className={cn(
            'text-muted-foreground',
            state.connection === 'offline' && 'text-state-failed',
          )}
        >
          {connectionLabel[state.connection]}
        </span>
        <Button
          size="sm"
          variant={state.write ? 'default' : 'outline'}
          disabled={state.steerDenied}
          onClick={toggleWrite}
        >
          {state.write ? 'Steering' : 'Take control'}
        </Button>
        {state.steerDenied && (
          <span className="text-muted-foreground">
            You cannot steer this run.
          </span>
        )}
        {state.message && (
          <span className="truncate text-muted-foreground">{state.message}</span>
        )}
        {state.refused && (
          <Button size="sm" variant="ghost" onClick={retry}>
            Retry
          </Button>
        )}
      </div>
      <div className="min-h-0 flex-1">
        <div ref={hostRef} className="h-full min-h-0 bg-background p-2 text-foreground" />
      </div>
      <RunDock runID={runID} />
    </div>
  )
}

registerRoute('terminal', TerminalView)
