import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
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
    <section aria-labelledby={headingID} className="flex min-w-0 flex-col items-start gap-2">
      <h4 id={headingID} className="text-ui font-medium text-text">Server-fetched source</h4>
      <p className="max-w-3xl text-ui-sm text-muted">
        {canManageSource
          ? 'The server can fetch the repository itself over public HTTPS or with a read-only deploy key, so no local clone is needed.'
          : 'The server can fetch the repository itself. An administrator manages it in Source control.'}
      </p>
      <div role="status" aria-label="Source mirror status" aria-live="polite" className="flex min-w-0 flex-col gap-2 self-stretch text-ui-sm">
        {loading && <p className="text-muted">Checking source mirror status...</p>}
        {!loading && statusError && <Callout tone="failed" role="alert">Could not check source mirror status: {statusError}</Callout>}
        {!loading && !statusError && !configured && (
          <p>
            <span className="font-medium">Local-only workspace.</span> No server-owned source mirror is configured yet.
          </p>
        )}
        {!loading && !statusError && configured && status && (
          <>
            <p className="font-medium">Source mirror configured.</p>
            <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
              <dt className="text-muted">Status</dt>
              <dd className="font-code">{statusText}</dd>
              <dt className="text-muted">Source</dt>
              <dd className="break-all font-code" title={status.source_url}>{status.source_url || 'Not reported'}</dd>
              <dt className="text-muted">Branch</dt>
              <dd className="break-all font-code">{status.branch || 'Not reported'}</dd>
              <dt className="text-muted">Accepted base</dt>
              <dd className="break-all font-code">{status.accepted_commit || (canManageSource ? 'None - verify and explicitly adopt a candidate before launch' : 'None - ask an administrator to verify and adopt a candidate before launch')}</dd>
            </dl>
            {status.last_error && <Callout tone="failed" role="alert" className="whitespace-pre-wrap">{status.last_error}</Callout>}
            {status.status !== 'ready' && <p>{canManageSource ? 'Source is not ready for a new run. Open Source control to repair, verify or review the candidate.' : 'Source is not ready for a new run. Ask an administrator to repair, verify or review the candidate in Source control.'} The workspace is retained; do not import it again.</p>}
          </>
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
