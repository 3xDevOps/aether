// The onboarding Environment step and what follows it. An admin chooses
// between mirroring this machine into the workspace image, letting the
// agent read the repository and build what the project needs, and keeping
// the standard environment the workspace was created with. The two agent
// paths run the chosen coding agent headless through the local gateway's
// /ws/envscan socket and hand the validated Dockerfile and manifest pair
// to the review gate (EnvironmentReview) through onReview; approve there
// saves the definition and starts the background build, and
// EnvironmentBanner follows that build wherever later steps and the run
// view render it. Nothing builds without the review. Every failure path
// offers the keep card, so the wizard never dead-ends here. Non-admin
// members see only the keep path: saving an environment is an
// administrator method.

import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { message } from '@/components/palette/palette'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { api, type Api, type EnvScanSession } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import { removeManifestItem } from '@/lib/manifest'
import type { EnvScanStatus, HarnessStatus, ManifestItem } from '@/lib/types'
import { useStore } from '@/store'
import { useIsAdmin } from '@/store/hooks'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

const card =
  'flex cursor-pointer items-start gap-3 rounded-md border bg-card p-3 text-sm has-checked:border-ring'

const pane =
  'max-h-64 overflow-x-auto overflow-y-auto border-t px-3 py-2 font-mono text-xs whitespace-pre-wrap'

/** The setup-capable CLIs by the names people know them under; the
 * workspace Environment panel reads it too. */
export const friendly: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
  pi: 'pi',
  amp: 'Amp',
}

/** One plain sentence per coarse scan status. */
const statusLine: Record<EnvScanStatus, string> = {
  detecting: 'Getting ready...',
  running: 'Looking at the tools on this machine...',
  validating: 'Checking what the agent found...',
  retrying: 'Fixing a problem with the result and trying once more...',
}

/** What a finished scan hands onward: the validated pair, the harness
 * that wrote it, and where it came from. The review gate consumes exactly
 * this payload: approval saves with the source, and refine runs for a
 * repo-sourced pair reuse the repository folder. */
export interface EnvScanReview {
  harness: string
  source: 'mirror' | 'repo'
  /** The repository folder a repo scan read; absent for mirror. */
  repoPath?: string
  dockerfile: string
  manifest: ManifestItem[]
}

/** The two scan starts: mirror reads this machine, repo reads a folder. */
type ScanMode = 'inventory' | 'repo'

type Phase =
  | { name: 'choose' }
  | { name: 'scanning'; mode: ScanMode; status: EnvScanStatus }
  | { name: 'failed'; mode: ScanMode; detail: string; outputTail?: string }

