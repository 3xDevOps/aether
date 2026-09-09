// The onboarding steps. Each step talks to the gateway through the injected
// Api client and reports completion to the wizard. Step and workspace choices
// are stored in the UI slice, while link status is checked against the local
// gateway whenever this route is entered or refocused.

import { type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type {
  AgentInfo,
  LinkApplyResult,
  LinkStatus,
  Workspace,
} from '@/lib/types'
import { cn, field, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import { onboardingStepIndex } from '@/store/ui'
import type { Capability } from '@/store/hooks'
import type { OnboardingRepo } from '@/store/ui'

/**
 * The row a step ends with, Back included. It sticks to the bottom of the
 * wizard's scroller because the step above it can be taller than the window
 * - Agents with the terminal dock open is - and a control that scrolls out
 * of reach is the reason Back moved here.
 */
export const actionRow =
  'sticky bottom-0 z-10 -mx-4 -mb-4 flex gap-2 border-t bg-background px-4 pb-7 pt-3'

// Raw command output - git's, and gh's on the Connect GitHub screen:
// scrollable, wrapped, never truncated.
export const pane =
  'max-h-64 overflow-x-auto overflow-y-auto px-3 py-2 font-mono text-xs whitespace-pre-wrap break-words'

const short = (commit: string) => commit.slice(0, 7)
const commits = (n: number) => `${n} commit${n === 1 ? '' : 's'}`

/**
 * The Link step: link this machine to a server. The gateway's local link
 * status determines whether the in-app link form or the linked summary is
 * shown.
 */
export function LinkStep({
  client,
  onNext,
}: {
  client: Api
  onNext: (step: number) => void
}) {
  const setLinkStatus = useStore((s) => s.setLinkStatus)
  const [status, setStatus] = useState<LinkStatus | null>(null)
  const [statusError, setStatusError] = useState<string | null>(null)
  const [linkError, setLinkError] = useState<string | null>(null)
  const [address, setAddress] = useState('')
  const [invite, setInvite] = useState('')
  const [name, setName] = useState('')
  const [linking, setLinking] = useState(false)
  const [success, setSuccess] = useState<LinkApplyResult | null>(null)

  const check = useCallback(async () => {
    setStatusError(null)
    try {
      const next = await client.localLinkStatus()
      setStatus(next)
      setLinkStatus(next)
    } catch (err) {
      setStatusError(message(err))
    }
  }, [client, setLinkStatus])

  useEffect(() => {
    void check()
    const onFocus = () => void check()
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [check])

  const link = async () => {
    setLinking(true)
    setLinkError(null)
    try {
      const next = await client.localLinkApply({
        addr: address.trim(),
        ...(invite.trim() ? { invite: invite.trim() } : {}),
        ...(name.trim() ? { name: name.trim() } : {}),
      })
      setSuccess(next)
      useStore.getState().reconnect()
      await check()
    } catch (err) {
      setLinkError(message(err))
    } finally {
      setLinking(false)
    }
  }

  const loading = useDelayed(status === null && statusError === null)
  const serverConfigured = status?.server_configured === true

  return (
    <section aria-label="Link" className="space-y-3">
      <h2 className="text-sm font-medium">Link to your server</h2>
      {loading && <Skeleton className="h-16 w-full" />}
      {statusError && <p className="text-xs text-state-failed">{statusError}</p>}
      {statusError && (
        <Button size="sm" variant="outline" onClick={() => void check()}>
          Retry
        </Button>
      )}
      {success && (
        <div className="space-y-2 text-sm">
          <p>
            Linked to <span className="font-mono">{success.addr}</span> as{' '}
            <span className="font-medium">{success.member.display_name}</span> (
            {success.member.role}).
          </p>
          {success.key_generated && (
            <p>
              Created SSH key <span className="font-mono">{success.key_generated}</span>.
            </p>
          )}
          <Button
            size="sm"
            onClick={() => onNext(onboardingStepIndex('Git identity'))}
          >
            Continue
          </Button>
        </div>
      )}
      {status && !serverConfigured && !success && (
        <form
          className="space-y-3 text-sm"
          aria-label="Link server"
          onSubmit={(e) => {
            e.preventDefault()
            void link()
          }}
        >
          <label className="block space-y-1">
            Server address
            <input
              className={field}
              required
              placeholder="server-host:2222"
              value={address}
              disabled={linking}
              onChange={(e) => setAddress(e.target.value)}
            />
          </label>
          <label className="block space-y-1">
            Invite code
            <input
              className={field}
              value={invite}
              disabled={linking}
              onChange={(e) => setInvite(e.target.value)}
            />
          </label>
          <p className="text-xs text-muted-foreground">
            Leave empty on a fresh server, where the first identity to link
            becomes the admin, or on a tailnet server.
          </p>
          <label className="block space-y-1">
            Your name
            <input
              className={field}
              value={name}
              disabled={linking}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <Button type="submit" size="sm" disabled={linking || !address.trim()}>
            {linking ? 'Linking...' : 'Link'}
          </Button>
          {linkError && <p className="text-xs text-state-failed">{linkError}</p>}
        </form>
      )}
      {status && serverConfigured && !status.linked && !success && (
        <div className="space-y-2 text-sm">
          <p>
            Connected to <span className="font-mono">{status.addr}</span> as{' '}
            <span className="font-medium">{status.user}</span>.
          </p>
          <p className="text-muted-foreground">
            No repository is linked yet. Continue to connect one.
          </p>
          <Button
            size="sm"
            onClick={() => onNext(onboardingStepIndex('Git identity'))}
          >
            Continue
          </Button>
        </div>
      )}
      {status && serverConfigured && status.linked && !success && (
        <>
          <p className="text-sm">
            Linked to <span className="font-mono">{status.addr}</span> as{' '}
            <span className="font-medium">{status.user}</span>, with{' '}
            <span className="font-mono">{status.repo}</span>.
          </p>
          <Button
            size="sm"
            onClick={() => onNext(onboardingStepIndex('Git identity'))}
          >
            Continue
          </Button>
        </>
      )}
    </section>
  )
}

/**
 * The Workspace step: pick the workspace runs will live in. With none on the
 * server and the add capability present, creation is inline. The base branch
 * is the ref every run in the workspace forks from, so it is settled here
 * rather than per run.
 */
export function WorkspaceStep({
  client,
  caps,
  back,
  onNext,
}: {
  client: Api
  caps: Capability
  back?: ReactNode
  onNext: (workspace: Workspace) => void
}) {
  const [workspaces, setWorkspaces] = useState<Workspace[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [baseBranch, setBaseBranch] = useState('main')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    client
      .workspaceListFull()
      .then(setWorkspaces)
      .catch((err) => setError(message(err)))
  }, [client])

  const loading = useDelayed(workspaces === null && error === null)
  // The one state with an action row of its own for Back to join.
  const creating = workspaces?.length === 0 && caps.hasMethod('workspace.add')

  const create = async () => {
    setBusy(true)
    setError(null)
    try {
      onNext(
        await client.workspaceAdd({
          name: name.trim(),
          base_branch: baseBranch.trim(),
          environment: {},
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
        (creating ? (
          <form
            className="space-y-3"
            aria-label="Create workspace"
            onSubmit={(e) => {
              e.preventDefault()
              void create()
            }}
          >
            <p className="text-sm text-muted-foreground">
              No workspaces yet - create the first one. A workspace is one
              repository plus the container the server builds for it, and
              every run in it starts from that same setup.
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
            <div className={actionRow}>
              <Button
                type="submit"
                size="sm"
                disabled={busy || !name.trim() || !baseBranch.trim()}
              >
                Create workspace
              </Button>
              {back}
            </div>
          </form>
        ) : (
          <p className="text-sm text-muted-foreground">
            No workspaces yet, and workspace creation is an administrator
            operation this membership does not have. Ask an admin to run
            workspace init, then come back.
          </p>
        ))}
      {back && !creating && <div className={actionRow}>{back}</div>}
    </section>
  )
}

/**
 * The record to merge an in-flight push or fast-forward answer into: the one
 * as it stands now, so a request started from the same screen sees the
 * other's answer, but only while it is still the connection the request was
 * issued against. Re-pointing, or a change of workspace, leaves that answer
 * - and its error - belonging to a connection the step has left. It compares
 * the link id rather than the path, because reconnecting the same folder to
 * the same workspace is a new connection whose remote was written again.
 */
const stillLinked = (origin: OnboardingRepo) => {
  const current = useStore.getState().onboardingRepo
  return current && current.link === origin.link ? current : null
}

/**
 * The Repository step: point a local clone at the workspace. The gateway
 * adds the `aether` git remote and, where the repo.push verb is served,
 * compares the clone with the workspace and pushes only when the clone is
 * ahead, keeping git's own answer on the page; without the verb the push
 * stays a copy-paste command. A workspace that is ahead is answered by a
 * fast-forward of the clone. Either way the history is the user's: nothing
 * rewrites it, and a divergence is resolved by hand.
 */
export function RepoStep({
  client,
  caps,
  workspace,
  back,
  onNext,
}: {
  client: Api
  caps: Capability
  workspace: Workspace | null
  back?: ReactNode
  onNext: () => void
}) {
  // What the step settled lives in the UI slice, not here: walking back to
  // this step must not ask for a path that is already connected. A remote
  // written for another workspace is not this step's answer, so a workspace
  // the user changed their mind about leaves the form empty again.
  const remembered = useStore((s) => s.onboardingRepo)
  const setConnected = useStore((s) => s.setOnboardingRepo)
  const connected =
    remembered && remembered.workspace === (workspace?.id ?? '')
      ? remembered
      : null
  const [repo, setRepo] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const setLinkStatus = useStore((s) => s.setLinkStatus)
  // The push runs on its own flag: a failed push must not disable the link
  // form the user may want to correct, and the reverse.
  const [pushing, setPushing] = useState(false)
  const [pushError, setPushError] = useState<string | null>(null)
  const [forwarding, setForwarding] = useState(false)
  const [forwardError, setForwardError] = useState<string | null>(null)
  // The command last copied, so the two Copy buttons cannot report each
  // other's success.
  const [copied, setCopied] = useState('')
  const cmdRef = useRef<HTMLInputElement>(null)

  // Every run forks from the workspace's base branch, so that is the branch
  // to seed - not always `main`.
  const branch = workspace?.base_branch ?? 'main'
  const pushCmd = `git push -u aether ${branch}`
  const canPush = caps.hasLocal('repo.push')
  const absolute = repo.trim().startsWith('/')
  const pushed = connected?.push ?? null
  const forwarded = connected?.fastForward ?? null
  // Settled means the workspace and the clone agree: nothing is left to
  // push, so Continue is the primary action and the push offer is gone.
  const settled =
    forwarded !== null ||
    pushed?.state === 'pushed' ||
    pushed?.state === 'up-to-date'
  // `git push` is the command to run only while the two tips have not
  // parted: offering it to a clone the workspace has moved past is offering
  // the `! [rejected] main -> main` this comparison exists to prevent.
  const manual = !pushed?.state || pushed.state === 'pushed'
  // The commands that resolve a divergence by hand, in the order they run.
  const resolveCmds =
    pushed?.state === 'diverged'
      ? [
          `git fetch ${pushed.remote} ${pushed.branch}`,
          `git log --oneline --left-right ${pushed.branch}...${pushed.remote}/${pushed.branch}`,
          `git rebase ${pushed.remote}/${pushed.branch}`,
          `git push ${pushed.remote} ${pushed.branch}`,
        ].join('\n')
      : ''

  const link = async () => {
    const path = repo.trim()
    setBusy(true)
    setError(null)
    try {
      setConnected({
        link: crypto.randomUUID(),
        workspace: workspace?.id ?? '',
        path,
        remote: await client.localLinkRepo(path, workspace?.id),
        push: null,
        fastForward: null,
      })
      void client.localLinkStatus().then(setLinkStatus).catch(() => undefined)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const push = async () => {
    if (!connected) return
    const origin = connected
    setPushing(true)
    setPushError(null)
    try {
      // The step stays put on success so git's own answer is readable:
      // "Everything up-to-date" and "[new branch]" mean different things,
      // and only git can tell them apart.
      const result = await client.localRepoPush(workspace?.id)
      const current = stillLinked(origin)
      if (current) setConnected({ ...current, push: result })
    } catch (err) {
      if (stillLinked(origin)) setPushError(message(err))
    } finally {
      if (stillLinked(origin)) setPushing(false)
    }
  }

  const fastForward = async () => {
    if (!connected) return
    const origin = connected
    setForwarding(true)
    setForwardError(null)
    try {
      const result = await client.localRepoFastForward(workspace?.id)
      const current = stillLinked(origin)
      if (current) setConnected({ ...current, fastForward: result })
    } catch (err) {
      if (stillLinked(origin)) setForwardError(message(err))
    } finally {
      if (stillLinked(origin)) setForwarding(false)
    }
  }

  const repoint = () => {
    setRepo(connected?.path ?? '')
    setConnected(null)
    setError(null)
    setPushError(null)
    setForwardError(null)
    // A push or fast-forward still in flight belongs to the clone being left
    // behind, so its answer is dropped: nothing will clear these later.
    setPushing(false)
    setForwarding(false)
  }

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(text)
    } catch {
      // Nothing to select when the command is the diverged block's <pre>.
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
      {!connected && (
        <>
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
            {back}
          </form>
          {repo.trim() !== '' && !absolute && (
            <p className="text-xs text-muted-foreground">
              The path must be absolute.
            </p>
          )}
          {error && <p className="text-xs text-state-failed">{error}</p>}
        </>
      )}
      {connected && (
        <div className="space-y-2">
          <p className="text-sm">
            Connected <span className="font-mono">{connected.path}</span>.
            Remote <span className="font-mono">{connected.remote.remote}</span>{' '}
            points at{' '}
            <span className="font-mono">{connected.remote.url}</span>.{' '}
            {connected.remote.origin && (
              <>
                Runs push to{' '}
                <span className="font-mono">{connected.remote.origin}</span>.{' '}
              </>
            )}
            {pushed?.state === 'pushed' && (
              <>
                Pushed <span className="font-mono">{pushed.branch}</span> to{' '}
                <span className="font-mono">{pushed.remote}</span>.
              </>
            )}
            {pushed?.state === 'up-to-date' && (
              <>
                Workspace already has{' '}
                <span className="font-mono">{pushed.branch}</span> at{' '}
                <span className="font-mono">
                  {short(pushed.workspace_commit)}
                </span>
                . Nothing to push.
              </>
            )}
            {!pushed && (
              <>
                Seed the workspace with{' '}
                <span className="font-mono">{branch}</span>:
              </>
            )}
          </p>
          {pushed?.state === 'behind' && !forwarded && (
            <div className="space-y-2" aria-live="polite">
              <p className="text-sm">
                The workspace is {commits(pushed.behind)} ahead of your clone.
              </p>
              <p className="text-xs text-muted-foreground">
                Your clone{' '}
                <span className="font-mono">{short(pushed.local_commit)}</span>{' '}
                - workspace{' '}
                <span className="font-mono">
                  {short(pushed.workspace_commit)}
                </span>
                .
              </p>
              <Button
                size="sm"
                disabled={forwarding}
                onClick={() => void fastForward()}
              >
                {forwarding ? 'Fast-forwarding...' : 'Fast-forward my clone'}
              </Button>
              {forwardError && (
                <div className="space-y-1">
                  <p className="text-xs text-state-failed">
                    The fast-forward failed:
                  </p>
                  <pre className={`rounded-md border bg-card ${pane}`}>
                    {forwardError}
                  </pre>
                </div>
              )}
              <p className="text-xs text-muted-foreground">
                Fast-forward only. Your history is never merged or rewritten.
              </p>
            </div>
          )}
          {pushed?.state === 'diverged' && (
            <div className="space-y-2" aria-live="polite">
              <p className="text-sm">
                Your clone and the workspace have both moved on:{' '}
                {commits(pushed.ahead)} here, {pushed.behind} there. Aether
                never force-pushes.
              </p>
              <p className="text-xs text-muted-foreground">
                Your clone{' '}
                <span className="font-mono">{short(pushed.local_commit)}</span>{' '}
                - workspace{' '}
                <span className="font-mono">
                  {short(pushed.workspace_commit)}
                </span>
                .
              </p>
              <p className="text-sm">Resolve it by hand, then push again:</p>
              <div className="flex items-start gap-2">
                <pre className={`flex-1 rounded-md border bg-card ${pane}`}>
                  {resolveCmds}
                </pre>
                <Button
                  variant="outline"
                  size="sm"
                  aria-label="Copy resolve commands"
                  onClick={() => void copy(resolveCmds)}
                >
                  {copied === resolveCmds ? 'Copied' : 'Copy'}
                </Button>
              </div>
            </div>
          )}
          {forwarded && (
            <p className="text-sm" aria-live="polite">
              {forwarded.current ? (
                <>
                  Fast-forwarded{' '}
                  <span className="font-mono">{forwarded.branch}</span> to{' '}
                  <span className="font-mono">{short(forwarded.commit)}</span>.
                </>
              ) : (
                <>
                  Updated <span className="font-mono">{forwarded.branch}</span>{' '}
                  to{' '}
                  <span className="font-mono">{short(forwarded.commit)}</span>.
                  Another branch is checked out, so the fast-forward left your
                  working tree alone.
                </>
              )}
              {forwarded.dirty &&
                ' The uncommitted changes you already had are still there.'}
            </p>
          )}
          {settled ? (
            // Git's own answer, open: "Everything up-to-date" and "[new
            // branch]" both mean success and say different things, and the
            // reader who needs that distinction is the one who would not
            // know to go looking for it.
            <details open className="rounded-md border bg-card">
              <summary className={cn(focusRing, 'cursor-pointer px-3 py-2 text-sm')}>
                What git did
              </summary>
              <pre className={pane}>
                {[pushed?.output, forwarded?.output]
                  .map((out) => out?.trim())
                  .filter(Boolean)
                  .join('\n\n') || 'git printed nothing.'}
              </pre>
            </details>
          ) : (
            canPush && (
              <>
                <Button size="sm" disabled={pushing} onClick={() => void push()}>
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
              </>
            )
          )}
          {manual && (
            <>
              {canPush && (
                <p className="text-xs text-muted-foreground">
                  {settled ? 'The same push, by hand:' : 'or run it yourself:'}
                </p>
              )}
              <div className="flex gap-2">
                <input
                  ref={cmdRef}
                  readOnly
                  aria-label="Push command"
                  className={cn(field, 'font-mono')}
                  value={pushCmd}
                  onFocus={(e) => e.target.select()}
                />
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void copy(pushCmd)}
                >
                  {copied === pushCmd ? 'Copied' : 'Copy'}
                </Button>
              </div>
            </>
          )}
          <div className={actionRow}>
            <Button
              size="sm"
              variant={canPush && !settled ? 'outline' : 'default'}
              onClick={onNext}
            >
              Continue
            </Button>
            <Button size="sm" variant="outline" onClick={repoint}>
              Use a different repository
            </Button>
            {back}
          </div>
        </div>
      )}
    </section>
  )
}

/**
 * The First run step: the first run, in the workspace the Workspace step
 * settled on. Only agents agent.list reports as installed in this account
 * are offered: a name the account has no executable for would fail in the
 * container, so the step sends the reader back to Agents instead of letting
 * them launch it. `defaultHarness` is the one the Agents step just set up.
 */
export function FirstRunStep({
  client,
  workspace,
  defaultHarness,
  back,
  onBackToWorkspace,
  onBackToAgents,
}: {
  client: Api
  workspace: Workspace | null
  defaultHarness?: string
  back?: ReactNode
  onBackToWorkspace?: () => void
  onBackToAgents: () => void
}) {
  const navigate = useStore((s) => s.navigate)
  const setOnboarded = useStore((s) => s.setOnboarded)
  const upsertRun = useStore((s) => s.upsertRun)
  // The draft outlives this component: the header can jump to another step
  // and back, which unmounts it.
  const draft = useStore((s) => s.onboardingFirstRun)
  const setDraft = useStore((s) => s.setOnboardingFirstRun)

  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [agentsError, setAgentsError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const { harness, task } = draft

  const loadAgents = useCallback(() => {
    let live = true
    // Back to loading, not to an empty account: a retry that left the list at
    // [] would tell the member nothing is installed while the call it is
    // waiting on is the only thing that knows.
    setAgents(null)
    setAgentsError(null)
    client
      .agentList()
      .then((list) => {
        if (!live) return
        const installed = list.filter((a) => a.installed === true)
        setAgents(installed)
        // The draft is persisted, so it can name an agent this account no
        // longer has: an offer the picker cannot show and the server would
        // refuse. What the member chose wins over the one the Agents step
        // just set up.
        const current = useStore.getState().onboardingFirstRun
        const kept = installed.some((a) => a.name === current.harness)
          ? current.harness
          : ''
        const harness =
          kept ||
          (defaultHarness && installed.some((a) => a.name === defaultHarness)
            ? defaultHarness
            : '')
        if (harness !== current.harness) setDraft({ ...current, harness })
      })
      .catch((err) => {
        if (!live) return
        setAgents([])
        setAgentsError(message(err))
      })
    return () => {
      live = false
    }
  }, [client, defaultHarness, setDraft])

  useEffect(loadAgents, [loadAgents])

  const loading = useDelayed(agents === null)
  // Launchable only against an agent this account was reported to have. A
  // persisted draft is on screen before agent.list answers, so a name it is
  // about to reject must not be launchable in the meantime.
  const ready =
    task.trim() !== '' &&
    workspace !== null &&
    (agents ?? []).some((a) => a.name === harness)

  const launch = async () => {
    setBusy(true)
    setError(null)
    try {
      if (!workspace) throw new Error('no workspace selected; go back a step')
      const run = await client.runLaunch({
        workspace_id: workspace.id,
        task: task.trim(),
        harness,
      })
      // Seed the store so the terminal view attaches without a refetch.
      upsertRun(run)
      setOnboarded(true)
      navigate('terminal', { runId: run.id })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const goToBoard = () => {
    setOnboarded(true)
    navigate('board')
  }

  // The escape hatch for a reader with no agent subscription. It is a CLI
  // flow: `fake` is a scheduler registration, so it is never installed in an
  // account and never appears in the picker above.
  const withoutASubscription = (
    <div className="space-y-1 rounded-md border bg-card p-3 text-xs text-muted-foreground">
      <p className="font-medium text-foreground">No agent subscription yet?</p>
      <p>
        Aether ships a deterministic fake agent that runs a script from your
        repo instead of a real one. Start the server with
        AETHER_FAKE_AGENT="sh /workspace/agent.sh" in its environment, commit
        an agent.sh that writes a file, and launch it from a terminal with{' '}
        <span className="font-mono">aether run "..." --agent fake</span>: the
        whole path - container, worktree, PTY, commit, fetch - runs with
        nothing mocked but the agent.
      </p>
    </div>
  )

  if (!workspace) {
    return (
      <section aria-label="First run" className="space-y-3">
        <h2 className="text-sm font-medium">Launch your first run</h2>
        <p className="text-sm text-muted-foreground">
          Choose a workspace before launching a run.
        </p>
        <div className={actionRow}>
          <Button variant="outline" size="sm" onClick={onBackToWorkspace}>
            Back to Workspace
          </Button>
        </div>
      </section>
    )
  }

  if (agents !== null && agents.length === 0) {
    return (
      <section aria-label="First run" className="space-y-3">
        <h2 className="text-sm font-medium">Launch your first run</h2>
        {agentsError ? (
          <p className="text-xs text-state-failed">{agentsError}</p>
        ) : (
          <p className="text-sm text-muted-foreground">
            A run launches an agent in a container, and no agent is installed
            in your environment yet.
          </p>
        )}
        {withoutASubscription}
        <div className={actionRow}>
          {/* Setting an agent up cannot fix a gateway that did not answer,
              so the failed list asks for the call again instead. */}
          {agentsError ? (
            <Button size="sm" onClick={loadAgents}>
              Retry
            </Button>
          ) : (
            <Button size="sm" onClick={onBackToAgents}>
              Set up an agent
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={goToBoard}>
            Go to board
          </Button>
          {back}
        </div>
      </section>
    )
  }

  return (
    <section aria-label="First run" className="space-y-3">
      <h2 className="text-sm font-medium">Launch your first run</h2>
      <p className="text-sm text-muted-foreground">
        The run forks from <span className="font-mono">{workspace.base_branch}</span>{' '}
        in {workspace.name}.
      </p>
      {loading && <Skeleton className="h-16 w-full" />}
      {agents && (
        <label className="block space-y-1 text-sm">
          Agent
          <select
            className={field}
            value={harness}
            onChange={(e) => setDraft({ ...draft, harness: e.target.value })}
          >
            <option value="">Choose an agent</option>
            {agents.map((a) => (
              <option key={a.name} value={a.name}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
      )}
      <label className="block space-y-1 text-sm">
        Task
        <textarea
          className={`${field} min-h-20`}
          value={task}
          placeholder="add a health check endpoint"
          onChange={(e) => setDraft({ ...draft, task: e.target.value })}
        />
      </label>
      {error && <p className="text-xs text-state-failed">{error}</p>}
      {withoutASubscription}
      <div className={actionRow}>
        <Button size="sm" disabled={busy || !ready} onClick={() => void launch()}>
          Launch
        </Button>
        <Button variant="outline" size="sm" onClick={goToBoard}>
          Go to board
        </Button>
        {back}
      </div>
    </section>
  )
}
