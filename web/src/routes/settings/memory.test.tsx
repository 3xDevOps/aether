import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { AssistantSettings } from '@/lib/assistant-types'
import type { Preference } from '@/lib/types'
import { SettingsRoute } from '@/routes/settings'
import { forgetWarning } from '@/routes/settings/memory-row'
import { MemorySection, preferencesLabel } from '@/routes/settings/memory'
import { useStore } from '@/store'
import { alice, fakeApi, serverInfo, workspace } from '@/test/fixtures'

function pref(over: Partial<Preference> = {}): Preference {
  return {
    id: 'pref-1',
    text: 'Prefer table output',
    enabled: true,
    source: 'assistant',
    created_at: '2026-10-27T10:00:00Z',
    updated_at: '2026-10-27T10:00:00Z',
    ...over,
  }
}

function seed(entry?: string) {
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice },
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    hydrated: true,
    capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    route: { name: 'settings', params: { section: 'memory', ...(entry ? { entry } : {}) } },
  }, true)
}

function show(preferences: Preference[], over: Parameters<typeof fakeApi>[0] = {}, enabled = true) {
  const client = fakeApi({ preferenceList: vi.fn(async () => ({ preferences, enabled })), ...over })
  render(<MemorySection client={client} />)
  return client
}

describe('memory in settings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seed()
  })

  it('is a Settings category only where the server has preferences', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['preference.list'], ws: [] } })
    const client = fakeApi({ preferenceList: vi.fn(async () => ({ preferences: [pref()], enabled: true })) })
    render(<SettingsRoute params={{ section: 'memory' }} client={client} />)
    expect(await screen.findByRole('heading', { name: 'Memory' })).toBeDefined()
    expect(await screen.findByText('Prefer table output')).toBeDefined()
  })

  it('offers the assistant switch in search only where the page shows it', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['preference.list'], ws: [] } })
    render(<SettingsRoute params={{ section: 'memory' }} client={fakeApi({ preferenceList: vi.fn(async () => ({ preferences: [pref()], enabled: true })) })} />)
    await screen.findByText('Prefer table output')
    expect(screen.queryByRole('checkbox', { name: preferencesLabel })).toBeNull()
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search settings' }), { target: { value: preferencesLabel } })
    expect(screen.getByRole('status').textContent).toBe(`No settings match “${preferencesLabel}”`)
  })

  it('says who can see the preferences and lists each with its scope, source and times', async () => {
    show([
      pref({ last_used_at: '2026-10-27T11:00:00Z' }),
      pref({ id: 'pref-2', text: 'Use pnpm here', workspace_id: workspace.id, source: 'settings', updated_at: '2026-10-26T10:00:00Z' }),
    ])
    expect(screen.getByText(/Only you can see these\. Teammates, admins, your runs and connected apps can't\. Server operators can read the server database\./)).toBeDefined()
    expect(screen.getByText('The assistant uses them when they fit; what you say in a message always wins.')).toBeDefined()
    const rows = within(await screen.findByRole('list', { name: 'Preferences' })).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.textContent).toContain('Prefer table output')
    expect(rows[0]!.textContent).toContain('All my workspaces')
    expect(rows[0]!.textContent).toMatch(/You said in the assistant · [A-Z][a-z]{2} \d+/)
    expect(rows[0]!.textContent).toContain('Last used')
    expect(rows[1]!.textContent).toContain(workspace.name)
    expect(rows[1]!.textContent).toContain('Added in Settings')
    expect(rows[1]!.textContent).toContain('Not used yet')
  })

  it('dates a quote only while the row is as first saved', async () => {
    show([pref({ text: 'Prefer JSON output', created_at: '2026-10-20T10:00:00Z', updated_at: '2026-10-27T10:00:00Z' })])
    const row = within(await screen.findByRole('list', { name: 'Preferences' })).getByRole('listitem')
    expect(row.textContent).toContain('You said in the assistant')
    expect(row.textContent).not.toContain(new Date('2026-10-20T10:00:00Z').toLocaleDateString(undefined, { month: 'short', day: 'numeric' }))
  })

  it('lists the newest change first within the same second', async () => {
    show([pref(), pref({ id: 'pref-2', text: 'Use pnpm here', updated_at: '2026-10-27T10:00:00.001Z' })])
    const rows = within(await screen.findByRole('list', { name: 'Preferences' })).getAllByRole('listitem')
    expect(rows.map((row) => row.id)).toEqual(['preference-pref-2', 'preference-pref-1'])
  })

  it('filters by text', async () => {
    const user = userEvent.setup()
    show([pref(), pref({ id: 'pref-2', text: 'Use pnpm here' })])
    await screen.findByText('Use pnpm here')
    await user.type(screen.getByRole('searchbox', { name: 'Search preferences' }), 'pnpm')
    expect(screen.queryByText('Prefer table output')).toBeNull()
    expect(screen.getByText('Use pnpm here')).toBeDefined()
  })

  it('reads the real error when the list cannot load', async () => {
    const client = fakeApi({ preferenceList: vi.fn(async () => { throw new ApiError(-32001, 'preference.list: database is locked') }) })
    render(<MemorySection client={client} />)
    expect((await screen.findByRole('alert')).textContent).toContain('database is locked')
  })

  it('adds a preference for all workspaces or this one, and keeps the draft with the server error', async () => {
    const user = userEvent.setup()
    const client = show([], {
      preferenceAdd: vi.fn()
        .mockRejectedValueOnce(new ApiError(-32003, 'preference.add: a preference needs at least 3 words and 12 characters'))
        .mockImplementation(async (params) => ({ preference: pref({ id: 'pref-9', text: params.text, workspace_id: params.workspace_id, source: 'settings' }) })),
    })
    await screen.findByRole('heading', { name: 'Nothing remembered yet' })
    const box = screen.getByRole('textbox', { name: 'Add a preference' })
    await user.type(box, 'Reply in English')
    await user.click(screen.getByRole('button', { name: 'Add' }))
    expect((await screen.findByRole('alert')).textContent).toBe('a preference needs at least 3 words and 12 characters')
    expect((box as HTMLTextAreaElement).value).toBe('Reply in English')

    await user.click(screen.getByRole('radio', { name: 'This workspace' }))
    await user.click(screen.getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(client.preferenceAdd).toHaveBeenLastCalledWith({ text: 'Reply in English', workspace_id: workspace.id }))
    expect(await screen.findByText('Added in Settings')).toBeDefined()
    expect((box as HTMLTextAreaElement).value).toBe('')
  })

  it('keeps a draft typed while the previous one saves', async () => {
    const user = userEvent.setup()
    let finish: (value: { preference: Preference }) => void = () => {}
    show([], { preferenceAdd: vi.fn(() => new Promise<{ preference: Preference }>((resolve) => { finish = resolve })) })
    await screen.findByRole('heading', { name: 'Nothing remembered yet' })
    const box = screen.getByRole('textbox', { name: 'Add a preference' }) as HTMLTextAreaElement
    await user.type(box, 'Reply in English please')
    await user.click(screen.getByRole('button', { name: 'Add' }))
    await user.clear(box)
    await user.type(box, 'Prefer table output')
    await act(async () => finish({ preference: pref({ id: 'pref-9', text: 'Reply in English please', source: 'settings' }) }))
    expect(await screen.findByText('Reply in English please')).toBeDefined()
    expect(box.value).toBe('Prefer table output')
  })

  it('edits a preference in place and shows a refusal verbatim', async () => {
    const user = userEvent.setup()
    const client = show([pref()], {
      preferenceUpdate: vi.fn()
        .mockRejectedValueOnce(new ApiError(-32602, 'preference.update: text looks like an API key (aws access key); not saved'))
        .mockImplementation(async (params) => ({ preference: pref({ text: params.text }), previous: pref() })),
    })
    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    const box = screen.getByRole('textbox', { name: 'Preference text' })
    await user.clear(box)
    await user.type(box, 'Prefer JSON output')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect((await screen.findByRole('alert')).textContent).toBe('text looks like an API key (aws access key); not saved')

    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(client.preferenceUpdate).toHaveBeenLastCalledWith({ id: 'pref-1', text: 'Prefer JSON output' }))
    expect(await screen.findByText('Prefer JSON output')).toBeDefined()
    expect(screen.queryByRole('textbox', { name: 'Preference text' })).toBeNull()
  })

  it("names a preference's own workspace when editing it from another", async () => {
    const user = userEvent.setup()
    useStore.setState({ workspaces: { [workspace.id]: workspace, 'ws-other': { ...workspace, id: 'ws-other', name: 'docs-site' } } })
    show([pref({ workspace_id: 'ws-other' })])
    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    const scope = within(document.getElementById('preference-pref-1')!).getByRole('radiogroup', { name: 'Applies to' })
    expect(within(scope).getByRole('radio', { name: 'docs-site' }).getAttribute('aria-checked')).toBe('true')
    expect(within(scope).queryByRole('radio', { name: 'This workspace' })).toBeNull()
  })

  it('turns a preference off and on without forgetting it', async () => {
    const user = userEvent.setup()
    const client = show([pref()], {
      preferenceUpdate: vi.fn(async (params) => ({ preference: pref({ enabled: params.enabled }), previous: pref() })),
    })
    await user.click(await screen.findByRole('button', { name: 'Turn off' }))
    expect(client.preferenceUpdate).toHaveBeenCalledWith({ id: 'pref-1', enabled: false })
    expect(await screen.findByText('Off')).toBeDefined()
    await user.click(screen.getByRole('button', { name: 'Turn on' }))
    expect(client.preferenceUpdate).toHaveBeenLastCalledWith({ id: 'pref-1', enabled: true })
  })

  it('asks before forgetting, says what forgetting does not undo, and offers undo', async () => {
    const user = userEvent.setup()
    const listed = vi.fn().mockResolvedValueOnce({ preferences: [pref()], enabled: true }).mockResolvedValue({ preferences: [], enabled: true })
    const client = show([pref()], {
      preferenceList: listed,
      preferenceDelete: vi.fn(async () => ({ deleted: pref() })),
      preferenceAdd: vi.fn(async (params) => ({ preference: pref({ id: 'pref-2', text: params.text }) })),
    })
    await user.click(await screen.findByRole('button', { name: 'Forget' }))
    expect(screen.getByText(new RegExp(`Forget this preference\\? ${forgetWarning.slice(0, 40)}`))).toBeDefined()
    expect(client.preferenceDelete).not.toHaveBeenCalled()
    await user.click(screen.getAllByRole('button', { name: 'Forget' }).at(-1)!)
    expect(client.preferenceDelete).toHaveBeenCalledWith('pref-1')
    const notice = await screen.findByRole('status')
    expect(notice.textContent).toContain('Forgotten: Prefer table output')
    expect(notice.textContent).toContain(`Forgotten. ${forgetWarning}`)
    expect(screen.queryByRole('list', { name: 'Preferences' })).toBeNull()

    await user.click(within(notice).getByRole('button', { name: 'Undo' }))
    expect(client.preferenceAdd).toHaveBeenCalledWith({ text: 'Prefer table output' })
    expect(await screen.findByRole('list', { name: 'Preferences' })).toBeDefined()
  })

  it('offers to put back the row the server deleted, not the one the list showed', async () => {
    const user = userEvent.setup()
    const listed = vi.fn().mockResolvedValueOnce({ preferences: [pref()], enabled: true }).mockResolvedValue({ preferences: [], enabled: true })
    const client = show([pref()], {
      preferenceList: listed,
      preferenceDelete: vi.fn(async () => ({ deleted: pref({ text: 'Prefer JSON output', workspace_id: workspace.id }) })),
      preferenceAdd: vi.fn(async (params) => ({ preference: pref({ id: 'pref-2', text: params.text, workspace_id: params.workspace_id }) })),
    })
    await user.click(await screen.findByRole('button', { name: 'Forget' }))
    await user.click(screen.getAllByRole('button', { name: 'Forget' }).at(-1)!)
    const notice = await screen.findByRole('status')
    expect(notice.textContent).toContain('Forgotten: Prefer JSON output')
    await user.click(within(notice).getByRole('button', { name: 'Undo' }))
    await waitFor(() => expect(client.preferenceAdd).toHaveBeenCalledWith({ text: 'Prefer JSON output', workspace_id: workspace.id }))
  })

  it("keeps another preference's Forgotten notice when an earlier Undo finishes", async () => {
    const user = userEvent.setup()
    let read: (value: { preferences: Preference[]; enabled: boolean }) => void = () => {}
    const second = pref({ id: 'pref-2', text: 'Use pnpm here' })
    const listed = vi.fn().mockResolvedValueOnce({ preferences: [pref(), second], enabled: true }).mockReturnValueOnce(new Promise((resolve) => { read = resolve }))
    show([], {
      preferenceList: listed,
      preferenceDelete: vi.fn(async (id: string) => ({ deleted: id === 'pref-1' ? pref() : second })),
      preferenceAdd: vi.fn(async (params) => ({ preference: pref({ id: 'pref-3', text: params.text }) })),
    })
    const row = async (text: string) => (await screen.findByText(text)).closest('li')!
    await user.click(within(await row('Prefer table output')).getByRole('button', { name: 'Forget' }))
    await user.click(within(await row('Prefer table output')).getAllByRole('button', { name: 'Forget' }).at(-1)!)
    await user.click(within(await screen.findByRole('status')).getByRole('button', { name: 'Undo' }))
    await user.click(within(await row('Use pnpm here')).getByRole('button', { name: 'Forget' }))
    await user.click(within(await row('Use pnpm here')).getAllByRole('button', { name: 'Forget' }).at(-1)!)
    await act(async () => read({ preferences: [], enabled: true }))
    await screen.findByText('Prefer table output')
    expect(screen.getByRole('status').textContent).toContain('Forgotten: Use pnpm here')
  })

  it('keeps a preference and shows the error when forgetting fails', async () => {
    const user = userEvent.setup()
    show([pref()], { preferenceDelete: vi.fn(async () => { throw new ApiError(-32004, 'preference.delete: preference pref-1 not found') }) })
    await user.click(await screen.findByRole('button', { name: 'Forget' }))
    await user.click(screen.getAllByRole('button', { name: 'Forget' }).at(-1)!)
    expect((await screen.findByRole('alert')).textContent).toBe('preference pref-1 not found')
    expect(screen.getByText('Prefer table output')).toBeDefined()
  })

  it('switches use in the assistant with the setting the server returns', async () => {
    const user = userEvent.setup()
    const client = show([pref()], {
      assistantSettings: vi.fn(async () => ({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: true, preferences_enabled: false })),
    })
    const toggle = await screen.findByRole('checkbox', { name: preferencesLabel })
    await waitFor(() => expect(toggle).toHaveProperty('disabled', false))
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    await user.click(toggle)
    expect(client.assistantSettings).toHaveBeenCalledWith({ preferences_enabled: false })
    await waitFor(() => expect(toggle.getAttribute('aria-checked')).toBe('false'))
    expect(screen.getByText(/Off\. The assistant doesn't read or save your preferences/)).toBeDefined()
  })

  it('highlights the entry in the address and says when it is gone', async () => {
    seed('pref-2')
    show([pref(), pref({ id: 'pref-2', text: 'Use pnpm here' })])
    const row = await waitFor(() => {
      const el = document.getElementById('preference-pref-2')
      if (!el || !el.hasAttribute('data-highlight')) throw new Error('not highlighted')
      return el
    })
    expect(row.textContent).toContain('Use pnpm here')
    expect(document.getElementById('preference-pref-1')?.hasAttribute('data-highlight')).toBe(false)
  })

  it('says so when the entry in the address no longer exists', async () => {
    seed('pref-gone')
    show([pref()])
    expect(await screen.findByText(/That preference no longer exists/)).toBeDefined()
  })

  it('puts a forgotten, turned-off preference back turned off', async () => {
    const user = userEvent.setup()
    const listed = vi.fn().mockResolvedValueOnce({ preferences: [pref({ enabled: false })], enabled: true }).mockResolvedValue({ preferences: [], enabled: true })
    const client = show([pref({ enabled: false })], {
      preferenceList: listed,
      preferenceDelete: vi.fn(async () => ({ deleted: pref({ enabled: false }) })),
      preferenceAdd: vi.fn(async () => ({ preference: pref({ id: 'pref-2' }) })),
      preferenceUpdate: vi.fn(async () => ({ preference: pref({ id: 'pref-2', enabled: false }) })),
    })
    await user.click(await screen.findByRole('button', { name: 'Forget' }))
    await user.click(screen.getAllByRole('button', { name: 'Forget' }).at(-1)!)
    await user.click(await screen.findByRole('button', { name: 'Undo' }))
    await waitFor(() => expect(client.preferenceUpdate).toHaveBeenCalledWith({ id: 'pref-2', enabled: false }))
  })

  it('drops another member\'s list when the identity changes', async () => {
    show([pref()])
    expect(await screen.findByText('Prefer table output')).toBeDefined()
    act(() => useStore.setState({ identityKey: 'someone-else' }))
    await waitFor(() => expect(screen.queryByText('Prefer table output')).toBeNull())
  })

  it('does not put a forgotten preference back under a member who signed in meanwhile', async () => {
    const user = userEvent.setup()
    let read: (value: { preferences: Preference[]; enabled: boolean }) => void = () => {}
    const listed = vi.fn().mockResolvedValueOnce({ preferences: [pref()], enabled: true }).mockReturnValueOnce(new Promise((resolve) => { read = resolve })).mockResolvedValue({ preferences: [], enabled: true })
    const client = show([pref()], {
      preferenceList: listed,
      preferenceDelete: vi.fn(async () => ({ deleted: pref() })),
      preferenceAdd: vi.fn(async (params) => ({ preference: pref({ id: 'pref-2', text: params.text }) })),
    })
    await user.click(await screen.findByRole('button', { name: 'Forget' }))
    await user.click(screen.getAllByRole('button', { name: 'Forget' }).at(-1)!)
    await user.click(await screen.findByRole('button', { name: 'Undo' }))
    act(() => useStore.setState({ identityKey: 'someone-else' }))
    await act(async () => read({ preferences: [], enabled: true }))
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(client.preferenceAdd).not.toHaveBeenCalled()
  })

  it('refuses to put a forgotten preference back when the same text is already remembered', async () => {
    const user = userEvent.setup()
    const client = show([pref()], { preferenceDelete: vi.fn(async () => ({ deleted: pref() })) })
    await user.click(await screen.findByRole('button', { name: 'Forget' }))
    await user.click(screen.getAllByRole('button', { name: 'Forget' }).at(-1)!)
    await user.click(await screen.findByRole('button', { name: 'Undo' }))
    expect((await screen.findAllByRole('alert')).at(-1)!.textContent).toBe('This preference is already remembered.')
    expect(client.preferenceAdd).not.toHaveBeenCalled()
  })

  it('retries only the turn-off when the second step of a restore failed', async () => {
    const user = userEvent.setup()
    const listed = vi.fn().mockResolvedValueOnce({ preferences: [pref({ enabled: false })], enabled: true }).mockResolvedValue({ preferences: [], enabled: true })
    const client = show([pref({ enabled: false })], {
      preferenceList: listed,
      preferenceDelete: vi.fn(async () => ({ deleted: pref({ enabled: false }) })),
      preferenceAdd: vi.fn(async () => ({ preference: pref({ id: 'pref-2' }) })),
      preferenceUpdate: vi.fn()
        .mockRejectedValueOnce(new ApiError(-32001, 'preference.update: database is locked'))
        .mockResolvedValue({ preference: pref({ id: 'pref-2', enabled: false }) }),
    })
    await user.click(await screen.findByRole('button', { name: 'Forget' }))
    await user.click(screen.getAllByRole('button', { name: 'Forget' }).at(-1)!)
    await user.click(await screen.findByRole('button', { name: 'Undo' }))
    expect((await screen.findAllByRole('alert')).at(-1)!.textContent).toBe('database is locked')
    await user.click(screen.getByRole('button', { name: 'Undo' }))
    await waitFor(() => expect(client.preferenceUpdate).toHaveBeenCalledTimes(2))
    expect(client.preferenceAdd).toHaveBeenCalledTimes(1)
  })

  it('disables the switch while it saves', async () => {
    const user = userEvent.setup()
    let finish: (value: AssistantSettings) => void = () => {}
    show([pref()], { assistantSettings: vi.fn(() => new Promise<AssistantSettings>((resolve) => { finish = resolve })) })
    const toggle = await screen.findByRole('checkbox', { name: preferencesLabel })
    await waitFor(() => expect(toggle).toHaveProperty('disabled', false))
    await user.click(toggle)
    await waitFor(() => expect(toggle).toHaveProperty('disabled', true))
    act(() => finish({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: true, preferences_enabled: false }))
    await waitFor(() => expect(toggle).toHaveProperty('disabled', false))
  })

  it("drops a settings reply that arrives after another member signed in", async () => {
    const user = userEvent.setup()
    let finish: (value: AssistantSettings) => void = () => {}
    useStore.getState().setAssistantStatus({ state: 'idle', agents: [], confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true })
    show([pref()], { assistantSettings: vi.fn(() => new Promise<AssistantSettings>((resolve) => { finish = resolve })) })
    const toggle = await screen.findByRole('checkbox', { name: preferencesLabel })
    await waitFor(() => expect(toggle).toHaveProperty('disabled', false))
    await user.click(toggle)
    act(() => useStore.setState({ identityKey: 'someone-else' }))
    act(() => finish({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: true, preferences_enabled: false }))
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(useStore.getState().assistantStatus).not.toHaveProperty('preferences_enabled', false)
  })

  it('clears the entry from the address after highlighting it, and when it is forgotten', async () => {
    const user = userEvent.setup()
    seed('pref-1')
    show([pref()], { preferenceDelete: vi.fn(async () => ({ deleted: pref() })) })
    await waitFor(() => expect(document.getElementById('preference-pref-1')?.hasAttribute('data-highlight')).toBe(true))
    await waitFor(() => expect(useStore.getState().route.params).toEqual({ section: 'memory' }), { timeout: 4000 })

    seed('pref-1')
    document.body.innerHTML = ''
    render(<MemorySection client={fakeApi({ preferenceList: vi.fn(async () => ({ preferences: [pref()], enabled: true })), preferenceDelete: vi.fn(async () => ({ deleted: pref() })) })} />)
    await user.click(await screen.findByRole('button', { name: 'Forget' }))
    await user.click(screen.getAllByRole('button', { name: 'Forget' }).at(-1)!)
    await screen.findByRole('status')
    expect(useStore.getState().route.params).toEqual({ section: 'memory' })
    expect(screen.queryByText(/That preference no longer exists/)).toBeNull()
  })
})
