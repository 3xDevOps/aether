import { useState } from 'react'
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
import { useStore } from '@/store'

// The harness registry (internal/harness) is not on the dashboard's method
// allowlist, so the names are listed here. An unknown one is refused by the
// server, not by this form.
const harnesses = ['claude', 'codex', 'aider', 'opencode', 'custom']
const modes = ['tui', 'headless']

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50'

export function LaunchDialog() {
  const sessions = useStore((s) => s.sessions)
  const close = useStore((s) => s.closePaletteDialog)
  const navigate = useStore((s) => s.navigate)
  const [sessionID, setSessionID] = useState(Object.keys(sessions)[0] ?? '')
  const [task, setTask] = useState('')
  const [harness, setHarness] = useState(harnesses[0])
  const [mode, setMode] = useState(modes[0])
  const [launching, setLaunching] = useState(false)

  const launch = async () => {
    setLaunching(true)
    try {
      const run = await api.runLaunch({
        session_id: sessionID,
        task: task.trim(),
        harness,
        mode,
      })
      close()
      navigate('run', { runId: run.id })
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
            The agent starts in a container on the session's base branch.
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
          <label className="block space-y-1 text-sm">
            Session
            <select
              className={field}
              value={sessionID}
              onChange={(e) => setSessionID(e.target.value)}
            >
              {Object.values(sessions).map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>
          </label>
          <label className="block space-y-1 text-sm">
            Task
            <textarea
              autoFocus
              rows={4}
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
                {harnesses.map((h) => (
                  <option key={h} value={h}>
                    {h}
                  </option>
                ))}
              </select>
            </label>
            <label className="flex-1 space-y-1 text-sm">
              Mode
              <select
                className={field}
                value={mode}
                onChange={(e) => setMode(e.target.value)}
              >
                {modes.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
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
            disabled={launching || !task.trim() || !sessionID}
          >
            Launch
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
