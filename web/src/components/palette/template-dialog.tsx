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
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import type { Template } from '@/lib/types'
import { field } from '@/lib/utils'
import { useStore } from '@/store'

/** Launches a saved task template into the active workspace. */
export function TemplateDialog({ onClose }: { onClose: () => void }) {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const [templates, setTemplates] = useState<Template[] | null>(null)
  const [name, setName] = useState('')
  const [launching, setLaunching] = useState(false)

  useEffect(() => {
    if (!workspaceID) return
    let stale = false
    setTemplates(null)
    setName('')
    api.templateList(workspaceID).then(
      (list) => {
        if (stale) return
        setTemplates(list)
        setName(list[0]?.name ?? '')
      },
      (err: unknown) => {
        if (stale) return
        setTemplates([])
        toast.error(`Listing templates failed: ${message(err)}`)
      },
    )
    return () => {
      stale = true
    }
  }, [workspaceID])

  const selected = templates?.find((t) => t.name === name)

  const launch = async () => {
    setLaunching(true)
    try {
      const { run } = await api.templateLaunch(workspaceID, name)
      // Seed the store so the terminal view attaches without a refetch.
      upsertRun(run)
      onClose()
      navigate('terminal', { runId: run.id })
      toast.success('Run launched')
    } catch (err) {
      setLaunching(false)
      toast.error(`Launch failed: ${message(err)}`)
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <DialogHeader>
          <DialogTitle>Launch from a template</DialogTitle>
          <DialogDescription>
            The template's saved task starts as a new run in the workspace.
          </DialogDescription>
        </DialogHeader>
        <form
          id="launch-template"
          className="min-h-0 space-y-4 overflow-y-auto -mx-1 px-1"
          onSubmit={(e) => {
            e.preventDefault()
            void launch()
          }}
        >
          <div className="rounded-md border border-border/70 bg-muted/30 px-3 py-2.5">
            <p className="text-xs font-medium uppercase tracking-[0.08em] text-muted-foreground">
              Target workspace
            </p>
            <p className="mt-1 text-sm" aria-label="Target workspace">
              {workspace ? (
                <>
                  <span className="font-medium">{workspace.name}</span>{' '}
                  <span className="font-mono text-xs text-muted-foreground">
                    {workspace.base_branch}
                  </span>
                </>
              ) : (
                <span className="text-muted-foreground">
                  Pick a workspace in the sidebar first.
                </span>
              )}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="template-launch-name">Template</Label>
            <select
              id="template-launch-name"
              className={field}
              value={name}
              disabled={!templates?.length}
              onChange={(e) => setName(e.target.value)}
            >
              {templates?.map((t) => (
                <option key={t.id} value={t.name}>
                  {t.name}
                </option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">
              Choose a saved task from this workspace.
            </p>
          </div>
          {templates === null && (
            <p className="text-sm text-muted-foreground">Loading templates...</p>
          )}
          {templates?.length === 0 && (
            <p className="text-sm text-muted-foreground">
              This workspace has no saved templates.
            </p>
          )}
          {selected && (
            <div className="rounded-md border border-border/70 bg-muted/20 px-3 py-2.5">
              <p className="text-xs font-medium uppercase tracking-[0.08em] text-muted-foreground">
                Saved task
              </p>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">
                {selected.harness} ({selected.mode}) - {selected.task}
              </p>
            </div>
          )}
        </form>
        <DialogFooter className="border-t pt-4">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="launch-template"
            disabled={launching || !name || !workspaceID}
          >
            {launching ? 'Launching...' : 'Launch'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
