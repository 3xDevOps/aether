import { useState } from 'react'
import { toast } from 'sonner'
import { GitBranch, Check } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { useCapability } from '@/store/hooks'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

/** Shows where a successful pull left the run branch on this machine. */
export function Land({ run }: { run: RunRecord }) {
  const pull = useStore((s) => s.pulls[run.id])
  const cap = useCapability()
  const [switching, setSwitching] = useState(false)

  if (!run.last_commit) {
    return <p className="border-b border-dashed px-3 py-2 text-[12px] text-muted-foreground">Nothing committed yet</p>
  }
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
      className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 border-b px-3 py-2 text-[12px]"
    >
      <GitBranch className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-[1_1_16rem]">
        Branch <code className="break-all font-mono text-[12px]">{pull.branch}</code> is on your machine
      </span>
      {pull.current ? (
        <span className="inline-flex shrink-0 items-center gap-1 text-[12px] text-muted-foreground">
          <Check className="size-3.5 text-state-done" aria-hidden />
          You're on it
        </span>
      ) : (
        cap.hasLocal('pull.switch') && (
          <Button size="sm" variant="outline" disabled={switching} onClick={() => void switchBranch()}>
            {switching ? 'Switching...' : 'Switch to it'}
          </Button>
        )
      )}
    </section>
  )
}
