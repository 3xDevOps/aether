import { useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
import type { AgentInfo, Member } from '@/lib/types'
import { useStore } from '@/store'

/** The two launch modes the server accepts; `tui` is its default. */
type LaunchMode = 'tui' | 'headless'

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
  const [accounts, setAccounts] = useState<Member[]>(self ? [self] : [])
  const [account, setAccount] = useState(self?.id ?? '')
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [agentError, setAgentError] = useState<string | null>(null)
  const [agentRefresh, setAgentRefresh] = useState(0)
  const [harness, setHarness] = useState('')
  const [task, setTask] = useState('')
  const [mode, setMode] = useState<LaunchMode>('tui')
  const [launching, setLaunching] = useState(false)
  const ownAccountID = self?.id ?? accounts[0]?.id
  const lastUsedHarness = useMemo(() => {
    const accountID = account || ownAccountID
    const remembered = accountID ? lastHarnessByAccount[accountID] : undefined
    if (remembered) return remembered
    return Object.values(runs)
      .filter((run) => (run.account_member_id ?? run.member_id) === accountID)
      .sort((a, b) => b.created_at.localeCompare(a.created_at))
      .find((run) => run.harness)?.harness
  }, [account, lastHarnessByAccount, ownAccountID, runs])
  // The server's rule: a taskless launch lands the member in the agent's
  // interactive TUI, but headless has no interactive surface, so it needs a
  // task to have anything to do. Say so here rather than sending a request
  // the gateway will refuse.
  const needsTask = mode === 'headless' && task.trim() === ''
  // The roster comes from agent.list so member-registered agents are
  // launchable, not just the shipped names, and both are filtered to what is
  // installed. agent.list never reports "custom": it is the harness a
  // deployment pins with --harness-definitions rather than a tool in the
  // member's account, so the field offers it unconditionally.
  const installedAgents = agents?.filter((agent) => agent.installed === true) ?? []
  const harnessLoading = agents === null
  const noAgents = !harnessLoading && installedAgents.length === 0

  useEffect(() => {
    let live = true
    api
      .accountList()
      .then((access) => {
        if (!live) return
        setAccounts(access.accounts)
        setAccount((current) => current || access.accounts[0]?.id || '')
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [])

  useEffect(() => {
    let live = true
    setAgents(null)
    setAgentError(null)
    setHarness('')
    api
      .agentList(account && account !== ownAccountID ? account : undefined)
      .then((list) => {
        if (!live) return
        setAgents(list)
        const installed = list.filter((agent) => agent.installed === true)
        setHarness((current) => {
          if (current && installed.some((agent) => agent.name === current)) {
            return current
          }
          if (lastUsedHarness && installed.some((agent) => agent.name === lastUsedHarness)) {
            return lastUsedHarness
          }
          return installed[0]?.name ?? ''
        })
      })
      .catch((err) => {
        if (!live) return
        setAgents([])
        setAgentError(message(err))
      })
    return () => {
      live = false
    }
  }, [account, lastUsedHarness, ownAccountID, agentRefresh])

  // The agents view is where an agent is added, and needs no workspace, so it
  // is the destination on every gateway.
  const setUpAgent = () => {
    close()
    navigate('agents')
  }

  const launch = async () => {
    setLaunching(true)
    try {
      // Only what the member actually chose goes on the wire: an empty task
      // and the default mode are the server's own defaults, and sending them
      // back would only restate them.
      const trimmed = task.trim()
      const run = await api.runLaunch({
        workspace_id: workspaceID,
        harness,
        ...(trimmed ? { task: trimmed } : {}),
        ...(mode === 'tui' ? {} : { mode }),
        ...(account && account !== ownAccountID
          ? { account_member_id: account }
          : {}),
      })
      rememberHarness(account || ownAccountID || '', harness)
      // Seed the store so the terminal view attaches without a refetch.
      upsertRun(run)
      close()
      navigate('terminal', { runId: run.id })
      toast.success('Run launched')
    } catch (err) {
      setLaunching(false)
      toast.error(`Launch failed: ${message(err)}`)
    }
  }

  return (
    <Dialog open onOpenChange={close}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0">
        <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
          <DialogTitle>Launch a run</DialogTitle>
          <DialogDescription>
            Start an agent in a container on the workspace&apos;s base branch. Interactive runs open
            a terminal; headless runs need a task.
          </DialogDescription>
        </DialogHeader>
        <form
          id="launch-run"
          className="min-h-0 min-w-0 space-y-3 overflow-y-auto px-3 py-3 sm:px-4"
          onSubmit={(e) => {
            e.preventDefault()
            void launch()
          }}
        >
          <div className="border-y border-border/70 px-2 py-2">
            <p className="text-xs font-medium text-muted-foreground">Target workspace</p>
            <p className="mt-0.5 break-words text-[13px]" aria-label="Target workspace">
              {workspace ? (
                <>
                  <span className="font-medium">{workspace.name}</span>{' '}
                  <span className="font-mono text-xs text-muted-foreground">
                    {workspace.base_branch}
                  </span>
                </>
              ) : (
                <span className="text-muted-foreground">Pick a workspace in the sidebar first.</span>
              )}
            </p>
          </div>
          <Label className="block space-y-1.5">
            <span>{mode === 'headless' ? 'Task (required)' : 'Task (optional)'}</span>
            <Textarea
              autoFocus
              required={mode === 'headless'}
              rows={3}
              placeholder="What should the agent do?"
              value={task}
              onChange={(e) => setTask(e.target.value)}
            />
            <span className="block text-xs leading-4 font-normal text-muted-foreground">
              {mode === 'headless'
                ? 'Headless runs start with this task and have no terminal.'
                : 'Leave blank to open an interactive terminal without a seeded task.'}
            </span>
          </Label>
          <div className="grid gap-2 sm:grid-cols-3">
            <div className="min-w-0 space-y-1.5 text-sm">
              <Label htmlFor="launch-account">Account</Label>
              <Select value={account} onValueChange={setAccount}>
                <SelectTrigger id="launch-account">
                  <SelectValue placeholder="Choose an account" />
                </SelectTrigger>
                <SelectContent>
                  {accounts.map((member) => (
                    <SelectItem key={member.id} value={member.id}>
                      {member.display_name}
                      {member.id === ownAccountID ? ' (you)' : ' (shared)'}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="min-w-0 space-y-1.5 text-sm">
              <Label htmlFor="launch-agent">Agent</Label>
              <Select value={harness} onValueChange={setHarness}>
                <SelectTrigger id="launch-agent" disabled={harnessLoading || launching}>
                  <SelectValue placeholder="Choose an agent" />
                </SelectTrigger>
                <SelectContent>
                  {installedAgents.map((agent) => (
                    <SelectItem key={agent.name} value={agent.name}>
                      {agent.name}
                    </SelectItem>
                  ))}
                  <SelectItem value="custom">custom</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="min-w-0 space-y-1.5 text-sm">
              <Label htmlFor="launch-mode">Mode</Label>
              <Select value={mode} onValueChange={(value) => setMode(value as LaunchMode)}>
                <SelectTrigger id="launch-mode">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="tui">Interactive (tui)</SelectItem>
                  <SelectItem value="headless">Headless</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          {agentError && (
            <p role="alert" className="break-words border-l-2 border-state-failed bg-state-failed/10 px-2 py-1.5 text-xs text-state-failed">
              {agentError}
            </p>
          )}
          {noAgents && !agentError && (
            <div className="border-y border-border/70 px-2 py-2">
              <p className="text-[13px] font-medium">No agent is installed in this account.</p>
              <p className="mt-0.5 text-xs leading-4 text-muted-foreground">
                Set one up before launching work for this account.
              </p>
              <Button type="button" size="sm" className="mt-2" onClick={setUpAgent}>
                Set up an agent
              </Button>
            </div>
          )}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={harnessLoading || launching}
              onClick={() => setAgentRefresh((current) => current + 1)}
            >
              Refresh agents
            </Button>
            {account && account !== ownAccountID && (
              <p className="max-w-[34ch] text-right text-xs leading-4 text-muted-foreground">
                Uses the selected member&apos;s environment, agent login, profile,
                and vendor quota. You remain its owner and actor.
              </p>
            )}
          </div>
          {needsTask && (
            <p id="launch-needs-task" className="border-l-2 border-state-needs-attention bg-state-needs-attention/10 px-2 py-1.5 text-xs text-muted-foreground">
              A headless run has no terminal to type into, so it needs a task.
            </p>
          )}
        </form>
        <DialogFooter className="border-t px-3 py-3 sm:px-4">
          <Button variant="outline" onClick={close}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="launch-run"
            aria-describedby={needsTask ? 'launch-needs-task' : undefined}
            disabled={launching || !workspaceID || !account || !harness || needsTask}
          >
            Launch
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
