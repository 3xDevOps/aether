import { useState } from 'react'
import { api } from '@/lib/api'
import { runClearDone } from '@/lib/commands'
import { ClearDoneConfirm } from '@/routes/board/clear-done-dialog'
import { useStore } from '@/store'

/** Hosts the shared Clear done confirm dialog for the palette's "Clear done
 * runs" entry, over the plan `openClearDoneDialog` snapshotted when it fired. */
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
