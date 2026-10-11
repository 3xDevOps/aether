import { useEffect, useRef, useState } from 'react'
import { X } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { api, ApiError } from '@/lib/api'
import { message } from '@/lib/format'
import type { DevBrowserPage } from '@/lib/types'
import { codeConflict } from '@/routes/terminal/attach'
import { logKey, selectLogs, useFeedback, type LogEntry } from './feedback'

const pollInterval = 2000
/** The companion keeps 100 rows of each kind per page; this is both. */
const kept = 200

export interface BrowserLogs {
  entries: LogEntry[]
  /** Why the last read failed. */
  error: string
}

/**
 * The console errors and warnings and the failed requests the server has
 * recorded for `page`, oldest first, read while the Browser is shown.
 */
export function useBrowserLogs(runID: string, page: DevBrowserPage | null): BrowserLogs {
  const [logs, setLogs] = useState<BrowserLogs>({ entries: [], error: '' })
  const shown = useRef(page)
  shown.current = page
  const tab = page ? `${page.session_id}:${page.page_id}` : ''

  useEffect(() => {
    setLogs({ entries: [], error: '' })
    if (!tab) return
    let disposed = false
    let reading = false
    const after = { console: 0, network: 0 }
    const read = async () => {
      const target = shown.current
      if (reading || !target || document.hidden) return
      reading = true
      const at = { run_id: runID, session_id: target.session_id, page_id: target.page_id, page_revision: target.page_revision }
      try {
        const [consoles, requests] = await Promise.all([
          api.devBrowserConsole({ ...at, after: after.console }),
          api.devBrowserNetwork({ ...at, after: after.network }),
        ])
        if (disposed) return
        after.console = consoles.next
        after.network = requests.next
        const fresh: LogEntry[] = [
          ...consoles.entries.map((entry) => ({ ...entry, kind: 'console' as const, page: target.url })),
          ...requests.entries.map((entry) => ({ ...entry, kind: 'network' as const, page: target.url })),
        ]
        setLogs((previous) => (fresh.length === 0 && !previous.error ? previous : {
          entries: [...previous.entries, ...fresh].sort((a, b) => a.sequence - b.sequence).slice(-kept),
          error: '',
        }))
      } catch (cause) {
        // A revision that moved on is read again once the page record catches up.
        if (!disposed && !(cause instanceof ApiError && cause.code === codeConflict)) setLogs((previous) => ({ ...previous, error: message(cause) }))
      } finally { reading = false }
    }
    void read()
    const timer = setInterval(() => void read(), pollInterval)
    return () => {
      disposed = true
      clearInterval(timer)
    }
  }, [runID, tab])

  return logs
}

/** The strip under the page that stands in for a browser's developer tools. */
export function LogStrip({ runID, logs, onClose }: { runID: string; logs: BrowserLogs; onClose: () => void }) {
  const selected = new Set(useFeedback(runID).logs.map(logKey))
  const { entries, error } = logs
  const chosen = entries.filter((entry) => selected.has(logKey(entry))).length
  return (
    <section aria-label="Console and network" className="flex max-h-[40%] min-h-24 shrink-0 flex-col border-t border-seam bg-canvas text-ui-sm">
      <header className="flex min-h-8 shrink-0 items-center gap-2 border-b border-seam bg-chrome px-3">
        <Checkbox
          aria-label="Select every row"
          disabled={entries.length === 0}
          checked={chosen > 0 && chosen === entries.length ? true : chosen > 0 ? 'indeterminate' : false}
          onCheckedChange={(next) => selectLogs(runID, entries, next === true)}
        />
        <h3 className="font-medium text-text">Console and network</h3>
        <p className="min-w-0 flex-1 truncate text-muted">Errors, warnings and failed requests. Tick rows to send them to the agent.</p>
        <Button variant="ghost" size="icon-sm" label="Close" onClick={onClose}><X /></Button>
      </header>
      {error && <p role="alert" className="shrink-0 break-words px-3 py-1 text-state-failed">{error}</p>}
      {entries.length === 0 ? (
        <p className="px-3 py-2 text-muted">Nothing recorded for this page. Only errors, warnings and failed requests are: not console.log, and not requests that succeeded.</p>
      ) : (
        <ul className="min-h-0 flex-1 overflow-y-auto py-0.5">
          {entries.map((entry) => {
            const key = logKey(entry)
            const failed = entry.kind === 'network' || entry.level === 'error'
            return (
              <li key={key}>
                <label className="flex min-w-0 items-start gap-2 px-3 py-0.5">
                  <Checkbox className="mt-0.5" checked={selected.has(key)} onCheckedChange={(next) => selectLogs(runID, [entry], next === true)} />
                  <time className="shrink-0 text-muted tabular-nums">{new Date(entry.time).toLocaleTimeString([], { hour12: false })}</time>
                  <Badge tone={failed ? 'failed' : 'needs-you'}>{entry.kind === 'console' ? entry.level : entry.status || 'failed'}</Badge>
                  <span className="min-w-0 flex-1 break-words font-code">{entry.kind === 'console' ? entry.text : `${entry.method} ${entry.url}${entry.failure ? `: ${entry.failure}` : ''}`}</span>
                </label>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
