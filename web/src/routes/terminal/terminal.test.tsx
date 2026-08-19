import { act, fireEvent, render, screen } from '@testing-library/react'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import { codeDenied } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { initialTerminal, type TerminalState } from '@/store/terminal'
import { run } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

// jsdom has no layout engine, so the fit addon has nothing to observe.
class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function mount(seed: Partial<TerminalState> = {}) {
  const View = lookupRoute('terminal')
  if (!View) throw new Error('terminal route not registered')
  useStore.getState().upsertRun(run())
  useStore.setState({ terminals: { run_1: { ...initialTerminal, ...seed } } })
  return render(<View params={{ runId: 'run_1' }} />)
}

function attached() {
  act(() => {
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ ok: true, cols: 80, rows: 24 }),
    })
  })
}

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('terminal view', () => {
  it('mirrors by default and steers only when the user asks', () => {
    const view = mount()
    attached()

    expect(StubSocket.last().frames()[0]).not.toHaveProperty('write')
    expect(screen.getByText('Attached')).toBeDefined()

    fireEvent.click(screen.getByText('Take control'))
    attached()

    expect(StubSocket.last().frames()[0]).toMatchObject({ write: true })
    expect(screen.getByText('Steering')).toBeDefined()
    view.unmount()
  })

  it('disables the toggle and says why when the server denies steering', () => {
    const view = mount()
    attached()
    fireEvent.click(screen.getByText('Take control'))
    act(() => StubSocket.last().onopen?.())

    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({
          ok: false,
          code: codeDenied,
          error: 'run.attach: permission denied',
        }),
      }),
    )

    const toggle = screen.getByText('Take control') as HTMLButtonElement
    expect(toggle.disabled).toBe(true)
    expect(screen.getByText('You cannot steer this run.')).toBeDefined()
    view.unmount()
  })

  it('starts every attach from the server, not from the last one', () => {
    // What a previous visit to this tab left behind: no steer, a refusal and
    // its message. A fresh attach must answer for itself.
    const view = mount({
      steerDenied: true,
      refused: true,
      message: 'no live terminal',
    })
    attached()

    const toggle = screen.getByText('Take control') as HTMLButtonElement
    expect(toggle.disabled).toBe(false)
    expect(screen.queryByText('no live terminal')).toBeNull()
    expect(screen.queryByText('Retry')).toBeNull()
    view.unmount()
  })

  it('offers a retry when the attach is refused outright', () => {
    const view = mount()
    act(() => StubSocket.last().onopen?.())
    act(() =>
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32004, error: 'no live terminal' }),
      }),
    )

    expect(screen.getByText('no live terminal')).toBeDefined()
    fireEvent.click(screen.getByText('Retry'))
    expect(StubSocket.opened).toHaveLength(2)
    view.unmount()
  })
})
