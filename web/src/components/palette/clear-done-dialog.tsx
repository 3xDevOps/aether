import { useState } from 'react'
import { api } from '@/lib/api'
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
  const [running, setRunning] = useState(false)

  const confirm = async () => {
    setRunning(true)
    await runClearDone(plan.eligible, { api, removeRun })
    setRunning(false)
    close()
  }

  return (
    <ClearDoneConfirm
      plan={plan}
      running={running}
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
