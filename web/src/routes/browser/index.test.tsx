import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import type { DevControlAcquireResult, DevController } from '@/lib/types'
import '@/routes/browser'
import { lookupRoute } from '@/routes/registry'
import { useStore } from '@/store'
import { run, workspace } from '@/test/fixtures'

const BrowserView = lookupRoute('browser')!
const surface = { kind: 'browser' as const, id: 'browser', incarnation: 'browser-1' }
let controller: DevController | null

beforeEach(() => {
  controller = null
  useStore.getState().upsertRun(run())
  useStore.setState({ workspaces: { [workspace.id]: workspace } })
  vi.spyOn(api, 'devBrowserStatus').mockResolvedValue({ available: true, running: true, state: 'running', session_id: surface.incarnation })
  vi.spyOn(api, 'devBrowserPages').mockResolvedValue({ pages: [] })
  vi.spyOn(api, 'devControlStatus').mockImplementation(async () => ({ surface, controller }))
  vi.spyOn(api, 'devControlAcquire').mockImplementation(async (params) => {
    controller = { kind: 'member', member_id: 'alice', control_session_id: params.control_session_id, control_generation: 7, connected: true, acquired_at: '2026-09-26T00:00:00Z' }
    return { surface, controller }
  })
  vi.spyOn(api, 'devControlRelease').mockImplementation(async () => { controller = null; return { released: true } })
})
afterEach(() => { vi.restoreAllMocks() })

it('releases only its own browser lease on window blur, not toolbar focus changes', async () => {
  const view = render(<BrowserView params={{ runId: 'run_1' }} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Acquire control' }))
  await screen.findByRole('button', { name: 'Release control' })
  const held = controller!
  fireEvent.focus(screen.getByRole('textbox', { name: 'Browser URL' }))
  expect(api.devControlRelease).not.toHaveBeenCalled()
  fireEvent.blur(window)
  await waitFor(() => expect(api.devControlRelease).toHaveBeenCalledWith({
    run_id: 'run_1', surface, control_session_id: held.control_session_id, control_generation: held.control_generation,
  }))
  expect(screen.queryByRole('button', { name: 'Release control' })).toBeNull()
  view.unmount()
  expect(api.devControlRelease).toHaveBeenCalledTimes(1)
})

it('does not release another controller when a denied watcher loses focus or detaches', async () => {
  controller = { kind: 'member', member_id: 'peer', control_session_id: 'peer-tab', control_generation: 9, connected: true, acquired_at: '2026-09-26T00:00:00Z' }
  vi.mocked(api.devControlAcquire).mockRejectedValue(new Error('Control is held by another member'))
  const view = render(<BrowserView params={{ runId: 'run_1' }} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Acquire control' }))
  await screen.findByRole('alert')
  fireEvent.blur(window)
  view.unmount()
  expect(api.devControlRelease).not.toHaveBeenCalled()
})

it('releases an acquisition that completes after local focus was lost', async () => {
  let finish!: (value: DevControlAcquireResult) => void
  const pending = new Promise<DevControlAcquireResult>((resolve) => { finish = resolve })
  vi.mocked(api.devControlAcquire).mockReturnValueOnce(pending)
  const view = render(<BrowserView params={{ runId: 'run_1' }} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Acquire control' }))
  const session = vi.mocked(api.devControlAcquire).mock.calls[0][0].control_session_id
  fireEvent.blur(window)
  await act(async () => {
    finish({ surface, controller: { kind: 'member', control_session_id: session, control_generation: 11, connected: true, acquired_at: '2026-09-26T00:00:00Z' } })
    await pending
  })
  expect(api.devControlRelease).toHaveBeenCalledWith({ run_id: 'run_1', surface, control_session_id: session, control_generation: 11 })
  expect(screen.queryByRole('button', { name: 'Release control' })).toBeNull()
  view.unmount()
})
