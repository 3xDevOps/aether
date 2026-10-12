import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { toast } from 'sonner'
import { create } from 'zustand'
import { modeLabel } from '@/components/launch/modes'
import { Ellipsis } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { RelativeTime } from '@/components/ui/relative-time'
import { ViewHeader } from '@/components/view-header'
import { api, ApiError, codeNotFound, type Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { Schedule, Template } from '@/lib/types'
import { draftKey, reopen, toastFailure, useReopenHost } from '@/components/failure'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { ScheduleEditor } from '@/routes/templates/schedule-editor'
import { DeleteTemplate, type Failure, type FormMode, type TemplateDraft, TemplateForm } from '@/routes/templates/template-form'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { optimistic, type Outcome } from '@/store/optimistic'
import { soleWorkspace } from '@/store/workspaces'

const saveKey = (kind: 'template' | 'schedule', workspaceID: string, name: string) => JSON.stringify([kind, workspaceID, name])

type Lists = { templates: Template[] | null; schedules: Schedule[] }
type Changes = { seq: number; templates: Map<string, number>; schedules: Map<string, number>; reads: number; applied: Record<keyof Lists, number> }

// Outlive the route, so an action sent before leaving Templates still holds Delete and Unschedule, and lands on the list, after coming back.
const useSaves = create<Record<string, number[]>>(() => ({}))
const useLists = create<Record<string, Lists>>(() => ({}))
const latestSave = new Map<string, number>()
// When each template and schedule last changed here, per workspace, so a list read sent before keeps that change.
const changes = new Map<string, Changes>()
// Each pending save's prediction, mapped to the server value it replaced.
const savingTemplates = new Map<Template, Template | undefined>()
const savingSchedules = new Map<Schedule, Schedule | undefined>()
// Keyed by what an action sends, not by mutation: a dialog's Retry is a new mutation, so optimistic() cannot drop the late refusal it supersedes.
const confirms = new Map<string, number>()
// Per action, the toast showing its failure, so a late success dismisses it.
const failureToasts = new Map<string, string | number>()
let saves = 0

useStore.subscribe((state, previous) => {
  if (state.identityKey === previous.identityKey) return
  useSaves.setState({}, true)
  useLists.setState({}, true)
  for (const held of [latestSave, changes, savingTemplates, savingSchedules, confirms, failureToasts]) held.clear()
})

const confirmed = (action: string) => confirms.get(action) ?? 0
const answered = (action: string) => {
  confirms.set(action, confirmed(action) + 1)
  const shown = failureToasts.get(action)
  if (shown === undefined) return
  toast.dismiss(shown)
  failureToasts.delete(action)
}
type Started = { action: string; superseded: () => boolean; same: () => boolean }
const started = (action: string): Started => {
  const sent = confirmed(action)
  return { action, superseded: () => confirmed(action) > sent, same: sameIdentity() }
}

function changesIn(workspaceID: string): Changes {
  let since = changes.get(workspaceID)
  if (!since) changes.set(workspaceID, (since = { seq: 0, templates: new Map(), schedules: new Map(), reads: 0, applied: { templates: 0, schedules: 0 } }))
  return since
}

function mark(workspaceID: string, kind: 'templates' | 'schedules', name: string) {
  const since = changesIn(workspaceID)
  since[kind].set(name, ++since.seq)
}

const unread: Lists = { templates: null, schedules: [] }

function setTemplates(workspaceID: string, next: (list: Template[] | null) => Template[] | null) {
  useLists.setState((all) => {
    const lists = all[workspaceID] ?? unread
    return { [workspaceID]: { ...lists, templates: next(lists.templates) } }
  })
}

function setSchedules(workspaceID: string, next: (list: Schedule[]) => Schedule[]) {
  useLists.setState((all) => {
    const lists = all[workspaceID] ?? unread
    return { [workspaceID]: { ...lists, schedules: next(lists.schedules) } }
  })
}

function sendSave<T>(key: string, send: () => Promise<T>): { sent: Promise<T>; latest: () => boolean } {
  const order = ++saves
  latestSave.set(key, order)
  useSaves.setState((sending) => ({ [key]: [...(sending[key] ?? []), order] }))
  const sent = send().finally(() => useSaves.setState((sending) => ({ [key]: (sending[key] ?? []).filter((o) => o !== order) })))
  return { sent, latest: () => latestSave.get(key) === order }
}

const byName = (a: Template, b: Template) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)

