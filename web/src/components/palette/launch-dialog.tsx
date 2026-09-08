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
import { api } from '@/lib/api'
import type { AgentInfo, Member } from '@/lib/types'
import { useStore } from '@/store'

// The harness roster comes from agent.list so member-registered agents are
// launchable, not just the shipped names; shipped and member entries are
// filtered to installed tools. agent.list never reports "custom" - it is the
// harness a deployment pins with --harness-definitions rather than a tool in
// the member's account - so the field offers it unconditionally.
const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

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

  // The agents view carries the install instructions and the terminal to run
  // them in, and needs no workspace, so it is the destination on every gateway.
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
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Launch a run</DialogTitle>
          <DialogDescription>
            The agent starts in a container on the workspace's base branch and
            drops you into its terminal.
          </DialogDescription>
        </DialogHeader>
        <form
          id="launch-run"
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault()
            void launch()
          }}
        >
          {/* Where the run lands, stated rather than asked: the sidebar's
              switcher is the one place scope changes. */}
          <p className="text-sm" aria-label="Target workspace">
            {workspace ? (
              <>
                Launching into <span className="font-medium">{workspace.name}</span>{' '}
                <span className="text-muted-foreground">({workspace.base_branch})</span>
              </>
            ) : (
              <span className="text-muted-foreground">
                Pick a workspace in the sidebar first.
              </span>
            )}
          </p>
          <label className="block space-y-1 text-sm">
            {mode === 'headless' ? 'Task (required)' : 'Task (optional)'}
            <textarea
              autoFocus
              rows={3}
              className={field}
              placeholder="What should the agent do?"
              value={task}
              onChange={(e) => setTask(e.target.value)}
            />
          </label>
          <div className="flex gap-3">
            <label className="flex-1 space-y-1 text-sm">
              Account
              <select
                className={field}
                value={account}
                onChange={(e) => setAccount(e.target.value)}
              >
                {accounts.map((member) => (
                  <option key={member.id} value={member.id}>
                    {member.display_name}
                    {member.id === ownAccountID ? ' (you)' : ' (shared)'}
                  </option>
                ))}
              </select>
            </label>
            <label className="flex-1 space-y-1 text-sm">
              Agent
              <select
                className={field}
                value={harness}
                disabled={harnessLoading || launching}
                onChange={(e) => setHarness(e.target.value)}
              >
                <option value="">Choose an agent</option>
                {installedAgents.map((agent) => (
                  <option key={agent.name} value={agent.name}>
                    {agent.name}
                  </option>
                ))}
                <option value="custom">custom</option>
              </select>
            </label>
            <label className="flex-1 space-y-1 text-sm">
              Mode
              <select
                className={field}
                value={mode}
                onChange={(e) => setMode(e.target.value as LaunchMode)}
              >
                <option value="tui">Interactive (tui)</option>
                <option value="headless">Headless</option>
              </select>
            </label>
          </div>
          {agentError && (
            <p role="alert" className="text-xs text-state-failed">{agentError}</p>
          )}
          {noAgents && !agentError && (
            <div className="space-y-2">
              <p className="text-sm">No agent is installed in this account.</p>
              <Button type="button" size="sm" onClick={setUpAgent}>
                Set up an agent
              </Button>
            </div>
          )}
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
            <p className="text-xs text-muted-foreground">
              This run uses the selected member&apos;s environment, agent login,
              profile, and vendor quota. You remain its owner and actor.
            </p>
          )}
          {needsTask && (
            <p id="launch-needs-task" className="text-xs text-muted-foreground">
              A headless run has no terminal to type into, so it needs a task.
            </p>
          )}
        </form>
        <DialogFooter>
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