export function EnvironmentStep({
  client,
  onNext,
  onReview,
}: {
  client: Api
  /** Advances the wizard, keeping the environment the workspace was
   * created with. Every path here can reach it. */
  onNext: () => void
  /** Receives a successful scan's pair for review and approval. */
  onReview: (review: EnvScanReview) => void
}) {
  const isAdmin = useIsAdmin()
  const [choice, setChoice] = useState<'mirror' | 'repo' | 'keep'>('mirror')
  const [harnesses, setHarnesses] = useState<HarnessStatus[] | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  const [harness, setHarness] = useState('')
  const [repoPath, setRepoPath] = useState('')
  const [repoError, setRepoError] = useState<string | null>(null)
  const [phase, setPhase] = useState<Phase>({ name: 'choose' })
  const [lines, setLines] = useState<string[]>([])
  const session = useRef<EnvScanSession | null>(null)
  const group = useId()

  const load = useCallback(() => {
    // A failed listing keeps harnesses null: unknown is not "none found",
    // so the render shows the error with a retry, and the keep card stays
    // the way on.
    setListError(null)
    client
      .envHarnesses()
      .then((result) => {
        setHarnesses(result.harnesses)
        const first = result.harnesses.find((h) => h.installed)
        if (first) setHarness((prev) => prev || first.name)
        const suggested = result.repo_path
        if (suggested) setRepoPath((prev) => prev || suggested)
      })
      .catch((err) => setListError(message(err)))
  }, [client])

  useEffect(() => {
    if (isAdmin) load()
  }, [isAdmin, load])

  // Leaving the step closes the socket, which cancels the scan and its
  // process on the gateway.
  useEffect(() => () => session.current?.close(), [])

  const listLoading = useDelayed(
    isAdmin && choice !== 'keep' && harnesses === null && listError === null,
  )

  const start = (mode: ScanMode) => {
    const path = repoPath.trim()
    setLines([])
    setRepoError(null)
    setPhase({ name: 'scanning', mode, status: 'detecting' })
    // The engine checks the repo folder before launching the agent, so an
    // error that arrives before any post-launch status is a refusal of
    // the folder: it belongs inline next to the input, not on the
    // failure screen.
    let launched = false
    session.current = client.openEnvScan(
      { harness, mode, ...(mode === 'repo' ? { repo_path: path } : {}) },
      {
        onOutput: (line) => setLines((prev) => [...prev, line]),
        onStatus: (status) => {
          if (status !== 'detecting') launched = true
          setPhase({ name: 'scanning', mode, status })
        },
        onResult: (result) => {
          session.current = null
          onReview({
            harness,
            source: mode === 'repo' ? 'repo' : 'mirror',
            ...(mode === 'repo' ? { repoPath: path } : {}),
            dockerfile: result.dockerfile,
            manifest: result.manifest,
          })
        },
        onError: (detail, outputTail) => {
          session.current = null
          if (mode === 'repo' && !launched) {
            setRepoError(detail)
            setPhase({ name: 'choose' })
            return
          }
          setPhase({ name: 'failed', mode, detail, outputTail })
        },
      },
    )
  }

  const cancel = () => {
    session.current?.close()
    session.current = null
    setPhase({ name: 'choose' })
  }

  if (!isAdmin) {
    return (
      <section aria-label="Environment" className="space-y-3">
        <h2 className="text-sm font-medium">Workspace environment</h2>
        <p className="text-sm text-muted-foreground">
          The workspace already has a ready-to-use environment. Changing it
          is an administrator action, so continue with what is set up.
        </p>
        <Button size="sm" onClick={onNext}>
          Continue
        </Button>
      </section>
    )
  }

  if (phase.name === 'scanning') {
    // The repo scan reads the project, not this machine; the copy says so.
    const line =
      phase.mode === 'repo' && phase.status === 'running'
        ? 'Reading the project files...'
        : statusLine[phase.status]
    return (
      <section aria-label="Environment" className="space-y-3">
        <h2 className="text-sm font-medium">
          {phase.mode === 'repo'
            ? 'Reading the repository'
            : 'Scanning this machine'}
        </h2>
        <p className="text-sm" role="status">
          {line}
        </p>
        <details className="rounded-md border bg-card">
          <summary className="cursor-pointer px-3 py-2 text-sm">
            View process
          </summary>
          <pre className={pane}>{lines.join('\n')}</pre>
        </details>
        <Button size="sm" variant="outline" onClick={cancel}>
          Cancel
        </Button>
      </section>
    )
  }

  if (phase.name === 'failed') {
    return (
      <section aria-label="Environment" className="space-y-3">
        <h2 className="text-sm font-medium">The scan did not finish</h2>
        <p className="text-xs text-state-failed">{phase.detail}</p>
        {phase.outputTail && (
          <details className="rounded-md border bg-card">
            <summary className="cursor-pointer px-3 py-2 text-sm">
              Last output
            </summary>
            <pre className={pane}>{phase.outputTail}</pre>
          </details>
        )}
        <div className="flex gap-2">
          <Button size="sm" onClick={() => start(phase.mode)}>
            Try again
          </Button>
          <Button size="sm" variant="outline" onClick={onNext}>
            Keep the standard environment
          </Button>
        </div>
      </section>
    )
  }

  const installed = (harnesses ?? []).filter((h) => h.installed)

  return (
    <section aria-label="Environment" className="space-y-3">
      <h2 className="text-sm font-medium">Set up the workspace environment</h2>
      <p className="text-sm text-muted-foreground">
        Agents run in a remote environment. Mirror this machine, build it
        from the repository, or keep the ready-made one.
      </p>
      <fieldset className="space-y-2">
        <legend className="sr-only">Environment path</legend>
        <label className={card}>
          <input
            type="radio"
            name={group}
            className="mt-0.5"
            checked={choice === 'mirror'}
            onChange={() => setChoice('mirror')}
            aria-label="Mirror my machine"
          />
          <span className="min-w-0 flex-1 space-y-0.5">
            <span className="flex items-center gap-2">
              <span className="font-medium">Mirror my machine</span>
              <span className="rounded-full border px-1.5 py-px text-[10px] uppercase tracking-wide text-muted-foreground">
                Recommended
              </span>
            </span>
            <span className="block text-xs text-muted-foreground">
              A coding agent on this machine looks at the tools you use and
              proposes a matching remote environment. You review the list
              before anything is built.
            </span>
          </span>
        </label>
        <label className={card}>
          <input
            type="radio"
            name={group}
            className="mt-0.5"
            checked={choice === 'repo'}
            onChange={() => setChoice('repo')}
            aria-label="From the repository"
          />
          <span className="min-w-0 flex-1 space-y-0.5">
            <span className="block font-medium">From the repository</span>
            <span className="block text-xs text-muted-foreground">
              The agent reads the project's own files and builds what it
              needs.
            </span>
          </span>
        </label>
        <label className={card}>
          <input
            type="radio"
            name={group}
            className="mt-0.5"
            checked={choice === 'keep'}
            onChange={() => setChoice('keep')}
            aria-label="Keep the standard environment"
          />
          <span className="min-w-0 flex-1 space-y-0.5">
            <span className="block font-medium">
              Keep the standard environment
            </span>
            <span className="block text-xs text-muted-foreground">
              The workspace already has a ready-to-use environment; keep it
              and move on.
            </span>
          </span>
        </label>
      </fieldset>
      {choice !== 'keep' && (
        <>
          {listLoading && <Skeleton className="h-8 w-full" />}
          {listError && (
            <div className="space-y-2">
              <p className="text-xs text-state-failed">{listError}</p>
              <Button size="sm" variant="outline" onClick={load}>
                Retry
              </Button>
            </div>
          )}
          {harnesses !== null && installed.length === 0 && (
            <p className="text-sm text-muted-foreground">
              No supported coding agent was found on this machine. Install
              Claude Code, Codex, pi, or Amp and come back, or keep the
              standard environment to continue now.
            </p>
          )}
          {installed.length > 0 && (
            <>
              <label className="block space-y-1 text-sm">
                Coding agent
                <select
                  className={field}
                  value={harness}
                  onChange={(e) => setHarness(e.target.value)}
                >
                  {installed.map((h) => (
                    <option key={h.name} value={h.name}>
                      {friendly[h.name] ?? h.name}
                    </option>
                  ))}
                </select>
              </label>
              {choice === 'repo' && (
                <>
                  <label className="block space-y-1 text-sm">
                    Repository folder
                    <input
                      className={field}
                      value={repoPath}
                      placeholder="/path/to/your/project"
                      onChange={(e) => {
                        setRepoPath(e.target.value)
                        setRepoError(null)
                      }}
                    />
                  </label>
                  {repoError && (
                    <p className="text-xs text-state-failed">{repoError}</p>
                  )}
                </>
              )}
              <Button
                size="sm"
                disabled={
                  !harness || (choice === 'repo' && repoPath.trim() === '')
                }
                onClick={() => start(choice === 'repo' ? 'repo' : 'inventory')}
              >
                Start scan
              </Button>
            </>
          )}
        </>
      )}
      {choice === 'keep' && (
        <Button size="sm" onClick={onNext}>
          Continue
        </Button>
      )}
    </section>
  )
}

