import { useEffect, useState } from 'react'
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
import type { AgentInfo } from '@/lib/types'
import { useStore } from '@/store'

// The harness roster comes from agent.list so member-registered agents are
// launchable, not just the shipped names. "custom" is the deployment escape
// hatch, always offered; an unknown name is refused by the server, not here.

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
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [harness, setHarness] = useState('')
  const [task, setTask] = useState('')
  const [mode, setMode] = useState<LaunchMode>('tui')
  const [launching, setLaunching] = useState(false)
  // The server's rule: a taskless launch lands the member in the agent's
  // interactive TUI, but headless has no interactive surface, so it needs a
  // task to have anything to do. Say so here rather than sending a request
  // the gateway will refuse.
  const needsTask = mode === 'headless' && task.trim() === ''

  useEffect(() => {
    // agent.list is the roster this server can run. A failed fetch still
    // leaves the "custom" escape hatch selectable below.
    let live = true
    api
      .agentList()
      .then((list) => {
        if (!live) return
        setAgents(list)
        setHarness(list[0]?.name ?? 'custom')
      })
      .catch(() => {
        if (!live) return
        setAgents([])
        setHarness('custom')
      })
    return () => {
      live = false
    }
  }, [])

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
      })
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
              Harness
              <select
                className={field}
                value={harness}
                onChange={(e) => setHarness(e.target.value)}
              >
                {(agents ?? []).map((a) => (
                  <option key={a.name} value={a.name}>
                    {a.name}
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
          {needsTask && (
            <p className="text-xs text-muted-foreground">
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
            disabled={launching || !workspaceID || !harness || needsTask}
          >
            Launch
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
