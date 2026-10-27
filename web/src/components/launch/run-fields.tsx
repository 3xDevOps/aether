import { useCallback, useEffect, useRef, useState } from 'react'
import { AgentPicker, customAgent, launchable } from '@/components/launch/agent-picker'
import { ModeControl } from '@/components/launch/mode-control'
import { ModelPicker } from '@/components/launch/model-picker'
import { initialMode, modeRefusal, refusals } from '@/components/launch/modes'
import { FormField } from '@/components/ui/form-field'
import { Textarea } from '@/components/ui/textarea'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentInfo, LaunchDefaultsResult, LaunchMode } from '@/lib/types'
import { useStore } from '@/store'
import { type LaunchDefaultsRead, rememberedChoice } from '@/store/launch-defaults'

function preselect(list: AgentInfo[], saved: LaunchDefaultsResult | null, preferred?: string): string {
  const { launchDefaults, runs } = useStore.getState()
  const ok = list.filter(launchable)
  if (preferred && ok.some((agent) => agent.name === preferred)) return preferred
  const last = saved
    ? ok.find((agent) => agent.name === saved.defaults.agent)
    : ok.filter((agent) => launchDefaults[agent.name]).sort((a, b) => launchDefaults[b.name].at - launchDefaults[a.name].at)[0]
  if (last) return last.name
  const recent = Object.values(runs)
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .find((run) => ok.some((agent) => agent.name === run.harness))
  return recent?.harness ?? ok[0]?.name ?? ''
}

/** The agent, mode and model a launch uses, reloaded for each account; `preferred`
 * wins the first pick when that agent can launch, with `preferredModel` as its model.
 * The first pick waits for `saved`, the server's saved choices, or uses this
 * browser's when the server keeps none or cannot be read. */
export function useAgentChoice({ account, ownAccountID, saved, preferred, preferredModel, hold = false, pick = 0, client = api }: {
  account: string
  ownAccountID: string
  saved: LaunchDefaultsRead
  preferred?: string
  preferredModel?: string
  /** Holds the first pick while the caller may still switch `account`. */
  hold?: boolean
  /** A change picks again from the saved choices, replacing the current agent. */
  pick?: number
  client?: Api
}) {
  const [listed, setListed] = useState<{ account: string; agents: AgentInfo[] } | null>(null)
  const agents = listed?.account === account ? listed.agents : null
  const [agentError, setAgentError] = useState<string | null>(null)
  const [harness, setHarness] = useState('')
  const [mode, setMode] = useState<LaunchMode>('tui')
  const [model, setModel] = useState('')
  const [attempt, setAttempt] = useState(0)
  const savedReady = saved.fresh
  const choice = useRef({ harness, mode, preferred, preferredModel, saved: saved.result, pick })
  choice.current = { ...choice.current, harness, mode, saved: saved.result }

  const choose = useCallback((name: string, list: AgentInfo[]) => {
    setHarness(name)
    const info = list.find((item) => item.name === name)
    const last = rememberedChoice(name, choice.current.saved)
    setMode(initialMode(info, name, last.mode))
    setModel(last.model ?? '')
  }, [])

  useEffect(() => {
    let live = true
    setListed(null)
    setAgentError(null)
    client.agentList(account && account !== ownAccountID ? account : undefined).then((list) => {
      if (live) setListed({ account, agents: list })
    }).catch((err) => {
      if (!live) return
      setListed({ account, agents: [] })
      setAgentError(message(err))
    })
    return () => { live = false }
  }, [account, ownAccountID, client, attempt])

  useEffect(() => {
    if (!agents || agentError !== null || !savedReady || hold) return
    const current = choice.current
    const repick = current.pick !== pick
    current.pick = pick
    const keep = !repick && (current.harness === customAgent || agents.some((item) => item.name === current.harness && launchable(item)))
    if (!keep) {
      const name = preselect(agents, current.saved, current.preferred)
      choose(name, agents)
      if (name === current.preferred && current.preferredModel !== undefined) setModel(current.preferredModel)
    }
    else if (modeRefusal(agents.find((item) => item.name === current.harness), current.harness, current.mode)) setMode('tui')
  }, [agents, agentError, savedReady, hold, pick, choose])

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

export function needsTask(mode: LaunchMode, task: string): boolean {
  return mode === 'headless' && task.trim() === ''
}
