import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ModelPicker } from '@/components/launch/model-picker'
import { api } from '@/lib/api'
import type { AgentModelsResult } from '@/lib/types'
import { useStore } from '@/store'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const discovered: AgentModelsResult = {
  source: 'agent',
  default_label: 'Sonnet 4.5',
  models: [
    { id: 'claude-opus-4-1', name: 'Opus 4.1', description: 'Most capable' },
    { id: 'claude-haiku-4-5', name: 'Haiku 4.5', description: 'Fastest' },
  ],
}

beforeEach(() => {
  useStore.setState({ agentModels: {}, capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
  vi.clearAllMocks()
  vi.mocked(api.agentModels).mockResolvedValue(discovered)
})

function setup(props: Partial<React.ComponentProps<typeof ModelPicker>> = {}) {
  const onChange = vi.fn()
  render(<ModelPicker harness="claude" value="" onChange={onChange} aria-label="Model" {...props} />)
  return { onChange, trigger: screen.getByRole('combobox', { name: 'Model' }) }
}

async function openPicker(trigger: HTMLElement) {
  await userEvent.click(trigger)
  return screen.findByLabelText('Search models')
}

describe('ModelPicker', () => {
  it('lists the discovered models with their id, description and the default label', async () => {
    const { trigger } = setup()
    expect(trigger.textContent).toBe('Agent default')
    await openPicker(trigger)

    const option = await screen.findByRole('option', { name: /^Opus 4.1/ })
    expect(option.textContent).toContain('claude-opus-4-1')
    expect(option.textContent).toContain('Most capable')
    expect(screen.getByRole('option', { name: /^Agent default/ }).textContent).toContain('Sonnet 4.5')
    expect(screen.getByText("Agent default", { selector: 'span.text-muted' })).toBeDefined()
  })

  it('filters by id, name and description', async () => {
    const { trigger } = setup()
    const search = await openPicker(trigger)
    await screen.findByRole('option', { name: /^Opus 4.1/ })
    const names = () => screen.getAllByRole('option').map((o) => o.textContent)

    await userEvent.type(search, 'haiku-4')
    expect(names().some((n) => n?.startsWith('Haiku 4.5'))).toBe(true)
    expect(names().some((n) => n?.startsWith('Opus 4.1'))).toBe(false)

    await userEvent.clear(search)
    await userEvent.type(search, 'Opus 4.1')
    expect(names().some((n) => n?.startsWith('Opus 4.1'))).toBe(true)
    expect(names().some((n) => n?.startsWith('Haiku 4.5'))).toBe(false)

    await userEvent.clear(search)
    await userEvent.type(search, 'fastest')
    expect(names().some((n) => n?.startsWith('Haiku 4.5'))).toBe(true)
    expect(names().some((n) => n?.startsWith('Opus 4.1'))).toBe(false)
  })

  it('selects a listed model and closes', async () => {
    const { trigger, onChange } = setup()
    await openPicker(trigger)
    await userEvent.click(await screen.findByRole('option', { name: /^Haiku 4.5/ }))

    expect(onChange).toHaveBeenCalledWith('claude-haiku-4-5')
    await waitFor(() => expect(screen.queryByLabelText('Search models')).toBeNull())
  })

  it('accepts an id no list offers through the Use item', async () => {
    const { trigger, onChange } = setup()
    const search = await openPicker(trigger)
    await userEvent.type(search, 'my-model')
    await userEvent.click(await screen.findByRole('option', { name: 'Use “my-model”' }))

    expect(onChange).toHaveBeenCalledWith('my-model')
  })

  it('offers no Use item for an id the list already has', async () => {
    const { trigger } = setup()
    const search = await openPicker(trigger)
    await screen.findByRole('option', { name: /^Opus 4.1/ })
    await userEvent.type(search, 'claude-opus-4-1')

    expect(screen.queryByRole('option', { name: /^Use / })).toBeNull()
  })

  it('shows an unlisted saved value as selected and leaves it alone', async () => {
    const { trigger, onChange } = setup({ value: 'legacy-model' })
    expect(trigger.textContent).toBe('legacy-model')
    await openPicker(trigger)

    const option = await screen.findByRole('option', { name: /^legacy-model/ })
    expect(within(option).getByText('Current model')).toBeDefined()
    expect(onChange).not.toHaveBeenCalled()
  })

  it('shows the listed name on the trigger for a saved id', async () => {
    const { trigger } = setup({ value: 'claude-opus-4-1' })
    await waitFor(() => expect(trigger.textContent).toBe('Opus 4.1'))
  })

  it('shows a failed agent.models call verbatim and still selects', async () => {
    vi.mocked(api.agentModels).mockRejectedValue(new Error('agent.models: connection closed'))
    const { trigger, onChange } = setup()
    const search = await openPicker(trigger)

    expect(await screen.findByText('agent.models: connection closed')).toBeDefined()
    await userEvent.type(search, 'typed')
    await userEvent.click(await screen.findByRole('option', { name: 'Use “typed”' }))
    expect(onChange).toHaveBeenCalledWith('typed')
  })

  it('shows a discovery error from the result verbatim next to the fallback list', async () => {
    vi.mocked(api.agentModels).mockResolvedValue({ source: 'fallback', models: discovered.models, error: 'claude: not logged in' })
    const { trigger, onChange } = setup()
    await openPicker(trigger)

    expect(await screen.findByText('claude: not logged in')).toBeDefined()
    await userEvent.click(await screen.findByRole('option', { name: /^Opus 4.1/ }))
    expect(onChange).toHaveBeenCalledWith('claude-opus-4-1')
  })

  it('says where a list came from', async () => {
    vi.mocked(api.agentModels).mockResolvedValue({ ...discovered, source: 'cache' })
    const { trigger } = setup()
    await openPicker(trigger)

    expect(await screen.findByText("From the agent's cache")).toBeDefined()
  })

  it('refreshes through the server', async () => {
    const { trigger } = setup({ accountMemberID: 'mem_alice' })
    await openPicker(trigger)
    await screen.findByRole('option', { name: /^Opus 4.1/ })
    expect(api.agentModels).toHaveBeenCalledWith('claude', 'mem_alice', false)

    await userEvent.click(screen.getByRole('button', { name: 'Refresh models' }))
    expect(api.agentModels).toHaveBeenLastCalledWith('claude', 'mem_alice', true)
  })

  it('refreshes instead of selecting when Enter is pressed on the refresh button', async () => {
    const { trigger, onChange } = setup()
    await openPicker(trigger)
    await screen.findByRole('option', { name: /^Opus 4.1/ })
    screen.getByRole('button', { name: 'Refresh models' }).focus()
    await userEvent.keyboard('{Enter}')
    expect(api.agentModels).toHaveBeenLastCalledWith('claude', undefined, true)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('fetches once per account and agent', async () => {
    setup()
    await waitFor(() => expect(useStore.getState().agentModels['/claude']?.result).toBeDefined())
    cleanup()
    setup()
    expect(api.agentModels).toHaveBeenCalledTimes(1)

    cleanup()
    setup({ accountMemberID: 'mem_bob' })
    await waitFor(() => expect(api.agentModels).toHaveBeenCalledTimes(2))
    expect(api.agentModels).toHaveBeenLastCalledWith('claude', 'mem_bob', false)
  })

  it('picks the typed id on Enter even when a listed model matches it loosely', async () => {
    vi.mocked(api.agentModels).mockResolvedValue({ source: 'agent', models: [{ id: 'opus', name: 'opus', description: 'Opus 4.1' }] })
    const { trigger, onChange } = setup()
    const search = await openPicker(trigger)
    await screen.findByRole('option', { name: /^opus/ })
    await userEvent.type(search, 'opus-4')
    await waitFor(() => expect(screen.getByRole('option', { name: 'Use “opus-4”' }).getAttribute('aria-selected')).toBe('true'))
    await userEvent.keyboard('{Enter}')

    expect(onChange).toHaveBeenCalledWith('opus-4')
  })

  it('retries a failed agent.models call on the next mount and drops answers after an identity change', async () => {
    vi.mocked(api.agentModels).mockRejectedValueOnce(new Error('agent.models: connection closed'))
    setup()
    await waitFor(() => expect(useStore.getState().agentModels['/claude']?.error).toBeDefined())
    cleanup()
    setup()
    await waitFor(() => expect(useStore.getState().agentModels['/claude']?.result).toEqual(discovered))
    expect(api.agentModels).toHaveBeenCalledTimes(2)

    cleanup()
    useStore.setState({ agentModels: {}, identityKey: 'member:alice' })
    let answer!: (result: AgentModelsResult) => void
    vi.mocked(api.agentModels).mockReturnValueOnce(new Promise((resolve) => { answer = resolve }))
    setup()
    await waitFor(() => expect(api.agentModels).toHaveBeenCalledTimes(3))
    vi.mocked(api.agentModels).mockReturnValueOnce(new Promise(() => {}))
    useStore.setState({ agentModels: {}, identityKey: 'member:bob' })
    answer(discovered)
    await waitFor(() => expect(api.agentModels).toHaveBeenCalledTimes(4))
    await Promise.resolve()
    expect(useStore.getState().agentModels['/claude']?.result).toBeUndefined()
  })

  it('neither fetches nor shows the status row when the server lacks agent.models', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.launch'], ws: [] } })
    const { trigger, onChange } = setup()
    const search = await openPicker(trigger)
    expect(screen.queryByRole('button', { name: 'Refresh models' })).toBeNull()
    await userEvent.type(search, 'typed')
    await userEvent.click(await screen.findByRole('option', { name: 'Use “typed”' }))

    expect(api.agentModels).not.toHaveBeenCalled()
    expect(onChange).toHaveBeenCalledWith('typed')
  })
})
