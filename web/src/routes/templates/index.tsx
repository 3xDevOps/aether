// Templates: the active workspace's saved tasks and their cron schedules.
// Deletion asks first; launching goes straight to the new run. The workspace
// is chosen in the sidebar switcher, not here - this route acts on whatever
// that names, like every other scoped surface.

import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/lib/format'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import type { Schedule, Template } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { ScheduleEditor } from '@/routes/templates/schedule-editor'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { soleWorkspace } from '@/store/workspaces'

const harnesses = ['claude', 'codex', 'opencode', 'custom']

export function TemplatesRoute({ client = api }: RouteProps & { client?: Api }) {
  const workspaces = useStore((s) => s.workspaces)
  const active = useStore((s) => s.activeWorkspace)
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const caps = useCapability()
  const [templates, setTemplates] = useState<Template[]>([])
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [editing, setEditing] = useState<Template | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Template | null>(null)

  // Templates are per workspace on the wire, so unlike the run list this has
  // no "all" reading: before hydration names one, the sole workspace is the
  // only unambiguous answer.
  const workspaceID = active || soleWorkspace(workspaces)
  const workspace = workspaces[workspaceID]

  const refetch = useCallback(async () => {
    if (!workspaceID) return
    setTemplates(await client.templateList(workspaceID))
    if (caps.hasMethod('schedule.list')) {
      setSchedules(await client.scheduleList(workspaceID))
    }
  }, [client, workspaceID, caps])

  // Schedules are optional on legacy gateways; the templates themselves are
  // not, so their failures split: templates report, schedules stay empty.
  useEffect(() => {
    if (!workspaceID) return
    let cancelled = false
    client
      .templateList(workspaceID)
      .then((list) => {
        if (!cancelled) setTemplates(list)
      })
      .catch((err) => toast.error(message(err)))
    if (caps.hasMethod('schedule.list')) {
      client
        .scheduleList(workspaceID)
        .then((list) => {
          if (!cancelled) setSchedules(list)
        })
        .catch(() => {})
    }
    return () => {
      cancelled = true
    }
  }, [client, workspaceID, caps])

  const launch = async (template: Template) => {
    try {
      const result = await client.templateLaunch(workspaceID, template.name)
      // Seed the store so the terminal view attaches without a refetch.
      upsertRun(result.run)
      navigate('terminal', { runId: result.run.id })
      toast.success('Run launched')
    } catch (err) {
      toast.error(message(err))
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ViewHeader title="Templates" subtitle={workspace?.name} />
      <div className="flex flex-wrap items-center justify-between gap-3 border-b bg-muted/10 px-4 py-3 sm:px-6">
        <div>
          <p className="text-sm font-medium">Saved tasks</p>
          <p className="text-xs text-muted-foreground">
            Reusable prompts for this workspace and their schedules.
          </p>
        </div>
        {caps.hasMethod('template.save') && workspaceID && (
          <Button size="sm" onClick={() => setCreating(true)}>
            New template
          </Button>
        )}
      </div>

      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-5xl space-y-4 p-4 sm:p-6">
          {templates.length > 0 ? (
            <ul className="space-y-4">
              {templates.map((template) => (
                <li
                  key={template.id}
                  className="rounded-lg border bg-card/50 p-4 shadow-sm"
                  aria-label={`Template ${template.name}`}
                >
                  <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
                    <div className="min-w-0 flex-1 space-y-3">
                      <div className="flex flex-wrap items-center gap-2">
                        <h2 className="min-w-0 truncate text-base font-semibold">{template.name}</h2>
                        <span className="rounded-md border bg-muted/40 px-2 py-0.5 text-xs text-muted-foreground">
                          {template.harness}
                        </span>
                        <span className="rounded-md border bg-muted/40 px-2 py-0.5 text-xs text-muted-foreground">
                          {template.mode}
                        </span>
                      </div>
                      <div>
                        <p className="text-xs font-medium uppercase tracking-[0.08em] text-muted-foreground">
                          Task
                        </p>
                        <p className="mt-1 whitespace-pre-wrap text-sm leading-5 text-foreground/90">
                          {template.task}
                        </p>
                      </div>
                    </div>
                    <div className="flex w-full flex-col gap-2 sm:flex-row lg:w-auto lg:shrink-0">
                      <Button className="w-full sm:w-auto" size="sm" onClick={() => void launch(template)}>
                        Launch
                      </Button>
                      {caps.hasMethod('template.save') && (
                        <Button
                          className="w-full sm:w-auto"
                          size="sm"
                          variant="outline"
                          onClick={() => setEditing(template)}
                        >
                          Edit
                        </Button>
                      )}
                      {caps.hasMethod('template.delete') && (
                        <Button
                          className="w-full sm:w-auto"
                          size="sm"
                          variant="ghost"
                          onClick={() => setDeleting(template)}
                        >
                          Delete
                        </Button>
                      )}
                    </div>
                  </div>
                  {caps.hasMethod('schedule.save') && (
                    <div className="mt-4 rounded-md border border-border/70 bg-muted/20 p-3">
                      <div className="mb-2">
                        <p className="text-xs font-medium uppercase tracking-[0.08em] text-muted-foreground">
                          Schedule
                        </p>
                        <p className="mt-1 text-xs text-muted-foreground">
                          Runs in UTC. Leave blank to keep this template manual.
                        </p>
                      </div>
                      <ScheduleEditor
                        workspaceID={workspaceID}
                        template={template.name}
                        schedule={schedules.find((s) => s.template === template.name)}
                        client={client}
                        onChanged={() => void refetch()}
                      />
                    </div>
                  )}
                </li>
              ))}
            </ul>
          ) : (
            <div className="rounded-lg border border-dashed border-border/80 bg-muted/20 px-4 py-8 text-center">
              <p className="text-sm font-medium">No templates in this workspace yet.</p>
              <p className="mt-1 text-xs text-muted-foreground">
                Save a reusable task to launch it again or put it on a schedule.
              </p>
            </div>
          )}
        </div>
      </main>

      {(creating || editing) && (
        <TemplateForm
          workspaceID={workspaceID}
          template={editing ?? undefined}
          client={client}
          onClose={() => {
            setCreating(false)
            setEditing(null)
          }}
          onSaved={() => void refetch()}
        />
      )}
      {deleting && (
        <DeleteDialog
          workspaceID={workspaceID}
          template={deleting}
          client={client}
          onClose={() => setDeleting(null)}
          onDeleted={() => void refetch()}
        />
      )}
    </div>
  )
}

function TemplateForm({
  workspaceID,
  template,
  client,
  onClose,
  onSaved,
}: {
  workspaceID: string
  template?: Template
  client: Api
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(template?.name ?? '')
  const [task, setTask] = useState(template?.task ?? '')
  const [harness, setHarness] = useState(template?.harness ?? harnesses[0])
  const [mode, setMode] = useState(template?.mode ?? 'headless')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.templateSave({
        workspace_id: workspaceID,
        name: name.trim(),
        task: task.trim(),
        harness,
        mode,
      })
      onSaved()
      onClose()
      toast.success('Template saved')
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <DialogHeader>
          <DialogTitle>{template ? 'Edit template' : 'New template'}</DialogTitle>
          <DialogDescription>
            template.save replaces a template by name; the schedule keeps
            pointing at it.
          </DialogDescription>
        </DialogHeader>
        <form
          id="template-save"
          className="min-h-0 space-y-4 overflow-y-auto -mx-1 px-1"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="template-name">Name</Label>
            <Input
              id="template-name"
              autoFocus
              value={name}
              readOnly={template !== undefined}
              onChange={(e) => setName(e.target.value)}
              aria-describedby="template-name-help"
            />
            <p id="template-name-help" className="text-xs text-muted-foreground">
              {template ? 'Template names stay fixed when editing.' : 'Use a short name people can scan.'}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="template-task">Task</Label>
            <Textarea
              id="template-task"
              rows={5}
              value={task}
              onChange={(e) => setTask(e.target.value)}
              aria-describedby="template-task-help"
            />
            <p id="template-task-help" className="text-xs text-muted-foreground">
              This task is sent unchanged when the template launches.
            </p>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="template-harness">Agent</Label>
              <Select value={harness} onValueChange={setHarness}>
                <SelectTrigger id="template-harness">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {harnesses.map((h) => (
                    <SelectItem key={h} value={h}>
                      {h}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="template-mode">Mode</Label>
              <Select value={mode} onValueChange={setMode}>
                <SelectTrigger id="template-mode">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="tui">tui</SelectItem>
                  <SelectItem value="headless">headless</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          {error && (
            <p role="alert" className="text-xs text-state-failed">
              {error}
            </p>
          )}
        </form>
        <DialogFooter className="border-t pt-4">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="template-save"
            disabled={busy || !name.trim() || !task.trim()}
          >
            {busy ? 'Saving...' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function DeleteDialog({
  workspaceID,
  template,
  client,
  onClose,
  onDeleted,
}: {
  workspaceID: string
  template: Template
  client: Api
  onClose: () => void
  onDeleted: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const remove = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.templateDelete(workspaceID, template.name)
      onDeleted()
      onClose()
      toast.success('Template deleted')
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <AlertDialog
      open
      onOpenChange={() => {
        if (!busy) onClose()
      }}
    >
      <AlertDialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(560px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {template.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The template and its schedule are removed together.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="min-h-0 overflow-y-auto -mx-1 px-1">
          <p className="text-sm text-muted-foreground">
            This cannot be undone. Existing runs are not changed.
          </p>
          {error && (
            <p role="alert" className="mt-3 text-xs text-state-failed">
              {error}
            </p>
          )}
        </div>
        <AlertDialogFooter className="border-t pt-4">
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void remove()
            }}
          >
            {busy ? 'Deleting...' : 'Delete'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

registerRoute('templates', TemplatesRoute)
