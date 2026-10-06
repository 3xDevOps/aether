import { useCallback, useEffect, useRef, useState } from 'react'
import { AgentPicker, customAgent, launchable } from '@/components/launch/agent-picker'
import { ModeControl } from '@/components/launch/mode-control'
import { initialMode, modeRefusal, refusals } from '@/components/launch/modes'
import { FormField } from '@/components/ui/form-field'
import { Textarea } from '@/components/ui/textarea'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentInfo, LaunchMode } from '@/lib/types'
import { useStore } from '@/store'

function preselect(list: AgentInfo[], preferred?: string): string {
  const { launchDefaults, runs } = useStore.getState()
  const ok = list.filter(launchable)
  if (preferred && ok.some((agent) => agent.name === preferred)) return preferred
  const remembered = ok.filter((agent) => launchDefaults[agent.name]).sort((a, b) => launchDefaults[b.name].at - launchDefaults[a.name].at)[0]
  if (remembered) return remembered.name
  const recent = Object.values(runs)
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .find((run) => ok.some((agent) => agent.name === run.harness))
  return recent?.harness ?? ok[0]?.name ?? ''
}

/** The agent and mode a launch uses, reloaded for each account; `preferred`
 * wins the first pick when that agent can launch. */
export function useAgentChoice({ account, ownAccountID, preferred, client = api }: {
  account: string
  ownAccountID: string
  preferred?: string
  client?: Api
}) {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [agentError, setAgentError] = useState<string | null>(null)
  const [harness, setHarness] = useState('')
  const [mode, setMode] = useState<LaunchMode>('tui')
  const [attempt, setAttempt] = useState(0)
  const choice = useRef({ harness, mode, preferred })
  choice.current = { harness, mode, preferred: choice.current.preferred }

  const choose = useCallback((name: string, list: AgentInfo[]) => {
    setHarness(name)
    const info = list.find((item) => item.name === name)
    setMode(initialMode(info, name, useStore.getState().launchDefaults[name]?.mode))
  }, [])

  useEffect(() => {
    let live = true
    setAgents(null)
    setAgentError(null)
    client.agentList(account && account !== ownAccountID ? account : undefined).then((list) => {
      if (!live) return
      setAgents(list)
      const current = choice.current
      const keep = current.harness === customAgent || list.some((item) => item.name === current.harness && launchable(item))
      if (!keep) choose(preselect(list, current.preferred), list)
      else if (modeRefusal(list.find((item) => item.name === current.harness), current.harness, current.mode)) setMode('tui')
    }).catch((err) => {
      if (!live) return
      setAgents([])
      setAgentError(message(err))
    })
    return () => { live = false }
  }, [account, ownAccountID, client, choose, attempt])

  const agent = agents?.find((item) => item.name === harness)
  const noAgents = agents !== null && agentError === null && !agents.some((item) => item.installed)
  return {
    agents,
    agentError,
    agent,
    harness,
    mode,
    setMode,
    noAgents,
    choose: (name: string) => choose(name, agents ?? []),
    reload: () => setAttempt((n) => n + 1),
  }
}

export function RunFields({
  choice,
  task,
  onTask,
  onSetUp,
  onChoice,
  disabled = false,
  autoFocus = false,
}: {
  choice: ReturnType<typeof useAgentChoice>
  task: string
  onTask: (task: string) => void
  onSetUp: () => void
  onChoice?: () => void
  disabled?: boolean
  autoFocus?: boolean
}) {
  const { agents, agent, harness, mode, noAgents } = choice
  return (
    <>
      <FormField
        label="Task"
        help={mode === 'headless'
          ? 'Required. A Background run starts with this task and takes no input.'
          : 'Optional. Leave it blank to open the agent with no prompt.'}
      >
        <Textarea autoFocus={autoFocus} required={mode === 'headless'} rows={3} placeholder="What should the agent do?" value={task} onChange={(event) => onTask(event.target.value)} />
      </FormField>
      {agents === null ? (
        <p className="text-ui-sm text-muted">Loading agents…</p>
      ) : (
        <AgentPicker
          label="Agent"
          agents={agents}
          value={harness}
          onChange={(name) => { onChoice?.(); choice.choose(name) }}
          onSetUp={noAgents ? undefined : onSetUp}
          disabled={disabled}
        />
      )}
      {harness && (
        <ModeControl label="Mode" value={mode} onChange={(next) => { onChoice?.(); choice.setMode(next) }} refused={refusals(agent, harness)} onSetUp={onSetUp} />
      )}
    </>
  )
}

export function needsTask(mode: LaunchMode, task: string): boolean {
  return mode === 'headless' && task.trim() === ''
}
