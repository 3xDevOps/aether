import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { SplitNewRun } from '@/components/shell/new-run-button'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { useLaunchDefaults } from '@/store/launch-defaults'
import { alice, run, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const saved = (mode: 'acp' | 'tui' | 'headless', extra = {}) => ({
  revision: 2,
  defaults: { agent: 'claude', agents: { claude: { mode, model: 'opus' } }, ...extra },
})

beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(toast, 'error').mockImplementation(() => 'x')
  useLaunchDefaults.setState({ result: null, error: null })
  useStore.setState({
    info: serverInfo,
    activeWorkspace: workspace.id,
    workspaces: { [workspace.id]: workspace },
    capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    launchDefaults: {},
    route: { name: 'board', params: {} },
  })
  vi.mocked(api.launchDefaultsGet).mockResolvedValue(saved('acp'))
  vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice], shared_with: [] })
  vi.mocked(api.runLaunch).mockResolvedValue(run({ id: 'run_new' }))
})

afterEach(cleanup)

const quick = () => screen.findByRole('button', { name: /Launch again/ })

it('relaunches on the saved permission tier', async () => {
  vi.mocked(api.launchDefaultsGet).mockResolvedValue(saved('acp', { permission: 'isolated' }))
  render(<SplitNewRun blocked={null} shortcut="n" onOpen={() => {}} />)
  fireEvent.click(await quick())
  await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', model: 'opus', permission: 'isolated' }))
})

it('is one plain New run button until a launch has been saved', async () => {
  vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 0, defaults: {} })
  render(<SplitNewRun blocked={null} shortcut="n" onOpen={() => {}} />)
  await waitFor(() => expect(api.launchDefaultsGet).toHaveBeenCalled())
  expect(screen.queryByRole('button', { name: /Launch again/ })).toBeNull()
  expect(screen.getByRole('button', { name: 'New run' })).toBeTruthy()
})

it('opens the panel from the left half and launches the saved choices, without a task, from the right', async () => {
  const onOpen = vi.fn()
  render(<SplitNewRun blocked={null} shortcut="n" onOpen={onOpen} />)
  fireEvent.click(screen.getByRole('button', { name: 'New run' }))
  expect(onOpen).toHaveBeenCalledOnce()
  expect(api.runLaunch).not.toHaveBeenCalled()

  fireEvent.click(await quick())
  await waitFor(() => expect(api.runLaunch).toHaveBeenCalled())
  expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', model: 'opus' })
  await waitFor(() => expect(useStore.getState().route.name).toBe('run'))
  expect(onOpen).toHaveBeenCalledOnce()
})

it('names the saved agent and mode, and shows them on hover and focus', async () => {
  render(<SplitNewRun blocked={null} shortcut="n" onOpen={() => {}} />)
  const half = await quick()
  expect(half.getAttribute('aria-label')).toBe('Launch again: claude, Enhanced')
  fireEvent.pointerEnter(half)
  const preview = await screen.findByRole('tooltip')
  expect(preview.textContent).toContain('Enhanced')
  expect(preview.textContent).toContain('opus')
  expect(half.getAttribute('aria-describedby')).toBe(preview.id)
  fireEvent.keyDown(window, { key: 'Escape' })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 300)) })
  expect(screen.queryByRole('tooltip')).toBeNull()
})

it('opens the panel instead for a Background launch, which needs a task', async () => {
  vi.mocked(api.launchDefaultsGet).mockResolvedValue(saved('headless'))
  const onOpen = vi.fn()
  render(<SplitNewRun blocked={null} shortcut="n" onOpen={onOpen} />)
  fireEvent.click(await quick())
  expect(onOpen).toHaveBeenCalledOnce()
  expect(api.runLaunch).not.toHaveBeenCalled()
})

it('sends the saved account and fallback with the launch', async () => {
  vi.mocked(api.launchDefaultsGet).mockResolvedValue(saved('tui', { fallback: { policy: 'off' } }))
  render(<SplitNewRun blocked={null} shortcut="n" onOpen={() => {}} />)
  fireEvent.click(await quick())
  await waitFor(() => expect(api.runLaunch).toHaveBeenCalled())
  expect(vi.mocked(api.runLaunch).mock.calls[0]![0]).toMatchObject({ mode: 'tui', no_fallback: true })
})

it('shows the server refusal as a toast and stays put', async () => {
  vi.mocked(api.runLaunch).mockRejectedValue(new Error('no capacity'))
  render(<SplitNewRun blocked={null} shortcut="n" onOpen={() => {}} />)
  fireEvent.click(await quick())
  await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Launch failed: no capacity'))
  expect(useStore.getState().route.name).toBe('board')
})

it('does nothing on either half while launching is blocked', async () => {
  const onOpen = vi.fn()
  render(<SplitNewRun blocked="Server is offline" shortcut="n" onOpen={onOpen} />)
  fireEvent.click(screen.getByRole('button', { name: 'New run' }))
  fireEvent.click(await quick())
  expect(onOpen).not.toHaveBeenCalled()
  expect(api.runLaunch).not.toHaveBeenCalled()
})
