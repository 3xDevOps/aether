import { useState } from 'react'
import { toast } from 'sonner'
import { modes } from '@/components/launch/modes'
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
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Template } from '@/lib/types'

const harnesses = ['claude', 'codex', 'pi', 'omp', 'opencode', 'custom']

export type FormMode = { kind: 'new' } | { kind: 'edit'; template: Template } | { kind: 'duplicate'; template: Template }

export function TemplateForm({
  workspaceID,
  form,
  client,
  onClose,
  onSaved,
}: {
  workspaceID: string
  form: FormMode
  client: Api
  onClose: () => void
  onSaved: () => void
}) {
  const source = form.kind === 'new' ? undefined : form.template
  const editing = form.kind === 'edit'
  const [name, setName] = useState(form.kind === 'duplicate' ? `${form.template.name} copy` : (source?.name ?? ''))
  const [task, setTask] = useState(source?.task ?? '')
  const [harness, setHarness] = useState(source?.harness ?? harnesses[0])
  const [mode, setMode] = useState(source?.mode ?? 'headless')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.templateSave({ workspace_id: workspaceID, name: name.trim(), task: task.trim(), harness, mode })
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
          <DialogTitle>{editing ? 'Edit template' : form.kind === 'duplicate' ? 'Duplicate template' : 'New template'}</DialogTitle>
          <DialogDescription>Launch this task again in one step or on a schedule.</DialogDescription>
        </DialogHeader>
        <form
          id="template-save"
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <FormField label="Name" help={editing ? 'Template names stay fixed when editing.' : 'Use a short name people can scan.'}>
            <Input autoFocus value={name} readOnly={editing} onChange={(e) => setName(e.target.value)} />
          </FormField>
          <FormField label="Task" help="This task is sent unchanged when the template launches.">
            <Textarea rows={5} value={task} onChange={(e) => setTask(e.target.value)} />
          </FormField>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
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
            <div className="flex flex-col gap-1">
              <Label htmlFor="template-mode">Mode</Label>
              <Select value={mode} onValueChange={setMode}>
                <SelectTrigger id="template-mode">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {modes.map((m) => (
                    <SelectItem key={m.value} value={m.value}>
                      {m.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          {error && (
            <p role="alert" className="text-ui-sm text-state-failed">
              {error}
            </p>
          )}
        </form>
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="template-save" disabled={busy || !name.trim() || !task.trim()}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function DeleteTemplate({
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
    <AlertDialog open onOpenChange={() => !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="break-words">Delete {template.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The template and its schedule are removed together. Existing runs are not changed.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="text-ui-sm text-state-failed">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void remove()
            }}
          >
            {busy ? 'Deleting…' : 'Delete'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
