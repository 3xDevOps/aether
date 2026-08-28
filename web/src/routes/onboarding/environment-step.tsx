// The onboarding Environment step. An admin chooses between mirroring this
// machine into the workspace image and keeping the standard environment the
// workspace was created with. Mirror runs the chosen coding agent headless
// through the local gateway's /ws/envscan socket and hands the validated
// Dockerfile and manifest pair to the review gate through onReview; nothing
// builds without that review. Every failure path offers the keep card, so
// the wizard never dead-ends here. Non-admin members see only the keep
// path: saving an environment is an administrator method.

import { useEffect, useId, useRef, useState } from 'react'
import { message } from '@/components/palette/palette'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api, EnvScanSession } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type { EnvScanStatus, HarnessStatus, ManifestItem } from '@/lib/types'
import { useIsAdmin } from '@/store/hooks'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50'

const card =
  'flex cursor-pointer items-start gap-3 rounded-md border bg-card p-3 text-sm has-checked:border-ring'

const pane =
  'max-h-64 overflow-x-auto overflow-y-auto border-t px-3 py-2 font-mono text-xs whitespace-pre-wrap'

/** The setup-capable CLIs by the names people know them under. */
const friendly: Record<string, string> = {
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

/** What a finished scan hands onward: the validated pair plus the harness
 * that wrote it. The review gate consumes exactly this payload. */
export interface EnvScanReview {
  harness: string
  dockerfile: string
  manifest: ManifestItem[]
}

type Phase =
  | { name: 'choose' }
  | { name: 'scanning'; status: EnvScanStatus }
  | { name: 'failed'; detail: string; outputTail?: string }

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
  const [choice, setChoice] = useState<'mirror' | 'keep'>('mirror')
  const [harnesses, setHarnesses] = useState<HarnessStatus[] | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  const [harness, setHarness] = useState('')
  const [phase, setPhase] = useState<Phase>({ name: 'choose' })
  const [lines, setLines] = useState<string[]>([])
  const session = useRef<EnvScanSession | null>(null)
  const group = useId()

  useEffect(() => {
    if (!isAdmin) return
    client
      .envHarnesses()
      .then((list) => {
        setHarnesses(list)
        const first = list.find((h) => h.installed)
        if (first) setHarness((prev) => prev || first.name)
      })
      .catch((err) => {
        // The detection verb failing leaves the keep card as the way on.
        setHarnesses([])
        setListError(message(err))
      })
  }, [client, isAdmin])

  // Leaving the step closes the socket, which cancels the scan and its
  // process on the gateway.
  useEffect(() => () => session.current?.close(), [])

  const listLoading = useDelayed(
    isAdmin && choice === 'mirror' && harnesses === null && listError === null,
  )

  const start = () => {
    setLines([])
    setPhase({ name: 'scanning', status: 'detecting' })
    session.current = client.openEnvScan(
      { harness, mode: 'inventory' },
      {
        onOutput: (line) => setLines((prev) => [...prev, line]),
        onStatus: (status) => setPhase({ name: 'scanning', status }),
        onResult: (result) => {
          session.current = null
          onReview({
            harness,
            dockerfile: result.dockerfile,
            manifest: result.manifest,
          })
        },
        onError: (detail, outputTail) => {
          session.current = null
          setPhase({ name: 'failed', detail, outputTail })
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
    return (
      <section aria-label="Environment" className="space-y-3">
        <h2 className="text-sm font-medium">Scanning this machine</h2>
        <p className="text-sm" role="status">
          {statusLine[phase.status]}
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
          <Button size="sm" onClick={start}>
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
        Agents run in a remote environment. Mirror this machine so it has the
        same tools you use here, or keep the ready-made one.
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
      {choice === 'mirror' && (
        <>
          {listLoading && <Skeleton className="h-8 w-full" />}
          {listError && <p className="text-xs text-state-failed">{listError}</p>}
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
              <Button size="sm" disabled={!harness} onClick={start}>
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
