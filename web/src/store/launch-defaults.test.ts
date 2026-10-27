import { waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { rememberChoice, saveLaunchDefaults, useLaunchDefaults } from '@/store/launch-defaults'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

beforeEach(() => {
  vi.clearAllMocks()
  useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] }, launchDefaults: {}, identityKey: 'ana' })
  useLaunchDefaults.setState({ result: { revision: 3, defaults: { agent: 'claude', agents: { claude: { mode: 'acp', model: 'opus' } } } }, error: null })
})

describe('saved launch defaults', () => {
  it('saves a Default mode change without changing the preselected agent', async () => {
    rememberChoice('codex', 'tui', undefined, false)
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({
      defaults: { agent: 'claude', agents: { claude: { mode: 'acp', model: 'opus' }, codex: { mode: 'tui' } } },
      revision: 3,
    }))
    expect(useStore.getState().launchDefaults.codex.mode).toBe('tui')
  })

  it('runs saves one after another, each from the last one\'s answer', async () => {
    await Promise.all([
      saveLaunchDefaults((d) => ({ ...d, agent: 'codex' })),
      saveLaunchDefaults((d) => ({ ...d, account_member_id: 'mem_bo' })),
    ])
    expect(vi.mocked(api.launchDefaultsSet).mock.calls.map(([p]) => p.revision)).toEqual([3, 4])
    expect(useLaunchDefaults.getState().result?.defaults).toMatchObject({ agent: 'codex', account_member_id: 'mem_bo' })
  })

  it('does not save once another member signed in', async () => {
    const pending = saveLaunchDefaults((d) => ({ ...d, agent: 'codex' }))
    useStore.setState({ identityKey: 'bo' })
    await expect(pending).rejects.toThrow('the signed-in member changed')
    expect(api.launchDefaultsSet).not.toHaveBeenCalled()
  })
})
