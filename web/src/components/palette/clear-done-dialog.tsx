import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import { readArchiveHolds, readCloseChecks } from '@/lib/close-check'
import type { RunCloseCheck } from '@/lib/types'
import type { StateContext } from '@/lib/needs-you'
import { clearDonePlan, releaseFinishedPlan, runClearDone, runReleaseFinished } from '@/lib/commands'
import { idleArchivePlan, idleRows } from '@/lib/idle-archive'
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
  const build = () => {
    const [workspace, ctx] = scope()
    return { plan: clearDonePlan(finishedRuns(workspace, ctx), cap, self), idle: idleArchivePlan(workspaceRuns(workspace, ctx), ctx, cap, self) }
  }
  const [{ plan, idle }, setPlans] = useState(build)
  const [held, setHeld] = useState<Map<string, RunCloseCheck>>()
  const [rows, setRows] = useState<ReturnType<typeof idleRows>>()
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [running, setRunning] = useState(false)

  useEffect(() => {
    let live = true
    const completed = plan.eligible.filter((run) => run.status === 'completed')
    setHeld(completed.length === 0 ? new Map() : undefined)
    if (completed.length > 0) void readArchiveHolds(completed.map((run) => run.id)).then((next) => live && setHeld(next))
    return () => {
      live = false
    }
  }, [plan])

  // A session with a close warning, or whose outcome is unknown, is listed unchecked, so closing it is the member's explicit choice.
  useEffect(() => {
    let live = true
    setRows(idle.done.length === 0 ? { idle: [], recent: [] } : undefined)
    if (idle.done.length > 0) {
      void readCloseChecks(idle.done.map(({ run }) => run.id)).then((checks) => {
        if (!live) return
        const next = idleRows(idle.done, checks, Date.now())
        setRows(next)
        setSelected(new Set(next.idle.filter((row) => row.clean).map((row) => row.run.id)))
      })
    }
    return () => {
      live = false
    }
  }, [idle])

  const confirm = async () => {
    if (!held || !rows) return
    setRunning(true)
    const closing = rows.idle.filter((row) => selected.has(row.run.id))
    const closes = new Map(closing.map((row) => [row.run.id, { outcome: row.outcome, asked: row.asked }]))
    await runClearDone([...plan.eligible.filter((run) => !held.has(run.id)), ...closing.map((row) => row.run)], { api, removeRun }, closes)
    setRunning(false)
    close()
  }

  return (
    <ClearDoneConfirm
      plan={plan}
      held={held}
      idle={rows}
      excluded={[...idle.excluded, ...(rows?.recent ?? [])]}
      selected={selected}
      running={running}
      onSelect={setSelected}
      onRecheck={() => setPlans(build())}
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
