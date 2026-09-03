// The onboarding steps. Each step talks to the gateway through the injected
// Api client and reports completion to the wizard; nothing here persists.
// Re-entering the wizard re-checks reality instead of trusting stale state,
// and every server refusal is rendered verbatim.

import { useCallback, useEffect, useRef, useState } from 'react'
import {
  EnvironmentChoice,
  type EnvironmentValue,
} from '@/components/environment-choice'
import { message } from '@/components/palette/palette'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type {
  AgentInfo,
  LinkRepoResult,
  LinkStatus,
  RepoPushResult,
  Workspace,
} from '@/lib/types'
import { EnvironmentBanner } from '@/routes/onboarding/environment-step'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

// Raw git output: scrollable, wrapped, never truncated.
const pane =
  'max-h-64 overflow-x-auto overflow-y-auto px-3 py-2 font-mono text-xs whitespace-pre-wrap break-words'

/**
 * Step 1: is this machine linked? Checked on every mount - the user may have
 * run `aether link` in a terminal since the last look - and mirrored into the
 * store so the status bar agrees with the wizard.
 */
export function LinkStep({ client, onNext }: { client: Api; onNext: () => void }) {
  const setLinkStatus = useStore((s) => s.setLinkStatus)
  const [status, setStatus] = useState<LinkStatus | null>(null)
  const [error, setError] = useState<string | null>(null)

  const check = useCallback(async () => {
    setError(null)
    try {
      const next = await client.localLinkStatus()
      setStatus(next)
      setLinkStatus(next)
    } catch (err) {
      setError(message(err))
    }
  }, [client, setLinkStatus])

  useEffect(() => {
    void check()
  }, [check])

  const loading = useDelayed(status === null && error === null)

  return (
    <section aria-label="Link" className="space-y-3">
      <h2 className="text-sm font-medium">Link to your server</h2>
      {loading && <Skeleton className="h-16 w-full" />}
      {error && <p className="text-xs text-state-failed">{error}</p>}
      {status?.linked && (
        <>
          <p className="text-sm">
            Linked to <span className="font-mono">{status.addr}</span> as{' '}
            <span className="font-medium">{status.user}</span>.
          </p>
          <Button size="sm" onClick={onNext}>
            Continue
          </Button>
        </>
      )}
      {status && !status.linked && (
        <div className="space-y-2 text-sm">
          <p>
            This machine is not linked yet. The gateway needs an SSH identity
            before the GUI can talk to a server, so the first link happens in a
            terminal:
          </p>
          <pre className="rounded-md border bg-card px-3 py-2 font-mono text-xs">
            aether link &lt;server-host&gt;:2222
          </pre>
          <p className="text-muted-foreground">
            The first identity to link a fresh server becomes the admin. Once
            the command reports success, retry here.
          </p>
          <Button size="sm" variant="outline" onClick={() => void check()}>
            Retry
          </Button>
        </div>
      )}
    </section>
  )
}

/**
 * Step 2: pick the workspace runs will live in. With none on the server and
 * the add capability present, creation is inline; the form mirrors
 * protocol.WorkspaceAddParams, with the environment settled by the shared
 * EnvironmentChoice cards (standard image by default). The base branch is
 * the ref every run in the workspace forks from, so it is settled here
 * rather than per run.
 */
export function WorkspaceStep({
  client,
  caps,
  onNext,
}: {
  client: Api
  caps: Capability
  onNext: (workspace: Workspace) => void
}) {
  const [workspaces, setWorkspaces] = useState<Workspace[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [baseBranch, setBaseBranch] = useState('main')
  const [environment, setEnvironment] = useState<EnvironmentValue | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    client
      .workspaceListFull()
      .then(setWorkspaces)
      .catch((err) => setError(message(err)))
  }, [client])

  const loading = useDelayed(workspaces === null && error === null)

  const create = async () => {
    setBusy(true)
    setError(null)
    try {
      if (!environment) throw new Error('choose an environment first')
      onNext(
        await client.workspaceAdd({
          name: name.trim(),
          base_branch: baseBranch.trim(),
          environment,
        }),
      )
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section aria-label="Workspace" className="space-y-3">
      <h2 className="text-sm font-medium">Choose a workspace</h2>
      {loading && <Skeleton className="h-16 w-full" />}
      {error && <p className="text-xs text-state-failed">{error}</p>}
      {workspaces && workspaces.length > 0 && (
        <ul className="divide-y rounded-md border">
          {workspaces.map((w) => (
            <li key={w.id} className="flex items-center gap-2 px-3 py-2 text-sm">
              <span className="min-w-0 flex-1 truncate">{w.name}</span>
              <span className="font-mono text-xs text-muted-foreground">
                {w.base_branch}
              </span>
              <Button
                size="sm"
                variant="outline"
                aria-label={`Use ${w.name}`}
                onClick={() => onNext(w)}
              >
                Use
              </Button>
            </li>
          ))}
        </ul>
      )}
      {workspaces?.length === 0 &&
        (caps.hasMethod('workspace.add') ? (
          <form
            className="space-y-3"
            aria-label="Create workspace"
            onSubmit={(e) => {
              e.preventDefault()
              void create()
            }}
          >
            <p className="text-sm text-muted-foreground">
              No workspaces yet - create the first one. A workspace is a repo
              plus a server-owned environment plan.
            </p>
            <label className="block space-y-1 text-sm">
              Name
              <input
                className={field}
                value={name}
                placeholder="myproject"
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label className="block space-y-1 text-sm">
              Base branch
              <input
                className={field}
                value={baseBranch}
                onChange={(e) => setBaseBranch(e.target.value)}
              />
            </label>
            <EnvironmentChoice onChange={setEnvironment} />
            <Button
              type="submit"
              size="sm"
              disabled={busy || !name.trim() || !baseBranch.trim() || !environment}
            >
              Create workspace
            </Button>
          </form>
        ) : (
          <p className="text-sm text-muted-foreground">
            No workspaces yet, and workspace creation is an administrator
            operation this membership does not have. Ask an admin to run
            workspace init, then come back.
          </p>
        ))}
    </section>
  )
}

