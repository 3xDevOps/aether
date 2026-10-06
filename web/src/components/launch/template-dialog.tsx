import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { WorkspaceLine } from '@/components/launch/launch-options'
import { modeLabel } from '@/components/launch/modes'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Template } from '@/lib/types'
import { useStore } from '@/store'

export function TemplateDialog({ onClose }: { onClose: () => void }) {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const [templates, setTemplates] = useState<Template[] | null>(null)
  const [name, setName] = useState('')
  const [launching, setLaunching] = useState(false)
  const [error, setError] = useState<string | null>(null)

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
        setError(`Listing templates failed: ${message(err)}`)
      },
    )
    return () => {
      stale = true
    }
  }, [workspaceID])

  const selected = templates?.find((t) => t.name === name)

  const launch = async () => {
    setLaunching(true)
    setError(null)
    try {
      const { run } = await api.templateLaunch(workspaceID, name)
      // Seed the store so the terminal view attaches without a refetch.
      upsertRun(run)
      onClose()
      navigate('run', { runId: run.id })
      toast.success('Run launched')
    } catch (err) {
      setLaunching(false)
      setError(`Launch failed: ${message(err)}`)
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-[min(520px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <DialogHeader>
          <DialogTitle>Launch from a template</DialogTitle>
          <DialogDescription>The template&apos;s saved task starts as a new run.</DialogDescription>
        </DialogHeader>
        <form
          id="launch-template"
          className="-mx-1 flex min-h-0 flex-col gap-3 overflow-y-auto px-1 py-1"
          onSubmit={(e) => {
            e.preventDefault()
            void launch()
          }}
        >
          <div className="flex flex-col gap-1">
            <Label htmlFor="template-name">Template</Label>
            <Select value={name} onValueChange={setName}>
              <SelectTrigger id="template-name" disabled={!templates?.length}>
                <SelectValue placeholder={templates === null ? 'Loading templates…' : 'This workspace has no saved templates'} />
              </SelectTrigger>
              <SelectContent>
                {templates?.map((t) => (
                  <SelectItem key={t.id} value={t.name}>
                    {t.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {selected && (
            <div className="flex flex-col gap-1 rounded-panel bg-chrome px-3 py-2">
              <p className="flex items-center gap-1.5 text-ui-sm text-muted">
                <AgentGlyph agent={selected.harness} />
                {selected.harness} · {modeLabel(selected.mode)}
              </p>
              <p className="max-h-32 overflow-y-auto text-ui break-words whitespace-pre-wrap text-text">{selected.task}</p>
            </div>
          )}
          {error && <Callout tone="failed" role="alert">{error}</Callout>}
        </form>
        <DialogFooter className="sm:items-center">
          <div className="min-w-0 max-sm:order-last sm:mr-auto">
            <WorkspaceLine workspace={workspace} />
          </div>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="launch-template" disabled={launching || !name || !workspaceID}>
            {launching ? 'Launching…' : 'Launch'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
