import { useId, useState } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { randomID } from '@/lib/random'
import { useReturnFocus } from '@/lib/hooks'
import { runLabel } from '@/lib/status'
import type { WatchCondition, WatchTarget } from '@/lib/types'
import { conditionChoices, defaultConditions, defaultExpiryDays, watchRoute } from '@/lib/watches'
import { useStore } from '@/store'
import { createWatch } from '@/store/watches'

function useTargetTitle(target: WatchTarget | null): string {
  return useStore((s) => {
    if (!target) return ''
    switch (target.kind) {
      case 'run': {
        const run = target.run_id ? s.runs[target.run_id] : undefined
        return run ? `“${runLabel(run)}”` : 'this session'
      }
      case 'pr':
        return `pull request #${target.number}${target.repo ? ` in ${target.repo}` : ''}`
    }
  })
}

/** Watch a run or a pull request; the server's receipt says what it will do. */
export function WatchDialog({ client = api }: { client?: Api }) {
  const returnFocus = useReturnFocus()
  const close = useStore((s) => s.closePaletteDialog)
  const runID = useStore((s) => s.paletteRunID)
  const picked = useStore((s) => s.paletteWatchTarget)
  const [target] = useState<WatchTarget | null>(() => picked ?? (runID ? { kind: 'run', run_id: runID } : null))
  const title = useTargetTitle(target)
  const [chosen, setChosen] = useState<WatchCondition[]>(() => (target ? defaultConditions(target.kind) : []))
  // One key per request: a retry after a lost answer cannot make a second watch, and changed conditions are a new request.
  const [key, setKey] = useState(() => randomID('watch'))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const id = useId()
  if (!target) return null

  const toggle = (condition: WatchCondition, on: boolean) => {
    setChosen((list) => (on ? [...list, condition] : list.filter((c) => c !== condition)))
    setKey(randomID('watch'))
  }

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const result = await createWatch(useStore, client, { target, conditions: chosen, source: 'dashboard', idempotency_key: key })
      close()
      if (result) toast.success(result.receipt, {
        action: {
          label: 'View watch',
          onClick: () => {
            const route = watchRoute(result.watch.id)
            useStore.getState().navigate(route.name, route.params)
          },
        },
      })
    } catch (err) {
      setBusy(false)
      setError(errorSentence(err))
    }
  }

  return (
    <Dialog open onOpenChange={() => !busy && close()}>
      <DialogContent {...returnFocus} className="max-w-[min(480px,calc(100%-2rem))]">
        <DialogHeader>
          <DialogTitle>Watch {title}</DialogTitle>
          <DialogDescription>
            The server keeps checking it after you close this page. Notices appear in the dashboard and go to your push devices when push is on; the confirmation lists where they will go. A watch never acts: it does not answer, approve or merge. It ends after {defaultExpiryDays} days.
          </DialogDescription>
        </DialogHeader>
        <fieldset className="flex min-w-0 flex-col gap-3">
          <legend className="sr-only">Notify me when</legend>
          {conditionChoices[target.kind].map((choice) => (
            <div key={choice.value} className="flex min-w-0 items-start gap-3">
              <Checkbox
                id={`${id}-${choice.value}`}
                aria-describedby={`${id}-${choice.value}-help`}
                className="mt-0.5"
                checked={chosen.includes(choice.value)}
                disabled={busy}
                onCheckedChange={(checked) => toggle(choice.value, checked === true)}
              />
              <div className="flex min-w-0 flex-col gap-0.5">
                <label htmlFor={`${id}-${choice.value}`} className="text-ui font-medium text-text">{choice.label}</label>
                <p id={`${id}-${choice.value}-help`} className="text-ui-sm text-muted">{choice.description}</p>
              </div>
            </div>
          ))}
        </fieldset>
        {error && <p role="alert" className="break-words text-ui-sm text-state-failed">{error}</p>}
        <DialogFooter>
          <Button variant="secondary" disabled={busy} onClick={close}>Cancel</Button>
          <Button disabled={busy || chosen.length === 0} onClick={() => void submit()}>{busy ? 'Watching…' : 'Watch'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
