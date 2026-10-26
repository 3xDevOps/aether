import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Template } from '@/lib/types'
import { useStore } from '@/store'
import { fakeApi, otherWorkspace, template, workspace } from '@/test/fixtures'
import { detail, rowAction, seed, Templates } from '@/test/templates'

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn(() => 'toast'), dismiss: vi.fn() },
}))

const audit: Template = {
  ...template,
  id: 'tpl_audit',
  name: 'dep-audit',
  task: 'Audit {{ecosystem}} dependencies for {{severity}} advisories. Fix {{ severity }} first.',
  params: { ecosystem: 'npm' },
  budget_usd: 2,
}

describe('template library', () => {
  it('fills placeholders inline and launches with only the values typed over the defaults', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []), templateList: vi.fn(async () => [audit]) })
    seed()
    render(<Templates client={client} />)

    const shown = within(await screen.findByRole('region', { name: 'dep-audit' }))
    expect(shown.getByText('1 of 2 filled')).toBeDefined()
    expect(shown.getByText('$2.00 budget hint')).toBeDefined()
    const launch = shown.getByRole('button', { name: 'Launch dep-audit' })
    expect(launch.getAttribute('aria-disabled')).toBe('true')
    fireEvent.pointerEnter(launch)
    expect(await shown.findByText('Fill 1 more to launch')).toBeDefined()
    fireEvent.click(launch)
    expect(client.templateLaunch).not.toHaveBeenCalled()

    expect((shown.getByRole('textbox', { name: 'ecosystem (default: npm)' }) as HTMLInputElement).value).toBe('npm')
    const severity = shown.getByRole('textbox', { name: 'severity (required, no default)' })
    fireEvent.change(severity, { target: { value: 'high' } })
    expect(within(shown.getByRole('region', { name: 'Preview' })).getByText((_, el) => el?.textContent === 'Audit npm dependencies for high advisories. Fix high first.')).toBeDefined()
    fireEvent.keyDown(severity, { key: 'Enter' })

    await waitFor(() => expect(client.templateLaunch).toHaveBeenCalledWith(workspace.id, 'dep-audit', { severity: 'high' }))
    await waitFor(() => expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_tpl' } }))
  })

  it('holds every Launch while a launch is in flight so one click starts one run', async () => {
    const weekly: Template = { ...template, id: 'tpl_weekly', name: 'weekly' }
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateList: vi.fn(async () => [template, weekly]),
      templateLaunch: vi.fn(() => new Promise<never>(() => {})),
    })
    seed()
    render(<Templates client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: `Launch ${template.name}` }))
    const held = await screen.findByRole('button', { name: 'Launching…' })
    expect(held.hasAttribute('disabled')).toBe(true)
    fireEvent.click(held)
    fireEvent.click(screen.getByRole('option', { name: 'Template weekly' }))
    const other = await screen.findByRole('button', { name: 'Launch weekly' })
    expect(other.hasAttribute('disabled')).toBe(true)
    fireEvent.click(other)
    fireEvent.click(screen.getByRole('option', { name: `Template ${template.name}` }))
    expect((await screen.findByRole('button', { name: 'Launching…' })).hasAttribute('disabled')).toBe(true)
    expect(client.templateLaunch).toHaveBeenCalledTimes(1)
  })

  it('keeps values typed in one workspace out of a same-named template in another', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateList: vi.fn(async (workspaceID: string) => [{ ...audit, workspace_id: workspaceID }]),
    })
    seed()
    render(<Templates client={client} />)

    const severity = { name: 'severity (required, no default)' }
    fireEvent.change(await screen.findByRole('textbox', severity), { target: { value: 'high' } })
    act(() => useStore.getState().setActiveWorkspace(otherWorkspace.id))
    expect((await screen.findByRole('textbox', severity) as HTMLInputElement).value).toBe('')
    expect(screen.getByText('1 of 2 filled')).toBeDefined()

    act(() => useStore.getState().setActiveWorkspace(workspace.id))
    expect((screen.getByRole('textbox', severity) as HTMLInputElement).value).toBe('high')
  })

  it('carries parameter defaults and the budget hint through an edit and drops a default the task no longer uses', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []), templateList: vi.fn(async () => [audit]), templateSave: vi.fn(async () => audit) })
    seed()
    render(<Templates client={client} />)

    await rowAction('Edit')
    const dialog = within(await screen.findByRole('dialog', { name: 'Edit template' }))
    const parameters = within(dialog.getByRole('group', { name: 'Parameters' }))
    expect((parameters.getByLabelText('ecosystem') as HTMLInputElement).value).toBe('npm')
    fireEvent.change(dialog.getByLabelText(/^Task/), { target: { value: 'Audit for {{severity}} advisories and {{scope}}.' } })
    expect(parameters.queryByLabelText('ecosystem')).toBeNull()
    fireEvent.change(parameters.getByLabelText('scope'), { target: { value: 'prod' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    expect(client.templateSave).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      name: 'dep-audit',
      task: 'Audit for {{severity}} advisories and {{scope}}.',
      harness: audit.harness,
      mode: audit.mode,
      params: { scope: 'prod' },
      budget_usd: 2,
    })
  })

  it('moves the selection with the arrow keys into the address and searches by task', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []), templateList: vi.fn(async () => [audit, template]) })
    seed()
    render(<Templates client={client} />)

    const first = await screen.findByRole('option', { name: 'Template dep-audit' })
    act(() => first.focus())
    await userEvent.keyboard('{ArrowDown}')
    expect(useStore.getState().route).toEqual({ name: 'templates', params: { template: template.name } })
    expect(document.activeElement).toBe(screen.getByRole('option', { name: `Template ${template.name}` }))
    expect(detail(template.name)).toBeDefined()

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search templates' }), { target: { value: 'advisories' } })
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('aria-label'))).toEqual(['Template dep-audit'])
    expect(screen.getByText('1 of 2')).toBeDefined()
    expect(detail(template.name)).toBeDefined()
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search templates' }), { target: { value: 'nothing like it' } })
    expect(screen.getByText('No templates match')).toBeDefined()
  })

  it('selects the next template after deleting the selected one', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []), templateList: vi.fn(async () => [audit, template]) })
    seed({ route: { name: 'templates', params: { template: 'dep-audit' } } })
    render(<Templates client={client} />)

    await screen.findByRole('region', { name: 'dep-audit' })
    const trigger = screen.getByRole('button', { name: 'More for dep-audit' })
    trigger.focus()
    await userEvent.keyboard('{Enter}')
    const item = within(await screen.findByRole('menu')).getByRole('menuitem', { name: 'Delete' })
    item.focus()
    await userEvent.keyboard('{Enter}')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete' }))

    expect(useStore.getState().route.params).toEqual({ template: template.name })
    expect(await screen.findByRole('region', { name: template.name })).toBeDefined()
  })

  it('says a linked template is not in this workspace and shows the first one', async () => {
    const client = fakeApi({ scheduleList: vi.fn(async () => []) })
    seed({ route: { name: 'templates', params: { template: 'gone' } } })
    render(<Templates client={client} />)

    expect(await screen.findByText('That template is not in this workspace.')).toBeDefined()
    expect(detail(template.name)).toBeDefined()
  })

  it('shows a failed list read with the server error and retries it', async () => {
    const client = fakeApi({
      scheduleList: vi.fn(async () => []),
      templateList: vi.fn().mockRejectedValueOnce(new Error('template.list: workspace is archived')).mockResolvedValue([template]),
    })
    seed()
    render(<Templates client={client} />)

    expect((await screen.findByRole('alert')).textContent).toContain('template.list: workspace is archived')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('option', { name: `Template ${template.name}` })).toBeDefined()
    expect(screen.queryByRole('alert')).toBeNull()
  })
})
