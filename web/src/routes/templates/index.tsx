import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { modeLabel } from '@/components/launch/modes'
import { Ellipsis } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { RelativeTime } from '@/components/ui/relative-time'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { Schedule, Template } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { ScheduleEditor } from '@/routes/templates/schedule-editor'
import { DeleteTemplate, type FormMode, TemplateForm } from '@/routes/templates/template-form'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { soleWorkspace } from '@/store/workspaces'

function Timing({ template, schedule }: { template: Template; schedule?: Schedule }) {
  if (schedule?.next_fire_at) return <>Next <RelativeTime at={schedule.next_fire_at} /></>
  if (schedule?.last_fire_at) return <>Last <RelativeTime at={schedule.last_fire_at} /></>
  return <>Saved <RelativeTime at={template.created_at} /></>
}

function TemplateRow({
  template,
  schedule,
  onLaunch,
  onSchedule,
  onEdit,
  onDuplicate,
  onDelete,
}: {
  template: Template
  schedule?: Schedule
  onLaunch: () => void
  onSchedule?: () => void
  onEdit?: () => void
  onDuplicate?: () => void
  onDelete?: () => void
}) {
  const menu = onSchedule || onEdit || onDuplicate || onDelete
  return (
    <li aria-label={`Template ${template.name}`} className="flex min-h-12 min-w-0 items-center gap-3 px-4 py-2 hover:bg-hover">
      <AgentGlyph agent={template.harness} className="size-4" />
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate font-medium text-text">{template.name}</span>
        <span className="truncate text-ui-sm text-muted" title={template.task}>
          {modeLabel(template.mode)}
          {schedule && ' · Scheduled'} · {template.task}
        </span>
      </div>
      <span className="shrink-0 text-ui-sm text-muted tabular-nums max-sm:hidden">
        <Timing template={template} schedule={schedule} />
      </span>
      <Button variant="secondary" size="sm" aria-label={`Launch ${template.name}`} onClick={onLaunch}>
        Launch
      </Button>
      {menu && (
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" label={`More for ${template.name}`}>
              <Ellipsis />
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            {onSchedule && <MenuItem onSelect={onSchedule}>Schedule…</MenuItem>}
            {onEdit && <MenuItem onSelect={onEdit}>Edit</MenuItem>}
            {onDuplicate && <MenuItem onSelect={onDuplicate}>Duplicate</MenuItem>}
            {onDelete && (
              <>
                <MenuSeparator />
                <MenuItem tone="danger" onSelect={onDelete}>Delete</MenuItem>
              </>
            )}
          </MenuContent>
        </Menu>
      )}
    </li>
  )
}

export function TemplatesRoute({ client = api }: RouteProps & { client?: Api }) {
  const workspaces = useStore((s) => s.workspaces)
  const active = useStore((s) => s.activeWorkspace)
  const navigate = useStore((s) => s.navigate)
  const upsertRun = useStore((s) => s.upsertRun)
  const caps = useCapability()
  const [templates, setTemplates] = useState<Template[] | null>(null)
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [form, setForm] = useState<FormMode | null>(null)
  const [deleting, setDeleting] = useState<Template | null>(null)
  const [scheduling, setScheduling] = useState<Template | null>(null)

  // Templates are per workspace on the wire, so there is no "all" reading;
  // before hydration names one, the sole workspace is the only safe answer.
  const workspaceID = active || soleWorkspace(workspaces)
  const canSave = caps.hasMethod('template.save')
  const canSchedule = caps.hasMethod('schedule.save')

  const refetch = useCallback(async () => {
    if (!workspaceID) return
    setTemplates(await client.templateList(workspaceID))
    if (caps.hasMethod('schedule.list')) setSchedules(await client.scheduleList(workspaceID))
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
      .catch((err) => toast.error(errorSentence(err)))
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
      upsertRun(result.run)
      navigate('run', { runId: result.run.id })
      toast.success('Run launched')
    } catch (err) {
      toast.error(errorSentence(err))
    }
  }

  const newTemplate = canSave && workspaceID && (
    <Button variant="secondary" size="sm" onClick={() => setForm({ kind: 'new' })}>
      New template
    </Button>
  )

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader title="Templates" actions={templates?.length ? newTemplate : undefined} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        {templates && templates.length > 0 && (
          <ul aria-label="Saved templates" className="mx-auto w-full max-w-4xl divide-y divide-seam py-2">
            {templates.map((template) => (
              <TemplateRow
                key={template.id}
                template={template}
                schedule={schedules.find((s) => s.template === template.name)}
                onLaunch={() => void launch(template)}
                onSchedule={canSchedule ? () => setScheduling(template) : undefined}
                onEdit={canSave ? () => setForm({ kind: 'edit', template }) : undefined}
                onDuplicate={canSave ? () => setForm({ kind: 'duplicate', template }) : undefined}
                onDelete={caps.hasMethod('template.delete') ? () => setDeleting(template) : undefined}
              />
            ))}
          </ul>
        )}
        {templates?.length === 0 && (
          <EmptyState title="No templates yet" action={newTemplate}>
            A template saves a task you launch often, so it starts again in one step or on a schedule.
          </EmptyState>
        )}
      </div>

      {form && (
        <TemplateForm workspaceID={workspaceID} form={form} client={client} onClose={() => setForm(null)} onSaved={() => void refetch()} />
      )}
      {deleting && (
        <DeleteTemplate
          workspaceID={workspaceID}
          template={deleting}
          client={client}
          onClose={() => setDeleting(null)}
          onDeleted={() => void refetch()}
        />
      )}
      {scheduling && (
        <Dialog open onOpenChange={(open) => !open && setScheduling(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Schedule {scheduling.name}</DialogTitle>
              <DialogDescription>The server launches this template at each occurrence. One missed while the server was down is skipped.</DialogDescription>
            </DialogHeader>
            <ScheduleEditor
              workspaceID={workspaceID}
              template={scheduling.name}
              schedule={schedules.find((s) => s.template === scheduling.name)}
              client={client}
              onChanged={() => void refetch()}
            />
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

registerRoute('templates', TemplatesRoute)
