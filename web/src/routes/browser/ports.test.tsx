import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import type { DevBrowserPage, DevController, DevPort } from '@/lib/types'
import { BrowserView } from '@/routes/browser'
import { useStore } from '@/store'
import { run, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const surface = { kind: 'browser' as const, id: 'browser', incarnation: 'browser-1' }
const page: DevBrowserPage = {
  session_id: 'browser-1', page_id: 'page-1', page_revision: 3, viewport_id: 'viewport-1',
  width: 1280, height: 800, url: 'http://localhost:3000/', title: 'App',
}
const vite: DevPort = { port: 5173, address: '::1', process: 'node' }
const served: DevPort = { port: 8000, address: '0.0.0.0', process: 'python3.12' }
const local = { gateway: 'local', methods: ['*'], ws: ['events'], local: ['forward.start'] }
const hosted = { gateway: 'server', methods: ['*'], ws: ['events'] }
let controller: DevController | null
let poll: () => void

beforeEach(() => {
  StubSocket.install()
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ clearRect: () => {} } as unknown as CanvasRenderingContext2D)
  const schedule = globalThis.setInterval
  vi.spyOn(globalThis, 'setInterval').mockImplementation((callback, delay) => {
    if (delay === 3000) poll = callback as () => void
    return schedule(callback, delay)
  })
  controller = null
  useStore.getState().upsertRun(run())
  useStore.setState({ workspaces: { [workspace.id]: workspace }, capabilities: local, browserPresets: {}, browserRequests: {} })
  vi.spyOn(api, 'devBrowserStatus').mockResolvedValue({ available: true, running: false, state: 'not_started' })
  vi.spyOn(api, 'devBrowserPages').mockResolvedValue({ pages: [] })
  vi.spyOn(api, 'devControlStatus').mockImplementation(async () => ({ surface, controller }))
  vi.spyOn(api, 'devControlAcquire').mockImplementation(async (params) => {
    controller = { kind: 'member', member_id: 'alice', control_session_id: params.control_session_id, control_generation: 7, connected: true, acquired_at: '2026-09-26T00:00:00Z' }
    return { surface, controller }
  })
  vi.spyOn(api, 'devControlRelease').mockImplementation(async () => { controller = null; return { released: true } })
  vi.spyOn(api, 'devBrowserNavigate').mockImplementation(async (params) => ({ page: { ...page, url: params.url ?? page.url, page_revision: page.page_revision + 1 } }))
  vi.spyOn(api, 'devBrowserOpen').mockImplementation(async (target) => {
    showPage()
    controller = { kind: 'member', control_session_id: target.control_session_id, control_generation: 1, connected: true, acquired_at: '2026-09-26T00:00:00Z' }
    return { page, control: controller }
  })
  vi.spyOn(api, 'devPortList').mockResolvedValue({ ports: [vite, served] })
  vi.spyOn(api, 'localForwardStart').mockImplementation(async (target, port) => ({ target, port, local_port: port, state: 'active' }))
  vi.spyOn(window, 'open').mockReturnValue(null)
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

function showPage() {
  vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: true, state: 'running', session_id: page.session_id })
  vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [page], selected_page_id: page.page_id })
}
const address = () => screen.getByRole('combobox', { name: 'Address' }) as HTMLInputElement
const listed = () => screen.findByRole('button', { name: 'Open localhost:5173, node' })

