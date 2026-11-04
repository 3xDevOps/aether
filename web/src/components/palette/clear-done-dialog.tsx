import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import { readArchiveHolds } from '@/lib/close-check'
import type { RunCloseCheck } from '@/lib/types'
import type { StateContext } from '@/lib/needs-you'
import { clearDonePlan, releaseFinishedPlan, runClearDone, runReleaseFinished } from '@/lib/commands'
import { ClearDoneConfirm, ReleaseFinishedConfirm } from '@/routes/board/clear-done-dialog'
import { finishedRuns, workspaceRuns } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { stateContextOf } from '@/store/selectors'
import { useCapability, useSelf } from '@/store/hooks'

// Each plan is snapshotted when its confirmation opens, so the count the
// member confirms is the set that is acted on even as runs keep changing.

function scope(): [string, StateContext] {
  const s = useStore.getState()
  return [s.activeWorkspace, stateContextOf(s, Date.now())]
}

export function ClearDoneDialog() {
  const cap = useCapability()
  const self = useSelf()
  const close = useStore((s) => s.closePaletteDialog)
  const removeRun = useStore((s) => s.removeRun)
  const [plan] = useState(() => clearDonePlan(finishedRuns(...scope()), cap, self))
  const [held, setHeld] = useState<Map<string, RunCloseCheck>>()
  const [reads, setReads] = useState(0)
  const [running, setRunning] = useState(false)

  useEffect(() => {
    let live = true
    const completed = plan.eligible.filter((run) => run.status === 'completed')
    if (completed.length === 0) {
      setHeld(new Map())
      return
    }
    setHeld(undefined)
    void readArchiveHolds(completed.map((run) => run.id)).then((next) => live && setHeld(next))
    return () => {
      live = false
    }
  }, [plan, reads])

  const confirm = async () => {
    if (!held) return
    setRunning(true)
    await runClearDone(plan.eligible.filter((run) => !held.has(run.id)), { api, removeRun })
    setRunning(false)
    close()
  }

  return (
    <ClearDoneConfirm
      plan={plan}
      held={held}
      running={running}
      onRecheck={() => setReads((n) => n + 1)}
      onConfirm={() => void confirm()}
      onCancel={close}
    />
  )
}

export function ReleaseFinishedDialog() {
  const cap = useCapability()
  const self = useSelf()
  const close = useStore((s) => s.closePaletteDialog)
  const [plan] = useState(() => releaseFinishedPlan(workspaceRuns(...scope()), cap, self))
  const [running, setRunning] = useState(false)

  const confirm = async () => {
    setRunning(true)
    await runReleaseFinished(plan.eligible, { api })
    setRunning(false)
    close()
  }

  return (
    <ReleaseFinishedConfirm
      plan={plan}
      running={running}
      onConfirm={() => void confirm()}
      onCancel={close}
    />
  )
}
