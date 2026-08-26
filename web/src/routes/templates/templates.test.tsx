import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { Schedule } from '@/lib/types'
import { TemplatesRoute } from '@/routes/templates'
import { useStore, type RootState } from '@/store'
import {
  alice,
  fakeApi,
  otherWorkspace,
  serverInfo,
  template,
  workspace,
} from '@/test/fixtures'

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
    workspaces: {
      [workspace.id]: workspace,
      [otherWorkspace.id]: otherWorkspace,
    },
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

describe('templates view', () => {
  it('saves a template through the gateway', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateSave: vi.fn(async () => template),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'New template' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/^Name/), {
      target: { value: 'weekly sweep' },
    })
    fireEvent.change(dialog.getByLabelText(/^Task/), {
      target: { value: 'sweep the flaky tests' },
    })
    fireEvent.change(dialog.getByLabelText(/^Mode/), { target: { value: 'headless' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    expect(client.templateSave).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      name: 'weekly sweep',
      task: 'sweep the flaky tests',
      harness: 'claude',
      mode: 'headless',
    })
  })

  it('renders next_fire_at from the schedule.save response, never client cron', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      scheduleSave: vi.fn(async () => schedule),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    const editor = within(
      await screen.findByRole('form', { name: `Schedule for ${template.name}` }),
    )
    fireEvent.change(editor.getByPlaceholderText(/cron/), {
      target: { value: '0 3 * * *' },
    })
    fireEvent.click(editor.getByRole('button', { name: 'Schedule' }))

    // The preview is the server's next_fire_at, verbatim.
    expect(await screen.findByText(/2026-08-23T03:00:00Z/)).toBeDefined()
    expect(client.scheduleSave).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      template: template.name,
      cron: '0 3 * * *',
    })
  })

  it('launches a template and navigates to the run', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Launch' }))

    expect(client.templateLaunch).toHaveBeenCalledWith(workspace.id, template.name)
    expect(await screen.findByText('nightly triage')).toBeDefined()
    // fakeApi's templateLaunch returns run_tpl; navigation lands on it.
    await waitFor(() => {
      expect(useStore.getState().route).toEqual({
        name: 'run',
        params: { runId: 'run_tpl' },
      })
    })
  })

  // The route follows the sidebar switcher rather than carrying a picker of
  // its own, so changing the active workspace re-reads against the new one.
  it('reads the active workspace, not a picker of its own', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []) })
    seed({ activeWorkspace: otherWorkspace.id })
    render(<TemplatesRoute params={{}} client={client} />)

    await waitFor(() => {
      expect(client.templateList).toHaveBeenCalledWith(otherWorkspace.id)
    })
    expect(screen.queryByLabelText('Workspace')).toBeNull()
    // The budget and settings verbs moved to the workspace route.
    expect(screen.queryByRole('button', { name: 'Budget' })).toBeNull()
    expect(
      screen.queryByRole('button', { name: 'Workspace settings' }),
    ).toBeNull()
  })
})
