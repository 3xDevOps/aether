import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { Button } from '@/components/ui/button'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { WorkspaceMirrorResult } from '@/lib/types'


export function OnboardingSourceOption({
  client,
  workspaceID,
  canManageSource,
  suggestedSource,
  onStatusChange,
}: {
  client: Api
  workspaceID: string
  canManageSource: boolean
  suggestedSource?: string
  onStatusChange?: (status: WorkspaceMirrorResult) => void
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
    onStatusChange?.(current)
  }, [onStatusChange])

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
        onStatusChange?.(current)
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
  }, [client, workspaceID, onStatusChange])

  const configured = status?.enabled === true
  const statusText = status?.status ?? 'configured'

  return (
    <section aria-labelledby={headingID} className="mt-4 space-y-3 border-t border-border/70 pt-3">
      <div className="space-y-1">
        <h2 id={headingID} className="text-sm font-medium">Public or private remote repository</h2>
        <p className="max-w-3xl text-xs leading-relaxed text-muted-foreground">
          {canManageSource
            ? 'Configure a server-fetched source using public HTTPS or a read-only deploy key. No local clone is needed. Checkout Origin is a separate publishing destination.'
            : 'View the server-fetched source here. An administrator manages remote sources and candidate adoption in Source control. Checkout Origin is a separate publishing destination.'}
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
            <p>Accepted base: <code className="break-all">{status.accepted_commit || (canManageSource ? 'None — verify and explicitly adopt a candidate before launch' : 'None — ask an administrator to verify and adopt a candidate before launch')}</code></p>
            {status.last_error && (
              <p role="alert" className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-state-failed">
                {status.last_error}
              </p>
            )}
            {status.status !== 'ready' && <p>{canManageSource ? 'Source is not ready for a new run. Open Source control to repair, verify or review the candidate.' : 'Source is not ready for a new run. Ask an administrator to repair, verify or review the candidate in Source control.'} The workspace is retained; do not import it again.</p>}
          </div>
        )}
      </div>

      {canManageSource && <Button type="button" variant="secondary" size="sm" onClick={() => setDialogOpen(true)}>
        {configured ? 'Review source mirror' : 'Set up source mirror'}
      </Button>}

      {canManageSource && dialogOpen && (
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
