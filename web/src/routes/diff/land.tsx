import { useState } from 'react'
import { toast } from 'sonner'
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
    return <p className="text-sm text-muted-foreground">Nothing committed yet</p>
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
    <section className="flex flex-wrap items-center gap-2 rounded-md border bg-card px-3 py-2 text-sm">
      <span>
        Branch <code>{pull.branch}</code> is on your machine
      </span>
      {pull.current ? (
        <span className="text-muted-foreground">You're on it</span>
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
