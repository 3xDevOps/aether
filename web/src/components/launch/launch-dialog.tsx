import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { AgentPicker, launchable, RefusalNotes } from '@/components/launch/agent-picker'
import { AccountOptions, WorkspaceLine } from '@/components/launch/launch-options'
import { ModeControl } from '@/components/launch/mode-control'
import { modeRefusal } from '@/components/launch/modes'
import { needsTask, RunFields, useAgentChoice } from '@/components/launch/run-fields'
import { sameWorker, WorkerAgents, type WorkerChoice } from '@/components/launch/worker-agents'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import type { AgentInfo, LaunchMode, Member, MissionExecutionChoice } from '@/lib/types'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'

type Kind = 'run' | 'swarm'

function idempotencyKey(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `mission-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

// Outlives the dialog: a failed create may already have stored the mission,
// and resending the same contents under the same key replays it.
const swarmKeys = new Map<string, string>()

export function LaunchDialog() {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const close = useStore((s) => s.closePaletteDialog)
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const rememberLaunch = useStore((s) => s.rememberLaunch)
  const self = useStore((s) => s.info?.member)
  const capabilities = useStore((s) => s.capabilities)
  const cap = useCapability()
  const identity = useSelf()
  const swarmAvailable = cap.hasMethod('mission.create') && allowed('launch', identity)
  const ownAccountID = identity?.id ?? self?.id ?? ''
  const [kind, setKind] = useState<Kind>(() => {
    const { paletteDialog, route } = useStore.getState()
    return swarmAvailable && (paletteDialog === 'swarm' || route.name === 'missions') ? 'swarm' : 'run'
  })
  const [accounts, setAccounts] = useState<Member[]>(self ? [self] : [])
  const [account, setAccount] = useState(self?.id ?? '')
  const choice = useAgentChoice({ account, ownAccountID })
  const { agents, agentError, harness, mode, noAgents } = choice
  const [task, setTask] = useState('')
  const [objective, setObjective] = useState('')
  const [agentsByAccount, setAgentsByAccount] = useState<Record<string, AgentInfo[]> | null>(null)
  const [workerError, setWorkerError] = useState<string | null>(null)
  const [workers, setWorkers] = useState<WorkerChoice[]>([])
  const [workerMode, setWorkerMode] = useState<LaunchMode>('headless')
  const [error, setError] = useState<string | null>(null)
  const [launching, setLaunching] = useState(false)

  const errorCallout = useRef<HTMLDivElement>(null)
  const sharedAccount = account !== '' && account !== ownAccountID
  const accountName = accounts.find((member) => member.id === account)?.display_name ?? account
  const accountField = kind === 'swarm' ? 'Integrator account' : 'Account'
  const taskMissing = kind === 'run' && needsTask(mode, task)
  // Availability can drop while open; stay on Swarm and say why rather than
  // switching under the reader.
  const swarmUnavailable = kind !== 'swarm' || swarmAvailable
    ? null
    : capabilities === null
      ? 'Swarm launch is unavailable: the server did not report its capabilities. Switch to Run to launch a run.'
      : !allowed('launch', identity)
        ? 'Swarm launch is unavailable: your role cannot launch.'
        : 'Swarm launch is unavailable: the gateway does not offer mission.create. Switch to Run to launch a run.'

  useEffect(() => {
    if (error) errorCallout.current?.scrollIntoView({ block: 'nearest' })
  }, [error])

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
    if (kind !== 'swarm' || !accounts.length) return
    let live = true
    setWorkerError(null)
    Promise.all(accounts.map(async (member) => {
      const list = await api.agentList(member.id === ownAccountID ? undefined : member.id)
      return [member.id, list.filter((item) => item.installed === true)] as const
    })).then((entries) => {
      if (!live) return
      const byAccount: Record<string, AgentInfo[]> = Object.fromEntries(entries)
      setAgentsByAccount(byAccount)
      setWorkers((current) => {
        if (current.length) return current.filter((choice) => (byAccount[choice.account_member_id] ?? []).some((item) => item.name === choice.harness && launchable(item)))
        const fallback = (byAccount[account] ?? []).find(launchable)
        return fallback ? [{ account_member_id: account, harness: fallback.name }] : []
      })
    }).catch((err) => {
      if (live) setWorkerError(message(err))
    })
    return () => { live = false }
  }, [accounts, account, kind, ownAccountID])

  const setUp = () => {
    close()
    navigate('agents')
  }

  const toggleWorker = (choice: WorkerChoice, on: boolean) => {
    setWorkers((current) => on
      ? current.some((item) => sameWorker(item, choice)) ? current : [...current, choice]
      : current.filter((item) => !sameWorker(item, choice)))
  }

  const launchRun = async () => {
    const trimmed = task.trim()
    const run = await api.runLaunch({
      workspace_id: workspaceID,
      harness,
      ...(trimmed ? { task: trimmed } : {}),
      ...(mode === 'tui' ? {} : { mode }),
      ...(sharedAccount ? { account_member_id: account } : {}),
    })
    rememberLaunch(harness, mode)
    upsertRun(run)
    close()
    navigate('terminal', { runId: run.id })
    toast.success('Run launched')
  }

  const createSwarm = async () => {
    const trimmed = objective.trim()
    const accountableHumanID = self?.id ?? ''
    if (!workspaceID || !accountableHumanID || !trimmed || !account || !harness) {
      throw new Error('Enter an objective and choose an integrator agent.')
    }
    const integrator: MissionExecutionChoice = { account_member_id: account, harness, mode: 'tui' }
    const workerChoices = workers.map((choice): MissionExecutionChoice => {
      const info = agentsByAccount?.[choice.account_member_id]?.find((item) => item.name === choice.harness)
      return { ...choice, mode: modeRefusal(info, choice.harness, workerMode) ? 'tui' : workerMode }
    })
    const contents = {
      workspace_id: workspaceID,
      objective: trimmed,
      accountable_human_id: accountableHumanID,
      integrator,
      // Sorted: the idempotency key and the server's receipt check both
      // follow the serialized list.
      execution_choices: [integrator, ...workerChoices.filter((choice) => !sameWorker(choice, integrator) || choice.mode !== integrator.mode)]
        .sort((a, b) => a.account_member_id.localeCompare(b.account_member_id) || a.harness.localeCompare(b.harness) || a.mode.localeCompare(b.mode)),
    }
    const serialized = JSON.stringify(contents)
    const key = swarmKeys.get(serialized) ?? idempotencyKey()
    swarmKeys.set(serialized, key)
    const result = await api.missionCreate({ ...contents, idempotency_key: key })
    swarmKeys.delete(serialized)
    close()
    navigate('missions', { missionId: result.mission.id })
    toast.success('Swarm created')
  }

  const submit = async () => {
    setLaunching(true)
    setError(null)
    try {
      await (kind === 'run' ? launchRun() : createSwarm())
    } catch (err) {
      setLaunching(false)
      setError(message(err))
    }
  }

  const integratorPicker = agents === null
    ? <p className="text-ui-sm text-muted">Loading agents…</p>
    : (
        <AgentPicker
          label="Integrator agent"
          agents={agents}
          value={harness}
          onChange={(name) => { setError(null); choice.choose(name) }}
          onSetUp={noAgents ? undefined : setUp}
          disabled={launching}
        />
      )
  const notes = (
    <>
      {agentError && <Callout tone="failed" role="alert">{agentError}</Callout>}
      {noAgents && (
        <Callout
          tone="neutral"
          title={sharedAccount ? `Neither you nor ${accountName} has an agent installed.` : 'No agent is installed in your environment.'}
          actions={<Button type="button" size="sm" onClick={setUp}>Set up an agent</Button>}
        >
          {sharedAccount
            ? `A run on ${accountName}'s account uses ${accountName}'s login and your installation of the agent, or ${accountName}'s when you have none. Set up an agent opens Agents, where Add agent installs it in your environment.`
            : 'Set up an agent opens Agents, where Add agent installs it.'}
        </Callout>
      )}
      {agents && agentError === null && <RefusalNotes agents={agents} accountName={accountName} accountField={accountField} />}
    </>
  )
  const options = accounts.length > 1 && (
    <AccountOptions label={accountField} accounts={accounts} ownAccountID={ownAccountID} account={account} onChange={setAccount} />
  )

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent className="max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <Tabs value={kind} onValueChange={(value) => { setKind(value as Kind); setError(null) }} className="contents">
          <DialogHeader>
            <DialogTitle>{kind === 'swarm' ? 'New swarm' : 'New run'}</DialogTitle>
            <DialogDescription>
              {kind === 'swarm'
                ? 'The integrator plans the work, starts a worker run per task, and combines the results.'
                : 'Starts an agent in its own container on the workspace\'s base branch.'}
            </DialogDescription>
            {(swarmAvailable || kind === 'swarm') && (
              <TabsList aria-label="Launch">
                <TabsTrigger value="run">Run</TabsTrigger>
                <TabsTrigger value="swarm">Swarm</TabsTrigger>
              </TabsList>
            )}
          </DialogHeader>
          <form
            id="launch-form"
            className="-mx-1 flex min-h-0 flex-col gap-4 overflow-y-auto px-1 py-1 [&>*]:shrink-0"
            onSubmit={(event) => { event.preventDefault(); void submit() }}
          >
            <TabsContent value="run" className="flex flex-col gap-4">
              <RunFields choice={choice} task={task} onTask={setTask} onSetUp={setUp} onChoice={() => setError(null)} disabled={launching} autoFocus />
            </TabsContent>
            <TabsContent value="swarm" className="flex flex-col gap-4">
              {swarmUnavailable && <Callout tone="needs-you" role="status" id="launch-swarm-unavailable">{swarmUnavailable}</Callout>}
              <FormField label="Objective" help="Required. The integrator asks you clarifying questions only if it needs answers, then runs the swarm to completion.">
                <Textarea autoFocus required rows={3} placeholder="What outcome should the integrator coordinate?" value={objective} onChange={(event) => setObjective(event.target.value)} />
              </FormField>
              {integratorPicker}
              <WorkerAgents accounts={accounts} agentsByAccount={agentsByAccount} error={workerError} choices={workers} mode={workerMode} onToggle={toggleWorker} />
              <ModeControl
                label="Worker mode"
                value={workerMode}
                onChange={setWorkerMode}
                onSetUp={setUp}
                help="The integrator runs in Standard. A worker whose agent cannot use the chosen mode runs in Standard."
              />
            </TabsContent>
            {notes}
            {options}
            {error && <Callout ref={errorCallout} tone="failed" role="alert" title={kind === 'swarm' ? 'Swarm not created' : 'Launch failed'}>{error}</Callout>}
          </form>
        </Tabs>
        <DialogFooter className="sm:items-center">
          <div className="min-w-0 max-sm:order-last sm:mr-auto">
            <WorkspaceLine workspace={workspace} />
          </div>
          <Button variant="secondary" onClick={close}>Cancel</Button>
          <Button
            type="submit"
            form="launch-form"
            aria-describedby={swarmUnavailable ? 'launch-swarm-unavailable' : undefined}
            disabled={launching || !workspaceID || !account || !harness || taskMissing || swarmUnavailable !== null || (kind === 'swarm' && objective.trim() === '')}
          >
            {kind === 'swarm' ? 'Create swarm' : 'Launch'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
