import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import type { DevBrowserCloseResult, DevBrowserPage, DevControlAcquireResult, DevController } from '@/lib/types'
import { BrowserView } from '@/routes/browser'
import { useStore } from '@/store'
import { run, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const surface = { kind: 'browser' as const, id: 'browser', incarnation: 'browser-1' }
let controller: DevController | null
let poll: () => void
const page: DevBrowserPage = {
  session_id: 'browser-1', page_id: 'page-1', page_revision: 3, viewport_id: 'viewport-1',
  width: 1280, height: 800, url: 'http://localhost:3000', title: 'App',
}

beforeEach(() => {
  StubSocket.install()
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ clearRect: () => {} } as unknown as CanvasRenderingContext2D)
  const schedule = globalThis.setInterval
  vi.spyOn(globalThis, 'setInterval').mockImplementation((callback, delay) => {
    if (delay === 1500) poll = callback as () => void
    return schedule(callback, delay)
  })
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
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

it('releases only its own browser lease on window blur, not toolbar focus changes', async () => {
  const view = render(<BrowserView runID="run_1" />)
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
  const view = render(<BrowserView runID="run_1" />)
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
  const view = render(<BrowserView runID="run_1" />)
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

async function openConfirmation(action: 'Close page' | 'Reset session') {
  vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [page], selected_page_id: page.page_id })
  render(<BrowserView runID="run_1" />)
  fireEvent.click(await screen.findByRole('button', { name: 'Acquire control' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Go' }).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: 'Browser tools' }))
  fireEvent.click(screen.getByRole('button', { name: action }))
  return screen.findByRole('alertdialog')
}

it.each(['Close page', 'Reset session'] as const)('cancels %s without mutating and returns focus to tools', async (action) => {
  const close = vi.spyOn(api, 'devBrowserClose')
  const reset = vi.spyOn(api, 'devBrowserReset')
  const dialog = await openConfirmation(action)
  const cancel = within(dialog).getByRole('button', { name: 'Cancel' })
  await waitFor(() => expect(document.activeElement).toBe(cancel))
  fireEvent.click(cancel)
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Browser tools' })))
  expect(close).not.toHaveBeenCalled()
  expect(reset).not.toHaveBeenCalled()
})

it.each(['session', 'generation', 'page', 'revision', 'blur'] as const)('refuses a close after its captured %s changes', async (changed) => {
  const close = vi.spyOn(api, 'devBrowserClose')
  const dialog = await openConfirmation('Close page')
  if (changed === 'session') {
    vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: true, state: 'running', session_id: 'replacement-browser' })
  } else if (changed === 'generation') {
    controller = { ...controller!, control_generation: controller!.control_generation + 1 }
  } else if (changed === 'page') {
    vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [page, { ...page, page_id: 'replacement-page' }], selected_page_id: 'replacement-page' })
  } else if (changed === 'revision') {
    vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [{ ...page, page_revision: page.page_revision + 1 }], selected_page_id: page.page_id })
  } else fireEvent.blur(window)
  await act(async () => { poll() })
  const confirm = within(dialog).getByRole('button', { name: 'Close page' })
  expect(confirm.hasAttribute('disabled')).toBe(true)
  fireEvent.click(confirm)
  expect(close).not.toHaveBeenCalled()
  expect(within(dialog).getByRole('alert').textContent).toContain('control changed')
})

it('never resets a replacement session after confirmation was opened', async () => {
  const reset = vi.spyOn(api, 'devBrowserReset')
  const dialog = await openConfirmation('Reset session')
  vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: true, state: 'running', session_id: 'replacement-browser' })
  await act(async () => { poll() })
  fireEvent.click(within(dialog).getByRole('button', { name: 'Reset session' }))
  expect(reset).not.toHaveBeenCalled()
})

it('submits the captured close once and keeps a raw refusal in the confirmation', async () => {
  const { promise, reject } = Promise.withResolvers<DevBrowserCloseResult>()
  const close = vi.spyOn(api, 'devBrowserClose').mockReturnValue(promise)
  const dialog = await openConfirmation('Close page')
  const confirm = within(dialog).getByRole('button', { name: 'Close page' })
  fireEvent.click(confirm)
  fireEvent.click(confirm)
  expect(close).toHaveBeenCalledTimes(1)
  expect(close).toHaveBeenCalledWith({
    run_id: 'run_1', session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision,
    control_session_id: controller!.control_session_id, control_generation: controller!.control_generation,
  })
  expect(within(dialog).getByRole('button', { name: 'Cancel' }).hasAttribute('disabled')).toBe(true)
  await act(async () => { reject(new Error('The shared page is protected by server policy')) })
  expect(within(dialog).getByRole('alert').textContent).toBe('The shared page is protected by server policy')
  expect(within(dialog).getByRole('button', { name: 'Cancel' }).hasAttribute('disabled')).toBe(false)
})

it('opens with the chosen preset before exposing page navigation', async () => {
  vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: false, state: 'not_started' })
  const open = vi.spyOn(api, 'devBrowserOpen').mockImplementation(async (target) => {
    const opened = { ...page, width: target.width!, height: target.height! }
    vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: true, state: 'running', session_id: page.session_id })
    vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [opened], selected_page_id: opened.page_id })
    controller = { kind: 'member', control_session_id: target.control_session_id, control_generation: 1, connected: true, acquired_at: '2026-09-26T00:00:00Z' }
    return { page: opened, control: controller }
  })
  render(<BrowserView runID="run_1" />)
  await waitFor(() => expect(screen.getByRole('button', { name: 'Open browser' }).hasAttribute('disabled')).toBe(false))
  expect(screen.queryByRole('button', { name: 'Back' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Reload page' })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Browser tools' }))
  fireEvent.change(screen.getByRole('combobox', { name: 'Browser viewport' }), { target: { value: '390x844' } })
  fireEvent.keyDown(screen.getByRole('dialog', { name: 'Browser tools' }), { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: 'Open browser' }))
  await screen.findByRole('button', { name: 'Back' })
  expect(open).toHaveBeenCalledWith(expect.objectContaining({ width: 390, height: 844, url: page.url }))
})

it('allows observers to capture evidence without granting page mutations', async () => {
  vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [page], selected_page_id: page.page_id })
  const screenshot = vi.spyOn(api, 'devBrowserScreenshot').mockRejectedValue(new Error('Capture quota reached'))
  const close = vi.spyOn(api, 'devBrowserClose')
  const reset = vi.spyOn(api, 'devBrowserReset')
  render(<BrowserView runID="run_1" />)
  await screen.findByRole('button', { name: 'Go' })
  fireEvent.click(screen.getByRole('button', { name: 'Browser tools' }))
  for (const name of ['Close page', 'Reset session', 'New page']) {
    const button = screen.getByRole('button', { name })
    expect(button.hasAttribute('disabled')).toBe(true)
    fireEvent.click(button)
  }
  expect(screen.getByRole('combobox', { name: 'Browser page' }).hasAttribute('disabled')).toBe(true)
  expect(screen.getByRole('combobox', { name: 'Browser viewport' }).hasAttribute('disabled')).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: 'Screenshot' }))
  await waitFor(() => expect(screenshot).toHaveBeenCalledWith({ run_id: 'run_1', session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision }))
  await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('Capture quota reached'))
  expect(close).not.toHaveBeenCalled()
  expect(reset).not.toHaveBeenCalled()
})
