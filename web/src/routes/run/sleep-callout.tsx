import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Spinner } from '@/components/ui/spinner'
import { api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { clockTime, errorSentence } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

/** An idle session whose container Aether stopped or whose VM it saved, and the wake that brings it back. */
export function SleepCallout({ run }: { run: RunRecord }) {
  const now = useClock()
  const self = useSelf(run.workspace_id)
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const since = run.asleep_since
  // A failure belongs to one sleep; the next one starts clean.
  useEffect(() => setError(undefined), [since])
  // A closed session reopens through Relaunch, which its composer offers.
  if (!since || isTerminal(run.status)) return null
  const waking = run.waking === true || busy
  const failure = error ?? run.wake_error
  const wake = async () => {
    setBusy(true)
    setError(undefined)
    try {
      await api.runWake(run.id)
    } catch (err) {
      setError(errorSentence(err))
    } finally {
      setBusy(false)
    }
  }
  const failed = failure !== undefined && !waking
  const saved = run.asleep_saved === true
  const at = <time dateTime={since}>{clockTime(since, now)}</time>
  return (
    <Callout
      tone={failed ? 'failed' : 'paused'}
      title={waking
        ? <span className="inline-flex items-center gap-1.5"><Spinner label="Waking" />Waking…</span>
        : failed ? 'Wake failed' : <>Asleep since {at}</>}
      actions={waking || !maySteer ? undefined : (
        <Button size="sm" variant="secondary" hint={saved ? 'Restores the saved session' : 'Starts the container and restores the conversation'} onClick={() => void wake()}>
          Wake
        </Button>
      )}
    >
      {waking && <p>{saved ? 'Restoring the saved session.' : 'Starting the container and restoring the agent’s conversation.'}</p>}
      {!waking && (
        <p>
          {failed && <>Asleep since {at}. </>}
          {saved
            ? 'Aether saved the idle session to disk. Its processes, terminals and conversation come back where they were.'
            : 'Aether stopped the idle container. The conversation, files and checkout are kept; running processes are not.'}
          {maySteer && ' Send a message or press Wake to start it.'}
        </p>
      )}
      {failed && <p className="mt-1">{failure}</p>}
    </Callout>
  )
}
