import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useState } from 'react'
import { toast } from 'sonner'
import { RunRetention } from '@/components/run-retention'
import { Checkbox } from '@/components/ui/checkbox'
import { api, type Api } from '@/lib/api'
import type { Command } from '@/lib/commands'
import { message } from '@/lib/format'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'
import { unfinishedChildren } from '@/store/children'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** Detaches every child first; one refusal stops the verb, so nothing is stopped that was meant to keep running. */
export async function detachThenConfirm(children: RunRecord[], onConfirm: () => void, client: Api = api) {
  try {
    await Promise.all(children.map((child) => client.runChildDetach(child.id)))
  } catch (err) {
    toast.error(`Detach failed, the run was not stopped: ${message(err)}`)
    return
  }
  onConfirm()
}

export function RunCommandConfirmation({
  run,
  confirmation,
  onConfirm,
  onClose,
  onCloseAutoFocus,
  client = api,
}: {
  run: RunRecord
  confirmation: NonNullable<Command['confirm']>
  onConfirm: () => void
  onClose: () => void
  onCloseAutoFocus?: (event: Event) => void
  client?: Api
}) {
  const runs = useStore((s) => s.runs)
  const detachable = useCapability().hasMethod('run.child.detach')
  const children = confirmation.stopsChildren ? unfinishedChildren(runs, run) : []
  const [detach, setDetach] = useState(false)
  const stops = children.length === 1
    ? 'Its unfinished child run stops too, with any runs it started.'
    : `Its ${children.length} unfinished child runs stop too, with any runs they started.`
  return (
    <AlertDialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <AlertDialogContent
        className="max-w-[min(420px,calc(100%-2rem))]"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>{confirmation.title}</AlertDialogTitle>
          <AlertDialogDescription>
            &quot;{runLabel(run)}&quot; - {confirmation.body}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {children.length > 0 && (
          <div className="flex flex-col gap-2 text-ui-sm">
            <p>{stops}</p>
            {detachable && (
              <label className="flex cursor-pointer items-center gap-2 text-ui">
                <Checkbox checked={detach} onCheckedChange={(on) => setDetach(on === true)} />
                Detach them and keep them running
              </label>
            )}
          </div>
        )}
        <RunRetention run={run} />
        <AlertDialogFooter>
          {/* Radix AlertDialog focuses Cancel when the confirmation opens. */}
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={() => (detach ? void detachThenConfirm(children, onConfirm, client) : onConfirm())}>
            {confirmation.action}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
