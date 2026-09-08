import { useEffect, useState } from 'react'
import { render, waitFor } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import { useXterm } from '@/components/xterm-host'
import { connectAttach } from '@/routes/terminal/attach'
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
