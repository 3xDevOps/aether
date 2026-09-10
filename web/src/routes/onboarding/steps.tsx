// The onboarding steps. Each step talks to the gateway through the injected
// Api client and reports completion to the wizard. Step and workspace choices
// are stored in the UI slice, while link status is checked against the local
// gateway whenever this route is entered or refocused.

import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type {
  AgentInfo,
  LinkApplyResult,
  LinkStatus,
  Workspace,
} from '@/lib/types'
import { field } from '@/lib/utils'
import { useStore } from '@/store'
import { onboardingStepIndex } from '@/store/ui'
import type { Capability } from '@/store/hooks'

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
          <Label className="block space-y-1">
            Server address
            <Input
              required
              placeholder="server-host:2222"
              value={address}
              disabled={linking}
              onChange={(e) => setAddress(e.target.value)}
            />
          </Label>
          <Label className="block space-y-1">
            Invite code
            <Input
              value={invite}
              disabled={linking}
              onChange={(e) => setInvite(e.target.value)}
            />
          </Label>
          <p className="text-xs text-muted-foreground">
            Leave empty on a fresh server, where the first identity to link
            becomes the admin, or on a tailnet server.
          </p>
          <Label className="block space-y-1">
            Your name
            <Input
              value={name}
              disabled={linking}
              onChange={(e) => setName(e.target.value)}
            />
          </Label>
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
            <Label className="block space-y-1">
              Name
              <Input
                value={name}
                placeholder="myproject"
                onChange={(e) => setName(e.target.value)}
              />
            </Label>
            <Label className="block space-y-1">
              Base branch
              <Input
                value={baseBranch}
                onChange={(e) => setBaseBranch(e.target.value)}
              />
            </Label>
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
        <Label className="block space-y-1">
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
        </Label>
      )}
      <Label className="block space-y-1">
        Task
        <Textarea
          className="min-h-20"
          value={task}
          placeholder="add a health check endpoint"
          onChange={(e) => setDraft({ ...draft, task: e.target.value })}
        />
      </Label>
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
