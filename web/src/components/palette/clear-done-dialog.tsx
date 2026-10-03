import { useState } from 'react'
import { api } from '@/lib/api'
import { runClearDone, runReleaseFinished } from '@/lib/commands'
import { ClearDoneConfirm, ReleaseFinishedConfirm } from '@/routes/board/clear-done-dialog'
import { useStore } from '@/store'

/** Hosts the archive confirmation over the palette's snapshotted plan. */
export function ClearDoneDialog() {
  const plan = useStore((s) => s.paletteClearDonePlan)
  const close = useStore((s) => s.closePaletteDialog)
  const removeRun = useStore((s) => s.removeRun)
  const [running, setRunning] = useState(false)

  if (!plan) return null

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
  const plan = useStore((s) => s.paletteReleaseFinishedPlan)
  const close = useStore((s) => s.closePaletteDialog)
  const [running, setRunning] = useState(false)

  if (!plan) return null

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
