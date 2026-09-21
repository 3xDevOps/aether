import { useEffect, useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { SearchAddon } from '@xterm/addon-search'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { TerminalPane } from '@/components/terminal-pane'
import { useXterm } from '@/components/xterm-host'
import type { XtermController } from '@/components/xterm-host'
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
  it('keeps the start of a transcript beyond the former 50,000-row limit', async () => {
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
      { length: 50_100 },
      (_, index) => `line ${index + 1}\r\n`,
    ).join('')
    socket.onmessage?.({ data: new TextEncoder().encode(replay).buffer })

    await waitFor(() =>
      expect(ready?.buffer.active.getLine(0)?.translateToString().trimEnd()).toBe('line 1'),
    )
    attachment.close()
  })

  it('retains a screen cleared by terminal output in normal scrollback', async () => {
    let ready: Terminal | null = null
    render(<Probe onReady={(terminal) => (ready = terminal)} />)
    await waitFor(() => expect(ready).not.toBeNull())
    const terminal = ready as unknown as Terminal

    await new Promise<void>((resolve) => terminal.write('before clear\r\n', resolve))
    await new Promise<void>((resolve) => terminal.write('\x1b[2Jafter clear', resolve))

    const lines = Array.from({ length: terminal.buffer.active.length }, (_, index) =>
      terminal.buffer.active.getLine(index)?.translateToString().trimEnd(),
    )
    expect(lines).toContain('before clear')
  })
})

function SizedProbe({
  follow = true,
  onResize,
  onReady,
}: {
  follow?: boolean
  onResize: (cols: number, rows: number) => void
  onReady: (controller: XtermController) => void
}) {
  const controller = useXterm({ follow, onResize })
  useEffect(() => {
    if (controller.terminal) onReady(controller)
  })
  return <div ref={controller.hostRef} />
}

describe('shared terminal geometry', () => {
  it('follows the server without reporting a phone-sized viewport', async () => {
    let controller: XtermController | null = null
    const onResize = vi.fn()
    const view = render(<SizedProbe onResize={onResize} onReady={(next) => { controller = next }} />)
    await waitFor(() => expect(controller?.terminal).toBeTruthy())
    const host = controller as unknown as XtermController
    host.setGeometry(132, 43)
    host.terminal!.write('\x1b[43;132HX')
    await waitFor(() =>
      expect(host.terminal!.buffer.active.getLine(42)?.translateToString().trimEnd()).toBe(`${' '.repeat(131)}X`),
    )
    expect(onResize).not.toHaveBeenCalled()
    view.unmount()
  })

  it('renders server-sized output in order without feeding that size back as its viewport', async () => {
    const proposal = vi.spyOn(FitAddon.prototype, 'proposeDimensions').mockReturnValue({ cols: 120, rows: 30 })
    let controller: XtermController | null = null
    const onResize = vi.fn()
    const view = render(<SizedProbe follow={false} onResize={onResize} onReady={(next) => { controller = next }} />)
    await waitFor(() => expect(controller?.terminal).toBeTruthy())
    const host = controller as unknown as XtermController
    const terminal = host.terminal!
    host.setGeometry(20, 4)
    terminal.write(`${'a'.repeat(20)}B`)
    host.setGeometry(30, 4)
    terminal.write(`\x1b[3;1H${'c'.repeat(30)}D`)
    await waitFor(() => expect(terminal.buffer.active.getLine(3)?.translateToString().trimEnd()).toBe('D'))
    expect(terminal.buffer.active.getLine(0)?.translateToString().trimEnd()).toBe('a'.repeat(20))
    expect(terminal.buffer.active.getLine(1)?.translateToString().trimEnd()).toBe('B')
    expect(host.geometry()).toEqual({ cols: 120, rows: 30 })

    proposal.mockReturnValue({ cols: 100, rows: 25 })
    act(() => useStore.getState().setTerminalFontSize(14))
    await waitFor(() => expect(onResize).toHaveBeenLastCalledWith(100, 25))
    terminal.write('\x1b[4;30HX')
    await waitFor(() => expect(terminal.buffer.active.getLine(3)?.getCell(29)?.getChars()).toBe('X'))
    expect([terminal.cols, terminal.rows]).toEqual([30, 4])
    expect(host.geometry()).toEqual({ cols: 100, rows: 25 })
    view.unmount()
    proposal.mockRestore()
  })

  it('starts measuring immediately when a phone becomes a desktop', async () => {
    const proposal = vi.spyOn(FitAddon.prototype, 'proposeDimensions').mockReturnValue({ cols: 100, rows: 25 })
    const onResize = vi.fn()
    const onReady = () => {}
    const view = render(<SizedProbe onResize={onResize} onReady={onReady} />)
    expect(onResize).not.toHaveBeenCalled()
    view.rerender(<SizedProbe follow={false} onResize={onResize} onReady={onReady} />)
    await waitFor(() => expect(onResize).toHaveBeenLastCalledWith(100, 25))
    view.unmount()
    proposal.mockRestore()
  })
})

