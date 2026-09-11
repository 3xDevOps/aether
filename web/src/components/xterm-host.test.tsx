import { useEffect, useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { SearchAddon } from '@xterm/addon-search'
import { Terminal } from '@xterm/xterm'
import { TerminalPane } from '@/components/terminal-pane'
import { useXterm } from '@/components/xterm-host'
import { defaultTerminalFontSize } from '@/lib/term-font'
import { connectAttach } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { StubSocket } from '@/test/stub-socket'
import { hintOn } from '@/test/tooltip'

function Probe({ onReady }: { onReady: (terminal: Terminal) => void }) {
  const { hostRef, terminal } = useXterm()
  useEffect(() => {
    if (terminal) onReady(terminal)
  }, [onReady, terminal])
  return <div ref={hostRef} />
}

/**
 * The environment dock's shape: the hook is enabled before the element it
 * draws into exists, because the dock renders "Checking environment..." while
 * `terminal.status` is in flight and only then swaps in the terminal's host.
 */
function LateHostProbe({ onReady }: { onReady: (terminal: Terminal) => void }) {
  const { hostRef, terminal } = useXterm()
  const [hostMounted, setHostMounted] = useState(false)
  useEffect(() => setHostMounted(true), [])
  useEffect(() => {
    if (terminal) onReady(terminal)
  }, [onReady, terminal])
  return hostMounted ? <div ref={hostRef} /> : <p>Checking environment...</p>
}

beforeEach(() => {
  StubSocket.install()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('xterm links', () => {
  it('opens OSC 8 links in a new browser tab without a confirm dialog', async () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    const confirm = vi.spyOn(window, 'confirm')
    let ready: Terminal | null = null
    const view = render(<Probe onReady={(terminal) => (ready = terminal)} />)
    await waitFor(() => expect(ready).not.toBeNull())

    const oscURL = 'https://osc.example/path'
    ready!.write(`\x1b]8;;${oscURL}\x07OSC link\x1b]8;;\x07`)
    ready!.options.linkHandler?.activate(new MouseEvent('click'), oscURL, {
      start: { x: 0, y: 0 },
      end: { x: 8, y: 0 },
    })

    expect(open).toHaveBeenCalledWith(oscURL, '_blank', 'noopener,noreferrer')
    expect(confirm).not.toHaveBeenCalled()
    view.unmount()
    open.mockRestore()
    confirm.mockRestore()
  })

  it('registers a link provider so plain URLs are clickable', async () => {
    const register = vi.spyOn(Terminal.prototype, 'registerLinkProvider')
    let ready: Terminal | null = null
    const view = render(<Probe onReady={(terminal) => (ready = terminal)} />)
    await waitFor(() => expect(ready).not.toBeNull())

    expect(register).toHaveBeenCalledTimes(1)
    view.unmount()
    register.mockRestore()
  })
})

describe('xterm replay scrollback', () => {
  it('keeps the first numbered line after replaying more than 64 KiB', async () => {
    let ready: Terminal | null = null
    render(<Probe onReady={(terminal) => (ready = terminal)} />)
    await waitFor(() => expect(ready).not.toBeNull())

    const attachment = connectAttach(() => '/ws/attach/run_1', {
      onData: (chunk) => ready?.write(new TextDecoder().decode(chunk)),
      onAttached: () => {},
      onState: () => {},
      onRefused: () => {},
      onWriteDenied: () => {},
      geometry: () => ({ cols: ready?.cols ?? 80, rows: ready?.rows ?? 24 }),
      wantsWrite: () => false,
    })
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })

    const replay = Array.from(
      { length: 5000 },
      (_, index) => `line ${index + 1} ${'x'.repeat(20)}\r\n`,
    ).join('')
    expect(new TextEncoder().encode(replay).byteLength).toBeGreaterThan(64 * 1024)
    socket.onmessage?.({ data: new TextEncoder().encode(replay).buffer })

    await waitFor(() =>
      expect(ready?.buffer.active.getLine(0)?.translateToString().trimEnd()).toBe(
        'line 1 xxxxxxxxxxxxxxxxxxxx',
      ),
    )
    attachment.close()
  })
})

/** A phone's terminal: the server's geometry, and no fit of its own. */
function SizedProbe({
  size,
  onResize,
  onReady,
}: {
  size: { cols: number; rows: number }
  onResize: (cols: number, rows: number) => void
  onReady: (terminal: Terminal) => void
}) {
  const { hostRef, terminal } = useXterm({ size, onResize })
  useEffect(() => {
    if (terminal) onReady(terminal)
  }, [onReady, terminal])
  return <div ref={hostRef} />
}

