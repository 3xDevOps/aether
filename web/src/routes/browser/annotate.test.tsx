import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { api, ApiError } from '@/lib/api'
import type { DevArtifact, DevBrowserElement, DevBrowserFrameMetadata, DevBrowserPage, DevController } from '@/lib/types'
import { BrowserView } from '@/routes/browser'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { alice, roomMessage, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'
import { patchFeedback } from './feedback'

const surface = { kind: 'browser' as const, id: 'browser', incarnation: 'browser-1' }
const page: DevBrowserPage = {
  session_id: 'browser-1', page_id: 'page-1', page_revision: 3, viewport_id: 'viewport-1',
  width: 1280, height: 800, url: 'http://localhost:3000/settings', title: 'Settings',
}
const theAgent: DevController = { kind: 'run_agent', control_session_id: 'agent', control_generation: 4, connected: true, acquired_at: '2026-09-26T00:00:00Z' }
const save: DevBrowserElement = {
  tag: 'button', role: 'button', name: 'Save changes', text: 'Save', selector: 'button#save',
  attributes: { id: 'save', class: 'btn primary' }, bounds: { x: 40, y: 30, width: 120, height: 36 }, scroll_x: 0, scroll_y: 0,
  html: '<button id="save" class="btn primary">Save</button>', styles: { display: 'block' },
  component: 'App > SaveButton', source: 'src/SaveButton.tsx:12:5',
}
const quiet = { entries: [], next: 0, missing_cursor: false, truncated: false }
const onCaptures = vi.fn()
const shared = {
  agent: { localControl: true, roomControl: { has_control: true, control_session_id: 'ctl', control_generation: 3 }, steerable: true, controlUnavailable: false } as AgentTerminal,
  agentName: 'Claude',
  onCaptures,
}

function frame(sequence = 1): ArrayBuffer {
  const metadata: DevBrowserFrameMetadata = { ...page, run_id: 'run_1', sequence, mime_type: 'image/png', timestamp: '2026-09-26T00:00:00Z' }
  const json = new TextEncoder().encode(JSON.stringify(metadata))
  const packet = new ArrayBuffer(8 + json.length + 1)
  const view = new DataView(packet)
  view.setUint32(0, json.length)
  view.setUint32(4, 1)
  new Uint8Array(packet, 8, json.length).set(json)
  return packet
}

const canvas = () => screen.getByLabelText('Shared browser page') as HTMLCanvasElement
const annotate = () => screen.getByRole('button', { name: 'Annotate' })
const bar = () => screen.getByRole('group', { name: 'Feedback for the agent' })

/** The Browser with one page painted at its own size, driven by the agent. */
async function show() {
  render(<BrowserView {...shared} runID="run_1" />)
  await screen.findByLabelText('Shared browser page')
  await act(async () => {
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
    socket.onmessage?.({ data: frame() })
  })
  await waitFor(() => expect(canvas().getAttribute('aria-busy')).toBe('false'))
  await screen.findByText('The agent is driving')
}

function click(x: number, y: number) {
  fireEvent.pointerDown(canvas(), { pointerId: 1, button: 0, clientX: x, clientY: y })
  fireEvent.pointerUp(canvas(), { pointerId: 1, button: 0, clientX: x, clientY: y })
}

beforeEach(() => {
  StubSocket.install()
  patchFeedback('run_1', { notes: [], logs: [], sending: undefined, sent: undefined })
  vi.stubGlobal('createImageBitmap', vi.fn(async () => ({ close: () => {} })))
  vi.stubGlobal('ResizeObserver', class {
    constructor(private readonly report: ResizeObserverCallback) {}
    observe(target: Element) {
      this.report([{ target, contentRect: { width: 1280, height: 800 } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
    }
    unobserve() {}
    disconnect() {}
  })
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ clearRect: () => {}, drawImage: () => {} } as unknown as CanvasRenderingContext2D)
  vi.spyOn(HTMLCanvasElement.prototype, 'getBoundingClientRect').mockReturnValue({ left: 0, top: 0, width: 1280, height: 800 } as DOMRect)
  HTMLCanvasElement.prototype.toBlob = function (done) { done(new Blob(['pixels'], { type: 'image/png' })) }
  useStore.getState().upsertRun(run())
  useStore.setState({
    info: serverInfo,
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice },
    capabilities: { gateway: 'local', methods: ['*'], ws: [] },
    browserPresets: { run_1: 'desktop' },
    browserRequests: {},
  })
  vi.spyOn(api, 'devBrowserStatus').mockResolvedValue({ available: true, running: true, state: 'running', session_id: surface.incarnation })
  vi.spyOn(api, 'devBrowserPages').mockResolvedValue({ pages: [page], selected_page_id: page.page_id })
  vi.spyOn(api, 'devControlStatus').mockResolvedValue({ surface, controller: theAgent })
  vi.spyOn(api, 'devControlAcquire')
  vi.spyOn(api, 'devBrowserAction')
  vi.spyOn(api, 'devBrowserConsole').mockResolvedValue(quiet)
  vi.spyOn(api, 'devBrowserNetwork').mockResolvedValue(quiet)
  vi.spyOn(api, 'devBrowserInspect').mockResolvedValue({ page, element: save })
  vi.spyOn(api, 'uploadTerminalImage').mockResolvedValue({ path: '/home/alice/.aether/terminal-images/a.png' })
  vi.spyOn(api, 'runRoomPost').mockImplementation(async (params) => ({ message: roomMessage({ id: 'msg_1', run_id: 'run_1', kind: 'steer_request', body: params.body }) }))
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

it('does not describe an element the page scrolled to after the frame it was pointed at', async () => {
  await show()
  await userEvent.click(annotate())
  vi.mocked(api.devBrowserInspect).mockResolvedValue({ page, element: { ...save, scroll_y: 300 } })
  click(100, 50)
  expect((await screen.findByRole('alert')).textContent).toContain('The page scrolled before it was read')
})

it('annotates an element of a page the agent is driving and sends it, without taking control', async () => {
  const complaints = vi.spyOn(console, 'error')
  await show()
  await userEvent.click(annotate())
  expect(annotate().getAttribute('aria-pressed')).toBe('true')

  fireEvent.pointerMove(canvas(), { pointerId: 1, pointerType: 'mouse', clientX: 100, clientY: 50 })
  await waitFor(() => expect(document.querySelector('[data-slot="annotate-hover"]')?.textContent).toBe('button#save 120 × 36'))
  expect(api.devBrowserInspect).toHaveBeenLastCalledWith({ run_id: 'run_1', session_id: 'browser-1', page_id: 'page-1', page_revision: 3, viewport_id: 'viewport-1', x: 100, y: 50 })

  click(100, 50)
  const field = await screen.findByRole('textbox', { name: 'Note 1' })
  expect(document.activeElement).toBe(field)
  expect(await screen.findByText('src/SaveButton.tsx:12:5')).toBeTruthy()
  await userEvent.type(field, 'The label is cut off.')
  await userEvent.click(screen.getByRole('button', { name: 'Comment' }))
  expect(screen.getByRole('button', { name: 'Note 1: button "Save changes"' })).toBeTruthy()
  expect(bar().textContent).toContain('1 annotation')

  await userEvent.click(within(bar()).getByRole('button', { name: 'Send to agent' }))
  await waitFor(() => expect(within(bar()).getByRole('status').textContent).toContain('Sent 1 annotation to Claude.'))
  expect(vi.mocked(api.uploadTerminalImage).mock.calls[0][1]).toBe('run_1')
  const { body } = vi.mocked(api.runRoomPost).mock.calls[0][0]
  expect(body).toContain('1. button "Save changes"\nThe label is cut off.\n- Screenshot: /home/alice/.aether/terminal-images/a.png\n- Selector: button#save\n- Source: src/SaveButton.tsx:12:5')
  expect(body).toContain('Page: http://localhost:3000/settings (viewport 1280x800)')
  expect(screen.queryByRole('button', { name: /^Note 1/ })).toBeNull()
  expect(api.devControlAcquire).not.toHaveBeenCalled()
  expect(api.devBrowserAction).not.toHaveBeenCalled()
  // The layer and the surface are siblings keyed by the same page.
  expect(complaints.mock.calls.filter(([text]) => String(text).includes('same key'))).toEqual([])
})

it('sends nothing to the run when the account changes while an image uploads', async () => {
  await show()
  await userEvent.click(annotate())
  click(100, 50)
  await userEvent.type(await screen.findByRole('textbox', { name: 'Note 1' }), 'The label is cut off.')
  await userEvent.click(screen.getByRole('button', { name: 'Comment' }))
  let finish: (value: { path: string }) => void = () => {}
  vi.mocked(api.uploadTerminalImage).mockReturnValue(new Promise((resolve) => { finish = resolve }))
  await userEvent.click(within(bar()).getByRole('button', { name: 'Send to agent' }))
  act(() => useStore.setState({ identityKey: 'someone-else' }))
  await act(async () => { finish({ path: '/home/alice/.aether/terminal-images/a.png' }) })
  expect(api.runRoomPost).not.toHaveBeenCalled()
})

it('sends a free page no key and no press while its pointer picks', async () => {
  vi.mocked(api.devControlStatus).mockResolvedValue({ surface, controller: null })
  render(<BrowserView {...shared} runID="run_1" />)
  await screen.findByLabelText('Shared browser page')
  await act(async () => {
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
    socket.onmessage?.({ data: frame() })
  })
  await waitFor(() => expect(canvas().getAttribute('aria-busy')).toBe('false'))
  await userEvent.click(annotate())
  fireEvent.keyDown(screen.getByRole('textbox', { name: 'Remote browser keyboard' }), { key: 'a' })
  click(100, 50)
  await screen.findByRole('textbox', { name: 'Note 1' })
  expect(api.devControlAcquire).not.toHaveBeenCalled()
  expect(api.devBrowserAction).not.toHaveBeenCalled()
})

it('answers Escape before the run does, and only outside a card', async () => {
  await show()
  await userEvent.click(annotate())
  click(100, 50)
  const field = await screen.findByRole('textbox', { name: 'Note 1' })
  await userEvent.type(field, 'Words')
  await userEvent.keyboard('{Escape}')
  expect(annotate().getAttribute('aria-pressed')).toBe('true')
  expect((field as HTMLTextAreaElement).value).toBe('Words')

  const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
  act(() => { document.body.dispatchEvent(escape) })
  // Prevented, which is what makes the run's own Escape stand down.
  expect(escape.defaultPrevented).toBe(true)
  expect(annotate().getAttribute('aria-pressed')).toBe('false')
  expect(bar().textContent).toContain('1 annotation')
})

it('leaves annotate off, with the server\'s reason, when the companion cannot inspect', async () => {
  const reason = "dev.browser.inspect: this run's browser companion predates dev.browser.inspect; update the server's browser image, then stop and reopen the run to replace the companion: browser: invalid_request: Unknown browser operation"
  vi.mocked(api.devBrowserInspect).mockRejectedValue(new ApiError(404, reason, -32601))
  await show()
  await userEvent.click(annotate())
  fireEvent.pointerMove(canvas(), { pointerId: 1, pointerType: 'mouse', clientX: 100, clientY: 50 })
  expect((await screen.findByRole('alert')).textContent).toContain(reason)
  expect(annotate().getAttribute('aria-pressed')).toBe('false')
  expect(annotate().getAttribute('aria-disabled')).toBe('true')
  await userEvent.click(annotate())
  expect(annotate().getAttribute('aria-pressed')).toBe('false')
})

it('keeps a note the page could not describe out of the message and says why', async () => {
  vi.mocked(api.devBrowserInspect).mockRejectedValue(new ApiError(409, 'dev.browser.inspect: browser: stale_target: Page navigated; take another snapshot', -32003))
  await show()
  await userEvent.click(annotate())
  click(100, 50)
  expect((await screen.findByRole('alert')).textContent).toContain('Page navigated; take another snapshot')
  await userEvent.type(screen.getByRole('textbox', { name: 'Note 1' }), 'Words')
  expect((screen.getByRole('button', { name: 'Comment' }) as HTMLButtonElement).disabled).toBe(true)
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByRole('group', { name: 'Feedback for the agent' })).toBeNull()
})

it('turns a screenshot into a note that can be sent, with or without words, or kept as evidence', async () => {
  const artifact = { id: 'shot_1', path: '/aether/captures/shot_1.png', width: 1280, height: 800 } as DevArtifact
  vi.spyOn(api, 'devBrowserScreenshot').mockResolvedValue({ artifact })
  await show()
  screen.getByRole('button', { name: 'Browser actions' }).focus()
  await userEvent.keyboard('{Enter}')
  await userEvent.click(await screen.findByRole('menuitem', { name: 'Screenshot' }))
  const card = await screen.findByRole('article', { name: 'Note 1: Screenshot of the page' })
  expect(bar().textContent).toContain('1 screenshot')
  await userEvent.click(within(card).getByRole('button', { name: 'Keep as evidence…' }))
  expect(onCaptures).toHaveBeenCalledTimes(1)

  await userEvent.click(within(bar()).getByRole('button', { name: 'Send to agent' }))
  await waitFor(() => expect(within(bar()).getByRole('status').textContent).toContain('Sent 1 screenshot to Claude.'))
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0].body).toContain('1. Screenshot of the page\n- Screenshot: /aether/captures/shot_1.png')
  expect(api.uploadTerminalImage).not.toHaveBeenCalled()
})

