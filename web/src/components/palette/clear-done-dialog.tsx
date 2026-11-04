import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import { closeWarned, idleOutcome, readArchiveHolds, readCloseChecks } from '@/lib/close-check'
import type { RunCloseCheck } from '@/lib/types'
import type { StateContext } from '@/lib/needs-you'
import { clearDonePlan, releaseFinishedPlan, runClearDone, runReleaseFinished } from '@/lib/commands'
import { idleArchivePlan } from '@/lib/idle-archive'
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
  const [idle] = useState(() => {
    const [workspace, ctx] = scope()
    return idleArchivePlan(workspaceRuns(workspace, ctx), ctx, cap, self)
  })
  const [held, setHeld] = useState<Map<string, RunCloseCheck>>()
  const [idleChecks, setIdleChecks] = useState<Map<string, RunCloseCheck>>()
  const [selected, setSelected] = useState<Set<string>>(new Set())
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

  // A session with a close warning is listed unchecked, so closing it is the member's explicit choice.
  useEffect(() => {
    let live = true
    if (idle.idle.length === 0) return
    setIdleChecks(undefined)
    void readCloseChecks(idle.idle.map(({ run }) => run.id)).then((checks) => {
      if (!live) return
      setIdleChecks(checks)
      setSelected(new Set(idle.idle.filter(({ run }) => {
        const check = checks.get(run.id)
        return check && !closeWarned(check, idleOutcome(check))
      }).map(({ run }) => run.id)))
    })
    return () => {
      live = false
    }
  }, [idle, reads])

  const confirm = async () => {
    if (!held) return
    setRunning(true)
    const closing = idle.idle.filter(({ run }) => selected.has(run.id)).map(({ run }) => run)
    const outcomes = new Map(closing.map((run) => [run.id, idleOutcome(idleChecks!.get(run.id)!)]))
    await runClearDone([...plan.eligible.filter((run) => !held.has(run.id)), ...closing], { api, removeRun }, outcomes)
    setRunning(false)
    close()
  }

  return (
    <ClearDoneConfirm
      plan={plan}
      held={held}
      idle={idle}
      idleChecks={idleChecks}
      selected={selected}
      running={running}
      onToggle={(id) => setSelected((current) => {
        const next = new Set(current)
        if (!next.delete(id)) next.add(id)
        return next
      })}
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
