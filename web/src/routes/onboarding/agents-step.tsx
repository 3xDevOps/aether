// The onboarding Agents step, between Repository and First run. Two
// optional halves: setting a coding agent up on the server (the same
// agent-setup shell the Agents page runs, embedded through AgentWizard),
// and bringing this machine's own agent configuration across
// (ProfileImport). Neither is required - "Skip for now" is reachable from
// every state, including a failed scan and an open setup shell - and
// nothing here touches another member's setup: an agent login and a
// profile snapshot are both per-member.

import { useCallback, useEffect, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type { AgentInfo, HarnessStatus, Workspace } from '@/lib/types'
import { AgentWizard } from '@/routes/agents/wizard'
import { friendly } from '@/routes/onboarding/environment-step'
import { ProfileImport } from '@/routes/onboarding/profile-import'
import type { Capability } from '@/store/hooks'

/**
 * The harness names worth previewing for a configuration import. Profile
 * sync is wider than environment setup: opencode syncs
 * ~/.local/share/opencode but cannot run a scan, so env.harnesses alone
 * would never offer it. agent.list's shipped entries name every harness
 * the registry knows; a name with no profile sync refuses the preview and
 * drops out there.
 */
export function profileCandidates(
  harnesses: HarnessStatus[] | null,
  agents: AgentInfo[] | null,
): string[] {
  const names = (harnesses ?? []).map((h) => h.name)
  for (const agent of agents ?? []) {
    if (agent.source === 'shipped' && !names.includes(agent.name)) {
      names.push(agent.name)
    }
  }
  return names
}

export function AgentsStep({
  client,
  caps,
  workspace,
  onNext,
  onReady,
}: {
  client: Api
  caps: Capability
  workspace: Workspace | null
  /** Advances the wizard; every state here can reach it. */
  onNext: () => void
  /** Names the harness a setup shell just finished, so the First run step
   * can preselect it. */
  onReady: (harness: string) => void
}) {
  const [harnesses, setHarnesses] = useState<HarnessStatus[] | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  const [repoPath, setRepoPath] = useState<string | undefined>(undefined)
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [agentsError, setAgentsError] = useState<string | null>(null)
  // The harness whose setup shell is open; the step renders nothing else
  // while a terminal is live.
  const [setup, setSetup] = useState<string | null>(null)
  const [done, setDone] = useState<string[]>([])

  const loadHarnesses = useCallback(() => {
    setListError(null)
    client
      .envHarnesses()
      .then((result) => {
        setHarnesses(result.harnesses)
        setRepoPath(result.repo_path)
      })
      .catch((err) => setListError(message(err)))
  }, [client])

  const loadAgents = useCallback(() => {
    setAgentsError(null)
    client
      .agentList()
      .then(setAgents)
      .catch((err) => setAgentsError(message(err)))
  }, [client])

  useEffect(() => {
    loadHarnesses()
    loadAgents()
  }, [loadHarnesses, loadAgents])

  const loading = useDelayed(harnesses === null && listError === null)
  const canSetUp = caps.hasMethod('agent.register') && caps.hasWS('shell')

  // Both halves are optional, so the way on is always here - including
  // while a setup shell is open and after a scan failed. Once something
  // has been set up, the primary Continue joins it rather than replacing
  // it, so "skip" never reads as "undo what I just did".
  const onward = (
    <div className="flex gap-2">
      {done.length > 0 && (
        <Button size="sm" onClick={onNext}>
          Continue
        </Button>
      )}
      <Button size="sm" variant="outline" onClick={onNext}>
        Skip for now
      </Button>
    </div>
  )

  if (setup) {
    return (
      <section
        aria-label="Agents"
        className="flex min-h-0 flex-1 flex-col gap-3"
      >
        <AgentWizard
          agents={agents ?? []}
          harness={setup}
          workspaceId={workspace?.id}
          onRegistered={() => {
            setDone((prev) =>
              prev.includes(setup) ? prev : [...prev, setup],
            )
            onReady(setup)
            loadAgents()
          }}
          onCancel={() => setSetup(null)}
        />
        {onward}
      </section>
    )
  }

  return (
    <section aria-label="Agents" className="space-y-4">
      <section aria-label="Set up an agent" className="space-y-3">
        <h2 className="text-sm font-medium">Set up an agent on the server</h2>
        <p className="text-sm text-muted-foreground">
          Runs launch a coding agent on the server. The server lists the
          harness names it can launch - every shipped harness is on that
          list whether or not you have installed and logged one in. The
          setup shell is what installs and logs in your agent, in your
          workspace, and it is safe to re-run.
        </p>
        {loading && <Skeleton className="h-16 w-full" />}
        {listError && (
          <div className="space-y-2">
            <p className="text-xs text-state-failed">{listError}</p>
            <Button size="sm" variant="outline" onClick={loadHarnesses}>
              Retry
            </Button>
          </div>
        )}
        {agentsError && (
          <p className="text-xs text-state-failed">{agentsError}</p>
        )}
        {harnesses && harnesses.length > 0 && (
          <ul className="divide-y rounded-md border">
            {harnesses.map((h) => {
              const label = friendly[h.name] ?? h.name
              const listed = (agents ?? []).some((a) => a.name === h.name)
              return (
                <li
                  key={h.name}
                  className="flex items-start gap-3 px-3 py-2 text-sm"
                >
                  <span className="min-w-0 flex-1 space-y-0.5">
                    <span className="block font-medium">{label}</span>
                    <span className="block text-xs text-muted-foreground">
                      {h.installed
                        ? 'installed on this machine'
                        : 'not installed on this machine'}
                      {' - '}
                      {listed
                        ? `the server can launch ${h.name}`
                        : `the server does not list ${h.name}`}
                    </span>
                    {done.includes(h.name) && (
                      <span className="block text-xs">
                        Set up in this session: the login is persisted and the
                        workspace tools are snapshotted.
                      </span>
                    )}
                  </span>
                  {canSetUp && workspace && (
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`Set up ${label}`}
                      onClick={() => setSetup(h.name)}
                    >
                      Set up
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
        {harnesses && harnesses.length > 0 && canSetUp && !workspace && (
          <p className="text-xs text-muted-foreground">
            No workspace is selected, and the setup shell installs the agent
            in one. Go back to the Workspace step to pick it.
          </p>
        )}
        {harnesses && harnesses.length > 0 && !canSetUp && (
          <p className="text-xs text-muted-foreground">
            This gateway does not serve the setup shell, so an agent is set
            up from a terminal with{' '}
            <span className="font-mono">aether agent add</span>.
          </p>
        )}
      </section>

      <ProfileImport
        client={client}
        harnesses={harnesses ?? []}
        candidates={profileCandidates(harnesses, agents)}
        served={caps.hasLocal('profile.preview') && caps.hasLocal('profile.push')}
        repoPath={repoPath}
        workspace={workspace}
      />

      {onward}
    </section>
  )
}