type ReviewPhase =
  | { name: 'review' }
  | { name: 'refining'; status: EnvScanStatus }
  | { name: 'refine-failed'; detail: string; outputTail?: string }

/**
 * The review gate: the scanned manifest as a readable list with per-item
 * remove toggles, a free-text change request that re-runs the agent in
 * refine mode, and the approve action - env.save then env.build - that
 * starts the background build and advances the wizard. Nothing builds
 * without this approval. `repair` mounts the gate mid-refine, seeded with
 * a build failure's detail: that is how "ask the agent to fix it" feeds
 * back into the same gate.
 */
export function EnvironmentReview({
  client,
  workspaceId,
  review,
  repair,
  onDone,
  onKeep,
}: {
  client: Api
  workspaceId?: string
  review: EnvScanReview
  /** A build failure's detail; when set, a refine run starts on mount. */
  repair?: string
  /** Called once approve has saved the definition and started the build. */
  onDone: () => void
  /** Keeps the standard environment instead; always reachable. */
  onKeep: () => void
}) {
  const startEnvBuild = useStore((s) => s.startEnvBuild)
  const clearEnvBuild = useStore((s) => s.clearEnvBuild)
  const [pair, setPair] = useState(review)
  const [removed, setRemoved] = useState<string[]>([])
  const [feedback, setFeedback] = useState('')
  const [phase, setPhase] = useState<ReviewPhase>({ name: 'review' })
  const [lines, setLines] = useState<string[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const session = useRef<EnvScanSession | null>(null)

  useEffect(() => () => session.current?.close(), [])

  // The pair with the removed rows' Dockerfile lines dropped and later
  // spans shifted: what approve ships and what a refine run starts from.
  const edited = useMemo(() => {
    let dockerfile = pair.dockerfile
    let items = pair.manifest
    for (const name of removed) {
      const next = removeManifestItem(dockerfile, items, name)
      if (next === null) break
      dockerfile = next.dockerfile
      items = next.items
    }
    return { dockerfile, items }
  }, [pair, removed])

  const refine = useCallback(
    (note: string) => {
      setLines([])
      setPhase({ name: 'refining', status: 'detecting' })
      session.current = client.openEnvScan(
        {
          harness: pair.harness,
          mode: 'refine',
          // A repo-sourced pair keeps its anchor: the refine run reads
          // the same repository the original scan did.
          ...(pair.repoPath ? { repo_path: pair.repoPath } : {}),
          previous_dockerfile: edited.dockerfile,
          previous_manifest_json: JSON.stringify(edited.items),
          feedback: note,
        },
        {
          onOutput: (line) => setLines((prev) => [...prev, line]),
          onStatus: (status) => setPhase({ name: 'refining', status }),
          onResult: (result) => {
            session.current = null
            setPair({
              ...pair,
              dockerfile: result.dockerfile,
              manifest: result.manifest,
            })
            setRemoved([])
            setFeedback('')
            setPhase({ name: 'review' })
          },
          onError: (detail, outputTail) => {
            session.current = null
            setPhase({ name: 'refine-failed', detail, outputTail })
          },
        },
      )
    },
    [client, pair, edited],
  )

  // A repair mount goes straight into the refine run with the failure
  // detail; the result lands in the same review below.
  const startedRepair = useRef(false)
  useEffect(() => {
    if (!repair || startedRepair.current) return
    startedRepair.current = true
    refine(repair)
  }, [repair, refine])

  const approve = async () => {
    setBusy(true)
    setError(null)
    try {
      if (!workspaceId) throw new Error('no workspace selected; go back a step')
      const ws = { id: workspaceId }
      const version = await client.envSave({
        workspace: ws,
        dockerfile: edited.dockerfile,
        manifest: edited.items,
        source: pair.source,
        harness: pair.harness,
      })
      // The events socket is live app-wide already; priming the slice
      // before the build call means no frame can beat the banner. The
      // pair rides along so a verification failure can seed its repair.
      startEnvBuild(workspaceId, {
        version,
        status: 'building',
        source: pair.source,
        repoPath: pair.repoPath,
        harness: pair.harness,
        dockerfile: edited.dockerfile,
        manifest: edited.items,
      })
      try {
        await client.envBuild(ws, version)
      } catch (err) {
        clearEnvBuild(workspaceId)
        throw err
      }
      onDone()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const toggle = (name: string) =>
    setRemoved((prev) =>
      prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name],
    )

  if (phase.name === 'refining') {
    return (
      <section aria-label="Environment review" className="space-y-3">
        <h2 className="text-sm font-medium">Updating the proposal</h2>
        <p className="text-sm" role="status">
          {statusLine[phase.status]}
        </p>
        <details className="rounded-md border bg-card">
          <summary className="cursor-pointer px-3 py-2 text-sm">
            View process
          </summary>
          <pre className={pane}>{lines.join('\n')}</pre>
        </details>
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            session.current?.close()
            session.current = null
            setPhase({ name: 'review' })
          }}
        >
          Cancel
        </Button>
      </section>
    )
  }

  if (phase.name === 'refine-failed') {
    return (
      <section aria-label="Environment review" className="space-y-3">
        <h2 className="text-sm font-medium">The update did not finish</h2>
        <p className="text-xs text-state-failed">{phase.detail}</p>
        {phase.outputTail && (
          <details className="rounded-md border bg-card">
            <summary className="cursor-pointer px-3 py-2 text-sm">
              Last output
            </summary>
            <pre className={pane}>{phase.outputTail}</pre>
          </details>
        )}
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={() => setPhase({ name: 'review' })}>
            Back to the review
          </Button>
          <Button size="sm" variant="outline" onClick={onKeep}>
            Keep the standard environment
          </Button>
        </div>
      </section>
    )
  }

  return (
    <section aria-label="Environment review" className="space-y-3">
      <h2 className="text-sm font-medium">Review the proposed environment</h2>
      <p className="text-sm text-muted-foreground">
        The agent found these tools. Remove anything you do not want, then
        approve to build the remote environment.
      </p>
      <ul className="divide-y rounded-md border">
        {pair.manifest.map((item) => {
          const isRemoved = removed.includes(item.name)
          return (
            <li
              key={item.name}
              className="flex items-start gap-3 px-3 py-2 text-sm"
            >
              <span
                className={`min-w-0 flex-1 space-y-0.5 ${isRemoved ? 'opacity-50' : ''}`}
              >
                <span className="flex items-baseline gap-2">
                  <span
                    className={`font-medium ${isRemoved ? 'line-through' : ''}`}
                  >
                    {item.name}
                  </span>
                  <span className="font-mono text-xs text-muted-foreground">
                    {item.version}
                  </span>
                </span>
                {item.reason && (
                  <span className="block text-xs text-muted-foreground">
                    {item.reason}
                  </span>
                )}
              </span>
              <Button
                size="sm"
                variant="outline"
                aria-label={`${isRemoved ? 'Put back' : 'Remove'} ${item.name}`}
                disabled={!isRemoved && edited.items.length === 1}
                onClick={() => toggle(item.name)}
              >
                {isRemoved ? 'Put back' : 'Remove'}
              </Button>
            </li>
          )
        })}
      </ul>
      <label className="block space-y-1 text-sm">
        Request changes
        <textarea
          className={`${field} min-h-16`}
          value={feedback}
          placeholder="use node 20 instead of 22"
          onChange={(e) => setFeedback(e.target.value)}
        />
      </label>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" disabled={busy} onClick={() => void approve()}>
          Approve and build
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={busy || feedback.trim() === ''}
          onClick={() => refine(feedback.trim())}
        >
          Send to the agent
        </Button>
        <Button size="sm" variant="outline" disabled={busy} onClick={onKeep}>
          Keep the standard environment
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        The build runs in the background; you keep going while it finishes.
      </p>
      {error && <p className="text-xs text-state-failed">{error}</p>}
    </section>
  )
}

