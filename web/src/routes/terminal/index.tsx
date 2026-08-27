import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useEffect, useRef } from 'react'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { terminalFontFamily, whenTerminalFontReady } from '@/lib/term-font'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { type Attachment, connectAttach } from '@/routes/terminal/attach'
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
  const hostRef = useRef<HTMLDivElement>(null)
  const attachRef = useRef<Attachment | null>(null)
  // Read at connect time by the attachment, so a toggle takes effect on the
  // reattach without re-running the terminal's own effect.
  const writeRef = useRef(state.write)
  writeRef.current = state.write

  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    // The DOM renderer, deliberately: @xterm/addon-webgl 0.19.0 reuses stale
    // glyph-atlas positions under heavy glyph churn, garbling scrolled rows
    // until a forced refresh (xtermjs/xterm.js#6038; the fix is unreleased).
    // The DOM renderer never desyncs and keeps up with agent TUI streams.
    const terminal = new Terminal({
      fontSize: 12,
      fontFamily: terminalFontFamily,
      scrollback: 10_000,
      cursorBlink: false,
    })

    // xterm measures and caches glyph metrics synchronously at open, so the
    // shipped symbols font must be usable first - opening earlier would bake
    // fallback-font metrics in for every agent-TUI symbol (term-font.ts).
    let teardown: (() => void) | null = null
    const cancelFontWait = whenTerminalFontReady(() => {
      const fit = new FitAddon()
      terminal.loadAddon(fit)
      terminal.open(host)

      // The renderer needs resolved colours rather than the CSS variables.
      // Reading them off the host keeps index.css the single source, and the
      // dark class landing on <html> is the signal to re-read them.
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
          // A colour xterm cannot parse is not worth losing the terminal over.
        }
      }
      paint()
      const themeWatch = new MutationObserver(paint)
      themeWatch.observe(document.documentElement, { attributeFilter: ['class'] })

      const resize = () => {
        fit.fit()
        attachRef.current?.resize(terminal.cols, terminal.rows)
      }
      resize()

      // A new attach answers for itself: the last one's refusal and steer denial
      // must not decide what this one shows, or a run whose steer was granted in
      // between stays greyed out until a reload.
      setTerminal(runID, initialTerminal)
      writeRef.current = initialTerminal.write

      const attachment = connectAttach(runID, {
        onData: (chunk) => terminal.write(chunk),
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

      const input = terminal.onData((data) => attachment.send(data))
      const observer = new ResizeObserver(resize)
      observer.observe(host)

      teardown = () => {
        observer.disconnect()
        themeWatch.disconnect()
        input.dispose()
        attachment.close()
        attachRef.current = null
      }
    })

    return () => {
      cancelFontWait()
      teardown?.()
      terminal.dispose()
    }
    // `known` is in the deps because the host element only exists once the run
    // is in the store; a run that arrives late still gets its terminal.
  }, [runID, setTerminal, known])

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
      <ViewHeader title={runLabel(run)} subtitle={run.branch} />
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
      <div ref={hostRef} className="min-h-0 flex-1 bg-background p-2 text-foreground" />
    </div>
  )
}

registerRoute('terminal', TerminalView)
