import { useCallback, useEffect, useRef, useState } from 'react'
import { AgentPicker, customAgent, launchable } from '@/components/launch/agent-picker'
import { EffortPicker } from '@/components/launch/effort-picker'
import { ModeControl } from '@/components/launch/mode-control'
import { ModelPicker } from '@/components/launch/model-picker'
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

/** The agent, mode, model and effort a launch uses, reloaded for each account; `preferred`
 * wins the first pick when that agent can launch, with `preferredModel` and `preferredEffort`. */
export function useAgentChoice({ account, ownAccountID, preferred, preferredModel, preferredEffort, client = api }: {
  account: string
  ownAccountID: string
  preferred?: string
  preferredModel?: string
  preferredEffort?: string
  client?: Api
}) {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [agentError, setAgentError] = useState<string | null>(null)
  const [harness, setHarness] = useState('')
  const [mode, setMode] = useState<LaunchMode>('tui')
  const [model, setModel] = useState('')
  const [effort, setEffort] = useState('')
  const [attempt, setAttempt] = useState(0)
  const choice = useRef({ harness, mode, preferred, preferredModel, preferredEffort })
  choice.current = { ...choice.current, harness, mode }

  const choose = useCallback((name: string, list: AgentInfo[]) => {
    setHarness(name)
    const info = list.find((item) => item.name === name)
    const remembered = useStore.getState().launchDefaults[name]
    setMode(initialMode(info, name, remembered?.mode))
    setModel(remembered?.model ?? '')
    setEffort((current) => (info?.effort_levels?.includes(current) ? current : ''))
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
      if (!keep) {
        const name = preselect(list, current.preferred)
        choose(name, list)
        if (name === current.preferred && current.preferredModel !== undefined) setModel(current.preferredModel)
        if (name === current.preferred && current.preferredEffort !== undefined) setEffort(current.preferredEffort)
      }
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
    accountMemberID: account && account !== ownAccountID ? account : undefined,
    harness,
    mode,
    setMode,
    model,
    setModel,
    effort,
    setEffort,
    effortLevels: agent?.effort_levels ?? [],
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
      {harness && <ModelField harness={harness} accountMemberID={choice.accountMemberID} value={choice.model} onChange={(next) => { onChoice?.(); choice.setModel(next) }} disabled={disabled} />}
      {choice.effortLevels.length > 0 && <EffortField levels={choice.effortLevels} value={choice.effort} onChange={(next) => { onChoice?.(); choice.setEffort(next) }} disabled={disabled} />}
    </>
  )
}

export function ModelField({ label = 'Model', harness, accountMemberID, value, onChange, disabled = false }: {
  label?: string
  harness: string
  accountMemberID?: string
  value: string
  onChange: (model: string) => void
  disabled?: boolean
}) {
  return (
    <FormField label={label}>
      <ModelPicker harness={harness} accountMemberID={accountMemberID} value={value} onChange={onChange} disabled={disabled} />
    </FormField>
  )
}

export function EffortField({ label = 'Effort', levels, value, onChange, disabled = false }: {
  label?: string
  levels: string[]
  value: string
  onChange: (effort: string) => void
  disabled?: boolean
}) {
  return (
    <FormField label={label}>
      <EffortPicker levels={levels} value={value} onChange={onChange} disabled={disabled} />
    </FormField>
  )
}

export function needsTask(mode: LaunchMode, task: string): boolean {
  return mode === 'headless' && task.trim() === ''
}
