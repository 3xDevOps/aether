import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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
  width: 1280, height: 800, url: 'http://localhost:3000/', title: 'App',
}
const peer: DevController = { kind: 'member', member_id: 'peer', control_session_id: 'peer-tab', control_generation: 9, connected: true, acquired_at: '2026-09-26T00:00:00Z' }

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
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { peer: { id: 'peer', display_name: 'Pat', color: '#000', role: 'collaborator' } },
    browserPresets: {},
    browserRequests: {},
  })
  vi.spyOn(api, 'devBrowserStatus').mockResolvedValue({ available: true, running: true, state: 'running', session_id: surface.incarnation })
  vi.spyOn(api, 'devBrowserPages').mockResolvedValue({ pages: [page], selected_page_id: page.page_id })
  vi.spyOn(api, 'devControlStatus').mockImplementation(async () => ({ surface, controller }))
  vi.spyOn(api, 'devControlAcquire').mockImplementation(async (params) => {
    controller = { kind: 'member', member_id: 'alice', control_session_id: params.control_session_id, control_generation: 7, connected: true, acquired_at: '2026-09-26T00:00:00Z' }
    return { surface, controller }
  })
  vi.spyOn(api, 'devControlRelease').mockImplementation(async () => { controller = null; return { released: true } })
  vi.spyOn(api, 'devBrowserNavigate').mockImplementation(async (params) => ({ page: { ...page, url: params.url ?? page.url, page_revision: page.page_revision + 1 } }))
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

const address = () => screen.getByRole('textbox', { name: 'Address' }) as HTMLInputElement
const ready = () => screen.findByLabelText('Shared browser page')
function submit(typed: string) {
  act(() => address().focus())
  fireEvent.change(address(), { target: { value: typed } })
  fireEvent.submit(address().closest('form')!)
}
async function menu(name: string) {
  screen.getByRole('button', { name }).focus()
  await userEvent.keyboard('{Enter}')
  return within(await screen.findByRole('menu'))
}

describe('the first page', () => {
  beforeEach(() => {
    vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: false, state: 'not_started' })
    vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [] })
  })

  it('is opened from the address bar, which has focus and adds the scheme', async () => {
    const open = vi.spyOn(api, 'devBrowserOpen').mockImplementation(async (target) => {
      vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: true, state: 'running', session_id: page.session_id })
      vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [page], selected_page_id: page.page_id })
      controller = { kind: 'member', control_session_id: target.control_session_id, control_generation: 1, connected: true, acquired_at: '2026-09-26T00:00:00Z' }
      return { page, control: controller }
    })
    render(<BrowserView runID="run_1" />)
    expect(await screen.findByRole('heading', { name: 'Open a page' })).toBeDefined()
    await waitFor(() => expect(document.activeElement).toBe(address()))
    expect(api.devBrowserOpen).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: /Take control|Take over/ })).toBeNull()
    submit('localhost:3000')
    expect(await screen.findByText('Starting the browser')).toBeDefined()
    await ready()
    expect(open).toHaveBeenCalledWith(expect.objectContaining({ run_id: 'run_1', url: 'http://localhost:3000', control_generation: 0, session_id: undefined }))
    expect(screen.getByRole('img', { name: 'You are driving' })).toBeDefined()
    expect(api.devControlAcquire).not.toHaveBeenCalled()
  })

  it('shows why the server cannot run a browser', async () => {
    vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: false, running: false, state: 'unavailable', reason: 'browser: image is not present on this server' })
    render(<BrowserView runID="run_1" />)
    expect(await screen.findByRole('heading', { name: 'Browser unavailable' })).toBeDefined()
    expect(screen.getByRole('alert').textContent).toBe('browser: image is not present on this server')
    expect(address().disabled).toBe(true)
  })

  it('shows a status read that fails', async () => {
    vi.mocked(api.devBrowserStatus).mockRejectedValue(new Error('dev.browser.status: scheduler: the run has no live environment'))
    render(<BrowserView runID="run_1" />)
    expect((await screen.findByRole('alert')).textContent).toBe('dev.browser.status: scheduler: the run has no live environment')
  })
})