function writeTerminal(terminal: Terminal, data: string): Promise<void> {
  return new Promise((resolve) => terminal.write(data, resolve))
}

function transcript(prefix: string, count: number): string {
  return Array.from({ length: count }, (_, index) => `${prefix}-${index}\r\n`).join('')
}

function bottomOffset(terminal: Terminal): number {
  return terminal.buffer.active.baseY - terminal.buffer.active.viewportY
}
/**
 * jsdom gives xterm's viewport no cell height, so its DOM scrollbar cannot
 * drive the buffer display cursor. Map xterm's public scroll methods to that
 * cursor while leaving the parser and write queue completely untouched.
 */
function installViewportSeam(terminal: Terminal): void {
  const buffer = () =>
    (terminal as unknown as {
      _core: { _bufferService: { buffer: { ybase: number; ydisp: number } } }
    })._core._bufferService.buffer
  const setBottomOffset = (offset: number) => {
    const active = buffer()
    active.ydisp = Math.max(0, active.ybase - Math.min(offset, active.ybase))
  }

  vi.spyOn(terminal, 'scrollLines').mockImplementation((amount) => {
    setBottomOffset(bottomOffset(terminal) - amount)
  })
  vi.spyOn(terminal, 'scrollToLine').mockImplementation((line) => {
    const active = buffer()
    active.ydisp = Math.max(0, Math.min(line, active.ybase))
  })
  vi.spyOn(terminal, 'scrollToBottom').mockImplementation(() => setBottomOffset(0))
}

async function mountSizedController(): Promise<{
  controller: XtermController
  unmount: () => void
}> {
  let controller: XtermController | null = null
  const view = render(
    <SizedProbe
      follow={false}
      onResize={() => {}}
      onReady={(next) => {
        controller = next
      }}
    />,
  )
  await waitFor(() => expect(controller?.terminal).toBeTruthy())
  installViewportSeam((controller as unknown as XtermController).terminal!)
  return {
    controller: controller as unknown as XtermController,
    unmount: view.unmount,
  }
}

