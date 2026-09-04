import { useEffect } from 'react'
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

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
})

afterEach(() => {
  vi.unstubAllGlobals()
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
