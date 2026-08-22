// The embedded workspace-shell terminal. One mount is one shell session:
// registration happens on a clean exit only, so the pane's whole job beyond
// rendering the terminal is making "exit cleanly" unmistakable and offering
// resume/reset when a previous shell was abandoned.

import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { connectShell, type ShellSession } from '@/routes/shell/client'
import { useStore } from '@/store'
import type { WorkspaceShellReq } from '@/store/shell'

const modeLabel: Record<WorkspaceShellReq['mode'], string> = {
  'bootstrap-tools': 'Bootstrap tools',
  'harness-login': 'Harness login',
  'agent-setup': 'Agent setup',
}

/** How the mounted shell session stands. */
type Outcome =
  | { kind: 'running' }
  | { kind: 'clean' }
  | { kind: 'dirty'; reason: string }
  | { kind: 'refused'; message: string }

export function ShellPane({
  req,
  onExit,
}: {
  req: WorkspaceShellReq
  onExit: (clean: boolean) => void
}) {
  const openShell = useStore((s) => s.openShell)
  const [outcome, setOutcome] = useState<Outcome>({ kind: 'running' })
  const hostRef = useRef<HTMLDivElement>(null)
  const shellRef = useRef<ShellSession | null>(null)
  // The exit callback must not restart the effect: the session is the
  // effect's own, and a re-run would open a second shell.
  const onExitRef = useRef(onExit)
  onExitRef.current = onExit

  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    const terminal = new Terminal({
      fontSize: 12,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      scrollback: 10_000,
      cursorBlink: true,
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.open(host)
    // WebGL per instance: a machine without a GL context loses acceleration
    // for this pane only, and keeps the DOM renderer everywhere else.
    try {
      const webgl = new WebglAddon()
      webgl.onContextLoss(() => webgl.dispose())
      terminal.loadAddon(webgl)
    } catch {
      // The DOM renderer stays loaded; nothing else to do.
    }

    // The renderers paint into a canvas, so they need resolved colours rather
    // than the CSS variables; reading them off the host keeps index.css the
    // single source (same dance as routes/terminal/index.tsx).
    const paint = () => {
      const style = getComputedStyle(host)
      if (!style.backgroundColor || !style.color) return
      try {
        terminal.options.theme = {
          background: style.backgroundColor,
          foreground: style.color,
          cursor: style.color,
        }
      } catch {
        // A colour xterm cannot parse is not worth losing the shell over.
      }
    }
    paint()
    const themeWatch = new MutationObserver(paint)
    themeWatch.observe(document.documentElement, { attributeFilter: ['class'] })

    const resize = () => {
      fit.fit()
      shellRef.current?.resize(terminal.cols, terminal.rows)
    }
    resize()

    setOutcome({ kind: 'running' })
    const session = connectShell(req, {
      onData: (chunk) => terminal.write(chunk),
      onAttached: () => {},
      onRefused: (message) => {
        setOutcome({ kind: 'refused', message })
        onExitRef.current(false)
      },
      onExit: (clean, reason) => {
        setOutcome(
          clean
            ? { kind: 'clean' }
            : { kind: 'dirty', reason: reason ?? 'shell exited without registering' },
        )
        onExitRef.current(clean)
      },
      geometry: () => ({ cols: terminal.cols, rows: terminal.rows }),
    })
    shellRef.current = session

    const input = terminal.onData((data) => session.send(data))
    const observer = new ResizeObserver(resize)
    observer.observe(host)

    return () => {
      observer.disconnect()
      themeWatch.disconnect()
      input.dispose()
      session.close()
      shellRef.current = null
      terminal.dispose()
    }
  }, [req])

  const workspace = req.workspace.name ?? req.workspace.id ?? 'workspace'
  const running = outcome.kind === 'running'

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-3 border-b px-4 py-1.5 text-xs">
        <span className="font-medium">{modeLabel[req.mode]}</span>
        <span className="text-muted-foreground">{workspace}</span>
        {req.harness && <span className="text-muted-foreground">{req.harness}</span>}
        <span className="min-w-0 flex-1 truncate text-muted-foreground">
          Exit the shell cleanly (type exit) to save your work - closing this
          window abandons the session
        </span>
        <Button
          size="sm"
          variant="default"
          disabled={!running}
          onClick={() => shellRef.current?.send('exit\r')}
        >
          Done
        </Button>
      </div>
      {outcome.kind === 'clean' && (
        <div className="border-b bg-accent px-4 py-2 text-sm">
          Registered and snapshotted.
        </div>
      )}
      {outcome.kind === 'refused' && (
        <div className="border-b px-4 py-2 text-sm text-state-failed">
          {outcome.message}
        </div>
      )}
      {outcome.kind === 'dirty' && (
        <div className="flex items-center gap-3 border-b px-4 py-2 text-sm">
          <span className="min-w-0 flex-1 truncate text-state-failed">
            {outcome.reason}
          </span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => openShell({ ...req, resume: true, reset: false })}
          >
            Resume
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => openShell({ ...req, resume: false, reset: true })}
          >
            Reset
          </Button>
        </div>
      )}
      <div
        ref={hostRef}
        className={cn(
          'min-h-0 flex-1 bg-background p-2 text-foreground',
          !running && 'opacity-60',
        )}
      />
    </div>
  )
}