/** The statuses during which the workspace still runs on its creation
 * image. `active` and a cleared entry both mean no banner. */
const buildPending: Record<string, true> = {
  saved: true,
  building: true,
  verifying: true,
}

/**
 * The environment build banner the First-run step and the run view share.
 * Renders nothing until this session has a build to report. While the
 * latest build for the workspace is pending it says the starter image is
 * in use; a verification failure offers the repair scan (when this
 * session still holds the approved pair) and the standard fallback, which
 * simply forgets the build - the workspace image already is the fallback.
 */
export function EnvironmentBanner({
  workspaceId,
  client = api,
}: {
  workspaceId?: string
  client?: Api
}) {
  const build = useStore((s) =>
    workspaceId ? s.envBuilds[workspaceId] : undefined,
  )
  const clearEnvBuild = useStore((s) => s.clearEnvBuild)
  const [repairing, setRepairing] = useState(false)

  if (!workspaceId || !build) return null

  if (buildPending[build.status]) {
    return (
      <p
        role="status"
        className="rounded-md border bg-card px-3 py-2 text-sm text-muted-foreground"
      >
        Your environment is still building, using the starter image for now.
      </p>
    )
  }
  if (build.status !== 'failed') return null

  const detail = build.detail || 'the built environment did not pass its checks'

  if (repairing && build.harness && build.dockerfile && build.manifest) {
    return (
      <div className="rounded-md border bg-card p-3">
        <EnvironmentReview
          client={client}
          workspaceId={workspaceId}
          review={{
            harness: build.harness,
            // Builds seen only through events carry no source; mirror is
            // the safe default for their repair saves.
            source: build.source ?? 'mirror',
            repoPath: build.repoPath,
            dockerfile: build.dockerfile,
            manifest: build.manifest,
          }}
          repair={detail}
          onDone={() => setRepairing(false)}
          onKeep={() => {
            setRepairing(false)
            clearEnvBuild(workspaceId)
          }}
        />
      </div>
    )
  }

  return (
    <div className="space-y-2 rounded-md border bg-card p-3 text-sm">
      <p>
        The environment build did not succeed, so runs use the starter image.
      </p>
      <p className="text-xs text-state-failed">{detail}</p>
      <div className="flex flex-wrap gap-2">
        {build.harness && build.dockerfile && build.manifest && (
          <Button size="sm" onClick={() => setRepairing(true)}>
            Ask the agent to fix it
          </Button>
        )}
        <Button
          size="sm"
          variant="outline"
          onClick={() => clearEnvBuild(workspaceId)}
        >
          Keep the standard environment
        </Button>
      </div>
    </div>
  )
}
