import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { api, ApiError } from '@/lib/api'
import type { Schedule, Template } from '@/lib/types'
import { reopen } from '@/components/failure'
import { TemplatesRoute } from '@/routes/templates'
import { useStore, type RootState } from '@/store'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { alice, fakeApi, otherWorkspace, serverInfo, template, workspace } from '@/test/fixtures'
import { chooseModel } from '@/test/model'
import { pickOption } from '@/test/select'

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn(() => 'toast'), dismiss: vi.fn() },
}))

const schedule: Schedule = {
  id: 'sch_1',
  workspace_id: workspace.id,
  template: template.name,
  cron: '0 3 * * *',
  member_id: alice.id,
  created_at: '2026-08-14T09:05:00Z',
  next_fire_at: '2026-08-23T03:00:00Z',
}

function seed(extra: Partial<RootState> = {}) {
  vi.spyOn(api, 'agentModels').mockResolvedValue({ models: [], source: 'none' })
  useStore.setState({
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    // template.save/delete are gated; an upgraded gateway advertising every
    // method renders them all.
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    hydrationError: null,
    agentModels: {},
    route: { name: 'templates', params: {} },
    // A new identity drops the saves earlier tests left in flight.
    identityKey: crypto.randomUUID(),
    ...extra,
  })
}

async function rowAction(name: string) {
  const trigger = await screen.findByRole('button', { name: `More for ${template.name}` })
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  const item = within(await screen.findByRole('menu')).getByRole('menuitem', { name })
  item.focus()
  await userEvent.keyboard('{Enter}')
}

