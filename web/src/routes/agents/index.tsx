import { useState } from 'react'
import { AddAgent } from '@/components/agents/add-agent'
import { defaultMode, enhancedSupported, label } from '@/components/agents/agent-copy'
import { AgentExtras } from '@/components/agents/agent-extras'
import { AgentList, useAgentList } from '@/components/agents/agent-list'
import { AgentSetup } from '@/components/agents/agent-setup'
import { modes } from '@/components/launch/modes'
import { ArrowLeft } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type { AgentInfo, GitHubConnectResult, LaunchMode } from '@/lib/types'
import { GitHubConnect } from '@/routes/onboarding/github-connect'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const addScreen = '@add'
const githubScreen = '@github'

function DefaultMode({ agent }: { agent: AgentInfo }) {
  const remembered = useStore((s) => s.launchDefaults[agent.name]?.mode)
  const setLaunchDefault = useStore((s) => s.setLaunchDefault)
  return (
    <Select value={defaultMode(agent, remembered)} onValueChange={(mode) => setLaunchDefault(agent.name, mode as LaunchMode)}>
      <SelectTrigger aria-label={`Default mode for ${label(agent)}`} className="w-32">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {modes.map((mode) => (
          <SelectItem key={mode.value} value={mode.value} disabled={mode.value === 'acp' && !enhancedSupported(agent)}>
            {mode.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function AgentsRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  const { agents, error, reload } = useAgentList(client)
  const [screen, setScreen] = useState('')
  const [returnFocusTo, setReturnFocusTo] = useState('')
  const [github, setGithub] = useState<GitHubConnectResult | null>(null)
  const loading = useDelayed(agents === null && error === null)
  const canSetUp = caps.hasMethod('agent.install') || caps.hasWS('terminal')
  const settingUp = agents?.find((agent) => agent.name === screen)
  const close = () => setScreen('')
  const openSetup = (name: string) => {
    setReturnFocusTo(name)
    setScreen(name)
  }

  const run = (agent: AgentInfo) => {
    const state = useStore.getState()
    state.rememberLaunch(agent.name, defaultMode(agent, state.launchDefaults[agent.name]?.mode))
    state.openPaletteDialog('launch')
  }

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader
        title="Agents"
        actions={screen
          ? <Button size="sm" variant="ghost" onClick={close}><ArrowLeft />All agents</Button>
          : caps.hasMethod('agent.register') && <Button size="sm" variant="secondary" onClick={() => setScreen(addScreen)}>Add agent…</Button>}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-4xl min-w-0 flex-col gap-5 px-4 py-6 sm:px-6">
          {screen === githubScreen ? (
            <GitHubConnect client={client} caps={caps} onConnected={setGithub} onClose={close} />
          ) : screen === addScreen ? (
            <AddAgent client={client} onAdded={(agent) => { reload(); openSetup(agent.name) }} onCancel={close} />
          ) : screen ? (
            settingUp
              ? <AgentSetup key={settingUp.name} agent={settingUp} client={client} onDone={() => { reload(); close() }} />
              : <div className="h-28"><Skeleton className="size-full" /></div>
          ) : (
            <>
              <p className="max-w-2xl text-ui text-muted">
                An agent is the coding CLI a run starts. Each one is installed once in your environment and every workspace uses it.
              </p>
              {loading && <div className="h-28"><Skeleton className="size-full" /></div>}
              {error && (
                <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={reload}>Retry</Button>}>
                  {error}
                </Callout>
              )}
              {agents?.length === 0 && <p className="text-ui text-muted">This server lists no agents.</p>}
              {agents && agents.length > 0 && (
                <AgentList
                  agents={agents}
                  onSetUp={canSetUp ? (agent) => openSetup(agent.name) : undefined}
                  onRun={caps.hasMethod('run.launch') ? run : undefined}
                  extra={(agent) => <DefaultMode agent={agent} />}
                  returnFocusTo={returnFocusTo}
                />
              )}
              <AgentExtras client={client} caps={caps} identity github={github} onConnectGitHub={() => setScreen(githubScreen)} />
            </>
          )}
        </div>
      </div>
    </div>
  )
}

registerRoute('agents', AgentsRoute)
