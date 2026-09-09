import { useEffect, useState } from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { SearchAddon } from '@xterm/addon-search'
import { Terminal } from '@xterm/xterm'
import { TerminalPane } from '@/components/terminal-pane'
import { useXterm } from '@/components/xterm-host'
import { defaultTerminalFontSize } from '@/lib/term-font'
import { connectAttach } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { StubSocket } from '@/test/stub-socket'

class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

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
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
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
    expect(handler(key({ code: 'KeyV', ctrlKey: true, shiftKey: true }))).toBe(false)
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
        }}
      />,
    )

    fireEvent.keyDown(screen.getByLabelText('Find in terminal'), { key: 'Enter' })

    expect(screen.queryByText('No matches')).toBeNull()
  })
})
