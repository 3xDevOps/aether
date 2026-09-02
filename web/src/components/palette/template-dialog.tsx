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
import type { Template } from '@/lib/types'
import { useStore } from '@/store'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

/** Launches a saved task template into the active workspace. */
export function TemplateDialog({ onClose }: { onClose: () => void }) {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const navigate = useStore((s) => s.navigate)
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
      onClose()
      navigate('run', { runId: run.id })
      toast.success('Run launched')
    } catch (err) {
      setLaunching(false)
      toast.error(`Launch failed: ${message(err)}`)
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Launch from a template</DialogTitle>
          <DialogDescription>
            The template's saved task starts as a new run in the workspace.
          </DialogDescription>
        </DialogHeader>
        <form
          id="launch-template"
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
            Template
            <select
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
          </label>
          {templates?.length === 0 && (
            <p className="text-xs text-muted-foreground">
              This workspace has no saved templates.
            </p>
          )}
          {selected && (
            <p className="line-clamp-3 text-xs text-muted-foreground">
              {selected.harness} ({selected.mode}) - {selected.task}
            </p>
          )}
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="launch-template"
            disabled={launching || !name || !workspaceID}
          >
            Launch
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
