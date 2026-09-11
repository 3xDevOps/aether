import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { Button } from '@/components/ui/button'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { WorkspaceMirrorResult } from '@/lib/types'


/**
 * The optional source mirror affordance in onboarding. Checkout Origin is
 * where runs push review branches; this mirror is server-owned and refreshed
 * before each fresh run.
 */
export function OnboardingSourceOption({
  client,
  workspaceID,
  suggestedSource,
}: {
  client: Api
  workspaceID: string
  suggestedSource?: string
}) {
  const headingID = useId()
  const [status, setStatus] = useState<WorkspaceMirrorResult | null>(null)
  const [statusError, setStatusError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [dialogOpen, setDialogOpen] = useState(false)
  const statusRevision = useRef(0)
  const handleStatusChange = useCallback((current: WorkspaceMirrorResult) => {
    statusRevision.current += 1
    setStatusError(null)
    setStatus(current)
    setLoading(false)
  }, [])

  useEffect(() => {
    const revision = ++statusRevision.current
    let live = true
    setStatus(null)
    setStatusError(null)
    setLoading(true)
    void client.workspaceMirrorStatus(workspaceID).then(
      (current) => {
        if (!live || statusRevision.current !== revision) return
        setStatus(current)
        setLoading(false)
      },
      (err) => {
        if (!live || statusRevision.current !== revision) return
        setStatusError(message(err))
        setLoading(false)
      },
    )
    return () => {
      live = false
      if (statusRevision.current === revision) {
        statusRevision.current += 1
      }
    }
  }, [client, workspaceID])

  const configured = status?.enabled === true
  const statusText = status?.status ?? 'configured'

  return (
    <section aria-labelledby={headingID} className="mt-4 space-y-3 border-t border-border/70 pt-3">
      <div className="space-y-1">
        <h2 id={headingID} className="text-sm font-medium">Optional source mirror</h2>
        <p className="max-w-3xl text-xs leading-relaxed text-muted-foreground">
          Checkout Origin is where runs push branches for review. A source mirror is server-owned and refreshed before each fresh run. This setup is optional and does not block onboarding.
        </p>
      </div>

      <div
        role="status"
        aria-label="Source mirror status"
        aria-live="polite"
        className="min-w-0 space-y-2 border-y border-border/70 bg-sidebar/30 px-3 py-2.5 text-xs"
      >
        {loading && <p>Checking source mirror status...</p>}
        {!loading && statusError && (
          <p role="alert" className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-state-failed">
            Could not check source mirror status: {statusError}
          </p>
        )}
        {!loading && !statusError && !configured && (
          <p>
            <span className="font-medium">Local-only workspace.</span>{' '}
            No server-owned source mirror is configured yet.
          </p>
        )}
        {!loading && !statusError && configured && status && (
          <div className="space-y-2">
            <p className="font-medium">Source mirror configured.</p>
            <dl className="grid min-w-0 gap-x-4 gap-y-1.5 sm:grid-cols-3">
              <div className="min-w-0">
                <dt className="text-muted-foreground">Status</dt>
                <dd className="font-mono">{statusText}</dd>
              </div>
              <div className="min-w-0 sm:col-span-2">
                <dt className="text-muted-foreground">Source</dt>
                <dd className="break-all font-mono" title={status.source_url}>{status.source_url || 'Not reported'}</dd>
              </div>
              <div className="min-w-0 sm:col-span-3">
                <dt className="text-muted-foreground">Branch</dt>
                <dd className="break-all font-mono">{status.branch || 'Not reported'}</dd>
              </div>
            </dl>
            {status.last_error && (
              <p role="alert" className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-state-failed">
                {status.last_error}
              </p>
            )}
          </div>
        )}
      </div>

      <Button type="button" variant="outline" size="sm" onClick={() => setDialogOpen(true)}>
        {configured ? 'Review source mirror' : 'Set up source mirror'}
      </Button>

      {dialogOpen && (
        <WorkspaceMirrorDialog
          workspaceID={workspaceID}
          client={client}
          suggestedSource={suggestedSource}
          onStatusChange={handleStatusChange}
          onClose={() => setDialogOpen(false)}
        />
      )}
    </section>
  )
}
