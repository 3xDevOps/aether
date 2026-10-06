
import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type {
  AgentInfo,
  GitHubConnectResult,
  Workspace,
} from '@/lib/types'
import { AgentWizard } from '@/routes/agents/wizard'
import {
  GitHubConnect,
  GitHubSection,
  githubSubStep,
} from '@/routes/onboarding/github-connect'
import { ProfileImport } from '@/components/profile-import'
import type { Capability } from '@/store/hooks'


export function AgentsStep({
  client,
  caps,
  workspace: _workspace,
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
  /** A harness name, `githubSubStep`, or empty. The wizard owns it so Back
   * closes the sub-screen before it leaves the step. */
  setup: string
  onSetup: (subStep: string) => void
  onNext: () => void
  /** Names the harness whose setup was just confirmed, so the First run
   * step can preselect it. */
  onReady: (harness: string) => void
}) {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [agentsError, setAgentsError] = useState<string | null>(null)
  const [done, setDone] = useState<string[]>([])
  const [github, setGithub] = useState<GitHubConnectResult | null>(null)

  const loadAgents = useCallback(() => {
    setAgentsError(null)
    client
      .agentList()
      .then(setAgents)
      .catch((err) => setAgentsError(message(err)))
  }, [client])

  useEffect(() => {
    loadAgents()
  }, [loadAgents])

  const loading = useDelayed(agents === null && agentsError === null)
  const canSetUp = caps.hasMethod('agent.register') && caps.hasMethod('env.save')

  // Continue joins Skip rather than replacing it, so "skip" never reads as
  // "undo what I just did".
  const onward = (
    <div className="sticky bottom-0 z-10 mt-1 flex flex-wrap items-center gap-2 border-t bg-card pb-3 pt-3">
      {(done.length > 0 || github !== null) && (
        <Button size="sm" onClick={onNext}>
          Continue
        </Button>
      )}
      <Button size="sm" variant="secondary" onClick={onNext}>
        Skip for now
      </Button>
      {back}
    </div>
  )

  if (setup) {
    return (
      <section
        aria-label="Agents"
        className="min-w-0 space-y-4 border-b border-border/70 py-4"
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
            harness={setup === '@custom' ? undefined : setup}
            client={client}
            onRegistered={() => {
              setDone((prev) =>
                prev.includes(setup) ? prev : [...prev, setup],
              )
              if (setup !== '@custom') onReady(setup)
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
      className="min-w-0 space-y-4 border-b border-border/70 py-4"
    >
      <div className="space-y-1">
        <h2 className="text-base font-semibold">Prepare your agents</h2>
        <p className="text-sm leading-6 text-muted-foreground">
          These optional setup paths make runs useful without blocking the
          rest of onboarding.
        </p>
      </div>
      <section
        aria-label="Set up an agent"
        className="min-w-0 space-y-4 border-t border-border/70 py-3"
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
        {agentsError && <Button size="sm" variant="secondary" onClick={loadAgents}>Retry agents</Button>}
        {agentsError && (
          <p className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed">
            {agentsError}
          </p>
        )}
        {agents?.length === 0 && canSetUp && <Button size="sm" variant="secondary" onClick={() => onSetup('@custom')}>Add an agent</Button>}
        {agents && agents.length > 0 && (
          <ul className="min-w-0 border-y border-border/70 bg-card">
            {agents.map((h) => {
              const label = h.display_name ?? h.name
              return (
                <li
                  key={h.name}
                  className="flex min-w-0 flex-wrap items-start gap-3 border-b border-border/70 px-0 py-2.5 last:border-b-0"
                >
                  <span className="min-w-0 flex-1 space-y-1 text-sm">
                    <span className="block font-medium">{label}</span>
                    <span className="block text-[13px] leading-5 text-muted-foreground">
                      {h.installed === true
                        ? 'Installed in your server environment; complete vendor login if needed.'
                        : 'Not installed in your server environment.'}
                    </span>
                    {done.includes(h.name) && (
                      <span className="block text-[13px] leading-5 text-state-done">
                        Installation confirmed. Vendor login is checked by the agent when it starts.
                      </span>
                    )}
                  </span>
                  {canSetUp && (
                    <Button
                      size="sm"
                      variant="secondary"
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
        {agents && agents.length > 0 && !canSetUp && (
          <p className="text-xs text-muted-foreground">
            This gateway cannot register agents, so an agent is set up from
            a terminal with{' '}
            <span className="font-mono">aether agent add</span>.
          </p>
        )}
      </section>

      {caps.hasMethod('github.connect') && caps.hasMethod('github.probe') ? <GitHubSection
        connection={github}
        onOpen={() => onSetup(githubSubStep)}
      /> : <section className="space-y-2 border-t py-3 text-sm">
        <h3 className="font-semibold">Publishing credentials</h3>
        <p>Source deploy keys are read-only. For GitHub publishing, use <code>aether terminal</code> to log in with <code>gh auth login</code>, then run <code>aether github connect</code> from your linked computer. Other hosts need their supported native Git credentials and upstream permission.</p>
      </section>}

      <ProfileImport client={client} />

      {onward}
    </section>
  )
}