describe('templates view', () => {
  it('lists a template as one row with its agent, mode word and task', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => [schedule]) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    const row = within(await screen.findByRole('listitem', { name: `Template ${template.name}` }))
    expect(row.getByText(template.name)).toBeDefined()
    expect(row.getByText(/Background · Scheduled · triage the flaky tests/)).toBeDefined()
    expect(row.getByText(/^Next/)).toBeDefined()
    expect(row.getByRole('button', { name: `Launch ${template.name}` })).toBeDefined()
    expect(screen.queryByRole('form')).toBeNull()
  })

  it('saves a template through the gateway', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []), templateSave: vi.fn(async () => template) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/^Name/), { target: { value: 'weekly sweep' } })
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep the flaky tests' } })
    await pickOption(dialog.getByLabelText(/^Mode$/), 'Standard')
    await chooseModel(dialog.getByRole('combobox', { name: /^Model/ }), 'sonnet')
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    expect(client.templateSave).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      name: 'weekly sweep',
      task: 'sweep the flaky tests',
      harness: 'claude',
      mode: 'tui',
      model: 'sonnet',
    })
  })

  it('lists a saved template at once and keeps one row once the server answers', async () => {
    let answer: (saved: Template) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve) => (answer = resolve))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/^Name/), { target: { value: 'weekly sweep' } })
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep the flaky tests' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByRole('listitem', { name: 'Template weekly sweep' }).textContent).toContain('sweep the flaky tests')
    await act(async () => answer({ ...template, id: 'tpl_2', name: 'weekly sweep', task: 'sweep the flaky tests' }))
    expect(screen.getAllByRole('listitem', { name: 'Template weekly sweep' })).toHaveLength(1)
    expect(client.templateList).toHaveBeenCalledTimes(1)
  })

  it('reverts a refused edit and reopens the form with what was typed', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(async () => {
        throw new ApiError(200, 'template.save: harness is not installed', -32602)
      }),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    const reopened = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    expect(reopened.getByRole('alert').textContent).toContain('harness is not installed')
    expect((reopened.getByLabelText(/^Task/) as HTMLTextAreaElement).value).toBe('sweep instead')
    expect(reopened.queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true }).textContent).toContain(template.task)
  })

  it('duplicates a template under a new name', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Duplicate')
    const dialog = within(await screen.findByRole('dialog', { name: 'Duplicate template' }))
    expect((dialog.getByLabelText(/^Name/) as HTMLInputElement).value).toBe(`${template.name} copy`)
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    expect(client.templateSave).toHaveBeenCalledWith(
      expect.objectContaining({ name: `${template.name} copy`, task: template.task, harness: template.harness, mode: template.mode }),
    )
  })

  it('schedules from a dialog and shows the next launch the server returned', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []), scheduleSave: vi.fn(async () => schedule) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Schedule…')
    const dialog = within(await screen.findByRole('dialog', { name: `Schedule ${template.name}` }))
    fireEvent.change(dialog.getByLabelText('Schedule (UTC)'), { target: { value: '0 3 * * *' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Schedule' }))

    expect(await dialog.findByText(/Sun, 23 Aug 2026 03:00:00 GMT/)).toBeDefined()
    expect(client.scheduleSave).toHaveBeenCalledWith({ workspace_id: workspace.id, template: template.name, cron: '0 3 * * *' })
  })

  it('shows a saved schedule at once, holds Unschedule until the server answers, then shows its next launch', async () => {
    let answer: (saved: Schedule) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      scheduleSave: vi.fn(() => new Promise<Schedule>((resolve) => (answer = resolve))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Schedule…')
    const dialog = within(await screen.findByRole('dialog', { name: `Schedule ${template.name}` }))
    fireEvent.change(dialog.getByLabelText('Schedule (UTC)'), { target: { value: '0 3 * * *' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Schedule' }))

    expect(dialog.getByRole('button', { name: 'Update schedule' })).toBeDefined()
    expect(dialog.getByRole('button', { name: 'Saving…' }).hasAttribute('disabled')).toBe(true)
    await act(async () => answer(schedule))
    expect(dialog.getByText(/Sun, 23 Aug 2026 03:00:00 GMT/)).toBeDefined()
    expect(dialog.getByRole('button', { name: 'Unschedule' }).hasAttribute('disabled')).toBe(false)
    expect(client.scheduleList).toHaveBeenCalledTimes(1)
  })

  it('reverts an unanswered schedule with Retry, then shows a late answer without the error', async () => {
    let answer: (saved: Schedule) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      scheduleSave: vi.fn(() => new Promise<Schedule>((resolve) => (answer = resolve))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Schedule…')
    const dialog = within(await screen.findByRole('dialog', { name: `Schedule ${template.name}` }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(dialog.getByLabelText('Schedule (UTC)'), { target: { value: '0 3 * * *' } })
      fireEvent.click(dialog.getByRole('button', { name: 'Schedule' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      expect(dialog.getByRole('alert').textContent).toContain('The server did not answer in 5 seconds.')
      expect(dialog.getByRole('button', { name: 'Retry' })).toBeDefined()
      expect(dialog.getByRole('button', { name: 'Schedule' })).toBeDefined()
      expect(dialog.getByLabelText('Schedule (UTC)')).toHaveProperty('value', '0 3 * * *')

      await act(async () => answer(schedule))
      expect(dialog.queryByRole('alert')).toBeNull()
      expect(dialog.getByText(/Sun, 23 Aug 2026 03:00:00 GMT/)).toBeDefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it('launches a template and navigates to the run', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []) })
    seed({ runs: {} })
    render(<TemplatesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: `Launch ${template.name}` }))

    expect(client.templateLaunch).toHaveBeenCalledWith(workspace.id, template.name)
    // fakeApi's templateLaunch returns run_tpl; navigation lands on it, and
    // the run is seeded with it so the tab does not call it deleted.
    await waitFor(() => {
      expect(useStore.getState().route).toEqual({
        name: 'run',
        params: { runId: 'run_tpl' },
      })
    })
    expect(useStore.getState().runs.run_tpl).toBeDefined()
  })

  it('removes a deleted template and its schedule at once', async () => {
    let answer: () => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => [schedule]),
      templateDelete: vi.fn(() => new Promise<unknown>((resolve) => (answer = () => resolve({})))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    const confirm = await screen.findByRole('alertdialog')
    expect(client.templateDelete).not.toHaveBeenCalled()
    // A confirm has no close X: answering it is the only way past it.
    expect(within(confirm).getAllByRole('button')).toHaveLength(2)
    fireEvent.click(within(confirm).getByRole('button', { name: 'Delete' }))

    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(screen.queryByRole('listitem', { name: `Template ${template.name}` })).toBeNull()
    expect(client.templateDelete).toHaveBeenCalledWith(workspace.id, template.name)
    await act(async () => answer())
    expect(screen.queryByRole('listitem', { name: `Template ${template.name}` })).toBeNull()
    expect(client.templateList).toHaveBeenCalledTimes(1)
  })

  it('keeps a schedule saved under a deleted name when the delete is answered later', async () => {
    let answer: () => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateDelete: vi.fn(() => new Promise<unknown>((resolve) => (answer = () => resolve({})))),
      templateSave: vi.fn(async () => ({ ...template, id: 'tpl_2' })),
      scheduleSave: vi.fn(async () => schedule),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete' }))
    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const form = within(await screen.findByRole('dialog'))
    fireEvent.change(form.getByLabelText(/^Name/), { target: { value: template.name } })
    fireEvent.change(form.getByLabelText(/^Task/), { target: { value: template.task } })
    fireEvent.click(form.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Template saved'))
    await rowAction('Schedule…')
    const dialog = within(await screen.findByRole('dialog', { name: `Schedule ${template.name}` }))
    fireEvent.change(dialog.getByLabelText('Schedule (UTC)'), { target: { value: '0 3 * * *' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Schedule' }))
    await waitFor(() => expect(client.scheduleSave).toHaveBeenCalled())
    await act(async () => answer())

    const row = screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true })
    expect(row.textContent).toContain('Scheduled')
  })

  it('restores a template whose delete is refused and reopens the confirm with the error', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => [schedule]),
      templateDelete: vi.fn(async () => {
        throw new ApiError(200, 'template.delete: a schedule still fires it', -32002)
      }),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete' }))

    const confirm = within(await screen.findByRole('alertdialog'))
    expect(confirm.getByRole('alert').textContent).toContain('a schedule still fires it')
    expect(screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true }).textContent).toContain('Scheduled')
  })

  it('restores nothing when a delete is refused after the signed-in member changed', async () => {
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => [schedule]),
      templateDelete: vi.fn(() => new Promise<unknown>((_, reject) => (refuse = reject))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(client.templateDelete).toHaveBeenCalled())
    act(() => useStore.setState({ identityKey: crypto.randomUUID() }))
    await act(async () => refuse(new ApiError(200, 'template.delete: a schedule still fires it', -32002)))

    expect(screen.queryByRole('listitem', { name: `Template ${template.name}`, hidden: true })).toBeNull()
  })

  it('keeps a save answered after the signed-in member changed out of the next member\'s rollback', async () => {
    const answers: { resolve: (saved: Template) => void; reject: (cause: Error) => void }[] = []
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve, reject) => answers.push({ resolve, reject }))),
    })
    seed()
    const view = render(<TemplatesRoute params={{}} client={client} />)
    const edit = async (task: string) => {
      await rowAction('Edit')
      const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
      fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: task } })
      fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    }

    await edit('alice edit')
    view.unmount()
    act(() => useStore.setState({ identityKey: crypto.randomUUID() }))
    render(<TemplatesRoute params={{}} client={client} />)
    await edit('bob edit')
    await act(async () => answers[0].resolve({ ...template, task: 'alice edit' }))
    await act(async () => answers[1].reject(new ApiError(200, 'template.save: refused', -32602)))

    expect(screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true }).textContent).toContain(template.task)
  })

  it('removes a template whose delete the server answers after the deadline', async () => {
    let answer: () => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => [schedule]),
      templateDelete: vi.fn(() => new Promise<unknown>((resolve) => (answer = () => resolve({})))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    const confirm = await screen.findByRole('alertdialog')
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(within(confirm).getByRole('button', { name: 'Delete' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      expect(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Retry' })).toBeDefined()
      expect(screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true })).toBeDefined()

      await act(async () => answer())
      expect(screen.queryByRole('alertdialog')).toBeNull()
      expect(screen.queryByRole('listitem', { name: `Template ${template.name}` })).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('drops a delete refused late once its retry was answered', async () => {
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateDelete: vi.fn()
        .mockReturnValueOnce(new Promise((_, reject) => (refuse = reject)))
        .mockResolvedValueOnce({}),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    const confirm = await screen.findByRole('alertdialog')
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(within(confirm).getByRole('button', { name: 'Delete' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Template deleted'))
    await act(async () => refuse(new ApiError(200, 'template.delete: a schedule still fires it', -32002)))

    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(toast.error).not.toHaveBeenCalled()
    expect(screen.queryByRole('listitem', { name: `Template ${template.name}` })).toBeNull()
  })

  it('keeps a template saved under a deleted name when the delete is refused later', async () => {
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateDelete: vi.fn(() => new Promise<unknown>((_, reject) => (refuse = reject))),
      templateSave: vi.fn(async () => ({ ...template, id: 'tpl_2', task: 'sweep instead' })),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete' }))
    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/^Name/), { target: { value: template.name } })
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Template saved'))
    await act(async () => refuse(new ApiError(200, 'template.delete: a schedule still fires it', -32002)))

    const row = screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true })
    expect(row.textContent).toContain('sweep instead')
  })

  it('keeps a template saved under a deleted name when the delete is answered later', async () => {
    let answer: () => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateDelete: vi.fn(() => new Promise<unknown>((resolve) => (answer = () => resolve({})))),
      templateSave: vi.fn(async () => ({ ...template, id: 'tpl_2', task: 'sweep instead' })),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete' }))
    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/^Name/), { target: { value: template.name } })
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Template saved'))
    await act(async () => answer())

    const row = screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true })
    expect(row.textContent).toContain('sweep instead')
  })

  it('shows the saved template after two overlapping edits are both refused', async () => {
    const refusals: ((cause: Error) => void)[] = []
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((_, reject) => refusals.push(reject))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    for (const task of ['first edit', 'second edit']) {
      await rowAction('Edit')
      const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
      fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: task } })
      fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    }
    await act(async () => refusals[0](new ApiError(200, 'template.save: refused', -32602)))
    await act(async () => refusals[1](new ApiError(200, 'template.save: refused', -32602)))

    expect(screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true }).textContent).toContain(template.task)
  })

  it('puts back an older save answered while a newer one was pending when the newer one is refused', async () => {
    const answers: { resolve: (saved: Template) => void; reject: (cause: Error) => void }[] = []
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve, reject) => answers.push({ resolve, reject }))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    for (const task of ['first edit', 'second edit']) {
      await rowAction('Edit')
      const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
      fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: task } })
      fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    }
    await act(async () => answers[0].resolve({ ...template, task: 'first edit' }))
    await act(async () => answers[1].reject(new ApiError(200, 'template.save: refused', -32602)))

    expect(screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true }).textContent).toContain('first edit')
  })

  it('puts back an older schedule answered while a newer one was pending when the newer one is refused', async () => {
    const answers: { resolve: (saved: Schedule) => void; reject: (cause: Error) => void }[] = []
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      scheduleSave: vi.fn(() => new Promise<Schedule>((resolve, reject) => answers.push({ resolve, reject }))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Schedule…')
    const dialog = within(await screen.findByRole('dialog', { name: `Schedule ${template.name}` }))
    for (const cron of ['0 3 * * *', '0 4 * * *']) {
      fireEvent.change(dialog.getByLabelText('Schedule (UTC)'), { target: { value: cron } })
      fireEvent.click(dialog.getByRole('button', { name: /^(Schedule|Update schedule)$/ }))
    }
    await act(async () => answers[0].resolve(schedule))
    await act(async () => answers[1].reject(new ApiError(200, 'schedule.save: refused', -32602)))

    expect(screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true }).textContent).toContain('Scheduled')
  })

  it('offers no Launch, Delete or Schedule on a template whose save is pending', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []), templateSave: vi.fn(() => new Promise<Template>(() => {})) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    expect(screen.getByRole('button', { name: `Launch ${template.name}` }).hasAttribute('disabled')).toBe(true)
    screen.getByRole('button', { name: `More for ${template.name}` }).focus()
    await userEvent.keyboard('{Enter}')

    const menu = within(await screen.findByRole('menu'))
    expect(menu.getByRole('menuitem', { name: 'Delete' }).getAttribute('aria-disabled')).toBe('true')
    expect(menu.getByRole('menuitem', { name: 'Schedule…' }).getAttribute('aria-disabled')).toBe('true')
    expect(menu.getByRole('menuitem', { name: 'Edit' }).getAttribute('aria-disabled')).toBeNull()
  })

  it('still offers no Delete after leaving Templates and coming back while a save is pending', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []), templateSave: vi.fn(() => new Promise<Template>(() => {})) })
    seed()
    const { unmount } = render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    unmount()

    render(<TemplatesRoute params={{}} client={client} />)
    ;(await screen.findByRole('button', { name: `More for ${template.name}` })).focus()
    await userEvent.keyboard('{Enter}')

    expect(within(await screen.findByRole('menu')).getByRole('menuitem', { name: 'Delete' }).getAttribute('aria-disabled')).toBe('true')
  })

  it('shows a save sent before leaving Templates on the list after coming back, over an older list read', async () => {
    vi.mocked(toast.success).mockClear()
    let answer: (saved: Template) => void = () => {}
    let list: (templates: Template[]) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve) => (answer = resolve))),
    })
    seed()
    const { unmount } = render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    unmount()

    client.templateList = vi.fn(() => new Promise<Template[]>((resolve) => (list = resolve)))
    render(<TemplatesRoute params={{}} client={client} />)
    await act(async () => list([template]))
    const row = () => screen.getByRole('listitem', { name: `Template ${template.name}` })
    const launch = () => screen.getByRole('button', { name: `Launch ${template.name}` })
    expect(row().textContent).toContain('sweep instead')
    expect(launch().hasAttribute('disabled')).toBe(true)

    await act(async () => answer({ ...template, task: 'sweep instead' }))
    expect(toast.success).toHaveBeenCalledWith('Template saved')
    expect(row().textContent).toContain('sweep instead')
    expect(launch().hasAttribute('disabled')).toBe(false)
  })

  it('keeps the newer list when an older list read answers after leaving and coming back', async () => {
    const lists: ((templates: Template[]) => void)[] = []
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateList: vi.fn(() => new Promise<Template[]>((resolve) => lists.push(resolve))),
    })
    seed()
    const { unmount } = render(<TemplatesRoute params={{}} client={client} />)
    unmount()
    render(<TemplatesRoute params={{}} client={client} />)

    await act(async () => lists[1]([{ ...template, task: 'newer task' }]))
    await act(async () => lists[0]([template]))

    expect(screen.getByRole('listitem', { name: `Template ${template.name}` }).textContent).toContain('newer task')
  })

  it('drops a list read answered after the signed-in member changed', async () => {
    let list: (templates: Template[]) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateList: vi.fn(() => new Promise<Template[]>((resolve) => (list = resolve))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    act(() => useStore.setState({ identityKey: crypto.randomUUID() }))
    await act(async () => list([template]))

    expect(screen.queryByRole('listitem', { name: `Template ${template.name}` })).toBeNull()
  })

  it('keeps the newer save when two saves of a template are answered in reverse order', async () => {
    const answers: ((saved: Template) => void)[] = []
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve) => answers.push(resolve))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    for (const task of ['first edit', 'second edit']) {
      await rowAction('Edit')
      const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
      fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: task } })
      fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    }
    await act(async () => answers[1]({ ...template, task: 'second edit' }))
    await act(async () => answers[0]({ ...template, task: 'first edit' }))

    expect(screen.getByRole('listitem', { name: `Template ${template.name}` }).textContent).toContain('second edit')
  })

  it('toasts an earlier save that went unanswered, then its refusal, and leaves a form opened since alone', async () => {
    vi.mocked(toast.error).mockClear()
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((_, reject) => (refuse = reject))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const first = within(await screen.findByRole('dialog'))
    fireEvent.change(first.getByLabelText(/^Name/), { target: { value: 'weekly sweep' } })
    fireEvent.change(first.getByLabelText(/^Task/), { target: { value: 'sweep the flaky tests' } })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(first.getByRole('button', { name: 'Save' }))
      fireEvent.click(screen.getByRole('button', { name: 'New template' }))
      const second = within(screen.getByRole('dialog', { name: 'New template' }))
      fireEvent.change(second.getByLabelText(/^Name/), { target: { value: 'other' } })
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})

      expect(toast.error).toHaveBeenCalledWith('The server did not answer in 5 seconds.', expect.objectContaining({ action: expect.objectContaining({ label: 'Retry' }) }))
      await act(async () => refuse(new ApiError(200, 'template.save: harness is not installed', -32602)))
      expect(toast.error).toHaveBeenLastCalledWith('harness is not installed', expect.objectContaining({ id: 'toast', action: undefined }))
      expect(second.queryByRole('alert')).toBeNull()
      expect((second.getByLabelText(/^Name/) as HTMLInputElement).value).toBe('other')
    } finally {
      vi.useRealTimers()
    }
  })

  it('dismisses the toast of an unanswered save once it succeeds late', async () => {
    let answer: (saved: Template) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve) => (answer = resolve))),
    })
    seed()
    vi.mocked(toast.dismiss).mockClear()
    render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Edit')
    const first = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    fireEvent.change(first.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(first.getByRole('button', { name: 'Save' }))
      fireEvent.click(screen.getByRole('button', { name: 'New template' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    expect(toast.dismiss).not.toHaveBeenCalled()

    await act(async () => answer({ ...template, task: 'sweep instead' }))
    expect(toast.dismiss).toHaveBeenCalledWith('toast')
  })

  it('keeps a reopened form open with its edits when the save lands late', async () => {
    let answer: (saved: Template) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve) => (answer = resolve))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
      fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    const reopened = within(screen.getByRole('dialog', { name: 'Edit template' }))
    fireEvent.change(reopened.getByLabelText(/^Task/), { target: { value: 'sweep again' } })
    await act(async () => answer({ ...template, task: 'sweep instead' }))

    expect(reopened.queryByRole('alert')).toBeNull()
    expect((reopened.getByLabelText(/^Task/) as HTMLTextAreaElement).value).toBe('sweep again')
  })

  it('keeps a reopened form open when only its model changed before the save lands late', async () => {
    let answer: (saved: Template) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve) => (answer = resolve))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
      fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    const reopened = within(screen.getByRole('dialog', { name: 'Edit template' }))
    await chooseModel(reopened.getByRole('combobox', { name: /^Model/ }), 'sonnet')
    await act(async () => answer({ ...template, task: 'sweep instead' }))

    expect(reopened.getByRole('combobox', { name: /^Model/ }).textContent).toBe('sonnet')
  })

  it('shows a reopened draft in place of the form open when Reopen is pressed', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Duplicate')
    const open = within(await screen.findByRole('dialog', { name: 'Duplicate template' }))
    fireEvent.change(open.getByLabelText(/^Task/), { target: { value: 'something else' } })

    const draft = { name: template.name, task: 'sweep instead', harness: template.harness, mode: template.mode }
    const failure = { ok: false as const, error: 'harness is not installed', unanswered: false, retry: vi.fn() }
    act(() => reopen('templates', { workspaceID: workspace.id, form: { kind: 'edit', template, draft, failure } }))

    const reopened = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    expect((reopened.getByLabelText(/^Name/) as HTMLInputElement).value).toBe(template.name)
    expect((reopened.getByLabelText(/^Task/) as HTMLTextAreaElement).value).toBe('sweep instead')
  })

  it('toasts a save refused after leaving Templates, and Reopen opens the form there with what was typed', async () => {
    vi.mocked(toast.error).mockClear()
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((_, reject) => (refuse = reject))),
    })
    seed()
    const { unmount } = render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    unmount()
    act(() => useStore.getState().navigate('members'))
    await act(async () => refuse(new ApiError(200, 'template.save: harness is not installed', -32602)))

    expect(toast.error).toHaveBeenLastCalledWith('harness is not installed', expect.objectContaining({ cancel: expect.objectContaining({ label: 'Reopen' }) }))
    const [, options] = vi.mocked(toast.error).mock.lastCall as unknown as [string, { cancel: { onClick: () => void } }]
    act(() => options.cancel.onClick())
    expect(useStore.getState().route.name).toBe('templates')
    render(<TemplatesRoute params={{}} client={client} />)
    const reopened = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    expect((reopened.getByLabelText(/^Task/) as HTMLTextAreaElement).value).toBe('sweep instead')
    expect(reopened.getByRole('alert').textContent).toContain('harness is not installed')
  })

  it('retries an unanswered save with the fields the form shows now', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>(() => {})),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep instead' } })
      fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    const reopened = within(screen.getByRole('dialog', { name: 'Edit template' }))
    fireEvent.change(reopened.getByLabelText(/^Task/), { target: { value: 'sweep again' } })
    fireEvent.click(reopened.getByRole('button', { name: 'Retry' }))

    expect(client.templateSave).toHaveBeenCalledTimes(2)
    expect(client.templateSave).toHaveBeenLastCalledWith(expect.objectContaining({ name: template.name, task: 'sweep again' }))
  })

  it('keeps a schedule saved while the first schedule read was pending', async () => {
    let list: (schedules: Schedule[]) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(() => new Promise<Schedule[]>((resolve) => (list = resolve))),
      scheduleSave: vi.fn(async () => schedule),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Schedule…')
    const dialog = within(await screen.findByRole('dialog', { name: `Schedule ${template.name}` }))
    fireEvent.change(dialog.getByLabelText('Schedule (UTC)'), { target: { value: '0 3 * * *' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Schedule' }))
    await waitFor(() => expect(dialog.getByRole('button', { name: 'Unschedule' })).toBeDefined())
    await act(async () => list([]))

    expect(screen.getByRole('listitem', { name: `Template ${template.name}`, hidden: true }).textContent).toContain('Scheduled')
  })

  it('keeps a template saved while the list read after a workspace switch was pending', async () => {
    let list: (templates: Template[]) => void = () => {}
    const client = fakeApi({
      templateList: vi.fn(() => new Promise<Template[]>((resolve) => (list = resolve))),
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(async () => ({ ...template, id: 'tpl_2', name: 'weekly sweep', task: 'sweep the flaky tests' })),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await act(async () => list([]))
    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/^Name/), { target: { value: 'weekly sweep' } })
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep the flaky tests' } })
    act(() => useStore.setState({ activeWorkspace: otherWorkspace.id }))
    await waitFor(() => expect(client.templateList).toHaveBeenCalledTimes(2))
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Template saved'))
    await act(async () => list([template]))

    expect(screen.getByRole('listitem', { name: 'Template weekly sweep' })).toBeDefined()
    expect(screen.getByRole('listitem', { name: `Template ${template.name}` })).toBeDefined()
  })

  it('keeps a save from a form reopened before the first list read answered', async () => {
    let list: (templates: Template[]) => void = () => {}
    const client = fakeApi({
      templateList: vi.fn(() => new Promise<Template[]>((resolve) => (list = resolve))),
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(async () => ({ ...template, id: 'tpl_2', name: 'weekly sweep', task: 'sweep the flaky tests' })),
    })
    seed()
    reopen('templates', { workspaceID: workspace.id, form: { kind: 'new' } })
    render(<TemplatesRoute params={{}} client={client} />)
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/^Name/), { target: { value: 'weekly sweep' } })
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep the flaky tests' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Template saved'))
    await act(async () => list([]))

    expect(screen.getByRole('listitem', { name: 'Template weekly sweep' })).toBeDefined()
  })

  it('reopens a failed delete in the workspace it was sent from', async () => {
    vi.mocked(toast.error).mockClear()
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateDelete: vi.fn(() => new Promise<unknown>((_, reject) => (refuse = reject))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Delete')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete' }))
    act(() => useStore.setState({ activeWorkspace: otherWorkspace.id }))
    await act(async () => refuse(new ApiError(200, 'template.delete: a schedule still fires it', -32002)))

    expect(screen.queryByRole('alertdialog')).toBeNull()
    const [, options] = vi.mocked(toast.error).mock.lastCall as unknown as [string, { cancel: { onClick: () => void } }]
    act(() => options.cancel.onClick())
    expect(useStore.getState().activeWorkspace).toBe(workspace.id)
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete' }))
    expect(client.templateDelete).toHaveBeenLastCalledWith(workspace.id, template.name)
  })

  it('leaves Unschedule free in a workspace whose same-named template has no save pending', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async (id: string) => (id === otherWorkspace.id ? [{ ...schedule, workspace_id: otherWorkspace.id }] : [])),
      scheduleSave: vi.fn(() => new Promise<Schedule>(() => {})),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await rowAction('Schedule…')
    const dialog = within(await screen.findByRole('dialog', { name: `Schedule ${template.name}` }))
    fireEvent.change(dialog.getByLabelText('Schedule (UTC)'), { target: { value: '0 3 * * *' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Schedule' }))
    expect(dialog.getByRole('button', { name: 'Saving…' })).toBeDefined()

    act(() => useStore.setState({ activeWorkspace: otherWorkspace.id }))
    await waitFor(() => expect(client.scheduleList).toHaveBeenCalledWith(otherWorkspace.id))
    expect(dialog.getByRole('button', { name: 'Unschedule' }).hasAttribute('disabled')).toBe(false)
  })

  it('labels Unschedule while the server answers, and says when it is slow', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => [schedule]), scheduleDelete: vi.fn(() => new Promise<unknown>(() => {})) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    await screen.findByText(/Scheduled/)
    await rowAction('Schedule…')
    const dialog = within(await screen.findByRole('dialog', { name: `Schedule ${template.name}` }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(dialog.getByRole('button', { name: 'Unschedule' }))
      expect(dialog.getByRole('button', { name: 'Unscheduling…' }).hasAttribute('disabled')).toBe(true)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      expect(dialog.getByRole('button', { name: 'Still waiting for the server…' })).toBeDefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps a save answered after a workspace switch off the newly shown list', async () => {
    let answer: (saved: Template) => void = () => {}
    const client = fakeApi({
      templateList: vi.fn(async (id: string) => (id === workspace.id ? [template] : [])),
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(() => new Promise<Template>((resolve) => (answer = resolve))),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)
    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/^Name/), { target: { value: 'weekly sweep' } })
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'sweep the flaky tests' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    act(() => useStore.setState({ activeWorkspace: otherWorkspace.id }))
    expect(await screen.findByRole('heading', { name: 'No templates yet' })).toBeDefined()
    await act(async () => answer({ ...template, id: 'tpl_2', name: 'weekly sweep' }))
    expect(screen.queryByRole('listitem', { name: 'Template weekly sweep' })).toBeNull()
  })

  it('explains templates and offers one when the workspace has none', async () => {
    const client = fakeApi({ templateList: vi.fn(async () => []), scheduleList: vi.fn(async () => []) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    expect(await screen.findByRole('heading', { name: 'No templates yet' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'New template' }))
    expect(await screen.findByRole('dialog', { name: 'New template' })).toBeDefined()
  })

  // The route follows the sidebar switcher rather than carrying a picker of
  // its own, so changing the active workspace re-reads against the new one.
  it('reads the active workspace, not a picker of its own', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []) })
    seed({ activeWorkspace: otherWorkspace.id })
    render(<TemplatesRoute params={{}} client={client} />)

    await waitFor(() => expect(client.templateList).toHaveBeenCalledWith(otherWorkspace.id))
    expect(screen.queryByLabelText('Workspace')).toBeNull()
  })
})
