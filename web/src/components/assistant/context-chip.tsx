import { useId, useState } from 'react'
import { ChevronRight } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { isRunView, runViewLabel } from '@/routes/run/views'
import { useStore } from '@/store'
import { buildViewContext } from '@/store/view-context'

function summary(route: { name: string; params: Record<string, string> }): string {
  const { runId, view } = route.params
  if (route.name === 'run' && runId) return [`run ${runId}`, isRunView(view) ? runViewLabel[view] : undefined].filter(Boolean).join(' · ')
  return route.name
}

/** Shows what the next prompt tells the assistant about this page, and turns it off. */
export function ContextChip() {
  const share = useStore((s) => s.assistantShareView)
  const route = useStore((s) => s.route)
  useStore((s) => s.activeFileKey)
  const [open, setOpen] = useState(false)
  const [error, setError] = useState<string>()
  const panelID = useId()
  const hintID = useId()
  const preview = open && share ? JSON.stringify(buildViewContext(useStore.getState()), null, 2) : ''

  const setShare = async (on: boolean) => {
    useStore.getState().setAssistantShareView(on)
    setError(undefined)
    if (on) return
    try {
      await api.assistantView(null)
    } catch (err) {
      setError(message(err))
    }
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
        <span className="truncate">{share ? `Sharing: ${summary(route)}` : 'Not sharing this page'}</span>
      </Button>
      <span id={hintID} className="sr-only">What the assistant is told about the page you are on with each message</span>
      {error && <p role="alert" className="text-ui-sm break-words text-state-failed">{error}</p>}
      {open && (
        <div id={panelID} className="flex flex-col gap-2 rounded-control border border-seam p-2 text-ui-sm">
          <label className="flex items-center gap-2">
            <Checkbox checked={share} onCheckedChange={(checked) => void setShare(checked === true)} />
            Share the page I am on with the assistant
          </label>
          {share ? (
            <pre aria-label="Sent with each message" className="max-h-48 overflow-auto font-mono text-ui-sm whitespace-pre-wrap break-all text-muted">{preview}</pre>
          ) : (
            <p className="text-muted">Nothing is sent, and the server forgets what it had.</p>
          )}
        </div>
      )}
    </div>
  )
}
