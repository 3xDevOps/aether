import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/components/palette/palette'
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

export function LaunchDialog() {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const close = useStore((s) => s.closePaletteDialog)
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [harness, setHarness] = useState('')
  const [launching, setLaunching] = useState(false)

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
      const run = await api.runLaunch({ workspace_id: workspaceID, harness })
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
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="launch-run"
            disabled={launching || !workspaceID || !harness}
          >
            Launch
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
