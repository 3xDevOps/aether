import { type ReactNode, useState } from 'react'
import { AddAgent } from '@/components/agents/add-agent'
import { defaultMode } from '@/components/agents/agent-copy'
import { AgentExtras } from '@/components/agents/agent-extras'
import { AgentList } from '@/components/agents/agent-list'
import { AgentSetup } from '@/components/agents/agent-setup'
import { ModeOverview } from '@/components/agents/mode-comparison'
import { GitHubConnection } from '@/components/github-connection'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import { cn } from '@/lib/utils'
import type { GitHubConnectResult } from '@/lib/types'
import { useAgentList } from '@/routes/agents/use-agents'
import { actionRow, Step } from '@/routes/onboarding/layout'
import { GitHubConnect, githubSubStep } from '@/routes/onboarding/github-connect'
import { useStore } from '@/store'
import { type Capability, useIsAdmin } from '@/store/hooks'

export const addAgentSubStep = '@custom'

export function AgentStep({
  client,
  caps,
  back,
  setup,
  onSetup,
  onNext,
}: {
  client: Api
  caps: Capability
  back?: ReactNode
  /** An agent name, `addAgentSubStep`, `githubSubStep`, or empty. The wizard
   * owns it so Back closes the sub-screen before it leaves the step. */
  setup: string
  onSetup: (subStep: string) => void
  onNext: (skipped: boolean) => void
}) {
  const isAdmin = useIsAdmin()
  const { agents, error, reload } = useAgentList(client, true)
  const [github, setGithub] = useState<GitHubConnectResult | null>(null)
  const rememberLaunch = useStore((s) => s.rememberLaunch)
  const loading = useDelayed(agents === null && error === null)
  const canSetUp = caps.hasMethod('agent.install') || caps.hasWS('terminal')
  const settingUp = agents?.find((agent) => agent.name === setup)
  const [returnFocusTo, setReturnFocusTo] = useState('')
  const openSetup = (name: string) => {
    setReturnFocusTo(name)
    onSetup(name)
  }

  if (setup) {
    return (
      <section aria-label="Agent" className="flex min-w-0 flex-col gap-5">
        {setup === githubSubStep ? (
          isAdmin ? <GitHubConnection client={client} /> : <GitHubConnect client={client} caps={caps} onConnected={setGithub} onClose={() => onSetup('')} />
        ) : setup === addAgentSubStep ? (
          <AddAgent client={client} onAdded={(agent) => { reload(); openSetup(agent.name) }} onCancel={() => onSetup('')} />
        ) : settingUp ? (
          <AgentSetup key={settingUp.name} agent={settingUp} client={client} onDone={() => { reload(); onSetup('') }} />
        ) : (
          <div className="h-20"><Skeleton className="size-full" /></div>
        )}
        <div className={cn(actionRow, 'max-sm:static')}>{back}</div>
      </section>
    )
  }

  const installed = agents?.some((agent) => agent.installed === true) ?? false
  return (
    <Step
      label="Agent"
      title="Set up an agent"
      lead="An agent is the coding CLI a run starts, such as Claude Code or Codex. Set one up once and every workspace can use it. A run shows the agent in one of two modes:"
      actions={
        <>
          {installed ? <Button onClick={() => onNext(false)}>Continue</Button> : <Button variant="secondary" onClick={() => onNext(true)}>Skip for now</Button>}
          {back}
        </>
      }
    >
      <ModeOverview />
      {loading && <div className="h-28"><Skeleton className="size-full" /></div>}
      {error && (
        <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={reload}>Retry agents</Button>}>
          {error}
        </Callout>
      )}
      {agents && (
        <div className="flex min-w-0 flex-col items-start gap-2">
          {agents.length > 0 && (
            <div className="self-stretch">
              <AgentList
                agents={agents}
                onSetUp={canSetUp ? (agent) => openSetup(agent.name) : undefined}
                returnFocusTo={returnFocusTo}
                primary={installed ? undefined : agents[0].name}
                onRun={(agent) => {
                  rememberLaunch(agent.name, defaultMode(agent, useStore.getState().launchDefaults[agent.name]?.mode))
                  onNext(false)
                }}
              />
            </div>
          )}
          {caps.hasMethod('agent.register') && (
            <Button size="sm" variant="ghost" onClick={() => onSetup(addAgentSubStep)}>
              Add agent…
            </Button>
          )}
          {!canSetUp && (
            <p className="text-ui-sm text-muted">
              This gateway cannot install agents; run <code className="font-code">aether agent add</code> from a terminal instead.
            </p>
          )}
        </div>
      )}
      <AgentExtras client={client} caps={caps} github={github} onConnectGitHub={() => onSetup(githubSubStep)} />
    </Step>
  )
}