describe('a terminal at a fixed size', () => {
  it('renders the size it was given and reports no resize of its own', async () => {
    let ready: Terminal | null = null
    const onResize = vi.fn()
    const onReady = (terminal: Terminal) => (ready = terminal)
    const view = render(
      <SizedProbe size={{ cols: 132, rows: 43 }} onResize={onResize} onReady={onReady} />,
    )
    await waitFor(() => expect(ready).not.toBeNull())

    const terminal = ready as unknown as Terminal
    expect([terminal.cols, terminal.rows]).toEqual([132, 43])
    // Reporting is what sends a resize to the shared PTY, and a pane that
    // adopted someone else's geometry has nothing to report.
    expect(onResize).not.toHaveBeenCalled()

    // A reattach can answer with a different size; the pane follows it.
    view.rerender(
      <SizedProbe size={{ cols: 100, rows: 30 }} onResize={onResize} onReady={onReady} />,
    )
    await waitFor(() => expect([terminal.cols, terminal.rows]).toEqual([100, 30]))
    expect(onResize).not.toHaveBeenCalled()
    view.unmount()
  })
})

/** The key bar's half of the Ctrl modifier lives in the host. */
function CtrlProbe({
  onData,
  onController,
}: {
  onData: (data: string) => void
  onController: (controller: ReturnType<typeof useXterm>) => void
}) {
  const controller = useXterm({ onData })
  useEffect(() => {
    if (controller.terminal) onController(controller)
  })
  return <div ref={controller.hostRef} />
}

describe('the key bar Ctrl modifier', () => {
  it('turns the next character into its control code, once', async () => {
    const sent: string[] = []
    let controller: ReturnType<typeof useXterm> | null = null
    const view = render(
      <CtrlProbe
        onData={(data) => sent.push(data)}
        onController={(next) => (controller = next)}
      />,
    )
    await waitFor(() => expect(controller).not.toBeNull())
    const host = () => controller as unknown as ReturnType<typeof useXterm>

    act(() => host().armCtrl(true))
    await waitFor(() => expect(host().ctrlArmed).toBe(true))
    act(() => host().terminal?.input('c'))
    expect(sent).toEqual(['\x03'])

    // One character, then the modifier is spent: the next keystroke is plain.
    await waitFor(() => expect(host().ctrlArmed).toBe(false))
    act(() => host().terminal?.input('c'))
    expect(sent).toEqual(['\x03', 'c'])

    // A key the modifier does not cover goes through as it is and leaves
    // Ctrl armed, so the next one can still use it rather than the wrong
    // control code arriving at the agent.
    act(() => host().armCtrl(true))
    act(() => host().terminal?.input('\x1b[A'))
    expect(sent).toEqual(['\x03', 'c', '\x1b[A'])
    expect(host().ctrlArmed).toBe(true)

    // Case folding is ASCII, not locale text: the German sharp s upper
    // cases to two letters and must not become Ctrl+S.
    act(() => host().terminal?.input('\u00df'))
    expect(sent).toEqual(['\x03', 'c', '\x1b[A', '\u00df'])
    act(() => host().terminal?.input('d'))
    expect(sent).toEqual(['\x03', 'c', '\x1b[A', '\u00df', '\x04'])
    await waitFor(() => expect(host().ctrlArmed).toBe(false))
    view.unmount()
  })
})

describe('xterm host arrival', () => {
  it('opens the terminal when its host mounts after the hook is enabled', async () => {
    let ready: Terminal | null = null
    const view = render(<LateHostProbe onReady={(terminal) => (ready = terminal)} />)

    // A hook that watched only `enabled` would have run its one effect while
    // the host was still the placeholder and never looked again, leaving the
    // environment terminal permanently blank.
    await waitFor(() => expect(ready).not.toBeNull())
    view.unmount()
  })
})

/** The pane as the three terminal surfaces render it. */
function PaneProbe({ onReady }: { onReady: (terminal: Terminal) => void }) {
  const controller = useXterm()
  useEffect(() => {
    if (controller.terminal) onReady(controller.terminal)
  }, [controller.terminal, onReady])
  return <TerminalPane controller={controller} />
}

/**
 * xterm delivers browser keys to its own handler before the shell sees them,
 * so the shortcuts are exercised through that handler rather than through a
 * DOM event on the host element.
 */
async function mountPane(): Promise<{
  handler: (ev: KeyboardEvent) => boolean
  terminal: Terminal
}> {
  const attachHandler = vi.spyOn(Terminal.prototype, 'attachCustomKeyEventHandler')
  let ready: Terminal | null = null
  render(<PaneProbe onReady={(terminal) => (ready = terminal)} />)
  await waitFor(() => expect(attachHandler).toHaveBeenCalled())
  await waitFor(() => expect(ready).not.toBeNull())
  const handler = attachHandler.mock.calls[0][0]
  attachHandler.mockRestore()
  return { handler, terminal: ready as unknown as Terminal }
}

function key(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
}

