// One workspace's tool snapshots: the immutable history behind ~/.local,
// with verify/rollback/reset controls. Every verb is the server's; the
// verify result and any refusal render verbatim.

import { useCallback, useEffect, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import type { ToolSnapshot } from '@/lib/types'
import { useCapability } from '@/store/hooks'

export function ToolsPanel({
  workspaceID,
  client = api,
}: {
  workspaceID: string
  client?: Api
}) {
  const caps = useCapability()
  const [snapshots, setSnapshots] = useState<ToolSnapshot[]>([])
  const [verifying, setVerifying] = useState(false)
  const [verdict, setVerdict] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<
    { kind: 'rollback'; snapshot: ToolSnapshot } | { kind: 'reset' } | null
  >(null)

  const refetch = useCallback(async () => {
    try {
      setSnapshots(await client.toolsList({ id: workspaceID }))
    } catch (err) {
      setError(message(err))
    }
  }, [client, workspaceID])

  useEffect(() => {
    void refetch()
  }, [refetch])

  const verify = async () => {
    setVerifying(true)
    setVerdict(null)
    setError(null)
    try {
      const result = await client.toolsVerify({ id: workspaceID })
      setVerdict(JSON.stringify(result, null, 2))
    } catch (err) {
      setError(message(err))
    } finally {
      setVerifying(false)
    }
  }

  const act = async () => {
    if (!confirming) return
    setError(null)
    try {
      if (confirming.kind === 'rollback') {
        await client.toolsRollback({ id: workspaceID }, confirming.snapshot.id)
      } else {
        await client.toolsReset({ id: workspaceID })
      }
      setConfirming(null)
      await refetch()
    } catch (err) {
      setError(message(err))
    }
  }

  return (
    <section aria-label="Tool snapshots" className="space-y-2">
      <div className="flex items-center gap-2">
        <h3 className="flex-1 text-xs font-medium text-muted-foreground">Tools</h3>
        {caps.hasMethod('workspace.tools.verify') && (
          <Button size="sm" variant="outline" disabled={verifying} onClick={() => void verify()}>
            Verify
          </Button>
        )}
        {caps.hasMethod('workspace.tools.reset') && snapshots.length > 0 && (
          <Button size="sm" variant="ghost" onClick={() => setConfirming({ kind: 'reset' })}>
            Reset
          </Button>
        )}
      </div>

      {snapshots.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No tool snapshots yet. Bootstrap the workspace to create one.
        </p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left text-xs text-muted-foreground">
              <th className="py-1 pr-2 font-normal">Digest</th>
              <th className="py-1 pr-2 font-normal">Created</th>
              <th className="py-1 pr-2 font-normal" />
              <th className="py-1 font-normal" />
            </tr>
          </thead>
          <tbody>
            {snapshots.map((snapshot) => (
              <tr key={snapshot.id} className="border-b last:border-b-0">
                <td className="py-1.5 pr-2 font-mono text-xs">
                  {snapshot.digest.slice(0, 12)}
                </td>
                <td className="py-1.5 pr-2 text-xs text-muted-foreground">
                  {timeAgo(snapshot.created_at)}
                </td>
                <td className="py-1.5 pr-2">
                  {snapshot.active && (
                    <span className="rounded-full border px-1.5 py-0.5 text-xs">
                      active
                    </span>
                  )}
                </td>
                <td className="py-1.5 text-right">
                  {caps.hasMethod('workspace.tools.rollback') && !snapshot.active && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setConfirming({ kind: 'rollback', snapshot })}
                    >
                      Rollback
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {verdict && (
        <pre className="overflow-x-auto rounded-md border bg-card p-2 text-xs whitespace-pre-wrap">
          {verdict}
        </pre>
      )}
      {error && <p className="text-xs text-state-failed">{error}</p>}

      {confirming && (
        <Dialog open onOpenChange={() => setConfirming(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>
                {confirming.kind === 'rollback'
                  ? `Roll back to ${confirming.snapshot.digest.slice(0, 12)}?`
                  : 'Reset tool snapshots?'}
              </DialogTitle>
              <DialogDescription>
                {confirming.kind === 'rollback'
                  ? 'New runs in this workspace mount the selected snapshot.'
                  : 'Every snapshot is removed; new runs start from the bare image.'}
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setConfirming(null)}>
                Cancel
              </Button>
              <Button variant="destructive" onClick={() => void act()}>
                {confirming.kind === 'rollback' ? 'Rollback' : 'Reset'}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </section>
  )
}
