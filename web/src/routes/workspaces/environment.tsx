// One workspace's remote environment: what the active version installs,
// which path made it, the version history, and rollback - plus the edit
// flow, where an admin describes a change in plain language, a coding
// agent registered on the server proposes a revised environment, and the
// review here (Dockerfile diff plus updated summary) gates the rebuild.
// Self-fetching like ToolsPanel, and it re-reads env.status whenever this
// session's build state moves or an edit run settles, so a finished build
// or a fresh proposal shows up without a reload.

import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
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
import type {
  EnvGetResult,
  EnvStatusResult,
  EnvironmentVersion,
  ManifestItem,
} from '@/lib/types'
import { parsePatch } from '@/routes/diff/parse'
import { FilePatch } from '@/routes/diff/patch-view'
import { friendly } from '@/routes/onboarding/environment-step'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

const pane =
  'max-h-64 overflow-x-auto overflow-y-auto border-t px-3 py-2 font-mono text-xs whitespace-pre-wrap'

/** How each source reads in a sentence; the agent name follows it. */
const madeBy: Record<string, string> = {
  mirror: 'mirrored from a machine',
  repo: 'built from the repository',
  standard: 'the standard environment',
  manual: 'written by hand',
}

/** One plain sentence per in-flight edit status; `proposed` and `failed`
 * render their own blocks instead. */
