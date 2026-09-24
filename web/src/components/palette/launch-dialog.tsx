import { useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import type { AgentInfo, Member, MissionExecutionChoice } from '@/lib/types'
import { useCapability, useSelf } from '@/store/hooks'
import { useStore } from '@/store'

type LaunchMode = 'tui' | 'headless'
type LaunchKind = 'single' | 'swarm'

function idempotencyKey(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `mission-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

// One key per submitted swarm contents, for this tab. The dialog unmounts on
// close, and a failed create may already have stored the mission: resending
// the same contents under the same key replays it instead of making another,
// while changed contents under a used key are refused as an idempotency
// conflict.
const swarmKeys = new Map<string, string>()

export function LaunchDialog() {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const close = useStore((s) => s.closePaletteDialog)
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const rememberHarness = useStore((s) => s.rememberHarness)
  const lastHarnessByAccount = useStore((s) => s.lastHarnessByAccount)
  const runs = useStore((s) => s.runs)
  const self = useStore((s) => s.info?.member)
  const capabilities = useStore((s) => s.capabilities)
  const cap = useCapability()
  const identity = useSelf()
  const swarmAvailable = cap.hasMethod('mission.create') && allowed('launch', identity)
  const ownAccountID = identity?.id ?? self?.id ?? ''
  const [accounts, setAccounts] = useState<Member[]>(self ? [self] : [])
  const [account, setAccount] = useState(self?.id ?? '')
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [agentsByAccount, setAgentsByAccount] = useState<Record<string, AgentInfo[]>>({})
  const [agentError, setAgentError] = useState<string | null>(null)
  const [agentRefresh, setAgentRefresh] = useState(0)
  const [harness, setHarness] = useState('')
  const [task, setTask] = useState('')
  const [mode, setMode] = useState<LaunchMode>('tui')
  const [workerChoices, setWorkerChoices] = useState<MissionExecutionChoice[]>([])
  const [maxConcurrent, setMaxConcurrent] = useState('2')
  const [maxAttempts, setMaxAttempts] = useState('8')
  const [swarmError, setSwarmError] = useState<string | null>(null)
  const [launching, setLaunching] = useState(false)
  const [kind, setKind] = useState<LaunchKind>(() => swarmAvailable && useStore.getState().route.name === 'missions' ? 'swarm' : 'single')
  const lastUsedHarness = useMemo(() => {
    const accountID = account || ownAccountID
    const remembered = accountID ? lastHarnessByAccount[accountID] : undefined
    if (remembered) return remembered
    return Object.values(runs)
      .filter((run) => (run.account_member_id ?? run.member_id) === accountID)
      .sort((a, b) => b.created_at.localeCompare(a.created_at))
      .find((run) => run.harness)?.harness
  }, [account, lastHarnessByAccount, ownAccountID, runs])
  // mission.create refuses a headless integrator.
  const integratorChoice: MissionExecutionChoice = { account_member_id: account, harness, mode: 'tui' }
  const needsTask = kind === 'single' && mode === 'headless' && task.trim() === ''
  const installedAgents = agents?.filter((agent) => agent.installed === true) ?? []
  const harnessLoading = agents === null
  const noAgents = !harnessLoading && installedAgents.length === 0
  // Availability can drop while the dialog is open: a re-hydration that
  // could not read the capabilities, or a role change. The form stays on
  // Swarm and says why, rather than switching under the reader.
  const swarmUnavailable = kind !== 'swarm' || swarmAvailable
    ? null
    : capabilities === null
      ? 'Swarm launch is unavailable: the server did not report its capabilities. Switch to Single agent to launch a run.'
      : !allowed('launch', identity)
        ? 'Swarm launch is unavailable: your role cannot launch.'
        : 'Swarm launch is unavailable: the gateway does not offer mission.create. Switch to Single agent to launch a run.'

  useEffect(() => {
    let live = true
    api.accountList().then((access) => {
      if (!live) return
      setAccounts(access.accounts)
      setAccount((current) => current || access.accounts[0]?.id || '')
    }).catch(() => {})
    return () => { live = false }
  }, [])

  useEffect(() => {
    let live = true
    setAgents(null)
    setAgentError(null)
    setHarness('')
    api.agentList(account && account !== ownAccountID ? account : undefined).then((list) => {
      if (!live) return
      setAgents(list)
      const installed = list.filter((agent) => agent.installed === true)
      setHarness((current) => {
        if (current && installed.some((agent) => agent.name === current)) return current
        if (lastUsedHarness && installed.some((agent) => agent.name === lastUsedHarness)) return lastUsedHarness
        return installed[0]?.name ?? ''
      })
    }).catch((err) => {
      if (!live) return
      setAgents([])
      setAgentError(message(err))
    })
    return () => { live = false }
  }, [account, lastUsedHarness, ownAccountID, agentRefresh])

  useEffect(() => {
    if (kind !== 'swarm' || !accounts.length) return
    let live = true
    const accountIDs = accounts.map((member) => member.id)
    Promise.all(accountIDs.map(async (memberID) => {
      const list = await api.agentList(memberID === ownAccountID ? undefined : memberID)
      return [memberID, list.filter((agent) => agent.installed === true)] as const
    })).then((entries) => {
      if (!live) return
      const byAccount = Object.fromEntries(entries)
      setAgentsByAccount(byAccount)
      setWorkerChoices((current) => {
        if (current.length) return current
        const own = byAccount[account] ?? []
        const fallback = own[0] ?? Object.values(byAccount).flat()[0]
        return fallback ? [{ account_member_id: account, harness: fallback.name, mode: 'headless' }] : []
      })
    }).catch((err) => {
      if (live) setAgentError(message(err))
    })
    return () => { live = false }
  }, [accounts, account, kind, ownAccountID])

  const setUpAgent = () => {
    close()
    navigate('agents')
  }

  const toggleChoice = (choice: MissionExecutionChoice, enabled: boolean) => {
    setWorkerChoices((current) => {
      const same = (item: MissionExecutionChoice) => item.account_member_id === choice.account_member_id && item.harness === choice.harness
      return enabled
        ? current.some(same) ? current : [...current, choice]
        : current.filter((item) => !same(item))
    })
  }

  const setChoiceMode = (choice: MissionExecutionChoice, nextMode: LaunchMode) => {
    setWorkerChoices((current) => current.map((item) =>
      item.account_member_id === choice.account_member_id && item.harness === choice.harness
        ? { ...item, mode: nextMode }
        : item,
    ))
  }

  const launch = async () => {
    setLaunching(true)
    setSwarmError(null)
    try {
      const trimmed = task.trim()
      if (kind === 'single') {
        const run = await api.runLaunch({
          workspace_id: workspaceID,
          harness,
          ...(trimmed ? { task: trimmed } : {}),
          ...(mode === 'tui' ? {} : { mode }),
          ...(account && account !== ownAccountID ? { account_member_id: account } : {}),
        })
        rememberHarness(account || ownAccountID || '', harness)
        upsertRun(run)
        close()
        navigate('terminal', { runId: run.id })
        toast.success('Run launched')
      } else {
        const concurrent = Number(maxConcurrent)
        const attempts = Number(maxAttempts)
        const accountableHumanID = self?.id ?? ''
        if (!workspaceID || !accountableHumanID || !trimmed || !account || !harness || !Number.isInteger(concurrent) || concurrent < 1 || concurrent > 8 || !Number.isInteger(attempts) || attempts < concurrent || attempts > 128) {
          setLaunching(false)
          setSwarmError('Enter an objective, an integrator account and harness, and finite limits (1–8 concurrent, 1–128 total attempts; total must cover concurrency).')
          return
        }
        const contents = {
          workspace_id: workspaceID,
          objective: trimmed,
          accountable_human_id: accountableHumanID,
          integrator: integratorChoice,
          // Sorted so the same set always serializes, and is sent, identically:
          // the key follows the serialization and the server's receipt check
          // compares the encoded list.
          execution_choices: [integratorChoice, ...workerChoices.filter((choice) => choice.account_member_id !== account || choice.harness !== harness || choice.mode !== integratorChoice.mode)]
            .sort((a, b) => a.account_member_id.localeCompare(b.account_member_id) || a.harness.localeCompare(b.harness) || a.mode.localeCompare(b.mode)),
          max_concurrent_attempts: concurrent,
          max_total_attempts: attempts,
        }
        const serialized = JSON.stringify(contents)
        const key = swarmKeys.get(serialized) ?? idempotencyKey()
        swarmKeys.set(serialized, key)
        const result = await api.missionCreate({ ...contents, idempotency_key: key })
        swarmKeys.delete(serialized)
        rememberHarness(account || ownAccountID, harness)
        close()
        navigate('missions', { missionId: result.mission.id })
        toast.success('Swarm created')
      }
    } catch (err) {
      setLaunching(false)
      if (kind === 'swarm') setSwarmError(message(err))
      else toast.error(`Launch failed: ${message(err)}`)
    }
  }

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(620px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0">
        <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
          <DialogTitle>{kind === 'swarm' ? 'Launch a swarm' : 'Launch a run'}</DialogTitle>
          <DialogDescription>
            {kind === 'swarm'
              ? 'Authorize one integrator and the execution choices it and its workers may use. The integrator asks you clarifying questions and submits a plan; no worker starts until you approve it.'
              : 'Start an agent in a container on the workspace\'s base branch. Interactive runs open a terminal; headless runs need a task.'}
          </DialogDescription>
        </DialogHeader>
        <form id="launch-run" className="min-h-0 min-w-0 space-y-3 overflow-y-auto px-3 py-3 sm:px-4" onSubmit={(event) => { event.preventDefault(); void launch() }}>
          <div className="border-y border-border/70 px-2 py-2">
            <p className="text-xs font-medium text-muted-foreground">Target workspace</p>
            <p className="mt-0.5 break-words text-[13px]" aria-label="Target workspace">
              {workspace ? <><span className="font-medium">{workspace.name}</span>{' '}<span className="font-mono text-xs text-muted-foreground">{workspace.base_branch}</span></> : <span className="text-muted-foreground">Pick a workspace in the sidebar first.</span>}
            </p>
          </div>
          {(swarmAvailable || kind === 'swarm') && (
            <Label className="block space-y-1.5"><span>Launch type</span><Select value={kind} onValueChange={(value) => setKind(value as LaunchKind)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="single">Single agent</SelectItem><SelectItem value="swarm">Swarm</SelectItem></SelectContent></Select></Label>
          )}
          {swarmUnavailable && <p id="launch-swarm-unavailable" role="status" className="border-l-2 border-state-needs-attention bg-state-needs-attention/10 px-2 py-1.5 text-xs">{swarmUnavailable}</p>}
          <Label className="block space-y-1.5">
            <span>{kind === 'swarm' ? 'Objective (required)' : mode === 'headless' ? 'Task (required)' : 'Task (optional)'}</span>
            <Textarea autoFocus required={kind === 'swarm' || mode === 'headless'} rows={3} placeholder={kind === 'swarm' ? 'What outcome should the integrator coordinate?' : 'What should the agent do?'} value={task} onChange={(event) => setTask(event.target.value)} />
            <span className="block text-xs leading-4 font-normal text-muted-foreground">{kind === 'swarm' ? 'The integrator turns this objective into a plan you approve before any worker runs.' : mode === 'headless' ? 'Headless runs start with this task and have no terminal.' : 'Leave blank to open an interactive terminal without a seeded task.'}</span>
          </Label>
          <div className={`grid gap-2 ${kind === 'swarm' ? 'sm:grid-cols-2' : 'sm:grid-cols-3'}`}>
            <div className="min-w-0 space-y-1.5 text-sm"><Label htmlFor="launch-account">{kind === 'swarm' ? 'Integrator account' : 'Account'}</Label><Select value={account} onValueChange={setAccount}><SelectTrigger id="launch-account"><SelectValue placeholder="Choose an account" /></SelectTrigger><SelectContent>{accounts.map((member) => <SelectItem key={member.id} value={member.id}>{member.display_name}{member.id === ownAccountID ? ' (you)' : ' (shared)'}</SelectItem>)}</SelectContent></Select></div>
            <div className="min-w-0 space-y-1.5 text-sm"><Label htmlFor="launch-agent">{kind === 'swarm' ? 'Integrator harness' : 'Agent'}</Label><Select value={harness} onValueChange={setHarness}><SelectTrigger id="launch-agent" disabled={harnessLoading || launching}><SelectValue placeholder="Choose an agent" /></SelectTrigger><SelectContent>{installedAgents.map((agent) => <SelectItem key={agent.name} value={agent.name}>{agent.name}</SelectItem>)}<SelectItem value="custom">custom</SelectItem></SelectContent></Select></div>
            {kind === 'single' && <div className="min-w-0 space-y-1.5 text-sm"><Label htmlFor="launch-mode">Mode</Label><Select value={mode} onValueChange={(value) => setMode(value as LaunchMode)}><SelectTrigger id="launch-mode"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="tui">Interactive (tui)</SelectItem><SelectItem value="headless">Headless</SelectItem></SelectContent></Select></div>}
          </div>
          {kind === 'swarm' && <SwarmFields integrator={integratorChoice} accounts={accounts} agentsByAccount={agentsByAccount} choices={workerChoices} onToggle={toggleChoice} onMode={setChoiceMode} maxConcurrent={maxConcurrent} maxAttempts={maxAttempts} onConcurrent={setMaxConcurrent} onAttempts={setMaxAttempts} />}
          {agentError && <p role="alert" className="break-words border-l-2 border-state-failed bg-state-failed/10 px-2 py-1.5 text-xs text-state-failed">{agentError}</p>}
          {swarmError && <p role="alert" className="break-words border-l-2 border-state-failed bg-state-failed/10 px-2 py-1.5 text-xs text-state-failed">{swarmError}</p>}
          {noAgents && !agentError && <div className="border-y border-border/70 px-2 py-2"><p className="text-[13px] font-medium">No agent is installed in this account.</p><p className="mt-0.5 text-xs leading-4 text-muted-foreground">Set one up before launching work for this account.</p><Button type="button" size="sm" className="mt-2" onClick={setUpAgent}>Set up an agent</Button></div>}
          <div className="flex flex-wrap items-center justify-between gap-2"><Button type="button" size="sm" variant="outline" disabled={harnessLoading || launching} onClick={() => setAgentRefresh((current) => current + 1)}>Refresh agents</Button>{account && account !== ownAccountID && <p className="max-w-[34ch] text-right text-xs leading-4 text-muted-foreground">Uses the selected member&apos;s environment, agent login, profile, and vendor quota. You remain its owner and actor.</p>}</div>
          {needsTask && <p id="launch-needs-task" className="border-l-2 border-state-needs-attention bg-state-needs-attention/10 px-2 py-1.5 text-xs text-muted-foreground">A headless run has no terminal to type into, so it needs a task.</p>}
        </form>
        <DialogFooter className="border-t px-3 py-3 sm:px-4"><Button variant="outline" onClick={close}>Cancel</Button><Button type="submit" form="launch-run" aria-describedby={needsTask ? 'launch-needs-task' : swarmUnavailable ? 'launch-swarm-unavailable' : undefined} disabled={launching || !workspaceID || !account || !harness || needsTask || swarmUnavailable !== null}>{kind === 'swarm' ? 'Create swarm' : 'Launch'}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function SwarmFields({
  integrator,
  accounts,
  agentsByAccount,
  choices,
  onToggle,
  onMode,
  maxConcurrent,
  maxAttempts,
  onConcurrent,
  onAttempts,
}: {
  integrator: MissionExecutionChoice
  accounts: Member[]
  agentsByAccount: Record<string, AgentInfo[]>
  choices: MissionExecutionChoice[]
  onToggle: (choice: MissionExecutionChoice, enabled: boolean) => void
  onMode: (choice: MissionExecutionChoice, mode: LaunchMode) => void
  maxConcurrent: string
  maxAttempts: string
  onConcurrent: (value: string) => void
  onAttempts: (value: string) => void
}) {
  return <div className="space-y-3 border-y border-border/70 px-2 py-2"><div><p className="text-xs font-medium">Allowed execution choices</p><p className="mt-0.5 text-xs text-muted-foreground">The integrator and its workers run only as these account, harness, and mode combinations. The integrator&apos;s own choice is always included and always interactive (tui).</p></div><div className="grid gap-1"><div className="flex items-center gap-2 text-xs"><input type="checkbox" checked disabled aria-label="Integrator execution choice" /><span className="min-w-0 flex-1">Integrator · {accounts.find((member) => member.id === integrator.account_member_id)?.display_name ?? integrator.account_member_id} · {integrator.harness || 'no harness'} · {integrator.mode}</span></div>{accounts.flatMap((member) => (agentsByAccount[member.id] ?? []).map((agent) => { const selected = choices.find((choice) => choice.account_member_id === member.id && choice.harness === agent.name); const base = selected ?? { account_member_id: member.id, harness: agent.name, mode: 'headless' }; return <label key={`${member.id}:${agent.name}`} className="flex items-center gap-2 text-xs"><input type="checkbox" checked={Boolean(selected)} onChange={(event) => onToggle(base, event.target.checked)} /><span className="min-w-0 flex-1">{member.display_name} · {agent.name}</span>{selected && <Select value={selected.mode} onValueChange={(value) => onMode(selected, value as LaunchMode)}><SelectTrigger className="w-28"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="tui">tui</SelectItem><SelectItem value="headless">headless</SelectItem></SelectContent></Select>}</label> }))}</div>{!accounts.some((member) => (agentsByAccount[member.id] ?? []).length) && <p className="text-xs text-muted-foreground">Loading installed worker harnesses…</p>}<div className="grid gap-2 sm:grid-cols-2"><Label className="space-y-1"><span>Max concurrent attempts</span><Input type="number" min={1} step={1} value={maxConcurrent} onChange={(event) => onConcurrent(event.target.value)} /></Label><Label className="space-y-1"><span>Max total attempts</span><Input type="number" min={1} step={1} value={maxAttempts} onChange={(event) => onAttempts(event.target.value)} /></Label></div></div>
}