describe('control', () => {
  it('takes a free lease for a toolbar action without asking', async () => {
    render(<BrowserView runID="run_1" />)
    await ready()
    expect(screen.queryByRole('button', { name: /Take control|Take over/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({
      direction: 'reload', page_id: page.page_id, page_revision: page.page_revision, control_generation: 7,
    })))
    expect(api.devControlAcquire).toHaveBeenCalledWith(expect.objectContaining({ surface, takeover: false, expected_generation: 0 }))
    expect(await screen.findByRole('img', { name: 'You are driving' })).toBeDefined()
  })

  it('names the driver, blocks without taking over, and takes over when asked', async () => {
    controller = peer
    render(<BrowserView runID="run_1" />)
    expect(await screen.findByText('Pat is driving')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    submit('localhost:8080')
    expect(await screen.findByText('Pat is driving. Take over to use the page.')).toBeDefined()
    expect(api.devControlAcquire).not.toHaveBeenCalled()
    expect(api.devBrowserNavigate).not.toHaveBeenCalled()
    expect(address().value).toBe('localhost:8080')
    fireEvent.click(screen.getByRole('button', { name: 'Take over' }))
    await waitFor(() => expect(api.devControlAcquire).toHaveBeenCalledWith(expect.objectContaining({ takeover: true, expected_generation: 9 })))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Take over' })).toBeNull())
  })

  it('says the agent is driving', async () => {
    controller = { ...peer, kind: 'run_agent', member_id: undefined, run_id: 'run_1' }
    render(<BrowserView runID="run_1" />)
    expect(await screen.findByText('The agent is driving')).toBeDefined()
  })

  it('gives the lease up on window blur and takes it back with the next action', async () => {
    const view = render(<BrowserView runID="run_1" />)
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    await screen.findByRole('img', { name: 'You are driving' })
    const held = controller!
    fireEvent.focus(screen.getByRole('button', { name: 'Back' }))
    expect(api.devControlRelease).not.toHaveBeenCalled()
    fireEvent.blur(window)
    await waitFor(() => expect(api.devControlRelease).toHaveBeenCalledWith({
      run_id: 'run_1', surface, control_session_id: held.control_session_id, control_generation: held.control_generation,
    }))
    expect(screen.queryByRole('button', { name: /Take control|Take over/ })).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    fireEvent.focus(window)
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ direction: 'back' })))
    expect(api.devControlAcquire).toHaveBeenCalledTimes(2)
    view.unmount()
    await waitFor(() => expect(api.devControlRelease).toHaveBeenCalledTimes(2))
  })

  it('does not release another controller when a denied watcher loses focus or detaches', async () => {
    controller = peer
    vi.mocked(api.devControlAcquire).mockRejectedValue(new Error('Control is held by another member'))
    const view = render(<BrowserView runID="run_1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Take over' }))
    expect((await screen.findByRole('alert')).textContent).toBe('Control is held by another member')
    fireEvent.blur(window)
    view.unmount()
    expect(api.devControlRelease).not.toHaveBeenCalled()
  })

  it('releases an acquisition that completes after local focus was lost', async () => {
    let finish!: (value: DevControlAcquireResult) => void
    const pending = new Promise<DevControlAcquireResult>((resolve) => { finish = resolve })
    vi.mocked(api.devControlAcquire).mockReturnValueOnce(pending)
    const view = render(<BrowserView runID="run_1" />)
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(api.devControlAcquire).toHaveBeenCalled())
    const session = vi.mocked(api.devControlAcquire).mock.calls[0][0].control_session_id
    fireEvent.blur(window)
    await act(async () => {
      finish({ surface, controller: { kind: 'member', control_session_id: session, control_generation: 11, connected: true, acquired_at: '2026-09-26T00:00:00Z' } })
      await pending
    })
    expect(api.devControlRelease).toHaveBeenCalledWith({ run_id: 'run_1', surface, control_session_id: session, control_generation: 11 })
    expect(api.devBrowserNavigate).not.toHaveBeenCalled()
    view.unmount()
  })

  it('offers Release control only while driving', async () => {
    render(<BrowserView runID="run_1" />)
    await ready()
    expect((await menu('Browser actions')).queryByRole('menuitem', { name: 'Release control' })).toBeNull()
    await userEvent.keyboard('{Escape}')
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    await screen.findByRole('img', { name: 'You are driving' })
    fireEvent.click((await menu('Browser actions')).getByRole('menuitem', { name: 'Release control' }))
    await waitFor(() => expect(api.devControlRelease).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.queryByRole('img', { name: 'You are driving' })).toBeNull())
  })
})

