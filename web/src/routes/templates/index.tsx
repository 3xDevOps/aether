import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { toast } from 'sonner'
import { draftKey, reopen, toastFailure, useReopenHost } from '@/components/failure'
import { launchParams } from '@/components/templates/placeholders'
import { useTypedValues, type Typed } from '@/components/templates/prompt-fill'
import { TemplateList } from '@/components/templates/template-list'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { api, ApiError, codeNotFound, type Api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { errorSentence, message } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import type { Schedule, Template } from '@/lib/types'
import { redirectRoute } from '@/lib/url-state'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { DetailPane } from '@/routes/templates/detail-pane'
import { ScheduleEditor } from '@/routes/templates/schedule-editor'
import { DeleteTemplate, type Failure, type FormMode, type TemplateDraft, TemplateForm } from '@/routes/templates/template-form'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { optimistic, type Outcome } from '@/store/optimistic'
import {
  answered,
  changesIn,
  failureToasts,
  mark,
  readSchedules,
  readTemplates,
  saveKey,
  savingSchedules,
  savingTemplates,
  sendSave,
  setSchedules,
  setTemplates,
  started,
  unread,
  useLists,
  useSaves,
  withSchedule,
  withTemplate,
  type Started,
} from '@/store/templates'
import type { Route } from '@/store/ui'
import { soleWorkspace } from '@/store/workspaces'

type FormState = FormMode & { draft?: TemplateDraft; failure?: Failure; edited?: boolean }
type DeletingState = { template: Template; failure?: Failure }
type SchedulingState = { template: Template; draft?: string; failure?: Failure }
type Reopened = { workspaceID: string; form?: FormState; deleting?: DeletingState; scheduling?: SchedulingState }

export function TemplatesRoute({ params, client = api }: RouteProps & { client?: Api }) {
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
  const launchAction = (name: string) => JSON.stringify(['launch', workspaceID, name])
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
  const phone = useIsMobile()
  const { agents } = useAgentList()
  const agentNames = agentDisplayNames(agents)
  const values = useTypedValues()
  const [readError, setReadError] = useState<string | null>(null)
  const [launching, setLaunching] = useState<string | null>(null)
  const loading = useDelayed(templates === null)
  const asked = params.template
  const found = templates?.find((t) => t.name === asked)
  const selected = found ?? (phone ? undefined : templates?.[0])
  const unknown = Boolean(asked && templates && !found)

  // A phone pushes the detail so Back returns to the list; a desktop swaps the selection in place.
  const select = (name: string | undefined) => {
    const route: Route = { name: 'templates', params: name ? { template: name } : {} }
    if (phone && name) navigate(route.name, route.params)
    else redirectRoute(useStore, route)
  }

  useReopenHost<Reopened>('templates', (state) => {
    if (state.workspaceID !== workspaceID) useStore.getState().setActiveWorkspace(state.workspaceID)
    if (state.form) setForm(state.form)
    if (state.deleting) setDeleting(state.deleting)
    if (state.scheduling) setScheduling(state.scheduling)
    const shownTemplate = state.deleting?.template ?? state.scheduling?.template ?? (state.form?.kind === 'edit' ? state.form.template : undefined)
    if (shownTemplate) select(shownTemplate.name)
  })

  // Schedules are optional on legacy gateways; the templates themselves are
  // not, so their failures split: templates report, schedules stay empty.
  const read = useCallback(() => {
    if (!workspaceID) return
    setReadError(null)
    readTemplates(client, workspaceID).catch((err: unknown) => shown.current === workspaceID && setReadError(message(err)))
    if (caps.hasMethod('schedule.list')) readSchedules(client, workspaceID).catch(() => {})
  }, [client, workspaceID, caps])
  useEffect(read, [read])

  const launch = async (template: Template, typed: Typed) => {
    setLaunching(launchAction(template.name))
    try {
      const result = await client.templateLaunch(workspaceID, template.name, launchParams(template, typed))
      upsertRun(result.run)
      navigate('run', { runId: result.run.id })
      toast.success('Run launched')
    } catch (err) {
      toast.error(errorSentence(err))
    } finally {
      setLaunching(null)
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

  const neighbour = (name: string) => {
    const list = templates ?? []
    const at = list.findIndex((t) => t.name === name)
    return phone ? undefined : (list[at + 1] ?? list[at - 1])?.name
  }

  const listPane = (
    <div className={phone ? 'flex min-w-0 flex-1 flex-col gap-3 overflow-y-auto p-3' : 'flex w-64 shrink-0 flex-col gap-3 border-r border-seam p-3'}>
      {phone && unknown && <Callout tone="neutral">That template is not in this workspace.</Callout>}
      {readError && <FailureCallout error={readError} onRetry={read} />}
      {templates ? (
        <TemplateList
          templates={templates}
          schedules={schedules}
          agentNames={agentNames}
          selected={selected?.name}
          onSelect={select}
          className="flex-1"
        />
      ) : (
        loading && !readError && (
          <div aria-label="Loading templates" className="flex flex-col gap-1">
            {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-8" />)}
          </div>
        )
      )}
    </div>
  )

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader title="Templates" actions={templates?.length ? newTemplate : undefined} />
      {templates?.length === 0 ? (
        <div className="min-h-0 flex-1 overflow-y-auto">
          {readError && <div className="mx-auto max-w-sm px-4 pt-4"><FailureCallout error={readError} onRetry={read} /></div>}
          <EmptyState title="No templates yet" action={newTemplate}>
            A template saves a task you launch often, so it starts again in one step or on a schedule.
          </EmptyState>
        </div>
      ) : (
        <div className="flex min-h-0 flex-1">
          {!(phone && selected) && listPane}
          {selected && (
            <DetailPane
              template={selected}
              schedule={schedules.find((s) => s.template === selected.name)}
              agentName={agentNames[selected.harness] ?? selected.harness}
              typed={values.of(selected)}
              onValue={(name, value) => values.set(selected, name, value)}
              saving={saving('template', selected.name)}
              primary={{
                text: 'Launch',
                label: `Launch ${selected.name}`,
                pending: launching === launchAction(selected.name) ? 'Launching…' : undefined,
                // Every Launch holds while one is in flight, since each ends by opening its run.
                disabled: launching !== null || saving('template', selected.name),
                run: () => void launch(selected, values.of(selected)),
              }}
              notice={unknown && <Callout tone="neutral">That template is not in this workspace.</Callout>}
              onBack={phone ? () => navigate('templates') : undefined}
              onEdit={canSave ? () => setForm({ kind: 'edit', template: selected }) : undefined}
              onSchedule={canSchedule ? () => setScheduling({ template: selected }) : undefined}
              onDuplicate={canSave ? () => setForm({ kind: 'duplicate', template: selected }) : undefined}
              onDelete={caps.hasMethod('template.delete') ? () => setDeleting({ template: selected }) : undefined}
            />
          )}
        </div>
      )}

      {form && (
        <TemplateForm
          key={draftKey(form.draft)}
          form={form}
          onClose={() => setForm(null)}
          onEdit={() => form.draft && !form.edited && setForm({ ...form, edited: true })}
          onSave={(draft) => {
            if (form.kind !== 'edit') select(draft.name)
            void saveTemplate(form, draft, () => templateSave(draft))
          }}
          onRetry={(draft) => void saveTemplate(form, draft, () => templateSave(draft))}
        />
      )}
      {deleting && (
        <DeleteTemplate
          template={deleting.template}
          failure={deleting.failure}
          onClose={() => setDeleting(null)}
          onDelete={() => {
            if (asked === deleting.template.name) select(neighbour(asked))
            void deleteTemplate(deleting.template, () => templateDelete(deleting.template.name))
          }}
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
              onChanged={read}
            />
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

registerRoute('templates', TemplatesRoute)
