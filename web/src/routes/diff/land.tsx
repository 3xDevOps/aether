import { useState } from 'react'
import { toast } from 'sonner'
import { Check, GitBranch } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** Shows where a successful pull left the run branch on this machine. */
export function Land({ run }: { run: RunRecord }) {
  const pull = useStore((s) => s.pulls[run.id])
  const cap = useCapability()
  const [switching, setSwitching] = useState(false)

  if (!pull) return null

  const switchBranch = async () => {
    setSwitching(true)
    try {
      await api.localPullSwitch(run.id)
      useStore.getState().recordPull(run.id, { ...pull, current: true })
      toast.success(`Now on ${pull.branch}`)
    } catch (err) {
      toast.error(message(err))
    } finally {
      setSwitching(false)
    }
  }

  return (
    <section
      aria-label="Pulled branch"
      className="flex min-h-8 min-w-0 shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-seam px-3 py-1 text-ui-sm text-text"
    >
      <GitBranch className="size-3.5 shrink-0 text-muted" aria-hidden />
      <span className="min-w-0 flex-1">
        Branch <code className="font-code break-all">{pull.branch}</code> is on your machine
      </span>
      {pull.current ? (
        <span className="inline-flex shrink-0 items-center gap-1 text-muted">
          <Check className="size-3.5 text-state-done" aria-hidden />
          You're on it
        </span>
      ) : (
        cap.hasLocal('pull.switch') && (
          <Button size="sm" variant="secondary" disabled={switching} onClick={() => void switchBranch()}>
            {switching ? 'Switching…' : 'Switch to it'}
          </Button>
        )
      )}
    </section>
  )
}
