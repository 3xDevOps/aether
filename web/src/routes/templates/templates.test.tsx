import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Schedule } from '@/lib/types'
import { TemplatesRoute } from '@/routes/templates'
import { useStore, type RootState } from '@/store'
import { alice, fakeApi, otherWorkspace, serverInfo, template, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

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
    route: { name: 'templates', params: {} },
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
    await pickOption(dialog.getByLabelText(/^Mode/), 'Standard')
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    expect(client.templateSave).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      name: 'weekly sweep',
      task: 'sweep the flaky tests',
      harness: 'claude',
      mode: 'tui',
    })
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

  // The confirm is where a refused delete is reported, so it has to outlive
  // the request: an Escape mid-flight would take the answer off screen with it.
  it('confirms a delete and keeps the refusal on screen', async () => {
    let refuse: (reason: Error) => void = () => {}
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateDelete: vi.fn(
        () =>
          new Promise<never>((_, reject) => {
            refuse = reject
          }),
      ),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    await rowAction('Delete')
    const confirm = await screen.findByRole('alertdialog')
    expect(client.templateDelete).not.toHaveBeenCalled()
    // A confirm has no close X: answering it is the only way past it.
    expect(within(confirm).getAllByRole('button')).toHaveLength(2)

    fireEvent.click(within(confirm).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(client.templateDelete).toHaveBeenCalledWith(workspace.id, template.name))

    fireEvent.keyDown(confirm, { key: 'Escape' })
    expect(screen.getByRole('alertdialog')).toBeDefined()

    refuse(new Error('template.delete: a schedule still fires it'))

    expect(await screen.findByText(/a schedule still fires it/)).toBeDefined()
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
