import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Spinner } from '@/components/ui/spinner'
import { api, type Api } from '@/lib/api'
import { rootCommand, useSysbox } from '@/lib/use-sysbox'
import { useStore } from '@/store'

/** Offers an admin Docker inside runs once; hidden for good after Dismiss. */
export function SysboxNudge({ client = api }: { client?: Api } = {}) {
  const dismissed = useStore((s) => s.sysboxNudgeDismissed)
  const dismiss = useStore((s) => s.dismissSysboxNudge)
  const { status, error, justInstalled, attempted, install } = useSysbox(client)
  if (dismissed || !status) return null
  const { state, reason } = status
  const command = rootCommand(status)
  if (state === 'on' ? !justInstalled : state === 'unsupported' && !attempted) return null
  return (
    <Callout
      tone="neutral"
      title={state === 'on' ? 'Docker is on' : 'Let agents use Docker in their runs'}
      actions={(
        <>
          {((state === 'off' && !command) || state === 'failed') && (
            <Button size="sm" variant="secondary" onClick={install}>{state === 'failed' ? 'Try again' : 'Install'}</Button>
          )}
          <Button size="sm" variant="ghost" onClick={dismiss}>Dismiss</Button>
        </>
      )}
    >
      <p role="status" className="flex items-center gap-2 text-muted empty:hidden">
        {state === 'installing' && <><Spinner />Installing… this takes a minute or two</>}
        {justInstalled && state === 'on' && 'New runs get it now. Reopen your Environment to use it there.'}
      </p>
      {state === 'off' && <p className="whitespace-pre-wrap break-words text-muted">{reason ?? 'Agents can build and run containers inside their own runs, isolated from this server.'}</p>}
      {command && <CopyableCommand command={command} />}
      {state === 'unsupported' && <p className="whitespace-pre-wrap break-words text-muted">{reason}</p>}
      {(state === 'failed' || error) && (
        <p role="alert" className="whitespace-pre-wrap break-words text-state-failed">{error ?? reason ?? 'Install failed'}</p>
      )}
    </Callout>
  )
}