/**
 * Step 3: point a local clone at the workspace. The gateway adds the
 * `aether` git remote and, where the repo.push verb is served, runs the
 * first push from here, keeping git's own answer on the page; without the
 * verb the push stays a copy-paste command. Either way the history is the
 * user's: nothing rewrites it.
 */
export function RepoStep({
  client,
  caps,
  workspace,
  onNext,
}: {
  client: Api
  caps: Capability
  workspace: Workspace | null
  onNext: () => void
}) {
  const [repo, setRepo] = useState('')
  const [result, setResult] = useState<LinkRepoResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // The push runs on its own flag: a failed push must not disable the link
  // form the user may want to correct, and the reverse.
  const [pushing, setPushing] = useState(false)
  const [pushed, setPushed] = useState<RepoPushResult | null>(null)
  const [pushError, setPushError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const cmdRef = useRef<HTMLInputElement>(null)

  // Every run forks from the workspace's base branch, so that is the branch
  // to seed - not always `main`.
  const branch = workspace?.base_branch ?? 'main'
  const pushCmd = `git push -u aether ${branch}`
  const canPush = caps.hasLocal('repo.push')
  const absolute = repo.trim().startsWith('/')

  const link = async () => {
    setBusy(true)
    setError(null)
    try {
      setResult(await client.localLinkRepo(repo.trim(), workspace?.id))
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const push = async () => {
    setPushing(true)
    setPushError(null)
    try {
      // The step stays put on success so git's own answer is readable:
      // "Everything up-to-date" and "[new branch]" mean different things,
      // and only git can tell them apart.
      setPushed(await client.localRepoPush(workspace?.id))
    } catch (err) {
      setPushError(message(err))
    } finally {
      setPushing(false)
    }
  }

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(pushCmd)
      setCopied(true)
    } catch {
      cmdRef.current?.focus()
      cmdRef.current?.select()
    }
  }

  return (
    <section aria-label="Repository" className="space-y-3">
      <h2 className="text-sm font-medium">Connect your repository</h2>
      <p className="text-sm text-muted-foreground">
        The gateway adds an <span className="font-mono">aether</span> git
        remote to a clone on this machine.{' '}
        {canPush
          ? 'Aether can then push your base branch for you - the history stays yours.'
          : 'Pushing stays manual - the history is yours.'}
      </p>
      <form
        className="flex items-end gap-3"
        aria-label="Link repository"
        onSubmit={(e) => {
          e.preventDefault()
          void link()
        }}
      >
        <label className="flex-1 space-y-1 text-sm">
          Repository path
          <input
            className={field}
            value={repo}
            placeholder="/home/you/code/myproject"
            onChange={(e) => setRepo(e.target.value)}
          />
        </label>
        <Button type="submit" size="sm" disabled={busy || !absolute}>
          Add remote
        </Button>
      </form>
      {repo.trim() !== '' && !absolute && (
        <p className="text-xs text-muted-foreground">
          The path must be absolute.
        </p>
      )}
      {error && <p className="text-xs text-state-failed">{error}</p>}
      {result && (
        <div className="space-y-2">
          <p className="text-sm">
            Remote <span className="font-mono">{result.remote}</span> added,
            pointing at <span className="font-mono">{result.url}</span>.{' '}
            {pushed ? (
              <>
                Pushed <span className="font-mono">{pushed.branch}</span> to{' '}
                <span className="font-mono">{pushed.remote}</span>.
              </>
            ) : (
              <>
                Seed the workspace with{' '}
                <span className="font-mono">{branch}</span>:
              </>
            )}
          </p>
          {pushed ? (
            // Git's own answer, open: "Everything up-to-date" and "[new
            // branch]" both mean success and say different things, and the
            // reader who needs that distinction is the one who would not
            // know to go looking for it.
            <details open className="rounded-md border bg-card">
              <summary className="cursor-pointer px-3 py-2 text-sm">
                What git did
              </summary>
              <pre className={pane}>
                {pushed.output.trim() || 'git printed nothing.'}
              </pre>
            </details>
          ) : (
            <>
              {canPush && (
                <>
                  <Button
                    size="sm"
                    disabled={pushing}
                    onClick={() => void push()}
                  >
                    {pushing ? 'Pushing...' : 'Push now'}
                  </Button>
                  {pushError && (
                    <div className="space-y-1">
                      <p className="text-xs text-state-failed">
                        The push failed. Git said:
                      </p>
                      <pre className={`rounded-md border bg-card ${pane}`}>
                        {pushError}
                      </pre>
                    </div>
                  )}
                  <p className="text-xs text-muted-foreground">
                    or run it yourself:
                  </p>
                </>
              )}
              <div className="flex gap-2">
                <input
                  ref={cmdRef}
                  readOnly
                  aria-label="Push command"
                  className="w-full rounded-md border bg-background px-2 py-1 font-mono text-sm"
                  value={pushCmd}
                  onFocus={(e) => e.target.select()}
                />
                <Button variant="outline" size="sm" onClick={() => void copy()}>
                  {copied ? 'Copied' : 'Copy'}
                </Button>
              </div>
            </>
          )}
          <Button
            size="sm"
            variant={canPush && !pushed ? 'outline' : 'default'}
            onClick={onNext}
          >
            Continue
          </Button>
        </div>
      )}
    </section>
  )
}

/**
 * Step 4: the first run, in the workspace step 2 settled on. The harness
 * comes from agent.list with a free-text fallback, and launch lands the user
 * on the run view.
 */
export function FirstRunStep({
  client,
  workspace,
}: {
  client: Api
  workspace: Workspace | null
}) {
  const navigate = useStore((s) => s.navigate)
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [harness, setHarness] = useState('')
  const [custom, setCustom] = useState('')
  const [task, setTask] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    // agent.list failing is not fatal: the harness field falls back to text.
    client
      .agentList()
      .then(setAgents)
      .catch(() => setAgents([]))
  }, [client])

  const freeText = agents !== null && agents.length === 0
  const chosen = freeText || harness === '__custom' ? custom.trim() : harness
  const ready = task.trim() !== '' && chosen !== '' && workspace !== null

  const launch = async () => {
    setBusy(true)
    setError(null)
    try {
      if (!workspace) throw new Error('no workspace selected; go back a step')
      const run = await client.runLaunch({
        workspace_id: workspace.id,
        task: task.trim(),
        harness: chosen,
      })
      navigate('run', { runId: run.id })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section aria-label="First run" className="space-y-3">
      <h2 className="text-sm font-medium">Launch your first run</h2>
      {/* A mirror build approved two steps back may still be running; the
          banner says the starter image is in use so a missing toolchain
          reads as expected, not broken. */}
      <EnvironmentBanner client={client} workspaceId={workspace?.id} />
      <p className="text-sm text-muted-foreground">
        The run forks from{' '}
        <span className="font-mono">{workspace?.base_branch ?? 'the base branch'}</span>{' '}
        in {workspace?.name ?? 'your workspace'}.
      </p>
      {freeText ? (
        <label className="block space-y-1 text-sm">
          Harness
          <input
            className={field}
            value={custom}
            placeholder="claude"
            onChange={(e) => setCustom(e.target.value)}
          />
        </label>
      ) : (
        <>
          <label className="block space-y-1 text-sm">
            Harness
            <select
              className={field}
              value={harness}
              onChange={(e) => setHarness(e.target.value)}
            >
              <option value="">Choose a harness</option>
              {(agents ?? []).map((a) => (
                <option key={a.name} value={a.name}>
                  {a.name}
                </option>
              ))}
              <option value="__custom">Other...</option>
            </select>
          </label>
          {harness === '__custom' && (
            <label className="block space-y-1 text-sm">
              Harness name
              <input
                className={field}
                value={custom}
                onChange={(e) => setCustom(e.target.value)}
              />
            </label>
          )}
        </>
      )}
      <label className="block space-y-1 text-sm">
        Task
        <textarea
          className={`${field} min-h-20`}
          value={task}
          placeholder="add a health check endpoint"
          onChange={(e) => setTask(e.target.value)}
        />
      </label>
      {error && <p className="text-xs text-state-failed">{error}</p>}
      <Button size="sm" disabled={busy || !ready} onClick={() => void launch()}>
        Launch
      </Button>
      <div className="space-y-1 rounded-md border bg-card p-3 text-xs text-muted-foreground">
        <p className="font-medium text-foreground">No agent subscription yet?</p>
        <p>
          Aether ships a deterministic fake harness that runs a script from
          your repo instead of an agent. Start the server with
          AETHER_FAKE_AGENT="sh /workspace/agent.sh" in its environment, commit
          an agent.sh that writes a file, and launch with harness{' '}
          <span className="font-mono">fake</span>: the whole path - container,
          worktree, PTY, commit, fetch - runs with nothing mocked but the
          agent.
        </p>
      </div>
    </section>
  )
}