describe('the address bar', () => {
  it('shows the server’s error for a failed navigation and keeps what was typed', async () => {
    vi.mocked(api.devBrowserNavigate).mockRejectedValue(new Error('dev.browser.navigate: browser: page.goto: net::ERR_CONNECTION_REFUSED at http://localhost:9999/'))
    render(<BrowserView runID="run_1" />)
    await ready()
    submit('localhost:9999')
    expect((await screen.findByRole('alert')).textContent).toBe('dev.browser.navigate: browser: page.goto: net::ERR_CONNECTION_REFUSED at http://localhost:9999/')
    expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ direction: 'url', url: 'http://localhost:9999' }))
    expect(address().value).toBe('localhost:9999')
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('keeps what is being typed when the page’s address changes, and shows the new one after', async () => {
    render(<BrowserView runID="run_1" />)
    await ready()
    expect(address().value).toBe('http://localhost:3000/')
    act(() => address().focus())
    fireEvent.change(address(), { target: { value: 'exam' } })
    vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [{ ...page, url: 'http://localhost:3000/next', page_revision: 4 }], selected_page_id: page.page_id })
    await act(async () => { poll() })
    expect(address().value).toBe('exam')
    fireEvent.keyDown(address(), { key: 'Escape' })
    expect(address().value).toBe('http://localhost:3000/next')
  })

  it('acts on Enter pressed while another navigation is in flight, once it ends', async () => {
    const first = Promise.withResolvers<{ page: DevBrowserPage }>()
    vi.mocked(api.devBrowserNavigate).mockReturnValueOnce(first.promise)
    render(<BrowserView runID="run_1" />)
    await ready()
    submit('slow.test')
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledTimes(1))
    submit('ignored.test')
    submit('next.test')
    expect(api.devBrowserNavigate).toHaveBeenCalledTimes(1)
    await act(async () => { first.resolve({ page: { ...page, url: 'https://slow.test/', page_revision: 4 } }) })
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledTimes(2))
    expect(api.devBrowserNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ url: 'https://next.test', page_revision: 4 }))
  })

  it('opens an address handed over by a terminal link', async () => {
    render(<BrowserView runID="run_1" />)
    await ready()
    act(() => useStore.getState().requestBrowser('run_1', 'http://127.0.0.1:5173/'))
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ direction: 'url', url: 'http://127.0.0.1:5173/' })))
    expect(useStore.getState().browserRequests).toEqual({})
  })
})

describe('shortcuts', () => {
  it('focuses the address bar from the page and keeps the key from the page', async () => {
    render(<BrowserView runID="run_1" />)
    const canvas = await ready()
    const press = new KeyboardEvent('keydown', { key: 'l', ctrlKey: true, bubbles: true, cancelable: true })
    const forwarded = vi.fn()
    canvas.addEventListener('keydown', forwarded)
    act(() => { canvas.dispatchEvent(press) })
    expect(document.activeElement).toBe(address())
    expect(press.defaultPrevented).toBe(true)
    expect(forwarded).not.toHaveBeenCalled()
  })

  it('reloads and walks history only with the Browser focused', async () => {
    render(<BrowserView runID="run_1" />)
    const canvas = await ready()
    fireEvent.keyDown(document.body, { key: 'r', ctrlKey: true })
    expect(api.devControlAcquire).not.toHaveBeenCalled()
    fireEvent.keyDown(canvas, { key: 'r', ctrlKey: true })
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ direction: 'reload' })))
    fireEvent.keyDown(canvas, { key: 'ArrowLeft', altKey: true })
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ direction: 'back' })))
    fireEvent.keyDown(address(), { key: 'ArrowRight', altKey: true })
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ direction: 'forward' })))
  })
})

