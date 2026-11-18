import { canReopenRun } from '@/lib/commands'
import type { Run } from '@/lib/types'

/** Ownership metadata is authoritative; neither status age nor silence proves expiry. */
export function RunRetention({ run, className = '' }: { run: Run; className?: string }) {
  if (!run.container_retained_until && !run.cleanup_pending && !run.cleanup_error) return null
  const until = run.container_retained_until && (
    <time className="text-text" dateTime={run.container_retained_until}>
      {new Date(run.container_retained_until).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}
    </time>
  )
  return (
    <div className={`space-y-1 text-ui-sm text-muted ${className}`} aria-label="Runtime retention">
      {until && (
        <p>
          {canReopenRun(run)
            ? <>Relaunch is available until {until}, or sooner if the server runs low on disk. Then the sandbox is deleted, but the branch and chat history stay. Relaunching and closing it again sets a new date.</>
            : <>Runtime grace ends {until}.</>}
          {run.asleep_saved
            ? ' The saved session holds no memory, only disk.'
            : run.asleep_since
              ? ' The stopped sandbox keeps only its disks; running processes do not come back.'
              : ' The paused sandbox keeps holding memory meanwhile.'}
        </p>
      )}
      {run.cleanup_pending && <p>Compute cleanup is pending; ownership stays protected until cleanup succeeds.</p>}
      {run.cleanup_error && <p role="status">{run.cleanup_error}</p>}
      <p>Releasing compute is separate from deleting files, history and result branches.</p>
    </div>
  )
}
