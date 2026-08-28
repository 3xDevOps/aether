// One workspace's remote environment: what the active version installs,
// which path made it, the version history, and rollback. This is the read
// side of the Environment panel; the edit flow lands in this file next.
// Self-fetching like ToolsPanel, and it re-reads env.status whenever this
// session's build state moves, so a finished build shows up without a
// reload.

import { Fragment, useCallback, useEffect, useState } from 'react'
import { message } from '@/components/palette/palette'
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
import type { EnvStatusResult, EnvironmentVersion } from '@/lib/types'
import { friendly } from '@/routes/onboarding/environment-step'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

/** How each source reads in a sentence; the agent name follows it. */
const madeBy: Record<string, string> = {
  mirror: 'mirrored from a machine',
  repo: 'built from the repository',
  standard: 'the standard environment',
  manual: 'written by hand',
}

/** The version rollback returns to: the newest good version below the
 * active one, the same pick the server makes. Versions arrive newest
 * first, so the first match is it. */
export function rollbackTarget(
  status: EnvStatusResult,
): EnvironmentVersion | undefined {
  const active = status.active_version
  if (active === undefined) return undefined
  return status.versions.find(
    (v) => v.version < active && v.status === 'saved',
  )
}

export function EnvironmentPanel({
  workspaceID,
  client = api,
}: {
  workspaceID: string
  client?: Api
}) {
  const caps = useCapability()
  const [status, setStatus] = useState<EnvStatusResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<EnvironmentVersion | null>(null)
  const buildStatus = useStore((s) => s.envBuilds[workspaceID]?.status)

  const refetch = useCallback(async () => {
    try {
      setStatus(await client.envStatus({ id: workspaceID }))
    } catch (err) {
      setError(message(err))
    }
  }, [client, workspaceID])

  // buildStatus rides the dependency list so an approved build's outcome
  // lands in the history as soon as the event stream reports it.
  useEffect(() => {
    void refetch()
  }, [refetch, buildStatus])

  const rollback = async () => {
    if (!confirming) return
    setError(null)
    try {
      await client.envRollback({ id: workspaceID })
      setConfirming(null)
      await refetch()
    } catch (err) {
      setError(message(err))
    }
  }

  const active = status?.versions.find(
    (v) => v.version === status.active_version,
  )
  const target = status ? rollbackTarget(status) : undefined

  return (
    <section aria-label="Environment" className="space-y-2">
      <div className="flex items-center gap-2">
        <h3 className="flex-1 text-xs font-medium text-muted-foreground">
          Environment
        </h3>
        {caps.hasMethod('env.rollback') && target && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => setConfirming(target)}
          >
            Rollback
          </Button>
        )}
      </div>

      {status && status.versions.length === 0 && (
        <p className="text-xs text-muted-foreground">
          This workspace uses the image it was created with.
        </p>
      )}

      {status && status.versions.length > 0 && !active && (
        <p className="text-xs text-muted-foreground">
          No version is active yet; the workspace uses the image it was
          created with.
        </p>
      )}

      {active && (
        <>
          <p className="text-xs text-muted-foreground">
            Version {active.version}, {madeBy[active.source] ?? active.source}
            {active.harness
              ? ` with ${friendly[active.harness] ?? active.harness}`
              : ''}
            .
          </p>
          <ul className="divide-y rounded-md border">
            {active.manifest.map((item) => (
              <li key={item.name} className="space-y-0.5 px-3 py-2 text-sm">
                <span className="flex items-baseline gap-2">
                  <span className="font-medium">{item.name}</span>
                  <span className="font-mono text-xs text-muted-foreground">
                    {item.version}
                  </span>
                </span>
                {item.reason && (
                  <span className="block text-xs text-muted-foreground">
                    {item.reason}
                  </span>
                )}
              </li>
            ))}
          </ul>
        </>
      )}

      {status && status.versions.length > 0 && (
        <table className="w-full text-sm" aria-label="Version history">
          <thead>
            <tr className="border-b text-left text-xs text-muted-foreground">
              <th className="py-1 pr-2 font-normal">Version</th>
              <th className="py-1 pr-2 font-normal">Status</th>
              <th className="py-1 pr-2 font-normal">Source</th>
              <th className="py-1 font-normal">When</th>
            </tr>
          </thead>
          <tbody>
            {status.versions.map((v) => (
              <Fragment key={v.version}>
                <tr className="border-b last:border-b-0">
                  <td className="py-1.5 pr-2 font-mono text-xs">
                    v{v.version}
                  </td>
                  <td className="py-1.5 pr-2 text-xs">{v.status}</td>
                  <td className="py-1.5 pr-2 text-xs text-muted-foreground">
                    {v.source}
                  </td>
                  <td className="py-1.5 text-xs text-muted-foreground">
                    {timeAgo(v.created_at)}
                  </td>
                </tr>
                {v.status === 'failed' && v.failure_detail && (
                  <tr className="border-b last:border-b-0">
                    <td colSpan={4} className="pb-1.5 text-xs text-state-failed">
                      {v.failure_detail}
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
      )}

      {error && <p className="text-xs text-state-failed">{error}</p>}

      {confirming && (
        <Dialog open onOpenChange={() => setConfirming(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Roll back to version {confirming.version}?</DialogTitle>
              <DialogDescription>
                The workspace returns to version {confirming.version}; new
                runs use that environment.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setConfirming(null)}>
                Cancel
              </Button>
              <Button variant="destructive" onClick={() => void rollback()}>
                Rollback
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </section>
  )
}