describe('the viewport', () => {
  beforeEach(() => {
    vi.stubGlobal('ResizeObserver', class {
      constructor(private readonly report: ResizeObserverCallback) {}
      observe(target: Element) {
        this.report([{ target, contentRect: { width: 900.6, height: 600.2 } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
      }
      unobserve() {}
      disconnect() {}
    })
    vi.spyOn(api, 'devBrowserViewport').mockImplementation(async (params) => ({ page: { ...page, width: params.width, height: params.height, viewport_id: `viewport-${params.width}` } }))
  })

  it('leaves a page nobody drives at its size, then follows the pane once driven', async () => {
    render(<BrowserView runID="run_1" />)
    await ready()
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 300)) })
    expect(api.devBrowserViewport).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(api.devBrowserViewport).toHaveBeenCalledWith(expect.objectContaining({ width: 900, height: 600, control_generation: 7 })))
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 300)) })
    expect(api.devBrowserViewport).toHaveBeenCalledTimes(1)
  })

  it('applies a preset from the toolbar and keeps it for the run', async () => {
    const view = render(<BrowserView runID="run_1" />)
    await ready()
    fireEvent.click((await menu('Viewport')).getByRole('menuitemradio', { name: /Phone/ }))
    await waitFor(() => expect(api.devBrowserViewport).toHaveBeenCalledWith(expect.objectContaining({ width: 390, height: 844 })))
    expect(useStore.getState().browserPresets).toEqual({ run_1: 'phone' })
    view.unmount()
  })

  it('opens the first page at the pane’s size', async () => {
    vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: false, state: 'not_started' })
    vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [] })
    const open = vi.spyOn(api, 'devBrowserOpen').mockRejectedValue(new Error('stop here'))
    render(<BrowserView runID="run_1" />)
    await screen.findByRole('heading', { name: 'Open a page' })
    submit('example.com')
    await waitFor(() => expect(open).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://example.com', width: 900, height: 600 })))
  })
})

describe('pages', () => {
  const popup: DevBrowserPage = { ...page, page_id: 'page-2', title: 'Authentication popup', url: 'http://localhost:3000/popup' }

  it('shows no strip for one page, then marks a page that opened by itself', async () => {
    const action = vi.spyOn(api, 'devBrowserAction').mockResolvedValue({ page: popup })
    render(<BrowserView runID="run_1" />)
    await ready()
    expect(screen.queryByRole('tablist', { name: 'Pages' })).toBeNull()
    vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [page, popup], selected_page_id: page.page_id })
    await act(async () => { poll() })
    const strip = within(screen.getByRole('tablist', { name: 'Pages' }))
    expect(strip.getByRole('tab', { name: 'App' }).getAttribute('aria-selected')).toBe('true')
    fireEvent.click(strip.getByRole('tab', { name: 'Authentication popup (new)' }))
    await waitFor(() => expect(action).toHaveBeenCalledWith(expect.objectContaining({ action: 'select', page_id: 'page-2', control_generation: 7 })))
  })

  it('opens a blank page and puts the caret in the address bar', async () => {
    const blank = { ...page, page_id: 'page-3', url: 'about:blank', title: '' }
    const open = vi.spyOn(api, 'devBrowserOpen').mockImplementation(async (target) => {
      vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [page, blank], selected_page_id: blank.page_id })
      return { page: blank, control: { control_session_id: target.control_session_id, control_generation: target.control_generation } }
    })
    render(<BrowserView runID="run_1" />)
    await ready()
    fireEvent.click((await menu('Browser actions')).getByRole('menuitem', { name: 'New page' }))
    await waitFor(() => expect(open).toHaveBeenCalledWith(expect.objectContaining({ session_id: page.session_id, url: '', control_generation: 7 })))
    await waitFor(() => expect(document.activeElement).toBe(address()))
    expect(address().value).toBe('')
  })
})

