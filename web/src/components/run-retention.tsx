import type { Run } from '@/lib/types'

/** Ownership metadata is authoritative; neither status age nor silence proves expiry. */
export function RunRetention({ run, className = '' }: { run: Run; className?: string }) {
  if (!run.container_retained_until && !run.cleanup_pending && !run.cleanup_error) return null
  return (
    <div className={`space-y-1 text-ui-sm text-muted ${className}`} aria-label="Runtime retention">
      {run.container_retained_until && (
        <p>
          Runtime grace ends <time dateTime={run.container_retained_until}>{new Date(run.container_retained_until).toLocaleString()}</time>.
          {' '}Exact-process Reopen ends with runtime expiry. Paused compute still holds memory during the grace period.
        </p>
      )}
      {run.cleanup_pending && <p>Compute cleanup is pending; ownership stays protected until cleanup succeeds.</p>}
      {run.cleanup_error && <p role="status">{run.cleanup_error}</p>}
      <p>Releasing compute is separate from deleting files, history and result branches.</p>
    </div>
  )
}
