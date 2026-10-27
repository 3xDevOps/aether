import { useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import { AgentPicker, launchable, RefusalNotes } from '@/components/launch/agent-picker'
import { LaunchChildHint } from '@/components/child-nudge'
import { AccountOptions, automaticFallback, ChildRunsOption, type ChildRunsChoice, fallbackParams, RunLimitsOption, WorkspaceLine } from '@/components/launch/launch-options'
import { ModeControl } from '@/components/launch/mode-control'
import { integratorModes } from '@/components/launch/modes'
import { ExtraRepos } from '@/components/launch/extra-repos'
import { ModelField, needsTask, RunFields, useAgentChoice } from '@/components/launch/run-fields'
import { emptyLimits, parseLimits, useServerRunLimits } from '@/components/launch/run-limits'
import { SetupOutput, setupOutput } from '@/components/launch/setup-output'
import { sameWorker, WorkerAgents, type WorkerChoice, workerMode, workerRefusals } from '@/components/launch/worker-agents'
import { LoaderCircle } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { randomID } from '@/lib/random'
import { multiRepoFeature, multiRepoMethod, swarmReposFeature } from '@/lib/repos'
import type { AgentInfo, LaunchMode, Member, MissionExecutionChoice } from '@/lib/types'
import { useReturnFocus } from '@/lib/hooks'
import { sameIdentity, useStore } from '@/store'
import { useChildSettingsRead } from '@/store/children'
import { useCapability, useSelf } from '@/store/hooks'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

type Kind = 'run' | 'swarm'

// Outlives the dialog: a failed create may already have stored the mission,
// and resending the same contents under the same key replays it.
const swarmKeys = new Map<string, string>()

export function LaunchDialog() {
  const returnFocus = useReturnFocus()
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const close = useStore((s) => s.closePaletteDialog)
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const rememberLaunch = useStore((s) => s.rememberLaunch)
  const self = useStore((s) => s.info?.member)
  const features = useStore((s) => s.info?.features)
  const capabilities = useStore((s) => s.capabilities)
  const cap = useCapability()
  const workspaces = useStore((s) => s.workspaces)
  const extraOptions = useMemo(
    () => Object.values(workspaces).filter((item) => item.id !== workspaceID).sort((a, b) => a.name.localeCompare(b.name)),
    [workspaces, workspaceID],
  )
  const [extras, setExtras] = useState<string[]>([])
  const identity = useSelf()
  const swarmAvailable = cap.hasMethod('mission.create') && allowed('launch', identity)
  const ownAccountID = identity?.id ?? self?.id ?? ''
  const [kind, setKind] = useState<Kind>(() => {
    const { paletteDialog, route } = useStore.getState()
    return swarmAvailable && (paletteDialog === 'swarm' || route.name === 'missions') ? 'swarm' : 'run'
  })
  const reposOffered = extraOptions.length > 0 && cap.hasMethod(multiRepoMethod) &&
    (features?.includes(kind === 'swarm' ? swarmReposFeature : multiRepoFeature) ?? false)
  const extraRepos = reposOffered && extras.length ? { extra_workspace_ids: extras } : {}
  const [accounts, setAccounts] = useState<Member[]>(self ? [self] : [])
  const [account, setAccount] = useState(self?.id ?? '')
  const [fallback, setFallback] = useState(automaticFallback)
  const childRunsOffered = cap.hasMethod('run.children.allow')
  const childSettings = useChildSettingsRead()
  const [childRuns, setChildRuns] = useState<ChildRunsChoice>('')
  const limitsOffered = cap.hasMethod('server.run_limits')
  const serverLimits = useServerRunLimits(api, limitsOffered)
  const [limits, setLimits] = useState(emptyLimits)
  const choice = useAgentChoice({ account, ownAccountID })
  const { agents, agentError, agent, harness, mode, model, noAgents } = choice
  const [task, setTask] = useState('')
  const [objective, setObjective] = useState('')
  const [agentsByAccount, setAgentsByAccount] = useState<Record<string, AgentInfo[]> | null>(null)
  const [workerError, setWorkerError] = useState<string | null>(null)
  const [workers, setWorkers] = useState<WorkerChoice[]>([])
  const [chosenWorkerMode, setWorkerMode] = useState<LaunchMode>('acp')
  const { refused: integratorRefused, mode: integratorMode } = integratorModes(agent, harness, mode)
  const workerRefused = workerRefusals(workers, agentsByAccount)
  const shownWorkerMode = ([chosenWorkerMode, 'acp', 'tui'] as const).find((next) => !workerRefused[next]) ?? 'headless'
  const [error, setError] = useState<string | null>(null)
  const [setup, setSetup] = useState<string | null>(null)
  const [launching, setLaunching] = useState(false)
  const late = usePastDeadline(launching)

  const errorCallout = useRef<HTMLDivElement>(null)
  const shown = useRef(true)
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
    shown.current = true
    return () => { shown.current = false }
  }, [])

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

  const setWorkerModel = (choice: WorkerChoice, next: string) => {
    setWorkers((current) => current.map((item) => sameWorker(item, choice) ? { ...item, model: next } : item))
  }

  const launchRun = async () => {
    const trimmed = task.trim()
    const chosenModel = model.trim()
    const signedIn = sameIdentity()
    const parsedLimits = limitsOffered ? parseLimits(limits) : { limits: {} }
    if (parsedLimits.error !== undefined) throw new Error(parsedLimits.error)
    const run = await api.runLaunch({
      workspace_id: workspaceID,
      harness,
      ...(trimmed ? { task: trimmed } : {}),
      mode,
      ...(chosenModel ? { model: chosenModel } : {}),
      ...(sharedAccount ? { account_member_id: account } : {}),
      ...fallbackParams(fallback, accounts, account),
      ...extraRepos,
      ...(childRunsOffered && childRuns ? { children: childRuns } : {}),
      ...(Object.keys(parsedLimits.limits).length ? { limits: parsedLimits.limits } : {}),
    })
    if (signedIn()) rememberLaunch(harness, mode, chosenModel)
    upsertRun(run)
    close()
    navigate('run', { runId: run.id })
    toast.success('Run launched')
  }

  const createSwarm = async () => {
    const trimmed = objective.trim()
    const accountableHumanID = self?.id ?? ''
    if (!workspaceID || !accountableHumanID || !trimmed || !account || !harness || !integratorMode) {
      throw new Error('Enter an objective and choose an integrator agent.')
    }
    const integratorModel = model.trim()
    const integrator: MissionExecutionChoice = { account_member_id: account, harness, mode: integratorMode, ...(integratorModel ? { model: integratorModel } : {}) }
    const workerChoices = workers.map((choice): MissionExecutionChoice => {
      const info = agentsByAccount?.[choice.account_member_id]?.find((item) => item.name === choice.harness)
      const workerModel = choice.model?.trim()
      return {
        account_member_id: choice.account_member_id,
        harness: choice.harness,
        mode: workerMode(info, choice.harness, shownWorkerMode),
        ...(workerModel ? { model: workerModel } : {}),
      }
    })
    const contents = {
      workspace_id: workspaceID,
      objective: trimmed,
      accountable_human_id: accountableHumanID,
      integrator,
      // Sorted: the idempotency key and the server's receipt check both
      // follow the serialized list.
      execution_choices: [integrator, ...workerChoices.filter((choice) => !sameWorker(choice, integrator) || choice.mode !== integrator.mode)]
        .sort((a, b) => a.account_member_id.localeCompare(b.account_member_id) || a.harness.localeCompare(b.harness) || a.mode.localeCompare(b.mode) || (a.model ?? '').localeCompare(b.model ?? '')),
      ...fallbackParams(fallback, accounts, ''),
      ...extraRepos,
    }
    const serialized = JSON.stringify(contents)
    const key = swarmKeys.get(serialized) ?? randomID('mission')
    swarmKeys.set(serialized, key)
    const signedIn = sameIdentity()
    const result = await api.missionCreate({ ...contents, idempotency_key: key })
    swarmKeys.delete(serialized)
    if (signedIn()) rememberLaunch(harness, integratorMode, integratorModel)
    close()
    navigate('missions', { missionId: result.mission.id })
    toast.success('Swarm created')
  }

  const submit = async () => {
    setLaunching(true)
    setError(null)
    setSetup(null)
    try {
      await (kind === 'run' ? launchRun() : createSwarm())
    } catch (err) {
      // Closed while the request ran, the dialog can no longer show the refusal.
      if (!shown.current) toast.error(`${kind === 'swarm' ? 'Swarm not created' : 'Launch failed'}: ${message(err)}`)
      setLaunching(false)
      setError(message(err))
      setSetup(setupOutput(err))
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
    <AccountOptions label={accountField} accounts={accounts} ownAccountID={ownAccountID} account={account} onChange={setAccount} fallback={fallback} onFallback={setFallback} swarm={kind === 'swarm'} />
  )

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent {...returnFocus} className="max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
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
              {limitsOffered && <RunLimitsOption value={limits} onChange={setLimits} effective={serverLimits.limits?.effective} error={serverLimits.error} onRetry={serverLimits.reload} disabled={launching} />}
              {childRunsOffered && (
                <>
                  <ChildRunsOption value={childRuns} onChange={setChildRuns} enabled={childSettings?.enabled} disabled={launching} />
                  <LaunchChildHint launchable={agents?.filter(launchable).length ?? 0} onLeave={close} />
                </>
              )}
            </TabsContent>
            <TabsContent value="swarm" className="flex flex-col gap-4">
              {swarmUnavailable && <Callout tone="needs-you" role="status" id="launch-swarm-unavailable">{swarmUnavailable}</Callout>}
              <FormField label="Objective" help="Required. The integrator asks you clarifying questions only if it needs answers, then runs the swarm to completion.">
                <Textarea autoFocus required rows={3} placeholder="What outcome should the integrator coordinate?" value={objective} onChange={(event) => setObjective(event.target.value)} />
              </FormField>
              {integratorPicker}
              {harness && (
                <ModeControl
                  label="Integrator mode"
                  value={integratorMode}
                  onChange={(next) => { setError(null); choice.setMode(next) }}
                  refused={integratorRefused}
                  onSetUp={setUp}
                />
              )}
              {harness && <ModelField label="Integrator model" harness={harness} accountMemberID={choice.accountMemberID} value={model} onChange={(next) => { setError(null); choice.setModel(next) }} disabled={launching} />}
              <WorkerAgents accounts={accounts} agentsByAccount={agentsByAccount} error={workerError} choices={workers} mode={shownWorkerMode} ownAccountID={ownAccountID} onToggle={toggleWorker} onModel={setWorkerModel} />
              <ModeControl
                label="Worker mode"
                value={shownWorkerMode}
                onChange={setWorkerMode}
                refused={workerRefused}
                onSetUp={setUp}
                help="A worker whose agent cannot use this mode runs in the mode its row names."
              />
            </TabsContent>
            {reposOffered && <ExtraRepos options={extraOptions} value={extras} onChange={setExtras} disabled={launching} swarm={kind === 'swarm'} />}
            {notes}
            {options}
            {error && <Callout ref={errorCallout} tone="failed" role="alert" title={kind === 'swarm' ? 'Swarm not created' : 'Launch failed'}>{error}{setup && <SetupOutput output={setup} />}</Callout>}
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
            disabled={launching || !workspaceID || !account || !harness || taskMissing || swarmUnavailable !== null || (kind === 'swarm' && (objective.trim() === '' || !integratorMode))}
          >
            {launching && <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden />}
            {late ? stillWaiting : launching ? (kind === 'swarm' ? 'Creating swarm…' : 'Launching…') : kind === 'swarm' ? 'Create swarm' : 'Launch'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
