import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { Schedule } from '@/lib/types'
import { TemplatesRoute } from '@/routes/templates'
import { useStore, type RootState } from '@/store'
import {
  alice,
  fakeApi,
  otherSession,
  serverInfo,
  session,
  template,
} from '@/test/fixtures'

const schedule: Schedule = {
  id: 'sch_1',
  session_id: session.id,
  template: template.name,
  cron: '0 3 * * *',
  member_id: alice.id,
  created_at: '2026-08-14T09:05:00Z',
  next_fire_at: '2026-08-23T03:00:00Z',
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    sessions: { [session.id]: session, [otherSession.id]: otherSession },
    members: { [alice.id]: alice },
    info: serverInfo,
    // template.save/delete and the budget/settings buttons are gated; an
    // upgraded gateway advertising every method renders them all.
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
      session_id: session.id,
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
      session_id: session.id,
      template: template.name,
      cron: '0 3 * * *',
    })
  })

  it('launches a template and navigates to the run', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []) })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Launch' }))

    expect(client.templateLaunch).toHaveBeenCalledWith(session.id, template.name)
    expect(await screen.findByText('nightly triage')).toBeDefined()
    // fakeApi's templateLaunch returns run_tpl; navigation lands on it.
    await waitFor(() => {
      expect(useStore.getState().route).toEqual({
        name: 'run',
        params: { runId: 'run_tpl' },
      })
    })
  })

  it('opens the budget dialog from the session header and sets a cap', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      budgetSet: vi.fn(async () => ({
        session_id: session.id,
        state: 'ok' as const,
        spend: {
          runs: 0,
          metered_runs: 0,
          unmetered_runs: 0,
          input_tokens: 0,
          output_tokens: 0,
          cost_usd: 0,
        },
      })),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Budget' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/Limit/), { target: { value: '2' } })
    fireEvent.change(dialog.getByLabelText(/Warn/), { target: { value: '1' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Set' }))

    expect(client.budgetSet).toHaveBeenCalledWith({
      session_id: session.id,
      limit_usd: 2,
      warn_usd: 1,
    })
  })

  it('saves session settings with the wire steer_others value', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      sessionSettings: vi.fn(async () => ({
        ...session,
        steer_others: 'admins_only',
      })),
    })
    seed()
    render(<TemplatesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Session settings' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/Who may steer/), {
      target: { value: 'admins_only' },
    })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    expect(client.sessionSettings).toHaveBeenCalledWith({
      session_id: session.id,
      steer_others: 'admins_only',
    })
    // The updated session lands back in the store.
    await waitFor(() => {
      expect(useStore.getState().sessions[session.id].steer_others).toBe(
        'admins_only',
      )
    })
  })
})