function withTemplate(list: Template[] | null, name: string, next?: Template): Template[] | null {
  if (!list) return next ? [next] : list
  const rest = list.filter((t) => t.name !== name)
  return next ? [...rest, next].sort(byName) : rest
}

function withSchedule(list: Schedule[], template: string, next?: Schedule): Schedule[] {
  const rest = list.filter((s) => s.template !== template)
  return next ? [...rest, next] : rest
}

type FormState = FormMode & { draft?: TemplateDraft; failure?: Failure; edited?: boolean }
type DeletingState = { template: Template; failure?: Failure }
type SchedulingState = { template: Template; draft?: string; failure?: Failure }
type Reopened = { workspaceID: string; form?: FormState; deleting?: DeletingState; scheduling?: SchedulingState }
/**
 * Returns a merge for a list read sent now: entries changed here after it was sent, or with a save still unanswered, keep their shown value, since the read may predate the change.
 * It returns null, to keep the shown list, for an answer to a read sent before the last one applied or before the signed-in identity changed.
 */
function sinceRead<T>(workspaceID: string, kind: 'templates' | 'schedules', name: (item: T) => string) {
  const since = changesIn(workspaceID)
  const sent = since.seq
  const order = ++since.reads
  const same = sameIdentity()
  const saving = (item: T) => Boolean(useSaves.getState()[saveKey(kind === 'templates' ? 'template' : 'schedule', workspaceID, name(item))]?.length)
  const changed = (item: T) => (since[kind].get(name(item)) ?? 0) > sent || saving(item)
  return (read: T[], shown: T[]): T[] | null => {
    if (!same() || order < since.applied[kind]) return null
    since.applied[kind] = order
    return [...read.filter((i) => !changed(i)), ...shown.filter(changed)]
  }
}

function Timing({ template, schedule }: { template: Template; schedule?: Schedule }) {
  if (schedule?.next_fire_at) return <>Next <RelativeTime at={schedule.next_fire_at} /></>
  if (schedule?.last_fire_at) return <>Last <RelativeTime at={schedule.last_fire_at} /></>
  return <>Saved <RelativeTime at={template.created_at} /></>
}