describe('before a page is open', () => {
  it('lists the run’s listening ports and opens the one picked', async () => {
    render(<BrowserView runID="run_1" />)
    const first = await listed()
    expect(screen.getByRole('button', { name: 'Open localhost:8000, python3.12' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Ports, 2 listening' })).toBeDefined()
    fireEvent.click(first)
    await screen.findByLabelText('Shared browser page')
    expect(api.devBrowserOpen).toHaveBeenCalledWith(expect.objectContaining({ run_id: 'run_1', url: 'http://localhost:5173' }))
    expect(api.devPortList).toHaveBeenCalledWith({ run_id: 'run_1' })
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Remote browser keyboard')))
  })

  it('offers nothing under the address field, which the pane already lists', async () => {
    render(<BrowserView runID="run_1" />)
    await listed()
    act(() => address().focus())
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('says that nothing is listening and what to do', async () => {
    vi.mocked(api.devPortList).mockResolvedValue({ ports: [] })
    render(<BrowserView runID="run_1" />)
    expect(await screen.findByText(/^Nothing in this run is listening on a port yet\. Start a server in the Terminal, or ask the agent to, and it appears here\./)).toBeDefined()
    expect(screen.getByRole('button', { name: 'Ports' })).toBeDefined()
  })

  it('shows why the ports could not be read', async () => {
    vi.mocked(api.devPortList).mockRejectedValue(new Error('dev.port.list: read the run’s listening ports: exit 1: grep: /proc/net/tcp: Permission denied'))
    render(<BrowserView runID="run_1" />)
    expect((await screen.findByRole('alert')).textContent).toBe('dev.port.list: read the run’s listening ports: exit 1: grep: /proc/net/tcp: Permission denied')
    expect(screen.getByRole('heading', { name: 'Open a page' })).toBeDefined()
  })

  it('reads nothing from a server that cannot list ports', async () => {
    useStore.setState({ capabilities: { gateway: 'server', methods: ['dev.browser.status'], ws: [] } })
    render(<BrowserView runID="run_1" />)
    expect(await screen.findByText(/^Type an address above and press Enter\./)).toBeDefined()
    expect(screen.queryByRole('button', { name: /^Ports/ })).toBeNull()
    expect(api.devPortList).not.toHaveBeenCalled()
  })

  it('reads the ports again every three seconds, but not while the tab is hidden', async () => {
    render(<BrowserView runID="run_1" />)
    await listed()
    const reads = vi.mocked(api.devPortList).mock.calls.length
    vi.mocked(api.devPortList).mockResolvedValue({ ports: [served] })
    await act(async () => poll())
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Open localhost:5173, node' })).toBeNull())
    expect(screen.getByRole('button', { name: 'Ports, 1 listening' })).toBeDefined()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    await act(async () => poll())
    expect(api.devPortList).toHaveBeenCalledTimes(reads + 1)
  })
})

describe('opening a port on this computer', () => {
  it('forwards it through the local gateway and links what is forwarded', async () => {
    render(<BrowserView runID="run_1" />)
    await listed()
    fireEvent.click(screen.getByRole('button', { name: 'Open 8000 on this computer' }))
    const link = await screen.findByRole('link', { name: 'localhost:8000' })
    expect(link.getAttribute('href')).toBe('http://localhost:8000')
    expect(api.localForwardStart).toHaveBeenCalledWith('run:run_1', 8000)
    expect(window.open).toHaveBeenLastCalledWith('http://localhost:8000', '_blank', 'noopener,noreferrer')
    expect(api.devBrowserOpen).not.toHaveBeenCalled()
  })

  it('names the address of a port only the run can reach instead of forwarding it', async () => {
    render(<BrowserView runID="run_1" />)
    await listed()
    fireEvent.click(screen.getByRole('button', { name: 'Open 5173 on this computer' }))
    expect(screen.getByRole('status').textContent).toBe('Port 5173 is bound to ::1 inside the run, so only the run can reach it. Start the server on 0.0.0.0 to open it on this computer.')
    expect(api.localForwardStart).not.toHaveBeenCalled()
    expect(window.open).not.toHaveBeenCalled()
  })

  it('keeps the gateway’s refusal', async () => {
    vi.mocked(api.localForwardStart).mockRejectedValue(new Error('localops: listen on 127.0.0.1:8000: bind: address already in use'))
    render(<BrowserView runID="run_1" />)
    await listed()
    fireEvent.click(screen.getByRole('button', { name: 'Open 8000 on this computer' }))
    expect((await screen.findByRole('alert')).textContent).toBe('localops: listen on 127.0.0.1:8000: bind: address already in use')
  })

  it('shows the aether forward command where the dashboard is served by the server', async () => {
    useStore.setState({ capabilities: hosted })
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    render(<BrowserView runID="run_1" />)
    await listed()
    fireEvent.click(screen.getByRole('button', { name: 'Open 8000 on your own computer' }))
    expect(screen.getByRole('status').textContent).toContain('aether forward run_1 8000')
    fireEvent.click(screen.getByRole('button', { name: 'Copy the command' }))
    expect(writeText).toHaveBeenCalledWith('aether forward run_1 8000')
    expect(api.localForwardStart).not.toHaveBeenCalled()
  })
})

describe('with a page open', () => {
  beforeEach(showPage)

  it('offers the ports under the focused address field and navigates to the one chosen', async () => {
    render(<BrowserView runID="run_1" />)
    await screen.findByLabelText('Shared browser page')
    await screen.findByRole('button', { name: 'Ports, 2 listening' })
    act(() => address().focus())
    const offers = within(screen.getByRole('listbox', { name: 'Listening ports' })).getAllByRole('option')
    expect(offers.map((offer) => offer.textContent)).toEqual(['localhost:5173node', 'localhost:8000python3.12'])
    fireEvent.keyDown(address(), { key: 'ArrowDown' })
    fireEvent.keyDown(address(), { key: 'ArrowDown' })
    expect(address().getAttribute('aria-activedescendant')).toBe(offers[1]!.id)
    fireEvent.keyDown(address(), { key: 'Enter' })
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ direction: 'url', url: 'http://localhost:8000' })))
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('narrows the offers to what is typed and drops them with Escape', async () => {
    render(<BrowserView runID="run_1" />)
    await screen.findByRole('button', { name: 'Ports, 2 listening' })
    act(() => address().focus())
    fireEvent.change(address(), { target: { value: 'localhost:80' } })
    expect(screen.getAllByRole('option').map((offer) => offer.textContent)).toEqual(['localhost:8000python3.12'])
    fireEvent.change(address(), { target: { value: 'example.com' } })
    expect(screen.queryByRole('listbox')).toBeNull()
    fireEvent.change(address(), { target: { value: '' } })
    expect(screen.getAllByRole('option')).toHaveLength(2)
    fireEvent.keyDown(address(), { key: 'Escape' })
    expect(screen.queryByRole('listbox')).toBeNull()
    expect(address().value).toBe(page.url)
  })

  it('navigates to a port pressed in the offers without taking focus from the field', async () => {
    render(<BrowserView runID="run_1" />)
    await screen.findByRole('button', { name: 'Ports, 2 listening' })
    act(() => address().focus())
    const offer = screen.getByRole('option', { name: /localhost:5173/ })
    expect(fireEvent.mouseDown(offer)).toBe(false)
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ url: 'http://localhost:5173' })))
  })

  it('lists them behind the toolbar’s count and navigates to the one picked', async () => {
    render(<BrowserView runID="run_1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Ports, 2 listening' }))
    const list = await screen.findByRole('dialog')
    expect(list.hasAttribute('data-browser')).toBe(true)
    fireEvent.click(within(list).getByRole('button', { name: 'Open localhost:8000, python3.12' }))
    await waitFor(() => expect(api.devBrowserNavigate).toHaveBeenCalledWith(expect.objectContaining({ url: 'http://localhost:8000' })))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Remote browser keyboard')))
  })
})
