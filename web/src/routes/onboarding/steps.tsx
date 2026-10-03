// The onboarding steps. Each step talks to the gateway through the injected
// Api client and reports completion to the wizard. Step and workspace choices
// are stored in the UI slice, while link status is checked against the local
// gateway whenever this route is entered or refocused.

import { type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import { edgeHost, linkTarget, message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { WorkspaceCreate } from '@/components/workspace-create'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type {
  AgentInfo,
  EdgeLinkResult,
  LinkApplyResult,
  LinkStatus,
  Workspace,
} from '@/lib/types'
import { EdgeSignIn } from '@/routes/onboarding/edge-link'
import { useStore } from '@/store'
import { onboardingStepIndex } from '@/store/ui'
import { useCapability, useIsAdmin, useSelfRole, type Capability } from '@/store/hooks'
import { canLaunch } from '@/lib/commands'

/**
 * The row a step ends with, Back included. It sticks to the bottom of the
 * wizard's scroller because the step above it can be taller than the window
 * - Agents with the terminal dock open is - and a control that scrolls out
 * of reach is the reason Back moved here.
 */
export const actionRow =
  'sticky bottom-0 z-10 mt-4 flex flex-wrap items-center gap-2 border-t bg-card pb-3 pt-3'

// Raw command output - git's, and gh's on the Connect GitHub screen:
// scrollable, wrapped, never truncated.
export const pane =
  'max-h-64 min-w-0 overflow-x-auto overflow-y-auto px-3 py-2 font-mono text-xs whitespace-pre-wrap break-words'

/**
 * The Link step: link this machine to a server, through an edge sign-in or
 * by address. The gateway's local link status determines whether those two
 * choices or the linked summary are shown.
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
  const [edgeLinked, setEdgeLinked] = useState<EdgeLinkResult | null>(null)
  const [byAddress, setByAddress] = useState(false)

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

  const linkedThroughEdge = async (result: EdgeLinkResult) => {
    setEdgeLinked(result)
    useStore.getState().reconnect()
    await check()
  }

  const loading = useDelayed(status === null && statusError === null)
  const serverConfigured = status?.server_configured === true
  const linked = success !== null || edgeLinked !== null
  const choosing = status !== null && !serverConfigured && !linked

  return (
    <section
      aria-label="Link"
      className="min-w-0 space-y-4 border-b border-border/70 py-4"
    >
      <div className="space-y-1">
        <p className="text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
          Step 1
        </p>
        <h2 className="text-base font-semibold">Link to your server</h2>
      </div>
      {loading && <Skeleton className="h-20 w-full rounded-md" />}
      {statusError && (
        <div className="flex min-w-0 flex-wrap items-center gap-3 border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2">
          <p className="text-sm text-state-failed">{statusError}</p>
          <Button size="sm" variant="outline" onClick={() => void check()}>
            Retry
          </Button>
        </div>
      )}
      {success && (
        <div className="space-y-3 border-t border-state-done/30 bg-state-done/5 py-3 text-sm">
          <p className="font-medium text-state-done">Server linked</p>
          <p>
            Linked to <span className="font-mono">{success.addr}</span> as{' '}
            <span className="font-medium">{success.member.display_name}</span> (
            {success.member.role}).
          </p>
          {success.key_generated && (
            <p className="text-muted-foreground">
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
      {edgeLinked && (
        <div className="space-y-3 border-t border-state-done/30 bg-state-done/5 py-3 text-sm">
          <p className="font-medium text-state-done">Server linked</p>
          <p>
            Linked to{' '}
            <span className="font-mono">{edgeLinked.server_name ?? edgeLinked.server_id}</span>{' '}
            through <span className="font-mono">{edgeHost(edgeLinked.edge)}</span> as{' '}
            <span className="font-medium">{edgeLinked.member.display_name}</span> (
            {edgeLinked.member.role}).
          </p>
          <Button
            size="sm"
            onClick={() => onNext(onboardingStepIndex('Git identity'))}
          >
            Continue
          </Button>
        </div>
      )}
      {choosing && !byAddress && (
        <>
          <EdgeSignIn client={client} onLinked={(result) => void linkedThroughEdge(result)} />
          <div className="min-w-0 max-w-2xl space-y-3 border-t border-border/70 pt-4 text-sm">
            <div className="space-y-1">
              <h3 className="text-sm font-semibold">Tailscale or a direct address</h3>
              <p className="text-[13px] leading-5 text-muted-foreground">
                For a server on your tailnet, or one this computer reaches over SSH.
              </p>
            </div>
            <Button size="sm" variant="outline" onClick={() => setByAddress(true)}>
              Link by address
            </Button>
          </div>
        </>
      )}
      {/* The form replaces the sign-in rather than following it, so on a
          phone its fields sit high enough to stay above a soft keyboard. */}
      {choosing && byAddress && (
        <form
          className="min-w-0 max-w-2xl space-y-4 text-sm"
          aria-label="Link server"
          onSubmit={(e) => {
            e.preventDefault()
            void link()
          }}
        >
          <div className="grid min-w-0 gap-3 sm:grid-cols-2">
            <Label className="block min-w-0 space-y-1">
              Server address
              <Input
                className="min-w-0"
                required
                placeholder="server-host:2222"
                value={address}
                disabled={linking}
                onChange={(e) => setAddress(e.target.value)}
              />
            </Label>
            <Label className="block min-w-0 space-y-1">
              Invite code
              <Input
                className="min-w-0"
                value={invite}
                disabled={linking}
                onChange={(e) => setInvite(e.target.value)}
              />
            </Label>
          </div>
          <p className="text-[13px] leading-5 text-muted-foreground">
            Leave empty on a fresh server, where the first identity to link
            becomes the admin, or on a tailnet server.
          </p>
          <Label className="block min-w-0 max-w-sm space-y-1">
            Your name
            <Input className="min-w-0"
              value={name}
              disabled={linking}
              onChange={(e) => setName(e.target.value)}
            />
          </Label>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" size="sm" disabled={linking || !address.trim()}>
              {linking ? 'Linking...' : 'Link'}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={linking}
              onClick={() => setByAddress(false)}
            >
              Sign in instead
            </Button>
            {linkError && <p className="text-sm text-state-failed">{linkError}</p>}
          </div>
        </form>
      )}
      {status && serverConfigured && !status.linked && !linked && (
        <div className="space-y-3 border-t border-border/70 bg-muted/30 px-3 py-3 text-sm">
          <p className="font-medium">Server is ready</p>
          <p>
            Connected to <span className="font-mono">{linkTarget(status)}</span> as{' '}
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
      {status && serverConfigured && status.linked && !linked && (
        <div className="space-y-3 border-t border-state-done/30 bg-state-done/5 py-3">
          <p className="text-sm font-medium text-state-done">Already linked</p>
          <p className="text-sm">
            Linked to <span className="font-mono">{linkTarget(status)}</span> as{' '}
            <span className="font-medium">{status.user}</span>, with{' '}
            <span className="font-mono">{status.repo}</span>.
          </p>
          <Button
            size="sm"
            onClick={() => onNext(onboardingStepIndex('Git identity'))}
          >
            Continue
          </Button>
        </div>
      )}
    </section>
  )
}

export function WorkspaceStep({
  client,
  caps,
  back,
  onNext,
}: {
  client: Api
  caps: Capability
  back?: ReactNode
  onNext: (workspace: Workspace, source?: 'local' | 'remote') => void
}) {
  const [workspaces, setWorkspaces] = useState<Workspace[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const fetchVersion = useRef(0)

  const refetch = useCallback(() => {
    const version = ++fetchVersion.current
    setError(null)
    return client.workspaceListFull().then((list) => {
      if (version !== fetchVersion.current) return
      setWorkspaces(list)
      useStore.getState().setWorkspaces(list)
    }).catch((err) => { if (version === fetchVersion.current) setError(message(err)) })
  }, [client])

  useEffect(() => {
    void refetch()
    return () => { fetchVersion.current += 1 }
  }, [refetch])
  const loading = useDelayed(workspaces === null && error === null)

  return (
    <section
      aria-label="Workspace"
      className="min-w-0 space-y-4 border-b border-border/70 py-4"
    >
      <div className="space-y-1">
        <h2 className="text-base font-semibold">Choose a workspace</h2>
        <p className="text-sm leading-6 text-muted-foreground">
          Runs share a repository and base branch inside one workspace.
        </p>
      </div>
      {loading && <Skeleton className="h-20 w-full rounded-md" />}
      {error && (
        <div role="alert" className="space-y-2 text-sm text-state-failed">
          <p>{error}</p>
          <Button size="sm" variant="outline" onClick={() => void refetch()}>Retry workspace list</Button>
        </div>
      )}
      {workspaces && workspaces.length > 0 && (
        <ul className="min-w-0 border-y border-border/70 bg-background">
          {workspaces.map((w) => (
            <li
              key={w.id}
              className="flex min-w-0 flex-wrap items-center gap-3 border-b border-border/70 px-0 py-2.5 last:border-b-0"
            >
              <span className="min-w-0 flex-1 truncate text-sm font-medium">{w.name}</span>
              <span className="rounded-sm bg-muted px-2 py-1 font-mono text-xs text-muted-foreground">
                {w.base_branch}
              </span>
              <Button
                size="sm"
                variant="outline"
                aria-label={`Use ${w.name}`}
                onClick={() => onNext(w)}
              >
                Use workspace
              </Button>
            </li>
          ))}
        </ul>
      )}
      <WorkspaceCreate client={client} onCreated={onNext} onRefresh={() => void refetch()} />
      {workspaces?.length === 0 && !caps.hasMethod('workspace.add') && <p className="text-sm text-muted-foreground">No workspaces yet. Ask an administrator to add a repository, then refresh this list.</p>}
      {back && <div className={actionRow}>{back}</div>}
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
  onBackToRepository,
}: {
  client: Api
  workspace: Workspace | null
  defaultHarness?: string
  back?: ReactNode
  onBackToWorkspace?: () => void
  onBackToAgents: () => void
  onBackToRepository?: () => void
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
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const launchable = canLaunch({ cap: caps, role: useSelfRole() })
  const [sourceCheck, setSourceCheck] = useState<{ workspace: string; error: string | null } | null>(null)
  const [sourceAttempt, setSourceAttempt] = useState(0)
  const workspaceID = workspace?.id
  useEffect(() => {
    let live = true
    setSourceCheck(null)
    if (!workspaceID) return
    void (async () => {
      try {
        if (caps.hasMethod('files.tree')) await client.filesTree({ workspace_id: workspaceID, path: '' })
        if (caps.hasMethod('workspace.mirror.status')) {
          const source = await client.workspaceMirrorStatus(workspaceID)
          if (source.enabled && (source.status !== 'ready' || !source.accepted_commit)) {
            throw new Error(source.last_error || `Source is ${source.status ?? 'pending'}; ${isAdmin ? 'verify and adopt the candidate' : 'ask an administrator to verify and adopt the candidate'} in repository setup before launching.`)
          }
        }
        if (live) setSourceCheck({ workspace: workspaceID, error: null })
      } catch (cause) {
        if (live) setSourceCheck({ workspace: workspaceID, error: message(cause) })
      }
    })()
    return () => { live = false }
  }, [client, caps, isAdmin, workspaceID, sourceAttempt])

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
    launchable &&
    sourceCheck?.workspace === workspace?.id &&
    sourceCheck?.error === null &&
    task.trim() !== '' &&
    workspace !== null &&
    (agents ?? []).some((a) => a.name === harness)

  const launch = async () => {
    if (!ready) return
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
    <div className="border-t border-border/70 py-3 text-xs text-muted-foreground">
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
      <section
        aria-label="First run"
        className="min-w-0 space-y-4 border-b border-border/70 py-4"
      >
        <div className="space-y-1">
          <h2 className="text-base font-semibold">Launch your first run</h2>
        </div>
        <p className="border-t border-border/70 bg-muted/30 py-3 text-sm text-muted-foreground">
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
      <section
        aria-label="First run"
        className="min-w-0 space-y-4 border-b border-border/70 py-4"
      >
        <div className="space-y-1">
          <h2 className="text-base font-semibold">Launch your first run</h2>
        </div>
        {agentsError ? (
          <p className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed">
            {agentsError}
          </p>
        ) : (
          <p className="text-sm leading-6 text-muted-foreground">
            A run launches an agent in a container, and no agent is installed
            in your environment yet.
          </p>
        )}
        {withoutASubscription}
        {onBackToRepository && <Button size="sm" variant="outline" onClick={onBackToRepository}>Review repository setup</Button>}
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
    <section
      aria-label="First run"
      className="min-w-0 space-y-4 border-b border-border/70 py-4"
    >
      <div className="space-y-1">
        <h2 className="text-base font-semibold">Launch your first run</h2>
        <p className="text-sm leading-6 text-muted-foreground">
          The run forks from <span className="font-mono">{workspace.base_branch}</span>{' '}
          in {workspace.name}.
        </p>
      </div>
      {!launchable && <p className="text-sm text-muted-foreground">Your membership or this gateway cannot launch runs. Ask an administrator for launch access; you can still prepare your account.</p>}
      <section aria-label="Source readiness" className="space-y-2 border-y py-3 text-sm">
        {sourceCheck?.workspace !== workspace.id ? <p>Checking the workspace base branch...</p> : sourceCheck.error ? <p role="alert" className="whitespace-pre-wrap break-words text-state-failed">{sourceCheck.error}</p> : <p>{caps.hasMethod('files.tree') ? 'The workspace base branch is available.' : 'This gateway checks the base branch at launch.'} The server rechecks source policy when launching.</p>}
        <Button size="sm" variant="outline" onClick={() => { setSourceCheck(null); setSourceAttempt((attempt) => attempt + 1) }}>Check source again</Button>
      </section>
      {loading && <Skeleton className="h-20 w-full rounded-md" />}
      {agents && (
        <div className="min-w-0 max-w-sm space-y-1.5 text-sm">
          <Label htmlFor="first-run-agent">Agent</Label>
          <Select
            value={harness}
            onValueChange={(value) => setDraft({ ...draft, harness: value })}
          >
            <SelectTrigger className="min-w-0 w-full" id="first-run-agent">
              <SelectValue placeholder="Choose an agent" />
            </SelectTrigger>
            <SelectContent>
              {agents.map((a) => (
                <SelectItem key={a.name} value={a.name}>
                  {a.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
      <Label className="block min-w-0 max-w-2xl space-y-1">
        Task
        <Textarea
          className="min-h-28 min-w-0"
          value={task}
          placeholder="add a health check endpoint"
          onChange={(e) => setDraft({ ...draft, task: e.target.value })}
        />
      </Label>
      {error && (
        <p className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed">
          {error}
        </p>
      )}
      {withoutASubscription}
      {onBackToRepository && <Button size="sm" variant="outline" onClick={onBackToRepository}>Review repository setup</Button>}
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