const editLine: Record<string, string> = {
  running: 'The agent is working on your request...',
  validating: 'Checking what the agent produced...',
  retrying: 'Fixing a problem with the result and trying once more...',
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

/** The manifest as a plain list: name, version, reason. The active
 * version and a proposal's summary render the same way. */
function ManifestList({ items }: { items: ManifestItem[] }) {
  return (
    <ul className="divide-y rounded-md border">
      {items.map((item) => (
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

  const edit = useStore((s) => s.envEdits[workspaceID])
  const startEnvEdit = useStore((s) => s.startEnvEdit)
  const clearEnvEdit = useStore((s) => s.clearEnvEdit)
  const startEnvBuild = useStore((s) => s.startEnvBuild)
  const clearEnvBuild = useStore((s) => s.clearEnvBuild)
  const [harness, setHarness] = useState('')
  const [request, setRequest] = useState('')
  const [busy, setBusy] = useState(false)
  const [proposal, setProposal] = useState<EnvGetResult | null>(null)

  const refetch = useCallback(async () => {
    try {
      setStatus(await client.envStatus({ id: workspaceID }))
    } catch (err) {
      setError(message(err))
    }
  }, [client, workspaceID])

  // buildStatus rides the dependency list so an approved build's outcome
  // lands in the history as soon as the event stream reports it; a
  // settled edit re-reads too, so a fresh proposal joins the history.
  const editSettled =
    edit !== undefined &&
    (edit.status === 'proposed' || edit.status === 'failed')
  useEffect(() => {
    void refetch()
  }, [refetch, buildStatus, editSettled])

  // A proposal names only its version; the pair and the diff against the
  // active version come from env.get once the event lands.
  useEffect(() => {
    if (!status || edit?.status !== 'proposed' || edit.version === undefined) {
      setProposal(null)
      return
    }
    let cancelled = false
    client
      .envGet({ id: workspaceID }, edit.version, status.active_version)
      .then((result) => {
        if (!cancelled) setProposal(result)
      })
      .catch((err) => {
        if (!cancelled) setError(message(err))
      })
    return () => {
      cancelled = true
    }
    // status.versions changing must not refetch the same proposal, so the
    // dependencies are exactly what the request reads.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, workspaceID, edit?.status, edit?.version, status?.active_version])

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

  // The active version's agent is the natural default for the next edit;
  // claude covers versions no setup-capable agent wrote.
  const defaultHarness =
    active?.harness && friendly[active.harness] ? active.harness : 'claude'
  const chosenHarness = harness || defaultHarness

  const submit = async () => {
    const note = request.trim()
    if (note === '') return
    setError(null)
    setBusy(true)
    // Priming the slice before the call means no event frame can beat
    // the in-flight state.
    startEnvEdit(workspaceID, chosenHarness)
    try {
      await client.envEdit({ id: workspaceID }, chosenHarness, note)
    } catch (err) {
      clearEnvEdit(workspaceID)
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const approve = async () => {
    if (!proposal) return
    setError(null)
    setBusy(true)
    try {
      // The build banner and its slice take over from here; the pair
      // rides along so a verification failure can seed its repair.
      startEnvBuild(workspaceID, {
        version: proposal.version,
        status: 'building',
        harness: proposal.harness,
        dockerfile: proposal.dockerfile,
        manifest: proposal.manifest,
        ...(proposal.source === 'mirror' || proposal.source === 'repo'
          ? { source: proposal.source }
          : {}),
      })
      try {
        await client.envBuild({ id: workspaceID }, proposal.version)
      } catch (err) {
        clearEnvBuild(workspaceID)
        throw err
      }
      clearEnvEdit(workspaceID)
      setRequest('')
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const diffFiles = useMemo(
    () => (proposal?.diff ? parsePatch(proposal.diff) : []),
    [proposal],
  )

  const editRunning = edit !== undefined && editLine[edit.status] !== undefined
  const canEdit =
    caps.hasMethod('env.edit') && status !== null && status.versions.length > 0

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
          <ManifestList items={active.manifest} />
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

      {canEdit && !edit && (
        <div className="space-y-2 rounded-md border bg-card p-3">
          <p className="text-xs text-muted-foreground">
            Describe a change and a coding agent updates the environment on
            the server. You review the change before anything is built.
          </p>
          <label className="block space-y-1 text-sm">
            Coding agent
            <select
              className={field}
              value={chosenHarness}
              onChange={(e) => setHarness(e.target.value)}
            >
              {Object.entries(friendly).map(([name, label]) => (
                <option key={name} value={name}>
                  {label}
                </option>
              ))}
            </select>
          </label>
          <label className="block space-y-1 text-sm">
            What should change?
            <textarea
              className={`${field} min-h-16`}
              value={request}
              placeholder="add go 1.24"
              onChange={(e) => setRequest(e.target.value)}
            />
          </label>
          <Button
            size="sm"
            disabled={busy || request.trim() === ''}
            onClick={() => void submit()}
          >
            Send to the agent
          </Button>
        </div>
      )}

      {editRunning && edit && (
        <div className="space-y-2">
          <p className="text-sm" role="status">
            {editLine[edit.status]}
          </p>
          <details className="rounded-md border bg-card">
            <summary className="cursor-pointer px-3 py-2 text-sm">
              View process
            </summary>
            <pre className={pane}>{edit.lines.join('\n')}</pre>
          </details>
        </div>
      )}

      {edit?.status === 'proposed' && proposal && (
        <div className="space-y-2 rounded-md border bg-card p-3">
          <h4 className="text-sm font-medium">Review the proposed change</h4>
          {diffFiles.length > 0 ? (
            diffFiles.map((file) => <FilePatch key={file.path} file={file} />)
          ) : (
            <p className="text-xs text-muted-foreground">
              The Dockerfile did not change.
            </p>
          )}
          <p className="text-xs text-muted-foreground">
            The updated environment:
          </p>
          <ManifestList items={proposal.manifest} />
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={busy} onClick={() => void approve()}>
              Approve and build
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => clearEnvEdit(workspaceID)}
            >
              Dismiss
            </Button>
          </div>
        </div>
      )}

      {edit?.status === 'failed' && (
        <div className="space-y-2 rounded-md border bg-card p-3">
          <p className="text-sm">The change did not finish.</p>
          <p className="text-xs text-state-failed">
            {edit.detail ?? 'the agent run failed'}
          </p>
          {edit.lines.length > 0 && (
            <details className="rounded-md border">
              <summary className="cursor-pointer px-3 py-2 text-sm">
                Last output
              </summary>
              <pre className={pane}>{edit.lines.join('\n')}</pre>
            </details>
          )}
          <Button size="sm" onClick={() => clearEnvEdit(workspaceID)}>
            Try again
          </Button>
        </div>
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