it('counts new console and network rows on its toggle and sends the ticked ones', async () => {
  vi.mocked(api.devBrowserConsole).mockResolvedValue({ ...quiet, next: 3, entries: [{ sequence: 3, time: '2026-10-11T12:01:03Z', level: 'error', text: 'Uncaught TypeError: x is not a function' }] })
  vi.mocked(api.devBrowserNetwork).mockResolvedValue({ ...quiet, next: 4, entries: [{ sequence: 4, time: '2026-10-11T12:01:04Z', method: 'GET', url: 'http://localhost:3000/api/user', status: 500 }] })
  await show()
  const toggle = screen.getByRole('button', { name: 'Console and network' })
  await waitFor(() => expect(toggle.textContent).toContain('2'))
  await userEvent.click(toggle)
  expect(toggle.textContent).not.toContain('2')
  const strip = screen.getByRole('region', { name: 'Console and network' })
  expect(within(strip).getByText('Uncaught TypeError: x is not a function')).toBeTruthy()
  await userEvent.click(within(strip).getAllByRole('checkbox')[2]!)
  expect(bar().textContent).toContain('1 failed request')
  await userEvent.click(within(strip).getByRole('checkbox', { name: 'Select every row' }))
  expect(bar().textContent).toContain('1 console message, 1 failed request')

  await userEvent.click(within(bar()).getByRole('button', { name: 'Send to agent' }))
  await waitFor(() => expect(api.runRoomPost).toHaveBeenCalled())
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0].body).toBe([
    "Feedback from the page in this run's browser: 1 console message, 1 failed request.",
    '',
    'Console (http://localhost:3000/settings):',
    '- 12:01:03Z error Uncaught TypeError: x is not a function',
    '',
    'Failed requests (http://localhost:3000/settings):',
    '- 12:01:04Z GET http://localhost:3000/api/user 500',
  ].join('\n'))
})
