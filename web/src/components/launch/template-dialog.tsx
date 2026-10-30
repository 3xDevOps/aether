import { useState } from 'react'
import { toast } from 'sonner'
import { WorkspaceLine } from '@/components/launch/launch-options'
import { SetupOutput, setupOutput } from '@/components/launch/setup-output'
import { launchParams } from '@/components/templates/placeholders'
import { keepEscapeForSearch } from '@/components/templates/template-list'
import { TemplatePicker } from '@/components/templates/template-picker'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import { useStore } from '@/store'
import { saveKey, useSaves } from '@/store/templates'

export function TemplateDialog({ onClose }: { onClose: () => void }) {
  const returnFocus = useReturnFocus()
  const workspaceID = useStore((s) => s.activeWorkspace)
  const workspace = useStore((s) => s.workspaces[s.activeWorkspace])
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const inFlight = useSaves()
  const [launching, setLaunching] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [setup, setSetup] = useState<string | null>(null)

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent
        {...returnFocus}
        onEscapeKeyDown={keepEscapeForSearch}
        className="flex h-[min(36rem,calc(100dvh-2rem))] max-w-[min(640px,calc(100%-2rem))] flex-col gap-0 overflow-hidden p-0"
      >
        <DialogHeader className="px-4 pt-4 pb-3">
          <DialogTitle>Launch from a template</DialogTitle>
          <DialogDescription>The filled-in task starts as a new session.</DialogDescription>
        </DialogHeader>
        <div className="flex min-h-0 flex-1 flex-col border-t border-seam">
          {workspaceID ? (
            <TemplatePicker
              workspaceID={workspaceID}
              action={(template, typed) => ({
                text: 'Launch',
                pending: launching ? 'Launching…' : undefined,
                // template.launch sends only the name, so an unanswered save would launch the server's older task.
                disabled: Boolean(inFlight[saveKey('template', workspaceID, template.name)]?.length),
                run: async () => {
                  setLaunching(true)
                  setError(null)
                  setSetup(null)
                  try {
                    const { run } = await api.templateLaunch(workspaceID, template.name, launchParams(template, typed))
                    // Seed the store so the terminal view attaches without a refetch.
                    upsertRun(run)
                    onClose()
                    navigate('run', { runId: run.id })
                    toast.success('Session launched')
                  } catch (err) {
                    setLaunching(false)
                    setError(`Launch failed: ${message(err)}`)
                    setSetup(setupOutput(err))
                  }
                },
              })}
            />
          ) : (
            <div className="p-4"><WorkspaceLine workspace={undefined} /></div>
          )}
        </div>
        {error && <Callout tone="failed" role="alert" className="mx-4 mt-3">{error}{setup && <SetupOutput output={setup} />}</Callout>}
        {workspace && <div className="px-4 py-3"><WorkspaceLine workspace={workspace} /></div>}
      </DialogContent>
    </Dialog>
  )
}
