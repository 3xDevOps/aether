import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { WorkspaceEnvironmentSection } from '@/components/workspace-environment'
import { ApiError } from '@/lib/api'
import type { WorkspaceEnvironment } from '@/lib/types'
import { fakeApi, workspace } from '@/test/fixtures'

const saved: WorkspaceEnvironment = {
  workspace_id: workspace.id,
  setup_script: 'npm ci\n',
  variables: [
    { name: 'API_URL', value: 'https://api.example.test' },
    { name: 'NODE_ENV', value: 'test' },
    { name: 'NPM_TOKEN', secret: true },
  ],
  egress: false,
  egress_hosts: [],
}

function mount(over: Parameters<typeof fakeApi>[0] = {}, editable = true) {
  const client = fakeApi({ workspaceEnvironment: vi.fn(async () => saved), ...over })
  render(<WorkspaceEnvironmentSection workspaceID={workspace.id} client={client} editable={editable} reveal={false} />)
  return client
}

const rowOf = (name: string) => screen.getByDisplayValue(name).closest('li')!

describe('workspace environment', () => {
  it('sends only what an admin changed, and never a secret it was not given', async () => {
    const after: WorkspaceEnvironment = { ...saved, setup_script: 'npm ci\nnpm session build\n' }
    const client = mount({ workspaceEnvironmentSet: vi.fn(async () => after) })

    const script = await screen.findByRole('textbox', { name: 'Setup script' })
    expect(screen.getByRole('button', { name: 'Save' })).toHaveProperty('disabled', true)
    expect(screen.getByText(/Sessions launched after you save get this\. Containers that already exist keep what they started with\./)).toBeDefined()

    fireEvent.change(script, { target: { value: 'npm ci\nnpm session build\n' } })
    fireEvent.change(within(rowOf('NODE_ENV')).getByRole('textbox', { name: 'Value of NODE_ENV' }), { target: { value: 'ci' } })
    fireEvent.click(screen.getByRole('button', { name: 'Remove API_URL' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add variable' }))
    const added = screen.getAllByRole('listitem').at(-1)!
    fireEvent.change(within(added).getByRole('textbox', { name: 'Name' }), { target: { value: 'SENTRY_DSN' } })
    fireEvent.change(within(added).getByLabelText('Value of SENTRY_DSN'), { target: { value: 'dsn-value' } })
    fireEvent.click(within(added).getByRole('checkbox', { name: 'Secret' }))
    expect(screen.getByText('Unsaved changes.')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.workspaceEnvironmentSet).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      setup_script: 'npm ci\nnpm session build\n',
      set: [{ name: 'NODE_ENV', value: 'ci' }, { name: 'SENTRY_DSN', value: 'dsn-value', secret: true }],
      unset: ['API_URL'],
    }))
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Setup script' })).toHaveProperty('value', after.setup_script))
    expect(screen.queryByText('Unsaved changes.')).toBeNull()
  })

  it('keeps a saved secret hidden until the admin replaces it', async () => {
    const client = mount({ workspaceEnvironmentSet: vi.fn(async () => saved) })

    const row = within(await waitFor(() => rowOf('NPM_TOKEN')))
    expect(row.getByLabelText('Value of NPM_TOKEN, hidden')).toHaveProperty('value', '••••••••')
    expect(row.getByDisplayValue('NPM_TOKEN')).toHaveProperty('readOnly', true)
    expect(row.queryByRole('checkbox')).toBeNull()

    fireEvent.click(row.getByRole('button', { name: 'Replace' }))
    const value = row.getByLabelText('Value of NPM_TOKEN')
    expect(value).toHaveProperty('type', 'password')
    expect(document.activeElement).toBe(value)

    // An empty replacement would erase the secret, so it is refused here.
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(screen.getByText('Enter the secret’s value.')).toBeDefined()
    expect(client.workspaceEnvironmentSet).not.toHaveBeenCalled()

    fireEvent.change(value, { target: { value: 'next-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.workspaceEnvironmentSet).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      set: [{ name: 'NPM_TOKEN', value: 'next-token', secret: true }],
      unset: [],
    }))
  })

  it('previews a pasted .env and adds it as secrets', async () => {
    const client = mount({ workspaceEnvironmentSet: vi.fn(async () => saved) })

    fireEvent.click(await screen.findByRole('button', { name: 'Import .env…' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Import .env' }))
    expect(dialog.getByRole('button', { name: 'Add variables' })).toHaveProperty('disabled', true)
    fireEvent.change(dialog.getByRole('textbox', { name: 'NAME=VALUE lines' }), {
      target: { value: 'NODE_ENV=production\nDATABASE_URL="postgres://db.example.test/app"\nnot a pair\nSENTRY_DSN=\nNPM_TOKEN=\n' },
    })
    expect(dialog.getByRole('status').textContent).toBe('Line 3 is not NAME=VALUE and is skipped.')
    const preview = dialog.getAllByRole('listitem').map((item) => item.textContent)
    expect(preview).toEqual([
      'NODE_ENVReplaces the current valueSecret',
      'DATABASE_URLNewSecret',
      'SENTRY_DSNNew',
      'NPM_TOKENReplaces the current value',
    ])

    fireEvent.click(dialog.getByRole('button', { name: 'Add 4 variables' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(client.workspaceEnvironmentSet).not.toHaveBeenCalled()
    expect(within(rowOf('DATABASE_URL')).getByLabelText('Value of DATABASE_URL')).toHaveProperty('type', 'password')

    // An empty line over a saved secret does not remove it: Save asks first.
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(within(rowOf('NPM_TOKEN')).getByText('Enter the secret’s value.')).toBeDefined()
    expect(client.workspaceEnvironmentSet).not.toHaveBeenCalled()
    fireEvent.click(within(rowOf('NPM_TOKEN')).getByRole('button', { name: 'Keep current' }))

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.workspaceEnvironmentSet).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      set: [
        { name: 'NODE_ENV', value: 'production', secret: true },
        { name: 'DATABASE_URL', value: 'postgres://db.example.test/app', secret: true },
        // An empty value that replaces no secret has nothing to hide.
        { name: 'SENTRY_DSN', value: '' },
      ],
      unset: [],
    }))
  })

  it('keeps the line breaks of a value, which a one-line field would drop', async () => {
    const client = mount({
      workspaceEnvironment: vi.fn(async () => ({ ...saved, variables: [{ name: 'BANNER', value: 'first\nsecond' }] })),
      workspaceEnvironmentSet: vi.fn(async () => saved),
    })

    const banner = await screen.findByLabelText('Value of BANNER')
    expect(banner.tagName).toBe('TEXTAREA')
    fireEvent.change(banner, { target: { value: 'first\nsecond!' } })

    fireEvent.click(screen.getByRole('button', { name: 'Import .env…' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Import .env' }))
    fireEvent.change(dialog.getByRole('textbox', { name: 'NAME=VALUE lines' }), { target: { value: 'DEPLOY_KEY="line one\\nline two"' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Add 1 variable' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    // A secret cannot be masked in a multi-line field, so it is shown hidden.
    expect(within(rowOf('DEPLOY_KEY')).getByLabelText('Value of DEPLOY_KEY, hidden')).toHaveProperty('readOnly', true)

    fireEvent.click(screen.getByRole('button', { name: 'Add variable' }))
    const added = within(screen.getAllByRole('listitem').at(-1)!)
    fireEvent.change(added.getByRole('textbox', { name: 'Name' }), { target: { value: 'SSH_KEY' } })
    fireEvent.click(added.getByRole('checkbox', { name: 'Secret' }))
    const paste = (text: string) => fireEvent.paste(added.getByLabelText('Value of SSH_KEY'), { clipboardData: { getData: () => text } })
    // The field takes one line as it always did; only a paste it would flatten is handled.
    expect(paste('one-line-token\n')).toBe(true)
    expect(paste('BEGIN\r\nkey\r\nEND\r\n')).toBe(false)
    expect(added.getByLabelText('Value of SSH_KEY, hidden')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.workspaceEnvironmentSet).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      set: [
        { name: 'BANNER', value: 'first\nsecond!' },
        { name: 'DEPLOY_KEY', value: 'line one\nline two', secret: true },
        { name: 'SSH_KEY', value: 'BEGIN\nkey\nEND\n', secret: true },
      ],
      unset: [],
    }))
  })

  it('says when a save took effect and its timeline note did not', async () => {
    const warning = 'the change is saved, but its timeline note was not recorded: database is locked'
    mount({ workspaceEnvironmentSet: vi.fn(async () => ({ ...saved, setup_script: 'make deps', warning })) })

    fireEvent.change(await screen.findByRole('textbox', { name: 'Setup script' }), { target: { value: 'make deps' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect((await screen.findByRole('status')).textContent).toBe(`Saved${warning}`)
    expect(screen.queryByText('Unsaved changes.')).toBeNull()
  })

  it('refuses two variables of one name before calling the server', async () => {
    const client = mount()

    fireEvent.click(await screen.findByRole('button', { name: 'Add variable' }))
    const added = screen.getAllByRole('listitem').at(-1)!
    const name = within(added).getByRole('textbox', { name: 'Name' })
    expect(document.activeElement).toBe(name)
    fireEvent.change(name, { target: { value: 'NODE_ENV' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(within(added).getByText('NODE_ENV is already in this list.')).toBeDefined()
    expect(name.getAttribute('aria-invalid')).toBe('true')
    expect(client.workspaceEnvironmentSet).not.toHaveBeenCalled()
  })

  it('shows the server’s refusal and keeps the edit', async () => {
    mount({
      workspaceEnvironmentSet: vi.fn(async () => {
        throw new ApiError(403, 'workspace.environment.set: workspace administration requires the admin role')
      }),
    })

    fireEvent.change(await screen.findByRole('textbox', { name: 'Setup script' }), { target: { value: 'make deps' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toBe('Not savedworkspace.environment.set: workspace administration requires the admin role')
    expect(screen.getByRole('textbox', { name: 'Setup script' })).toHaveProperty('value', 'make deps')
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
    expect(screen.getByRole('textbox', { name: 'Setup script' })).toHaveProperty('value', 'npm ci\n')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('gives a member without the admin role the script and the names, read-only', async () => {
    mount({}, false)

    expect((await screen.findByLabelText('Setup script')).textContent).toBe('npm ci\n')
    const variables = within(screen.getByRole('list', { name: 'Variables' })).getAllByRole('listitem').map((item) => item.textContent)
    expect(variables).toEqual(['API_URLhttps://api.example.test', 'NODE_ENVtest', 'NPM_TOKENSecret'])
    expect(screen.getByText('Only an admin can change this.')).toBeDefined()
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('turns egress control on and edits the allowed hosts in the same save', async () => {
    const limited: WorkspaceEnvironment = { ...saved, egress: true, egress_hosts: ['api.anthropic.com', 'github.com'] }
    const client = mount({
      workspaceEnvironment: vi.fn(async () => ({ ...limited, egress: false })),
      workspaceEnvironmentSet: vi.fn(async () => limited),
    })

    const toggle = await screen.findByRole('checkbox', { name: 'Limit outbound traffic' })
    expect(screen.getByText(/the host list applies at once to runs started with it on/)).toBeDefined()
    expect(screen.queryByRole('list', { name: 'Allowed hosts' })).toBeNull()
    fireEvent.click(toggle)
    fireEvent.click(screen.getByRole('button', { name: 'Remove github.com' }))
    const host = screen.getByRole('textbox', { name: 'Host to allow' })
    fireEvent.change(host, { target: { value: 'https://mirror.example.com' } })
    fireEvent.keyDown(host, { key: 'Enter' })
    expect(screen.getByText('Enter a host such as example.com or *.example.com, without https:// or a port.')).toBeDefined()
    fireEvent.change(host, { target: { value: '*.Mirror.Example.com.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Allow' }))
    const allowed = within(screen.getByRole('list', { name: 'Allowed hosts' })).getAllByRole('listitem').map((item) => item.textContent)
    expect(allowed).toEqual(['*.mirror.example.com', 'api.anthropic.com'])

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.workspaceEnvironmentSet).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      set: [],
      unset: [],
      egress: true,
      egress_allow: ['*.mirror.example.com'],
      egress_deny: ['github.com'],
    }))
  })

  it('leaves egress control out for a server that predates it', async () => {
    const { egress: _egress, egress_hosts: _hosts, ...older } = saved
    const client = mount({ workspaceEnvironment: vi.fn(async () => older), workspaceEnvironmentSet: vi.fn(async () => older) })

    fireEvent.change(await screen.findByRole('textbox', { name: 'Setup script' }), { target: { value: 'make deps' } })
    expect(screen.queryByRole('checkbox', { name: 'Limit outbound traffic' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.workspaceEnvironmentSet).toHaveBeenCalledWith({
      workspace_id: workspace.id,
      setup_script: 'make deps',
      set: [],
      unset: [],
    }))
  })

  it('shows a member without the admin role whether egress is limited, read-only', async () => {
    mount({ workspaceEnvironment: vi.fn(async () => ({ ...saved, egress: true, egress_hosts: ['github.com'] })) }, false)

    expect(await screen.findByText('On.')).toBeDefined()
    expect(within(screen.getByRole('list', { name: 'Allowed hosts' })).getByRole('listitem').textContent).toBe('github.com')
    expect(screen.queryByRole('checkbox')).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('says so when there is nothing set', async () => {
    mount({ workspaceEnvironment: vi.fn(async () => ({ workspace_id: workspace.id, setup_script: '', variables: [], egress: false, egress_hosts: [] })) }, false)

    expect(await screen.findByText('No setup script.')).toBeDefined()
    expect(screen.getByText('No variables.')).toBeDefined()
  })

  it('shows why the environment did not load and retries', async () => {
    const load = vi.fn()
      .mockRejectedValueOnce(new ApiError(404, 'workspace.environment.get: method not found'))
      .mockResolvedValue(saved)
    mount({ workspaceEnvironment: load })

    expect((await screen.findByRole('alert')).textContent).toContain('workspace.environment.get: method not found')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('textbox', { name: 'Setup script' })).toHaveProperty('value', 'npm ci\n')
  })
})