describe('closing and resetting', () => {
  async function openConfirmation(action: 'Close page…' | 'Reset session…') {
    render(<BrowserView runID="run_1" />)
    await ready()
    fireEvent.click((await menu('Browser actions')).getByRole('menuitem', { name: action }))
    return screen.findByRole('alertdialog')
  }

  it.each(['Close page…', 'Reset session…'] as const)('cancels %s without mutating and returns focus to the menu button', async (action) => {
    const close = vi.spyOn(api, 'devBrowserClose')
    const reset = vi.spyOn(api, 'devBrowserReset')
    const dialog = await openConfirmation(action)
    const cancel = within(dialog).getByRole('button', { name: 'Cancel' })
    await waitFor(() => expect(document.activeElement).toBe(cancel))
    fireEvent.click(cancel)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Browser actions' })))
    expect(close).not.toHaveBeenCalled()
    expect(reset).not.toHaveBeenCalled()
  })

  it.each(['session', 'generation', 'page', 'revision', 'blur'] as const)('refuses a close after its captured %s changes', async (changed) => {
    const close = vi.spyOn(api, 'devBrowserClose')
    const dialog = await openConfirmation('Close page…')
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
    const dialog = await openConfirmation('Reset session…')
    vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: true, state: 'running', session_id: 'replacement-browser' })
    await act(async () => { poll() })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reset session' }))
    expect(reset).not.toHaveBeenCalled()
  })

  it('opens the next page in the session a reset returned, before the next status read', async () => {
    vi.spyOn(api, 'devBrowserReset').mockImplementation(async () => {
      controller = null
      vi.mocked(api.devBrowserStatus).mockReturnValue(new Promise(() => {}))
      return { session_id: 'browser-2' }
    })
    const open = vi.spyOn(api, 'devBrowserOpen').mockRejectedValue(new Error('stop here'))
    const dialog = await openConfirmation('Reset session…')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reset session' }))
    expect(await screen.findByRole('heading', { name: 'Open a page' })).toBeDefined()
    submit('localhost:3000')
    await waitFor(() => expect(open).toHaveBeenCalledWith(expect.objectContaining({ session_id: 'browser-2', url: 'http://localhost:3000' })))
    expect(api.devControlAcquire).toHaveBeenLastCalledWith(expect.objectContaining({ surface: { ...surface, incarnation: 'browser-2' } }))
  })

  it('submits the captured close once and keeps a raw refusal in the confirmation', async () => {
    const { promise, reject } = Promise.withResolvers<DevBrowserCloseResult>()
    const close = vi.spyOn(api, 'devBrowserClose').mockReturnValue(promise)
    const dialog = await openConfirmation('Close page…')
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

  it('lets a watcher capture evidence but not close or reset what someone else drives', async () => {
    controller = peer
    const screenshot = vi.spyOn(api, 'devBrowserScreenshot').mockRejectedValue(new Error('Capture quota reached'))
    const close = vi.spyOn(api, 'devBrowserClose')
    const reset = vi.spyOn(api, 'devBrowserReset')
    render(<BrowserView runID="run_1" />)
    await ready()
    for (const name of ['Close page…', 'Reset session…']) {
      fireEvent.click((await menu('Browser actions')).getByRole('menuitem', { name }))
      expect(await screen.findByText('Pat is driving. Take over to use the page.')).toBeDefined()
      expect(screen.queryByRole('alertdialog')).toBeNull()
    }
    fireEvent.click((await menu('Browser actions')).getByRole('menuitem', { name: 'Screenshot' }))
    await waitFor(() => expect(screenshot).toHaveBeenCalledWith({ run_id: 'run_1', session_id: page.session_id, page_id: page.page_id, page_revision: page.page_revision }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('Capture quota reached'))
    expect(close).not.toHaveBeenCalled()
    expect(reset).not.toHaveBeenCalled()
    expect(api.devControlAcquire).not.toHaveBeenCalled()
  })
})
