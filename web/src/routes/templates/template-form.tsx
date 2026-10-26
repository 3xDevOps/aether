import { useState } from 'react'
import { modes } from '@/components/launch/modes'
import { own, placeholderNames } from '@/components/templates/placeholders'
import { ModelField } from '@/components/launch/run-fields'
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
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FailureCallout } from '@/components/ui/failure-callout'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { SectionLabel } from '@/components/ui/section-label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { Template } from '@/lib/types'
import { useAgentList } from '@/routes/agents/use-agents'
import type { Outcome } from '@/store/optimistic'

export type FormMode = { kind: 'new' } | { kind: 'edit'; template: Template } | { kind: 'duplicate'; template: Template }

export type Failure = Extract<Outcome<unknown>, { ok: false }>

export interface TemplateDraft {
  name: string
  task: string
  harness: string
  mode: string
  model?: string
  params?: Record<string, string>
  budget_usd?: number
}

export function TemplateForm({
  form,
  onClose,
  onEdit,
  onSave,
  onRetry,
}: {
  form: FormMode & { draft?: TemplateDraft; failure?: Failure }
  onClose: () => void
  onEdit: () => void
  onSave: (draft: TemplateDraft) => void
  onRetry: (draft: TemplateDraft) => void
}) {
  const source = form.draft ?? (form.kind === 'new' ? undefined : form.template)
  const editing = form.kind === 'edit'
  const [name, setName] = useState(form.kind === 'duplicate' && !form.draft ? `${form.template.name} copy` : (source?.name ?? ''))
  const [task, setTask] = useState(source?.task ?? '')
  const [harness, setHarness] = useState(source?.harness ?? 'claude')
  const [mode, setMode] = useState(source?.mode ?? 'headless')
  const [model, setModel] = useState(source?.model ?? '')
  const [defaults, setDefaults] = useState<Record<string, string>>(source?.params ?? {})
  const { agents, error: agentsError, reload: reloadAgents } = useAgentList()
  const options = (agents ?? []).map((agent) => ({ value: agent.name, label: agent.display_name || agent.name }))
  // agent.list leaves out the deployment's `custom` agent, which a template may still name.
  for (const value of [harness, 'custom']) {
    if (!options.some((option) => option.value === value)) options.push({ value, label: value })
  }
  const chosenModel = model.trim()
  const names = placeholderNames(task)
  // Only names the task uses: the server refuses a default for any other.
  const params = Object.fromEntries(names.flatMap((n) => (Object.hasOwn(defaults, n) ? [[n, defaults[n]]] : [])))
  const draft: TemplateDraft = {
    name: name.trim(),
    task: task.trim(),
    harness,
    mode,
    ...(chosenModel ? { model: chosenModel } : {}),
    ...(Object.keys(params).length ? { params } : {}),
    ...(source?.budget_usd ? { budget_usd: source.budget_usd } : {}),
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
            onSave(draft)
          }}
        >
          <FormField label="Name" help={editing ? 'Template names stay fixed when editing.' : 'Use a short name people can scan.'}>
            <Input autoFocus value={name} readOnly={editing} onChange={(e) => {
              setName(e.target.value)
              onEdit()
            }} />
          </FormField>
          <FormField label="Task" help={<>Write <code className="font-code">{'{{name}}'}</code> where each launch fills in a value.</>}>
            <Textarea rows={5} value={task} onChange={(e) => {
              setTask(e.target.value)
              onEdit()
            }} />
          </FormField>
          {names.length > 0 && (
            <div role="group" aria-label="Parameters" className="flex flex-col gap-2">
              <SectionLabel>Parameters</SectionLabel>
              <p className="text-ui-sm text-muted">A default fills its parameter unless the launch gives a value. One without a default must be filled at every launch, so a scheduled template needs them all.</p>
              {names.map((n) => (
                <FormField key={n} label={<code className="font-code">{n}</code>}>
                  <Input placeholder="No default" value={own(defaults, n) ?? ''} onChange={(e) => {
                    const value = e.target.value
                    setDefaults((all) => {
                      const next = { ...all }
                      if (value) next[n] = value
                      else delete next[n]
                      return next
                    })
                    onEdit()
                  }} />
                </FormField>
              ))}
            </div>
          )}
          {agentsError && (
            <Callout tone="failed" role="alert" actions={<Button type="button" size="sm" variant="secondary" onClick={reloadAgents}>Retry</Button>}>
              Agents did not load: {agentsError}
            </Callout>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="template-harness">Agent</Label>
              <Select value={harness} onValueChange={(value) => {
                setHarness(value)
                onEdit()
              }}>
                <SelectTrigger id="template-harness">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {options.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="template-mode">Mode</Label>
              <Select value={mode} onValueChange={(value) => {
                setMode(value)
                onEdit()
              }}>
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
          <ModelField harness={harness} value={model} onChange={(value) => {
            setModel(value)
            onEdit()
          }} />
        </form>
        {form.failure && <FailureCallout error={form.failure.error} onRetry={form.failure.unanswered ? () => onRetry(draft) : undefined} />}
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="template-save" disabled={!name.trim() || !task.trim()}>
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function DeleteTemplate({
  template,
  failure,
  onClose,
  onDelete,
  onRetry,
}: {
  template: Template
  failure?: Failure
  onClose: () => void
  onDelete: () => void
  onRetry: () => void
}) {
  return (
    <AlertDialog open onOpenChange={onClose}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="break-words">Delete {template.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The template and its schedule are removed together. Existing runs are not changed.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {failure && <FailureCallout error={failure.error} onRetry={failure.unanswered ? onRetry : undefined} />}
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => {
              event.preventDefault()
              onDelete()
            }}
          >
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
