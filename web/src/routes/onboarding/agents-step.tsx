// The onboarding Agents step, between Repository and First run. Three
// optional parts: setting a coding agent up on the server (the same
// environment-terminal instructions the Agents page shows, embedded
// through AgentWizard), connecting GitHub, and bringing this machine's own
// agent configuration across (ProfileImport). None is required - "Skip for
// now" is reachable from every state, including a failed scan and open
// setup instructions - and nothing here touches another member's setup:
// an agent login, a GitHub account and a profile snapshot are all
// per-member.

import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { friendly, message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type {
  AgentInfo,
  GitHubConnectResult,
  HarnessStatus,
  Workspace,
} from '@/lib/types'
import { AgentWizard } from '@/routes/agents/wizard'
import {
  GitHubConnect,
  GitHubSection,
  githubSubStep,
} from '@/routes/onboarding/github-connect'
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
  back,
  setup,
  onSetup,
  onNext,
  onReady,
}: {
  client: Api
  caps: Capability
  workspace: Workspace | null
  back?: ReactNode
  /** The open sub-screen: a harness's setup instructions, `githubSubStep`,
   * or empty for the step's own screen. The step renders nothing else while
   * one is open. The wizard owns it so Back closes this screen before it
   * leaves the step. */
  setup: string
  onSetup: (subStep: string) => void
  /** Advances the wizard; every state here can reach it. */
  onNext: () => void
  /** Names the harness whose setup was just confirmed, so the First run
   * step can preselect it. */
  onReady: (harness: string) => void
}) {
  const [harnesses, setHarnesses] = useState<HarnessStatus[] | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  const [repoPath, setRepoPath] = useState<string | undefined>(undefined)
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [agentsError, setAgentsError] = useState<string | null>(null)
  const [done, setDone] = useState<string[]>([])
  const [github, setGithub] = useState<GitHubConnectResult | null>(null)

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
  const canSetUp = caps.hasMethod('agent.register')

  // Every part is optional, so the way on is always here - including
  // while a setup shell is open and after a scan failed. Once something
  // has been set up, the primary Continue joins it rather than replacing
  // it, so "skip" never reads as "undo what I just did".
  const onward = (
    <div className="sticky bottom-0 z-10 -mx-5 mt-1 flex flex-wrap items-center gap-2 border-t bg-background/95 px-5 pb-5 pt-4 backdrop-blur-sm sm:-mx-6 sm:px-6">
      {(done.length > 0 || github !== null) && (
        <Button size="sm" onClick={onNext}>
          Continue
        </Button>
      )}
      <Button size="sm" variant="outline" onClick={onNext}>
        Skip for now
      </Button>
      {back}
    </div>
  )

  if (setup) {
    return (
      <section
        aria-label="Agents"
        className="mx-auto w-full max-w-5xl space-y-5 rounded-lg border bg-card p-5 shadow-sm sm:p-6"
      >
        {setup === githubSubStep ? (
          <GitHubConnect
            client={client}
            caps={caps}
            onConnected={setGithub}
            onClose={() => onSetup('')}
          />
        ) : (
          <AgentWizard
            agents={agents ?? []}
            harness={setup}
            client={client}
            onRegistered={() => {
              setDone((prev) =>
                prev.includes(setup) ? prev : [...prev, setup],
              )
              onReady(setup)
              loadAgents()
            }}
            onCancel={() => onSetup('')}
          />
        )}
        {onward}
      </section>
    )
  }

  return (
    <section
      aria-label="Agents"
      className="mx-auto w-full max-w-5xl space-y-5 rounded-lg border bg-card p-5 shadow-sm sm:p-6"
    >
      <div className="space-y-1">
        <p className="text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
          Step 5
        </p>
        <h2 className="text-xl font-semibold tracking-tight">Prepare your agents</h2>
        <p className="text-sm leading-6 text-muted-foreground">
          These optional setup paths make runs useful without blocking the
          rest of onboarding.
        </p>
      </div>
      <section
        aria-label="Set up an agent"
        className="space-y-4 rounded-md border bg-background p-4 sm:p-5"
      >
        <div className="space-y-1">
          <h3 className="text-base font-semibold">Set up an agent on the server</h3>
          <p className="text-sm leading-6 text-muted-foreground">
            Runs launch a coding agent on the server. Every agent the server
            knows how to launch is listed below, installed or not, and a run
            can only use one you have installed and logged in. Setup installs
            the agent into your environment home, once for every workspace,
            and it is safe to re-run. Confirming the install saves your
            environment, so runs start with the agent already there.
          </p>
        </div>

        {loading && <Skeleton className="h-20 w-full rounded-md" />}
        {listError && (
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-state-failed/30 bg-state-failed/5 p-3">
            <p className="text-sm text-state-failed">{listError}</p>
            <Button size="sm" variant="outline" onClick={loadHarnesses}>
              Retry
            </Button>
          </div>
        )}
        {agentsError && (
          <p className="rounded-md border border-state-failed/30 bg-state-failed/5 p-3 text-sm text-state-failed">
            {agentsError}
          </p>
        )}
        {harnesses && harnesses.length > 0 && (
          <ul className="overflow-hidden rounded-lg border bg-card">
            {harnesses.map((h) => {
              const label = friendly[h.name] ?? h.name
              const listed = (agents ?? []).some((a) => a.name === h.name)
              return (
                <li
                  key={h.name}
                  className="flex flex-wrap items-start gap-4 border-b px-4 py-3 last:border-b-0 sm:px-5"
                >
                  <span className="min-w-0 flex-1 space-y-1 text-sm">
                    <span className="block font-medium">{label}</span>
                    <span className="block text-[13px] leading-5 text-muted-foreground">
                      {h.installed
                        ? 'installed on this machine'
                        : 'not installed on this machine'}
                      {' - '}
                      {listed
                        ? `the server can launch ${h.name}`
                        : `the server does not list ${h.name}`}
                    </span>
                    {done.includes(h.name) && (
                      <span className="block text-[13px] leading-5 text-state-done">
                        Set up in this session: the login and the installed
                        tools persist in your environment home.
                      </span>
                    )}
                  </span>
                  {canSetUp && (
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`Set up ${label}`}
                      onClick={() => onSetup(h.name)}
                    >
                      Set up
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
        {harnesses && harnesses.length > 0 && !canSetUp && (
          <p className="text-xs text-muted-foreground">
            This gateway cannot register agents, so an agent is set up from
            a terminal with{' '}
            <span className="font-mono">aether agent add</span>.
          </p>
        )}
      </section>

      <GitHubSection
        connection={github}
        onOpen={() => onSetup(githubSubStep)}
      />

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
