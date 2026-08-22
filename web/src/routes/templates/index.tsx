// Templates: a session's saved tasks, their cron schedules, and the session
// admin verbs (budget, steering settings) that live on the same header.
// Deletion asks first; launching goes straight to the new run.

import { useCallback, useEffect, useState } from 'react'
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
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import type { Schedule, Template } from '@/lib/types'
import { BudgetDialog, SessionSettingsDialog } from '@/routes/admin-dialogs'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { ScheduleEditor } from '@/routes/templates/schedule-editor'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const harnesses = ['claude', 'codex', 'aider', 'opencode', 'custom']

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50'

export function TemplatesRoute({ client = api }: RouteProps & { client?: Api }) {
  const sessions = useStore((s) => s.sessions)
  const navigate = useStore((s) => s.navigate)
  const caps = useCapability()
  const [sessionID, setSessionID] = useState(Object.keys(sessions)[0] ?? '')
  const [templates, setTemplates] = useState<Template[]>([])
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [editing, setEditing] = useState<Template | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Template | null>(null)
  const [dialog, setDialog] = useState<'budget' | 'settings' | null>(null)

  const refetch = useCallback(async () => {
    if (!sessionID) return
    setTemplates(await client.templateList(sessionID))
    if (caps.hasMethod('schedule.list')) {
      setSchedules(await client.scheduleList(sessionID))
    }
  }, [client, sessionID, caps])

  // Schedules are optional on legacy gateways; the templates themselves are
  // not, so their failures split: templates report, schedules stay empty.
  useEffect(() => {
    if (!sessionID) return
    let cancelled = false
    client
      .templateList(sessionID)
      .then((list) => {
        if (!cancelled) setTemplates(list)
      })
      .catch((err) => toast.error(message(err)))
    if (caps.hasMethod('schedule.list')) {
      client
        .scheduleList(sessionID)
        .then((list) => {
          if (!cancelled) setSchedules(list)
        })
        .catch(() => {})
    }
    return () => {
      cancelled = true
    }
  }, [client, sessionID, caps])

  const launch = async (template: Template) => {
    try {
      const result = await client.templateLaunch(sessionID, template.name)
      navigate('run', { runId: result.run.id })
      toast.success('Run launched')
    } catch (err) {
      toast.error(message(err))
    }
  }

  const session = sessions[sessionID]

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title="Templates" subtitle={session?.name} />
      <div className="flex items-center gap-2 border-b px-4 py-2">
        <select
          aria-label="Session"
          className="rounded-md border bg-background px-2 py-1 text-sm"
          value={sessionID}
          onChange={(e) => setSessionID(e.target.value)}
        >
          {Object.values(sessions).map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        <span className="flex-1" />
        {caps.hasMethod('budget.set') && sessionID && (
          <Button size="sm" variant="outline" onClick={() => setDialog('budget')}>
            Budget
          </Button>
        )}
        {caps.hasMethod('session.settings') && sessionID && (
          <Button size="sm" variant="outline" onClick={() => setDialog('settings')}>
            Session settings
          </Button>
        )}
        {caps.hasMethod('template.save') && sessionID && (
          <Button size="sm" onClick={() => setCreating(true)}>
            New template
          </Button>
        )}
      </div>

      <div className="flex-1 overflow-y-auto p-4">
        <ul className="space-y-4">
          {templates.map((template) => (
            <li key={template.id} className="space-y-2 rounded-md border bg-card p-3">
              <div className="flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate text-sm font-medium">
                  {template.name}
                </span>
                <span className="text-xs text-muted-foreground">
                  {template.harness} / {template.mode}
                </span>
                <Button size="sm" onClick={() => void launch(template)}>
                  Launch
                </Button>
                {caps.hasMethod('template.save') && (
                  <Button size="sm" variant="ghost" onClick={() => setEditing(template)}>
                    Edit
                  </Button>
                )}
                {caps.hasMethod('template.delete') && (
                  <Button size="sm" variant="ghost" onClick={() => setDeleting(template)}>
                    Delete
                  </Button>
                )}
              </div>
              <p className="text-xs whitespace-pre-wrap text-muted-foreground">
                {template.task}
              </p>
              {caps.hasMethod('schedule.save') && (
                <ScheduleEditor
                  sessionID={sessionID}
                  template={template.name}
                  schedule={schedules.find((s) => s.template === template.name)}
                  client={client}
                  onChanged={() => void refetch()}
                />
              )}
            </li>
          ))}
        </ul>
        {templates.length === 0 && (
          <p className="text-sm text-muted-foreground">
            No templates in this session yet.
          </p>
        )}
      </div>

      {(creating || editing) && (
        <TemplateForm
          sessionID={sessionID}
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
          sessionID={sessionID}
          template={deleting}
          client={client}
          onClose={() => setDeleting(null)}
          onDeleted={() => void refetch()}
        />
      )}
      {dialog === 'budget' && (
        <BudgetDialog sessionID={sessionID} client={client} onClose={() => setDialog(null)} />
      )}
      {dialog === 'settings' && (
        <SessionSettingsDialog
          sessionID={sessionID}
          client={client}
          onClose={() => setDialog(null)}
        />
      )}
    </div>
  )
}

function TemplateForm({
  sessionID,
  template,
  client,
  onClose,
  onSaved,
}: {
  sessionID: string
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
        session_id: sessionID,
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
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{template ? 'Edit template' : 'New template'}</DialogTitle>
          <DialogDescription>
            template.save replaces a template by name; the schedule keeps
            pointing at it.
          </DialogDescription>
        </DialogHeader>
        <form
          id="template-save"
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <label className="block space-y-1 text-sm">
            Name
            <input
              autoFocus
              className={field}
              value={name}
              readOnly={template !== undefined}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label className="block space-y-1 text-sm">
            Task
            <textarea
              rows={4}
              className={field}
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
                <option value="tui">tui</option>
                <option value="headless">headless</option>
              </select>
            </label>
          </div>
        </form>
        {error && <p className="text-xs text-state-failed">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="template-save"
            disabled={busy || !name.trim() || !task.trim()}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function DeleteDialog({
  sessionID,
  template,
  client,
  onClose,
  onDeleted,
}: {
  sessionID: string
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
      await client.templateDelete(sessionID, template.name)
      onDeleted()
      onClose()
      toast.success('Template deleted')
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete {template.name}?</DialogTitle>
          <DialogDescription>
            The template and its schedule are removed together.
          </DialogDescription>
        </DialogHeader>
        {error && <p className="text-xs text-state-failed">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="destructive" disabled={busy} onClick={() => void remove()}>
            Delete
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

registerRoute('templates', TemplatesRoute)
