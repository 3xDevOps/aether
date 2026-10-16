import { useId, useState } from 'react'
import { ChevronRight } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import type { ViewContext } from '@/lib/assistant-types'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { isRunView, runViewLabel } from '@/routes/run/views'
import { useStore } from '@/store'
import { assistantShareView, buildViewContext, saveShareView, viewContextKey } from '@/store/view-context'

function summary({ route }: ViewContext): string {
  if (route.name === 'run' && route.run_id) return [`run ${route.run_id}`, isRunView(route.view) ? runViewLabel[route.view] : undefined].filter(Boolean).join(' · ')
  return route.name
}

/** Shows what the next prompt tells the assistant about this page, and turns it off. */
export function ContextChip() {
  const share = useStore(assistantShareView)
  const saving = useStore((s) => s.assistantShareChoice !== null)
  const retained = useStore((s) => (s.assistantAttempt?.text === s.assistantDraft.trim() ? s.assistantAttempt : null))
  useStore((s) => viewContextKey(buildViewContext(s)))
  const [open, setOpen] = useState(false)
  const [error, setError] = useState<string>()
  const panelID = useId()
  const hintID = useId()
  const context = retained ? retained.viewContext : buildViewContext(useStore.getState())

  const setShare = (on: boolean) => {
    setError(undefined)
    saveShareView(useStore, api, on, (err) => setError(message(err)))
  }

  return (
    <div className="flex min-w-0 flex-col gap-1">
      <Button
        variant="ghost"
        size="sm"
        aria-expanded={open}
        aria-controls={panelID}
        aria-describedby={hintID}
        onClick={() => setOpen(!open)}
        className={cn('max-w-full self-start px-1 font-normal', open && '[&>svg]:rotate-90')}
      >
        <ChevronRight className="size-3" />
        <span className="truncate">{share && context ? `Sharing: ${summary(context)}` : 'Not sharing this page'}</span>
      </Button>
      <span id={hintID} className="sr-only">What the assistant is told about the page you are on with each message</span>
      {error && saving && <p role="alert" className="text-ui-sm break-words text-state-failed">{error}; retrying</p>}
      {open && (
        <div id={panelID} className="flex flex-col gap-2 rounded-control border border-seam p-2 text-ui-sm">
          <label className="flex items-center gap-2">
            <Checkbox checked={share} onCheckedChange={(checked) => setShare(checked === true)} />
            Share the page I am on with the assistant
          </label>
          {share && context ? (
            <pre aria-label="Sent with this message" className="max-h-48 overflow-auto font-mono text-ui-sm whitespace-pre-wrap break-all text-muted">
              {JSON.stringify(context, null, 2)}
            </pre>
          ) : (
            <p className="text-muted">Nothing is sent, and the server forgets what it had, on every device.</p>
          )}
        </div>
      )}
    </div>
  )
}
