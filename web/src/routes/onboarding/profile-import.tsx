// Part B of the onboarding Agents step: bringing this machine's agent
// configuration - skills, commands, standing instructions, settings, MCP
// servers, plugins - to the server, one harness at a time. Every harness
// is previewed first (profile.preview uploads nothing), the preview's
// exclusions are shown with the reason the guard gave, and only a checked
// row is pushed. A scanner finding blocks its harness outright: the fix is
// local, and the --allow-secret override stays on the CLI. The agent path
// (a `profile` scan) only proposes a checklist; the push is still the
// user's click.

import { useCallback, useEffect, useRef, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { ApiError, type Api, type EnvScanSession } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import type {
  EnvScanStatus,
  HarnessStatus,
  ProfileExclusion,
  ProfilePreview,
  ProfilePreviewCategory,
  ProfilePushResult,
  ProfileStatus,
  Workspace,
} from '@/lib/types'
import { friendly } from '@/routes/onboarding/environment-step'

const pane =
  'max-h-64 overflow-x-auto overflow-y-auto border-t px-3 py-2 font-mono text-xs whitespace-pre-wrap'

/** One plain sentence per coarse scan status, in this step's terms. */
const statusLine: Record<EnvScanStatus, string> = {
  detecting: 'Getting ready...',
  running: 'Reading the file lists on this machine...',
  validating: 'Checking what the agent proposed...',
  retrying: 'Fixing a problem with the proposal and trying once more...',
}

/** Singular and plural for each category the preview reports. */
const categoryWords: Record<string, [string, string]> = {
  memory: ['memory file', 'memory files'],
  skills: ['skill', 'skills'],
  commands: ['command', 'commands'],
  settings: ['settings file', 'settings files'],
  mcp: ['MCP file', 'MCP files'],
  plugins: ['plugin file', 'plugin files'],
  other: ['other file', 'other files'],
}

function countPhrase(c: ProfilePreviewCategory): string {
  const words = categoryWords[c.category]
  if (!words) return `${c.files} ${c.category}`
  return `${c.files} ${c.files === 1 ? words[0] : words[1]}`
}

/** "12 skills, 4 commands, 1 memory file - 179 KB", in report order. */
export function previewSummary(preview: ProfilePreview): string {
  const parts = (preview.categories ?? [])
    .filter((c) => c.files > 0)
    .map(countPhrase)
  const counted = parts.length > 0 ? parts.join(', ') : `${preview.files} files`
  return `${counted} - ${formatBytes(preview.bytes)}`
}

/** The exclusion behind a blocked preview: the one the gateway named, or
 * the first push-aborting one when an older gateway named none. */
function flagged(preview: ProfilePreview) {
  const excluded = preview.excluded ?? []
  return (
    excluded.find((e) => e.path === preview.blocked_path) ??
    excluded.find((e) => e.reason === 'secret' || e.reason === 'symlink')
  )
}

/** What blocked this profile, in the gateway's own words where it gave
 * them. A symlink escape and a scanner finding read differently and the
 * user acts on them differently, so neither is described as the other. */
function blockedLine(
  preview: ProfilePreview,
  secret: ProfileExclusion | undefined,
): string {
  const path = preview.blocked_path ?? secret?.path
  const detail = preview.blocked_detail ?? secret?.detail
  if (!path) return 'A file in this profile refuses the push.'
  return detail ? `${path}: ${detail}` : `${path} refuses the push.`
}

/**
 * Whether a preview failure means "this harness does not sync a profile"
 * rather than "something went wrong". The gateway answers -32602 for a
 * harness name the registry does not know or that has no profile root;
 * every other failure is real and belongs on the screen.
 */
function notSyncable(err: unknown): boolean {
  return err instanceof ApiError && err.status === 400
}

type ScanPhase =
  | { name: 'idle' }
  | { name: 'scanning'; status: EnvScanStatus }
  | { name: 'failed'; detail: string; outputTail?: string }

export function ProfileImport({
  client,
  harnesses,
  candidates,
  served,
  repoPath,
  workspace,
}: {
  client: Api
  /** The setup-capable harnesses env.harnesses reported. Only these can
   * run the scan; profile sync itself covers more of them. */
  harnesses: HarnessStatus[]
  /** Every harness name worth previewing. Wider than `harnesses`:
   * opencode syncs a profile from ~/.local/share/opencode but is not
   * setup-capable, so it would otherwise never be offered. */
  candidates: string[]
  /** Whether this gateway serves profile.preview and profile.push. An
   * older one serves neither, and saying so beats reporting an empty
   * machine. */
  served: boolean
  /** The linked repository folder, passed to a profile scan when known. */
  repoPath?: string
  /** The workspace the wizard settled on; only named in the CLI fallback
   * command, which needs it for the --allow-secret audit trail. */
  workspace: Workspace | null
}) {
  const [previews, setPreviews] = useState<Record<string, ProfilePreview>>({})
  const [statuses, setStatuses] = useState<Record<string, ProfileStatus>>({})
  // Which harnesses have answered a preview, and which one is being
  // walked right now ('' when none is).
  const [previewed, setPreviewed] = useState<string[]>([])
  const [looking, setLooking] = useState('')
  const [previewErrors, setPreviewErrors] = useState<Record<string, string>>({})
  const [checked, setChecked] = useState<Record<string, boolean>>({})
  const [results, setResults] = useState<Record<string, ProfilePushResult>>({})
  const [failures, setFailures] = useState<Record<string, string>>({})
  const [reasons, setReasons] = useState<Record<string, string>>({})
  const [suggested, setSuggested] = useState<Record<string, string[]>>({})
  const [busy, setBusy] = useState(false)
  const [phase, setPhase] = useState<ScanPhase>({ name: 'idle' })
  const [lines, setLines] = useState<string[]>([])
  const session = useRef<EnvScanSession | null>(null)

  // A preview walks and secret-scans a whole profile root. That is not
  // free - an agent's configuration directory routinely holds hundreds of
  // megabytes of transcripts - so it never runs on mount: the user asks
  // for it, one harness at a time, and can stop it.
  const scanning = useRef<AbortController | null>(null)

  const cancelPreviews = useCallback(() => {
    scanning.current?.abort()
    scanning.current = null
    setLooking('')
  }, [])

  // Aborting on unmount stops the walk on the gateway, not just this
  // component's interest in it.
  useEffect(() => () => scanning.current?.abort(), [])

  const runPreviews = useCallback(async () => {
    if (scanning.current) return
    const controller = new AbortController()
    scanning.current = controller
    setPreviewErrors({})
    // One at a time: several concurrent walks of the same home directory
    // are slower than the same work in sequence, and the per-harness
    // label would be a lie.
    for (const name of candidates) {
      if (controller.signal.aborted) break
      setLooking(name)
      try {
        const preview = await client.localProfilePreview(name, controller.signal)
        if (controller.signal.aborted) break
        setPreviews((prev) => ({ ...prev, [name]: preview }))
        setPreviewed((prev) => (prev.includes(name) ? prev : [...prev, name]))
        if (!preview.present) continue
        // A snapshot on the server means this member already imported
        // this harness; the row says so rather than redoing it.
        try {
          const status = await client.profileStatus(name)
          setStatuses((prev) => ({ ...prev, [name]: status }))
        } catch {
          // The row simply does not claim a previous import. Nothing the
          // user has to act on, and the preview itself stands.
        }
      } catch (err) {
        if (controller.signal.aborted) break
        setPreviewed((prev) => (prev.includes(name) ? prev : [...prev, name]))
        // A harness the registry has no profile sync for refuses with
        // -32602. That is the one answer that means "not something this
        // machine can import" rather than "something went wrong", so it
        // is the only one swallowed; everything else is the user's to
        // see, on that harness's own row.
        if (!notSyncable(err)) {
          setPreviewErrors((prev) => ({ ...prev, [name]: message(err) }))
        }
      }
    }
    if (scanning.current === controller) {
      scanning.current = null
      setLooking('')
    }
  }, [client, candidates])

  // Leaving the step closes the socket, which cancels the scan and its
  // process on the gateway.
  useEffect(() => () => session.current?.close(), [])

  const present = candidates
    .map((name) => previews[name])
    .filter((p): p is ProfilePreview => p !== undefined && p.present)

  const looked = previewed.length >= candidates.length && looking === ''

  const selected = present
    .filter((p) => checked[p.harness] && !p.blocked)
    .map((p) => p.harness)

  const importSelected = useCallback(async () => {
    setBusy(true)
    // One harness at a time, each result or refusal landing on its own
    // row: a refusal must not abandon the harnesses behind it.
    for (const name of selected) {
      setFailures((prev) => {
        const next = { ...prev }
        delete next[name]
        return next
      })
      try {
        const result = await client.localProfilePush(name)
        setResults((prev) => ({ ...prev, [name]: result }))
        setChecked((prev) => ({ ...prev, [name]: false }))
      } catch (err) {
        setFailures((prev) => ({ ...prev, [name]: message(err) }))
      }
    }
    setBusy(false)
  }, [client, selected])

  const scanHarness = harnesses.find((h) => h.installed)?.name

  const startScan = () => {
    if (!scanHarness) return
    setLines([])
    setPhase({ name: 'scanning', status: 'detecting' })
    session.current = client.openProfileScan(
      { harness: scanHarness, ...(repoPath ? { repo_path: repoPath } : {}) },
      {
        onOutput: (line) => setLines((prev) => [...prev, line]),
        onStatus: (status) => setPhase({ name: 'scanning', status }),
        onResult: (recommendation) => {
          session.current = null
          // A proposal, not an action: the checklist is pre-filled and the
          // user still approves it.
          const next: Record<string, boolean> = {}
          const why: Record<string, string> = {}
          const cats: Record<string, string[]> = {}
          for (const entry of recommendation.harnesses) {
            // A recommendation for a harness with no preview, or a blocked
            // one, cannot be pushed: `selected` filters those out, so the
            // proposal is recorded as it arrived.
            next[entry.harness] = entry.import
            why[entry.harness] = entry.reason
            cats[entry.harness] = entry.categories
          }
          setChecked((prev) => ({ ...prev, ...next }))
          setReasons(why)
          setSuggested(cats)
          setPhase({ name: 'idle' })
        },
        onError: (detail, outputTail) => {
          session.current = null
          setPhase({ name: 'failed', detail, outputTail })
        },
      },
    )
  }

  const cancelScan = () => {
    session.current?.close()
    session.current = null
    setPhase({ name: 'idle' })
  }

  return (
    <section aria-label="Bring your configuration" className="space-y-3">
      <h2 className="text-sm font-medium">Bring your configuration</h2>
      <p className="text-sm text-muted-foreground">
        Your skills, custom commands, standing instructions, settings, MCP
        servers and plugins live on this machine. Aether can copy them to the
        server so an agent there runs with your configuration. Credential
        files are excluded before anything is read, a file over 1 MiB or
        past the 20 MiB a snapshot holds is left behind, and a push carries
        a harness's whole configuration minus what is listed as left out.
      </p>

      {served && looking === '' && !looked && (
        <div className="space-y-1">
          <Button size="sm" onClick={() => void runPreviews()}>
            {previewed.length > 0 ? 'Look again' : 'Look at what is here'}
          </Button>
          <p className="text-xs text-muted-foreground">
            Reads the file list under each agent's configuration directory
            on this machine and checks it for secrets. Nothing is uploaded,
            and nothing runs until you press it - a large configuration
            directory takes a while to read.
          </p>
        </div>
      )}
      {looking !== '' && (
        <div className="space-y-1">
          <p className="text-sm" role="status">
            Reading {friendly[looking] ?? looking}...
          </p>
          <Button size="sm" variant="outline" onClick={cancelPreviews}>
            Stop
          </Button>
        </div>
      )}
      {Object.entries(previewErrors).map(([name, detail]) => (
        <div key={name} className="space-y-1">
          <p className="text-xs text-state-failed">
            Reading the {friendly[name] ?? name} configuration failed:{' '}
            {detail}
          </p>
        </div>
      ))}

      {served && looked && scanHarness && phase.name === 'idle' && (
        <div className="space-y-1">
          <Button size="sm" variant="outline" onClick={startScan}>
            Ask an agent which configuration to bring
          </Button>
          <p className="text-xs text-muted-foreground">
            {friendly[scanHarness] ?? scanHarness} runs on this machine and
            proposes what is worth bringing. It sees paths and counts, never
            file contents. Nothing is copied until you approve the list.
          </p>
        </div>
      )}
      {phase.name === 'scanning' && (
        <div className="space-y-2">
          <p className="text-sm" role="status">
            {statusLine[phase.status]}
          </p>
          <details className="rounded-md border bg-card">
            <summary className="cursor-pointer px-3 py-2 text-sm">
              View process
            </summary>
            <pre className={pane}>{lines.join('\n')}</pre>
          </details>
          <Button size="sm" variant="outline" onClick={cancelScan}>
            Cancel
          </Button>
        </div>
      )}
      {phase.name === 'failed' && (
        <div className="space-y-2">
          <p className="text-sm">The agent did not finish.</p>
          <p className="text-xs text-state-failed">{phase.detail}</p>
          {phase.outputTail && (
            <details className="rounded-md border bg-card">
              <summary className="cursor-pointer px-3 py-2 text-sm">
                Last output
              </summary>
              <pre className={pane}>{phase.outputTail}</pre>
            </details>
          )}
          <p className="text-xs text-muted-foreground">
            Choose what to bring below instead, or skip this step.
          </p>
          <Button size="sm" variant="outline" onClick={startScan}>
            Try again
          </Button>
        </div>
      )}

      {!served && (
        <p className="text-sm text-muted-foreground">
          This gateway does not serve the profile verbs, so the import runs
          from a terminal with{' '}
          <span className="font-mono">aether profile push --agent claude</span>
          .
        </p>
      )}

      {served &&
        candidates.length > 0 &&
        looked &&
        Object.keys(previewErrors).length === 0 &&
        present.length === 0 && (
          <p className="text-sm text-muted-foreground">
            No agent configuration was found on this machine, so there is
            nothing to bring.
          </p>
        )}

      {present.length > 0 && (
        <>
          <ul className="divide-y rounded-md border">
            {present.map((preview) => (
              <ProfileRow
                key={preview.harness}
                preview={preview}
                status={statuses[preview.harness]}
                checked={checked[preview.harness] === true}
                reason={reasons[preview.harness]}
                suggested={suggested[preview.harness]}
                result={results[preview.harness]}
                failure={failures[preview.harness]}
                workspace={workspace}
                onToggle={(on) =>
                  setChecked((prev) => ({ ...prev, [preview.harness]: on }))
                }
              />
            ))}
          </ul>
          <Button
            size="sm"
            disabled={busy || selected.length === 0}
            onClick={() => void importSelected()}
          >
            Import selected
          </Button>
        </>
      )}
    </section>
  )
}

function ProfileRow({
  preview,
  status,
  checked,
  reason,
  suggested,
  result,
  failure,
  workspace,
  onToggle,
}: {
  preview: ProfilePreview
  status?: ProfileStatus
  checked: boolean
  reason?: string
  suggested?: string[]
  result?: ProfilePushResult
  failure?: string
  workspace: Workspace | null
  onToggle: (on: boolean) => void
}) {
  const label = friendly[preview.harness] ?? preview.harness
  const excluded = preview.excluded ?? []
  // excluded is capped by the gateway; excluded_total is exact.
  const excludedTotal = preview.excluded_total ?? excluded.length
  const secret = flagged(preview)
  const snapshot = status?.snapshot

  return (
    <li className="space-y-1 px-3 py-2 text-sm">
      <div className="flex items-start gap-3">
        {!preview.blocked && (
          <input
            type="checkbox"
            className="mt-1"
            aria-label={`Bring ${label} configuration`}
            checked={checked}
            onChange={(e) => onToggle(e.target.checked)}
          />
        )}
        <div className="min-w-0 flex-1 space-y-0.5">
          <p className="font-medium">{label}</p>
          <p className="text-xs text-muted-foreground">
            {previewSummary(preview)}
          </p>
          <p className="font-mono text-xs text-muted-foreground">
            {preview.root}
          </p>
          {snapshot && (
            <p className="text-xs text-muted-foreground">
              Already imported on{' '}
              {new Date(snapshot.created_at).toLocaleDateString()}. Importing
              again replaces it with what is on this machine now.
            </p>
          )}
          {reason && <p className="text-xs">{reason}</p>}
          {suggested && suggested.length > 0 && (
            <p className="text-xs text-muted-foreground">
              The agent pointed at {suggested.join(', ')}; a push carries the
              whole configuration.
            </p>
          )}
        </div>
      </div>
      {excludedTotal > 0 && (
        <details className="rounded-md border bg-card">
          <summary className="cursor-pointer px-3 py-2 text-xs">
            {`Left out of ${label}: ${excludedTotal} ${
              excludedTotal === 1 ? 'entry' : 'entries'
            }`}
          </summary>
          <ul className="space-y-1 border-t px-3 py-2 text-xs">
            {excluded.map((e) => (
              <li key={e.path}>
                <span className="font-mono">{e.path}</span>
                <span className="text-muted-foreground"> - {e.detail}</span>
              </li>
            ))}
            {/* The gateway caps the list it sends; the count above is
                exact, so say how many are not shown rather than implying
                the list is all of them. */}
            {excludedTotal > excluded.length && (
              <li className="text-muted-foreground">
                and {excludedTotal - excluded.length} more
              </li>
            )}
          </ul>
        </details>
      )}
      {preview.blocked && (
        <div className="space-y-1 rounded-md border bg-card p-3 text-xs">
          <p className="text-state-failed">
            {blockedLine(preview, secret)}
          </p>
          <p>
            The push is refused until that is fixed on this machine.
            {preview.blocked_reason === 'symlink'
              ? ' A link out of the profile root has no override: point it inside the directory, or drop it.'
              : ' Remove the secret, or push this harness from a terminal, where the override is attributable:'}
          </p>
          {/* Only a scanner finding has an override, so only a scanner
              finding gets the command. Offering it for a symlink escape
              would send the user to a flag that cannot help them. */}
          {preview.blocked_reason !== 'symlink' && (
            <pre className="overflow-x-auto rounded-md border bg-background px-2 py-1 font-mono">
              {`aether profile push --agent ${preview.harness} --allow-secret ${
                preview.blocked_path ?? secret?.path ?? '<file>'
              } --workspace ${workspace?.id ?? '<workspace-id>'}`}
            </pre>
          )}
        </div>
      )}
      {result && (
        <>
          <p className="text-xs">
            Imported {result.files} files, {formatBytes(result.bytes)}, as
            snapshot <span className="font-mono">{result.snapshot_id}</span>.
          </p>
          {/* The push succeeded without these, so this is the only place
              the user learns they are not on the server. */}
          {result.skipped && result.skipped.length > 0 && (
            <ul className="space-y-1 text-xs text-muted-foreground">
              {result.skipped.map((e) => (
                <li key={e.path}>
                  <span className="font-mono">{e.path}</span> was not sent -{' '}
                  {e.detail}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      {failure && <p className="text-xs text-state-failed">{failure}</p>}
    </li>
  )
}
