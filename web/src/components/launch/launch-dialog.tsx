import { useEffect, useMemo, useRef, useState } from 'react'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Dialog as DialogPrimitive } from 'radix-ui'
import { toast } from 'sonner'
import { RefusalNotes } from '@/components/launch/agent-picker'
import { AccountOptions, automaticFallback, ChildRunsSwitch, type ChildRunsChoice, fallbackParams, RunLimitsOption, WorkspaceLine } from '@/components/launch/launch-options'
import { PermissionControl, permissionFeature } from '@/components/launch/permission-control'
import { ExtraRepos } from '@/components/launch/extra-repos'
import { needsTask, RunFields, useAgentChoice } from '@/components/launch/run-fields'
import { emptyLimits, parseLimits, useServerRunLimits } from '@/components/launch/run-limits'
import { fallbackFromSaved, fallbackToSaved, SaveAccountDefault, SavedIssues } from '@/components/launch/saved-defaults'
import { SetupOutput, setupOutput } from '@/components/launch/setup-output'
import { LoaderCircle, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { SlideOut } from '@/components/ui/slide-out'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { multiRepoFeature, multiRepoMethod } from '@/lib/repos'
import type { Member, Permission } from '@/lib/types'
import { useReturnFocus } from '@/lib/hooks'
import { cn, surface } from '@/lib/utils'
import { sameIdentity, useStore } from '@/store'
import { useChildSettings, useChildSettingsRead } from '@/store/children'
import { rememberChoice, saveLaunchDefaults, useLaunchDefaultsRead } from '@/store/launch-defaults'
import { useCapability, useSelf } from '@/store/hooks'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

/** `open` turns false when the panel starts to leave; the host unmounts it after the slide. */
export function LaunchDialog({ open = true }: { open?: boolean }) {
  const returnFocus = useReturnFocus()
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const close = useStore((s) => s.closePaletteDialog)
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const self = useStore((s) => s.info?.member)
  const features = useStore((s) => s.info?.features)
  const cap = useCapability()
  const workspaces = useStore((s) => s.workspaces)
  const extraOptions = useMemo(
    () => Object.values(workspaces).filter((item) => item.id !== workspaceID).sort((a, b) => a.name.localeCompare(b.name)),
    [workspaces, workspaceID],
  )
  const [extras, setExtras] = useState<string[]>([])
  const identity = useSelf()
  const ownAccountID = identity?.id ?? self?.id ?? ''
  const reposOffered = extraOptions.length > 0 && cap.hasMethod(multiRepoMethod) &&
    (features?.includes(multiRepoFeature) ?? false)
  // The panel stays open while the sidebar switches workspace, so the new primary may be among the picked extras.
  const pickedExtras = extras.filter((id) => extraOptions.some((item) => item.id === id))
  const extraRepos = reposOffered && pickedExtras.length ? { extra_workspace_ids: pickedExtras } : {}
  const [accounts, setAccounts] = useState<Member[]>(self ? [self] : [])
  const [accountsRead, setAccountsRead] = useState(false)
  const [account, setAccount] = useState(self?.id ?? '')
  const [fallback, setFallback] = useState(automaticFallback)
  const [savedAttempt, setSavedAttempt] = useState(0)
  const savedRead = useLaunchDefaultsRead(api, savedAttempt)
  const saved = savedRead.result
  const savesDefaults = cap.hasMethod('launch.defaults.set')
  // local: preset while the saved choices could not be read, so a later
  // successful read presets again.
  const [preset, setPreset] = useState<'pending' | 'local' | 'done'>('pending')
  const [pick, setPick] = useState(0)
  const childRunsOffered = cap.hasMethod('run.children.allow')
  const childSettings = useChildSettingsRead()
  const childReadError = useChildSettings((s) => s.error)
  const [childRuns, setChildRuns] = useState<ChildRunsChoice>('')
  const limitsOffered = cap.hasMethod('server.run_limits')
  const serverLimits = useServerRunLimits(api, limitsOffered)
  const [limits, setLimits] = useState(emptyLimits)
  const permissionOffered = features?.includes(permissionFeature) ?? false
  const [permission, setPermission] = useState<Permission>('default')
  // A saved choice read after the member picked a tier must not replace it.
  const permissionPicked = useRef(false)
  const choice = useAgentChoice({ account, ownAccountID, saved: savedRead, hold: preset === 'pending', pick })
  const { agents, agentError, harness, mode, model, effort, noAgents } = choice
  const [task, setTask] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [setup, setSetup] = useState<string | null>(null)
  const [launching, setLaunching] = useState(false)
  const late = usePastDeadline(launching)

  const errorCallout = useRef<HTMLDivElement>(null)
  const shown = useRef(true)
  const sharedAccount = account !== '' && account !== ownAccountID
  const accountName = accounts.find((member) => member.id === account)?.display_name ?? account
  const taskMissing = needsTask(mode, task)
  useEffect(() => {
    if (error) errorCallout.current?.scrollIntoView({ block: 'nearest' })
  }, [error])

  useEffect(() => {
    shown.current = open
    return () => { shown.current = false }
  }, [open])

  useEffect(() => {
    let live = true
    api.accountList().then((access) => {
      if (!live) return
      setAccounts(access.accounts)
      setAccount((current) => current || access.accounts[0]?.id || '')
    }).catch(() => {}).finally(() => {
      if (live) setAccountsRead(true)
    })
    return () => { live = false }
  }, [])

  useEffect(() => {
    if (preset === 'done' || !savedRead.fresh || !accountsRead) return
    if (preset === 'local' && (savedRead.error !== null || !saved)) return
    if (preset === 'local') setPick((n) => n + 1)
    setPreset(savedRead.error === null ? 'done' : 'local')
    if (!saved) return
    const { account_member_id: savedAccount, fallback: savedFallback, permission: savedPermission } = saved.defaults
    if (savedPermission && !permissionPicked.current) setPermission(savedPermission)
    const refused = saved.invalid?.some((issue) => issue.field === 'account_member_id')
    const usable = savedAccount && !refused && accounts.some((member) => member.id === savedAccount)
    setAccount((current) => usable ? savedAccount : ownAccountID || current)
    setFallback(fallbackFromSaved(savedFallback, accounts))
  }, [preset, savedRead.fresh, savedRead.error, saved, accounts, accountsRead, ownAccountID])

  const saveAccountDefault = () => saveLaunchDefaults((current) => ({
    ...current,
    account_member_id: account === ownAccountID ? '' : account,
    fallback: fallbackToSaved(fallback, accounts, account),
  }))

  const setUp = () => {
    close()
    navigate('agents')
  }

  const choosePermission = (next: Permission) => {
    permissionPicked.current = true
    setPermission(next)
    if (savesDefaults) saveLaunchDefaults((current) => ({ ...current, permission: next })).catch((err) => toast.error(`Permission not saved for your next launch: ${message(err)}`))
  }

  const launchRun = async () => {
    const trimmed = task.trim()
    const chosenModel = model.trim()
    const signedIn = sameIdentity()
    const parsedLimits = limitsOffered ? parseLimits(limits) : { limits: {} }
    if (parsedLimits.error !== undefined) throw new Error(parsedLimits.error)
    // An older server drops limits it does not know and reports success, so send none it has not confirmed.
    if (Object.keys(parsedLimits.limits).length && serverLimits.limits === null) {
      throw new Error(`Session limits need a server that answers server.run_limits: ${serverLimits.error ?? 'still reading the server limits, try again in a moment'}`)
    }
    const run = await api.runLaunch({
      workspace_id: workspaceID,
      harness,
      ...(trimmed ? { task: trimmed } : {}),
      mode,
      ...(chosenModel ? { model: chosenModel } : {}),
      ...(effort ? { effort } : {}),
      ...(permissionOffered ? { permission } : {}),
      ...(sharedAccount ? { account_member_id: account } : {}),
      ...fallbackParams(fallback, accounts, account),
      ...extraRepos,
      ...(childRunsOffered && childRuns && mode !== 'headless' ? { children: childRuns } : {}),
      ...(Object.keys(parsedLimits.limits).length ? { limits: parsedLimits.limits } : {}),
    })
    if (signedIn()) rememberChoice(harness, mode, chosenModel)
    upsertRun(run)
    close()
    navigate('run', { runId: run.id })
    toast.success('Session launched')
  }

  const submit = async () => {
    setLaunching(true)
    setError(null)
    setSetup(null)
    try {
      await launchRun()
    } catch (err) {
      // Closed while the request ran, the dialog can no longer show the refusal.
      if (!shown.current) toast.error(`Launch failed: ${message(err)}`)
      setLaunching(false)
      setError(message(err))
      setSetup(setupOutput(err))
    }
  }

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
            ? `A session on ${accountName}'s account uses ${accountName}'s login and your installation of the agent, or ${accountName}'s when you have none. Set up an agent opens Agents, where Add agent installs it in your environment.`
            : 'Set up an agent opens Agents, where Add agent installs it.'}
        </Callout>
      )}
      {agents && agentError === null && <RefusalNotes agents={agents} accountName={accountName} />}
      {savedRead.error && (
        <Callout
          tone="failed"
          role="alert"
          title="Your saved launch choices could not be read."
          actions={<Button type="button" size="sm" variant="secondary" onClick={() => setSavedAttempt((n) => n + 1)}>Try again</Button>}
        >
          {savedRead.error} {saved ? 'The choices shown are the ones last read from the server.' : 'The choices shown are this browser\'s last launch.'}
        </Callout>
      )}
      {saved && <SavedIssues title="Saved defaults not used" issues={(saved.invalid ?? []).filter((issue) => !issue.field.startsWith('agents.') || issue.field.split('.')[1] === harness)} />}
    </>
  )
  const options = accounts.length > 1 && (
    <AccountOptions label="Account" accounts={accounts} ownAccountID={ownAccountID} account={account} onChange={setAccount} fallback={fallback} onFallback={setFallback} save={savesDefaults && <SaveAccountDefault choice={JSON.stringify([account, fallback])} onSave={saveAccountDefault} />} />
  )

  return (
    <Dialog open={open} modal={false} onOpenChange={close}>
      <SlideOut open={open} width="26rem">
      <DialogPrimitive.Content
        {...returnFocus}
        forceMount
        // The panel stays open while the view behind it is used; only Escape and its own buttons close it.
        onInteractOutside={(event) => event.preventDefault()}
        className={cn(surface, 'relative grid h-full w-full grid-rows-[auto_minmax(0,1fr)_auto] gap-3 overflow-hidden rounded-none border-y-0 border-l-0 p-4 pt-[calc(1rem+var(--safe-top))] shadow-dialog outline-none')}
      >
        <DialogHeader>
          <DialogTitle>New session</DialogTitle>
          <DialogDescription>Starts an agent in its own container on the workspace's base branch.</DialogDescription>
        </DialogHeader>
        <form
          id="launch-form"
          className="-mx-1 flex min-h-0 flex-col gap-4 overflow-y-auto px-1 py-1 [&>*]:shrink-0"
          onSubmit={(event) => { event.preventDefault(); void submit() }}
        >
          {permissionOffered && <PermissionControl value={permission} onChange={choosePermission} disabled={launching} />}
          <RunFields choice={choice} task={task} onTask={setTask} onSetUp={setUp} onChoice={() => setError(null)} disabled={launching} autoFocus />
          {limitsOffered && <RunLimitsOption value={limits} onChange={setLimits} effective={serverLimits.limits?.effective} disabled={launching} />}
          {childRunsOffered && <ChildRunsSwitch value={childRuns} onChange={setChildRuns} enabled={childSettings?.enabled} readError={childReadError} unavailable={mode === 'headless' ? 'Background sessions cannot start subsessions.' : undefined} disabled={launching} />}
          {reposOffered && <ExtraRepos options={extraOptions} value={extras} onChange={setExtras} disabled={launching} />}
          {notes}
          {options}
          {error && <Callout ref={errorCallout} tone="failed" role="alert" title="Launch failed">{error}{setup && <SetupOutput output={setup} />}</Callout>}
        </form>
        <DialogFooter className="sm:items-center">
          <div className="min-w-0 max-sm:order-last sm:mr-auto">
            <WorkspaceLine workspace={workspace} />
          </div>
          <Button variant="secondary" onClick={close}>Cancel</Button>
          <Button
            type="submit"
            form="launch-form"
            disabled={launching || !workspaceID || !account || !harness || taskMissing}
          >
            {launching && <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden />}
            {late ? stillWaiting : launching ? 'Launching…' : 'Launch'}
          </Button>
        </DialogFooter>
        <DialogPrimitive.Close asChild>
          <Button variant="ghost" size="icon-sm" label="Close" className="absolute top-2 right-2">
            <X />
          </Button>
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
      </SlideOut>
    </Dialog>
  )
}