describe('terminal shortcuts', () => {
  beforeEach(() => {
    useStore.setState({ terminalFontSize: defaultTerminalFontSize })
  })

  it('zooms every terminal through the shared preference', async () => {
    const { handler, terminal } = await mountPane()

    // The key is also the browser's page-zoom accelerator, so cancelling it is
    // half of what the shortcut does; the size has to reach the terminal, not
    // only the store.
    const zoomIn = key({ code: 'Equal', ctrlKey: true })
    expect(handler(zoomIn)).toBe(false)
    expect(zoomIn.defaultPrevented).toBe(true)
    expect(useStore.getState().terminalFontSize).toBe(defaultTerminalFontSize + 1)
    await waitFor(() => expect(terminal.options.fontSize).toBe(defaultTerminalFontSize + 1))

    handler(key({ code: 'Minus', ctrlKey: true }))
    handler(key({ code: 'Minus', ctrlKey: true }))
    expect(useStore.getState().terminalFontSize).toBe(defaultTerminalFontSize - 1)

    handler(key({ code: 'Digit0', ctrlKey: true }))
    expect(useStore.getState().terminalFontSize).toBe(defaultTerminalFontSize)
    await waitFor(() => expect(terminal.options.fontSize).toBe(defaultTerminalFontSize))
  })

  it('keeps the clipboard shortcuts in the chain behind zoom and find', async () => {
    const { handler } = await mountPane()

    // The host owns this wiring since the key handlers were composed, so this
    // is where a lost clipboard shortcut would now go unnoticed.
    expect(handler(key({ code: 'KeyC', ctrlKey: true, shiftKey: true }))).toBe(false)
    const paste = key({ code: 'KeyV', ctrlKey: true, shiftKey: true })
    expect(handler(paste)).toBe(true)
    expect(paste.defaultPrevented).toBe(false)
    expect(handler(key({ code: 'KeyA', ctrlKey: true }))).toBe(true)
  })

  it('finds through the search addon and closes on Escape', async () => {
    const findNext = vi.spyOn(SearchAddon.prototype, 'findNext').mockReturnValue(true)
    const findPrevious = vi.spyOn(SearchAddon.prototype, 'findPrevious').mockReturnValue(true)
    const { handler } = await mountPane()

    const open = key({ code: 'KeyF', ctrlKey: true, shiftKey: true })
    expect(handler(open)).toBe(false)
    expect(open.defaultPrevented).toBe(true)
    const input = await screen.findByLabelText('Find in terminal')

    fireEvent.change(input, { target: { value: 'panic' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(findNext).toHaveBeenCalledWith('panic')

    fireEvent.click(screen.getByLabelText('Find previous'))
    expect(findPrevious).toHaveBeenCalledWith('panic')

    fireEvent.keyDown(input, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByLabelText('Find in terminal')).toBeNull())
    findNext.mockRestore()
    findPrevious.mockRestore()
  })

  it('says so when the term is nowhere in the scrollback', async () => {
    const findNext = vi.spyOn(SearchAddon.prototype, 'findNext').mockReturnValue(false)
    const { handler } = await mountPane()
    handler(key({ code: 'KeyF', ctrlKey: true, shiftKey: true }))

    const input = await screen.findByLabelText('Find in terminal')

    // An empty term is not a search that missed, so it reports nothing even
    // while the addon would answer false.
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(screen.queryByText('No matches')).toBeNull()

    fireEvent.change(input, { target: { value: 'nothing here' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(await screen.findByText('No matches')).toBeDefined()
    findNext.mockRestore()
  })

  it('reports nothing when there is no addon to search with', () => {
    render(
      <TerminalPane
        controller={{
          hostRef: () => {},
          terminal: null,
          ready: false,
          search: null,
          findOpen: true,
          setFindOpen: () => {},
          focusTerminal: () => {},
          ctrlArmed: false,
          armCtrl: () => {},
        }}
      />,
    )

    fireEvent.keyDown(screen.getByLabelText('Find in terminal'), { key: 'Enter' })

    expect(screen.queryByText('No matches')).toBeNull()
  })
})

// Before the socket opens there is no terminal, so every button that needs one
// is blocked, and the chord is the only place the toolbar names it.
describe('the terminal toolbar', () => {
  it('keeps a blocked button reachable, and its click inert', async () => {
    useStore.setState({ terminalFontSize: defaultTerminalFontSize })
    render(
      <TerminalPane
        controller={{
          hostRef: () => {},
          terminal: null,
          ready: false,
          search: null,
          findOpen: false,
          setFindOpen: () => {},
          focusTerminal: () => {},
          ctrlArmed: false,
          armCtrl: () => {},
        }}
      />,
    )

    const copy = screen.getByRole('button', { name: 'Copy terminal selection' })
    expect(copy.getAttribute('aria-disabled')).toBe('true')
    expect(await hintOn(copy)).toBe('Copy terminal selection (Ctrl+Shift+C)')

    fireEvent.click(screen.getByRole('button', { name: 'Decrease terminal text size' }))

    expect(useStore.getState().terminalFontSize).toBe(defaultTerminalFontSize)
  })
})