describe('xterm viewport ownership', () => {
  it('lets xterm follow new output at the bottom and pin a user-scrolled viewport', async () => {
    const mounted = await mountSizedController()
    const terminal = mounted.controller.terminal!
    await mounted.controller.setGeometry(20, 4)
    await writeTerminal(terminal, transcript('initial', 12))

    expect(bottomOffset(terminal)).toBe(0)
    const priorBase = terminal.buffer.active.baseY
    await writeTerminal(terminal, 'following\r\n')
    expect(terminal.buffer.active.baseY).toBeGreaterThan(priorBase)
    expect(bottomOffset(terminal)).toBe(0)

    terminal.scrollLines(-2)
    const pinnedOffset = bottomOffset(terminal)
    expect(pinnedOffset).toBe(2)
    const pinnedBase = terminal.buffer.active.baseY
    const core = (terminal as unknown as {
      _core: { _bufferService: { buffer: { ybase: number; ydisp: number } } }
    })._core
    core._bufferService.buffer.ybase += 2

    expect(terminal.buffer.active.baseY).toBeGreaterThan(pinnedBase)
    expect(bottomOffset(terminal)).toBeGreaterThan(0)
    mounted.unmount()
  })

  it('restores follow-bottom only after a structural full replay completes', async () => {
    const mounted = await mountSizedController()
    const terminal = mounted.controller.terminal!
    await mounted.controller.setGeometry(20, 4)
    await writeTerminal(terminal, transcript('before', 12))
    expect(bottomOffset(terminal)).toBe(0)

    const generation = mounted.controller.beginStructuralReplay!()
    await mounted.controller.setGeometry(20, 4, true)
    await writeTerminal(terminal, transcript('after', 16))
    terminal.scrollLines(-2)
    expect(bottomOffset(terminal)).toBe(2)
    await mounted.controller.finishStructuralReplay!(generation)

    expect(bottomOffset(terminal)).toBe(0)
    mounted.unmount()
  })

  it('restores a pinned bottom offset across a structural full replay', async () => {
    const mounted = await mountSizedController()
    const terminal = mounted.controller.terminal!
    await mounted.controller.setGeometry(20, 4)
    await writeTerminal(terminal, transcript('before', 14))
    terminal.scrollLines(-4)
    expect(bottomOffset(terminal)).toBe(4)

    const generation = mounted.controller.beginStructuralReplay!()
    await mounted.controller.setGeometry(20, 4, true)
    await writeTerminal(terminal, transcript('after', 18))
    await mounted.controller.finishStructuralReplay!(generation)

    expect(bottomOffset(terminal)).toBe(4)
    mounted.unmount()
  })

  it('does not restore viewport intent into a different active buffer', async () => {
    const mounted = await mountSizedController()
    const terminal = mounted.controller.terminal!
    await mounted.controller.setGeometry(20, 4)
    await writeTerminal(terminal, transcript('normal', 14))
    terminal.scrollLines(-5)
    await writeTerminal(terminal, '\x1b[?1049h')

    const generation = mounted.controller.beginStructuralReplay!()
    await writeTerminal(terminal, '\x1b[?1049l')
    terminal.scrollToLine(terminal.buffer.active.baseY - 2)
    expect(bottomOffset(terminal)).toBe(2)

    await mounted.controller.finishStructuralReplay!(generation)

    expect(bottomOffset(terminal)).toBe(2)
    mounted.unmount()
  })

  it('leaves viewport intent made during replay newer than the captured intent', async () => {
    const mounted = await mountSizedController()
    const terminal = mounted.controller.terminal!
    await mounted.controller.setGeometry(20, 4)
    await writeTerminal(terminal, transcript('before', 14))
    terminal.scrollLines(-4)

    const generation = mounted.controller.beginStructuralReplay!()
    await mounted.controller.setGeometry(20, 4, true)
    await writeTerminal(terminal, transcript('after', 18))
    terminal.scrollLines(-1)
    terminal.element!.dispatchEvent(new WheelEvent('wheel', { deltaY: -1, bubbles: true }))
    expect(bottomOffset(terminal)).toBe(1)

    await mounted.controller.finishStructuralReplay!(generation)

    expect(bottomOffset(terminal)).toBe(1)
    mounted.unmount()
  })

  it('cannot finish an aborted replay through an older generation', async () => {
    const mounted = await mountSizedController()
    const terminal = mounted.controller.terminal!
    await mounted.controller.setGeometry(20, 4)
    await writeTerminal(terminal, transcript('before', 14))
    terminal.scrollLines(-4)

    const stale = mounted.controller.beginStructuralReplay!()
    await mounted.controller.setGeometry(20, 4, true)
    await writeTerminal(terminal, transcript('aborted', 16))
    await mounted.controller.cancelStructuralReplay!(stale)

    terminal.scrollToBottom()
    const current = mounted.controller.beginStructuralReplay!()
    await mounted.controller.setGeometry(20, 4, true)
    await writeTerminal(terminal, transcript('current', 18))
    const viewport = terminal.buffer.active.viewportY

    await mounted.controller.finishStructuralReplay!(stale)
    expect(terminal.buffer.active.viewportY).toBe(viewport)

    await mounted.controller.finishStructuralReplay!(current)
    expect(bottomOffset(terminal)).toBe(0)
    mounted.unmount()
  })

  it('keeps a pinned bottom offset when structural replay reflows columns', async () => {
    const mounted = await mountSizedController()
    const terminal = mounted.controller.terminal!
    await mounted.controller.setGeometry(24, 4)
    await writeTerminal(terminal, transcript('a-wide-logical-line', 14))
    terminal.scrollLines(-3)
    expect(bottomOffset(terminal)).toBe(3)

    const generation = mounted.controller.beginStructuralReplay!()
    await mounted.controller.setGeometry(12, 4)
    await mounted.controller.finishStructuralReplay!(generation)

    expect(bottomOffset(terminal)).toBe(3)
    mounted.unmount()
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
function PaneProbe({
  onReady,
  replaying = false,
}: {
  onReady: (terminal: Terminal) => void
  replaying?: boolean
}) {
  const controller = useXterm()
  useEffect(() => {
    if (controller.terminal) onReady(controller.terminal)
  }, [controller.terminal, onReady])
  return <TerminalPane controller={controller} replaying={replaying} />
}

describe('terminal replay surface', () => {
  it('hides the mounted xterm until replay parsing completes', async () => {
    let ready: Terminal | null = null
    const view = render(<PaneProbe replaying onReady={(terminal) => (ready = terminal)} />)
    await waitFor(() => expect(ready).not.toBeNull())

    const host = document.querySelector(
      '.min-h-0.min-w-0.flex-1.overflow-x-auto.overflow-y-hidden.bg-background',
    ) as HTMLElement
    expect(host.style.visibility).toBe('hidden')
    expect(screen.getByRole('status', { name: 'Restoring terminal history' })).toBeDefined()

    view.rerender(<PaneProbe onReady={(terminal) => (ready = terminal)} />)
    await waitFor(() => expect(host.style.visibility).toBe(''))
    expect(screen.queryByRole('status', { name: 'Restoring terminal history' })).toBeNull()
    view.unmount()
  })
})

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

  it('focuses xterm and terminal find without scrolling an ancestor', async () => {
    let ancestor: HTMLElement | null = null
    const scrollIntoView = vi.fn(() => {
      if (ancestor) ancestor.scrollTop = 0
    })
    const previous = Element.prototype.scrollIntoView
    Object.defineProperty(Element.prototype, 'scrollIntoView', {
      configurable: true,
      value: scrollIntoView,
    })
    try {
      const { handler, terminal } = await mountPane()
      ancestor = terminal.element!.parentElement!.parentElement as HTMLElement
      ancestor.scrollTop = 37
      terminal.focus()
      await writeTerminal(terminal, 'cursor moved')
      handler(key({ code: 'KeyF', ctrlKey: true, shiftKey: true }))
      await screen.findByLabelText('Find in terminal')
      expect(scrollIntoView).not.toHaveBeenCalled()
      expect(ancestor.scrollTop).toBe(37)
    } finally {
      if (previous) {
        Object.defineProperty(Element.prototype, 'scrollIntoView', {
          configurable: true,
          value: previous,
        })
      } else {
        delete (Element.prototype as { scrollIntoView?: typeof Element.prototype.scrollIntoView })
          .scrollIntoView
      }
    }
  })

  it('says so when the term is nowhere in the scrollback', async () => {
    const findNext = vi.spyOn(SearchAddon.prototype, 'findNext').mockReturnValue(false)
    const { handler } = await mountPane()
    handler(key({ code: 'KeyF', ctrlKey: true, shiftKey: true }))
    const input = await screen.findByLabelText('Find in terminal')
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
          geometry: () => ({ cols: 80, rows: 24 }),
          setGeometry: async () => {},
          beginStructuralReplay: () => 0,
          cancelStructuralReplay: async () => {},
          finishStructuralReplay: async () => {},
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

describe('the terminal toolbar', () => {
  it('keeps a blocked button reachable, and its click inert', async () => {
    useStore.setState({ terminalFontSize: defaultTerminalFontSize })
    render(
      <TerminalPane
        controller={{
          hostRef: () => {},
          terminal: null,
          ready: false,
          geometry: () => ({ cols: 80, rows: 24 }),
          setGeometry: async () => {},
          beginStructuralReplay: () => 0,
          cancelStructuralReplay: async () => {},
          finishStructuralReplay: async () => {},
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
