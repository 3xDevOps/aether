import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { BudgetDialog, WorkspaceSettingsDialog } from '@/routes/admin-dialogs'
import { useStore } from '@/store'
import { budget, fakeApi, workspace } from '@/test/fixtures'

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
  })
}

describe('workspace settings dialog', () => {
  it('saves settings with the wire steer_others value', async () => {
    const client = fakeApi({
      workspaceSettings: vi.fn(async () => ({
        ...workspace,
        steer_others: 'admins_only',
      })),
    })
    seed()
    render(
      <WorkspaceSettingsDialog
        workspaceID={workspace.id}
        client={client}
        onClose={() => {}}
      />,
    )

    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/Who may steer/), {
      target: { value: 'admins_only' },
    })
    fireEvent.click(dialog.getByRole('button', { name: 'Save' }))

    expect(client.workspaceSettings).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      steer_others: 'admins_only',
    })
    // The updated workspace lands back in the store.
    await waitFor(() => {
      expect(useStore.getState().workspaces[workspace.id].steer_others).toBe(
        'admins_only',
      )
    })
  })

  // The branch runs fork from is what the steering policy governs, so the
  // dialog shows it beside the policy rather than making the reader guess.
  it('shows the workspace base branch as read-only context', async () => {
    seed()
    render(
      <WorkspaceSettingsDialog
        workspaceID={workspace.id}
        client={fakeApi()}
        onClose={() => {}}
      />,
    )

    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByText(workspace.base_branch)).toBeDefined()
  })
})

describe('budget dialog', () => {
  it('sets a cap on the workspace', async () => {
    const client = fakeApi({
      budgetSet: vi.fn(async () => budget(workspace.id)),
    })
    seed()
    render(
      <BudgetDialog workspaceID={workspace.id} client={client} onClose={() => {}} />,
    )

    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/Limit/), { target: { value: '2' } })
    fireEvent.change(dialog.getByLabelText(/Warn/), { target: { value: '1' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Set' }))

    expect(client.budgetSet).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      limit_usd: 2,
      warn_usd: 1,
    })
  })

  it('clears the cap through the same verb', async () => {
    const client = fakeApi({
      budgetSet: vi.fn(async () => budget(workspace.id)),
    })
    seed()
    render(
      <BudgetDialog workspaceID={workspace.id} client={client} onClose={() => {}} />,
    )

    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Clear budget' }))

    expect(client.budgetSet).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      clear: true,
    })
  })
})