function TemplateRow({
  template,
  schedule,
  saving,
  onLaunch,
  onSchedule,
  onEdit,
  onDuplicate,
  onDelete,
}: {
  template: Template
  schedule?: Schedule
  saving: boolean
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
          {template.model && ` · ${template.model}`}
          {schedule && ' · Scheduled'} · {template.task}
        </span>
      </div>
      <span className="shrink-0 text-ui-sm text-muted tabular-nums max-sm:hidden">
        <Timing template={template} schedule={schedule} />
      </span>
      <Button variant="secondary" size="sm" aria-label={`Launch ${template.name}`} disabled={saving} onClick={onLaunch}>
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
            {onSchedule && <MenuItem disabled={saving} onSelect={onSchedule}>Schedule…</MenuItem>}
            {onEdit && <MenuItem onSelect={onEdit}>Edit</MenuItem>}
            {onDuplicate && <MenuItem onSelect={onDuplicate}>Duplicate</MenuItem>}
            {onDelete && (
              <>
                <MenuSeparator />
                <MenuItem tone="danger" disabled={saving} onSelect={onDelete}>Delete</MenuItem>
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
  const selfID = useStore((s) => s.info?.member.id ?? '')
  const caps = useCapability()
  // Templates are per workspace on the wire, so there is no "all" reading;
  // before hydration names one, the sole workspace is the only safe answer.
  const workspaceID = active || soleWorkspace(workspaces)
  const { templates, schedules } = useLists((all) => all[workspaceID] ?? unread)
  const [form, setForm] = useState<FormState | null>(null)
  const [deleting, setDeleting] = useState<DeletingState | null>(null)
  const [scheduling, setScheduling] = useState<SchedulingState | null>(null)
  const inFlight = useSaves()
  const saving = (kind: 'template' | 'schedule', name: string) => Boolean(inFlight[saveKey(kind, workspaceID, name)]?.length)
  const saveAction = (draft: TemplateDraft) => JSON.stringify(['save', workspaceID, draft])
  const deleteAction = (name: string) => JSON.stringify(['delete', workspaceID, name])
  const scheduleAction = (template: string, cron: string) => JSON.stringify(['schedule', workspaceID, template, cron])
  const canSave = caps.hasMethod('template.save')
  const canSchedule = caps.hasMethod('schedule.save')
  // A failure of an action sent from another workspace toasts rather than opening its dialog over this one.
  const shown = useRef(workspaceID)
  const dialogs = useRef({ form, deleting, scheduling })
  const mounted = useRef(true)
  useEffect(() => {
    shown.current = workspaceID
    dialogs.current = { form, deleting, scheduling }
  })
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  useReopenHost<Reopened>('templates', (state) => {
    if (state.workspaceID !== workspaceID) useStore.getState().setActiveWorkspace(state.workspaceID)
    if (state.form) setForm(state.form)
    if (state.deleting) setDeleting(state.deleting)
    if (state.scheduling) setScheduling(state.scheduling)
  })

  const refetch = useCallback(async () => {
    if (!workspaceID) return
    const templates = sinceRead<Template>(workspaceID, 'templates', (t) => t.name)
    const list = await client.templateList(workspaceID)
    setTemplates(workspaceID, (current) => templates(list, current ?? [])?.sort(byName) ?? current)
    if (!caps.hasMethod('schedule.list')) return
    const schedules = sinceRead<Schedule>(workspaceID, 'schedules', (s) => s.template)
    const read = await client.scheduleList(workspaceID)
    setSchedules(workspaceID, (current) => schedules(read, current) ?? current)
  }, [client, workspaceID, caps])

  // Schedules are optional on legacy gateways; the templates themselves are
  // not, so their failures split: templates report, schedules stay empty.
  useEffect(() => {
    if (!workspaceID) return
    const templates = sinceRead<Template>(workspaceID, 'templates', (t) => t.name)
    client
      .templateList(workspaceID)
      .then((list) => setTemplates(workspaceID, (current) => templates(list, current ?? [])?.sort(byName) ?? current))
      .catch((err) => toast.error(errorSentence(err)))
    if (caps.hasMethod('schedule.list')) {
      const schedules = sinceRead<Schedule>(workspaceID, 'schedules', (s) => s.template)
      client
        .scheduleList(workspaceID)
        .then((list) => setSchedules(workspaceID, (current) => schedules(list, current) ?? current))
        .catch(() => {})
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

  // A dialog the user opened since the action, or a page left since, keeps what it shows; `own` is the dialog the action left on screen.
  const report = <S extends { failure?: Failure }>(
    failed: S & { failure: Failure },
    set: Dispatch<SetStateAction<S | null>>,
    as: (state: S) => Omit<Reopened, 'workspaceID'>,
    retry: () => void,
    { action, superseded, same }: Started,
    own?: object,
  ) => {
    if (!same()) return
    const failure = failed.failure
    const again = (state: S) => () => void (same() && reopen<Reopened>('templates', { workspaceID, ...as(state) }, 'templates'))
    const open = () => dialogs.current.form ?? dialogs.current.deleting ?? dialogs.current.scheduling
    const current = open()
    let id: string | number | undefined
    if (!mounted.current || shown.current !== workspaceID || (current && current !== own)) {
      id = toastFailure(failure.error, { retry: failure.unanswered ? () => void (same() && retry()) : undefined, reopen: again(failed), same })
      failureToasts.set(action, id)
    } else set(failed)
    void failure.refused?.then((error) => {
      if (superseded() || !same()) return
      const refused = { ...failed, failure: { ...failure, error, unanswered: false } }
      if (mounted.current && open()?.failure === failure) set((state) => (state?.failure === failure ? refused : state))
      else failureToasts.set(action, toastFailure(error, { id, reopen: again(refused), same }))
    })
  }

  const templateSave = (draft: TemplateDraft) => {
    let predicted: Template | undefined
    let latest = () => true
    let same = () => true
    return optimistic({
      apply: () => {
        setTemplates(workspaceID, (list) => {
          const current = list?.find((t) => t.name === draft.name)
          const previous = current && savingTemplates.has(current) ? savingTemplates.get(current) : current
          const created_at = previous?.created_at ?? new Date().toISOString()
          predicted = { id: previous?.id ?? draft.name, workspace_id: workspaceID, ...draft, created_at }
          savingTemplates.set(predicted, previous)
          return withTemplate(list, draft.name, predicted)
        })
        mark(workspaceID, 'templates', draft.name)
      },
      send: () => {
        const save = sendSave(saveKey('template', workspaceID, draft.name), () => client.templateSave({ workspace_id: workspaceID, ...draft }))
        latest = save.latest
        same = sameIdentity()
        return save.sent
      },
      confirm: (saved) => {
        if (!same()) return
        answered(saveAction(draft))
        if (predicted) savingTemplates.delete(predicted)
        if (latest()) {
          setTemplates(workspaceID, (list) => withTemplate(list, saved.name, saved))
          mark(workspaceID, 'templates', saved.name)
        } else {
          for (const pending of savingTemplates.keys()) if (pending.workspace_id === workspaceID && pending.name === draft.name) savingTemplates.set(pending, saved)
        }
        setForm((current) => (current?.draft === draft ? (current.edited ? { ...current, failure: undefined } : null) : current))
      },
      revert: () => {
        if (!predicted || !same()) return
        const previous = savingTemplates.get(predicted)
        savingTemplates.delete(predicted)
        setTemplates(workspaceID, (list) => (list?.find((t) => t.name === draft.name) === predicted ? withTemplate(list, draft.name, previous) : list))
      },
    })
  }

  const saveTemplate = async (opened: FormMode, draft: TemplateDraft, attempt: () => Promise<Outcome<unknown>>) => {
    setForm((current) => (current === opened ? null : current))
    const start = started(saveAction(draft))
    const outcome = await attempt()
    if (outcome.ok) toast.success('Template saved')
    else report({ ...opened, draft, failure: outcome, edited: false }, setForm, (form) => ({ form }), () => void saveTemplate(opened, draft, outcome.retry), start)
  }

  const templateDelete = (name: string) => {
    let previous: Template | undefined
    let schedule: Schedule | undefined
    let sent = 0
    let same = () => true
    return optimistic({
      apply: () => {
        mark(workspaceID, 'schedules', name)
        setTemplates(workspaceID, (list) => {
          previous = list?.find((t) => t.name === name)
          return withTemplate(list, name)
        })
        mark(workspaceID, 'templates', name)
        setSchedules(workspaceID, (list) => {
          schedule = list.find((s) => s.template === name)
          return withSchedule(list, name)
        })
      },
      send: () => {
        sent = changesIn(workspaceID).seq
        same = sameIdentity()
        return client.templateDelete(workspaceID, name).catch((cause: unknown) => {
          if (!(cause instanceof ApiError && cause.code === codeNotFound)) throw cause
        })
      },
      confirm: () => {
        if (!same()) return
        answered(deleteAction(name))
        if ((changesIn(workspaceID).templates.get(name) ?? 0) <= sent) setTemplates(workspaceID, (list) => withTemplate(list, name))
        if ((changesIn(workspaceID).schedules.get(name) ?? 0) <= sent) setSchedules(workspaceID, (list) => withSchedule(list, name))
        mark(workspaceID, 'templates', name)
        setDeleting((current) => (current?.template.name === name ? null : current))
      },
      revert: () => {
        if (!same()) return
        setTemplates(workspaceID, (list) => (list?.some((t) => t.name === name) ? list : withTemplate(list, name, previous)))
        setSchedules(workspaceID, (list) => (list.some((s) => s.template === name) ? list : withSchedule(list, name, schedule)))
      },
    })
  }

  const deleteTemplate = async (template: Template, attempt: () => Promise<Outcome<unknown>>) => {
    setDeleting((current) => (current?.template.name === template.name ? null : current))
    const start = started(deleteAction(template.name))
    const outcome = await attempt()
    if (outcome.ok) toast.success('Template deleted')
    else report({ template, failure: outcome }, setDeleting, (deleting) => ({ deleting }), () => void deleteTemplate(template, outcome.retry), start)
  }

  const scheduleSave = (template: string, cron: string) => {
    let predicted: Schedule | undefined
    let latest = () => true
    let same = () => true
    return optimistic({
      apply: () => {
        mark(workspaceID, 'schedules', template)
        setSchedules(workspaceID, (list) => {
          const current = list.find((s) => s.template === template)
          const previous = current && savingSchedules.has(current) ? savingSchedules.get(current) : current
          const created_at = previous?.created_at ?? new Date().toISOString()
          predicted = { id: previous?.id ?? template, workspace_id: workspaceID, template, cron, member_id: selfID, created_at }
          savingSchedules.set(predicted, previous)
          return withSchedule(list, template, predicted)
        })
      },
      send: () => {
        const save = sendSave(saveKey('schedule', workspaceID, template), () => client.scheduleSave({ workspace_id: workspaceID, template, cron }))
        latest = save.latest
        same = sameIdentity()
        return save.sent
      },
      confirm: (saved) => {
        if (!same()) return
        answered(scheduleAction(template, cron))
        if (predicted) savingSchedules.delete(predicted)
        if (latest()) {
          setSchedules(workspaceID, (list) => withSchedule(list, template, saved))
          mark(workspaceID, 'schedules', template)
        } else {
          for (const pending of savingSchedules.keys()) if (pending.workspace_id === workspaceID && pending.template === template) savingSchedules.set(pending, saved)
        }
        setScheduling((current) => (current?.template.name === template && current.draft === cron ? { template: current.template } : current))
      },
      revert: () => {
        if (!predicted || !same()) return
        const previous = savingSchedules.get(predicted)
        savingSchedules.delete(predicted)
        setSchedules(workspaceID, (list) => (list.find((s) => s.template === template) === predicted ? withSchedule(list, template, previous) : list))
      },
    })
  }

  const saveSchedule = async (template: Template, cron: string, attempt: () => Promise<Outcome<unknown>>) => {
    const waiting = dialogs.current.scheduling?.template.name === template.name ? { template } : undefined
    if (waiting) setScheduling(waiting)
    const start = started(scheduleAction(template.name, cron))
    const outcome = await attempt()
    if (outcome.ok) return
    report({ template, draft: cron, failure: outcome }, setScheduling, (scheduling) => ({ scheduling }), () => void saveSchedule(template, cron, outcome.retry), start, waiting)
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
                saving={saving('template', template.name)}
                onLaunch={() => void launch(template)}
                onSchedule={canSchedule ? () => setScheduling({ template }) : undefined}
                onEdit={canSave ? () => setForm({ kind: 'edit', template }) : undefined}
                onDuplicate={canSave ? () => setForm({ kind: 'duplicate', template }) : undefined}
                onDelete={caps.hasMethod('template.delete') ? () => setDeleting({ template }) : undefined}
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
        <TemplateForm
          key={draftKey(form.draft)}
          form={form}
          onClose={() => setForm(null)}
          onEdit={() => form.draft && !form.edited && setForm({ ...form, edited: true })}
          onSave={(draft) => void saveTemplate(form, draft, () => templateSave(draft))}
          onRetry={(draft) => void saveTemplate(form, draft, () => templateSave(draft))}
        />
      )}
      {deleting && (
        <DeleteTemplate
          template={deleting.template}
          failure={deleting.failure}
          onClose={() => setDeleting(null)}
          onDelete={() => void deleteTemplate(deleting.template, () => templateDelete(deleting.template.name))}
          onRetry={() => deleting.failure && void deleteTemplate(deleting.template, deleting.failure.retry)}
        />
      )}
      {scheduling && (
        <Dialog open onOpenChange={(open) => !open && setScheduling(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Schedule {scheduling.template.name}</DialogTitle>
              <DialogDescription>The server launches this template at each occurrence. One missed while the server was down is skipped.</DialogDescription>
            </DialogHeader>
            <ScheduleEditor
              key={scheduling.template.name}
              workspaceID={workspaceID}
              template={scheduling.template.name}
              schedule={schedules.find((s) => s.template === scheduling.template.name)}
              draft={scheduling.draft}
              failure={scheduling.failure}
              saving={saving('schedule', scheduling.template.name)}
              client={client}
              onSave={(cron) => void saveSchedule(scheduling.template, cron, () => scheduleSave(scheduling.template.name, cron))}
              onRetry={(cron) => void saveSchedule(scheduling.template, cron, () => scheduleSave(scheduling.template.name, cron))}
              onChanged={() => void refetch()}
            />
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

registerRoute('templates', TemplatesRoute)
