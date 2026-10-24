import { act, render, screen } from '@testing-library/react'
import { api } from '@/lib/api'
import { TypingIndicator, useTypingSignal } from '@/routes/run/typing'
import { useStore } from '@/store'
import { applyEvent } from '@/store/sync'
import { typingThrottleMs, typingTtlMs } from '@/store/typing'
import { alice, bob, serverInfo, vera } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const runID = 'run_1'

function Probe({ body }: { body: string }) {
  useTypingSignal(runID, body)
  return null
}

function seed(watching: string[] = [alice.id, bob.id]) {
  useStore.setState({
    info: { ...serverInfo, member: alice },
    members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera },
    presence: watching.map((id) => ({ member_id: id, state: 'watching' as const, watching: [runID], last_seen: '2026-08-14T10:05:00Z' })),
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    typing: {},
  })
}

describe('TypingIndicator', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-14T10:05:00Z'))
    vi.mocked(api.presenceTyping).mockClear()
  })
  afterEach(() => vi.useRealTimers())

  it('names others who type, never the local member, and drops them after the expiry', () => {
    seed()
    render(<TypingIndicator runID={runID} />)
    expect(screen.getByRole('status').textContent).toBe('')
    act(() => {
      useStore.getState().noteTyping(runID, alice.id)
      useStore.getState().noteTyping(runID, bob.id)
    })
    expect(screen.getByRole('status').textContent).toBe('Bob is typing...')
    act(() => void vi.advanceTimersByTime(typingTtlMs + 100))
    expect(screen.getByRole('status').textContent).toBe('')
  })

  it('says several people past three typists', () => {
    seed()
    render(<TypingIndicator runID={runID} />)
    act(() => {
      for (const id of ['a', 'b', 'c', 'd']) useStore.getState().noteTyping(runID, id)
    })
    expect(screen.getByRole('status').textContent).toBe('Several people are typing...')
  })

  it('renders nothing on a run nobody else watches', () => {
    seed([alice.id])
    const { container } = render(<TypingIndicator runID={runID} />)
    expect(container.firstChild).toBeNull()
  })

  it('shows a typing event from the stream and clears it on the typist’s stop event', async () => {
    seed()
    render(<TypingIndicator runID={runID} />)
    const event = (payload: unknown) => ({ id: 'x', seq: 0, time: '', workspace_id: 'wsp_1', run_id: runID, actor_id: bob.id, type: 'run.typing', payload })
    await act(async () => void (await applyEvent(useStore, event({}))))
    expect(screen.getByRole('status').textContent).toBe('Bob is typing...')
    expect(useStore.getState().lastSeq).toBe(0)
    expect(useStore.getState().feed).toEqual([])
    await act(async () => void (await applyEvent(useStore, event({ stopped: true }))))
    expect(screen.getByRole('status').textContent).toBe('')
  })
})

describe('useTypingSignal', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-14T10:05:00Z'))
    vi.mocked(api.presenceTyping).mockClear()
  })
  afterEach(() => vi.useRealTimers())

  it('signals at most once per throttle window, then once to stop when the draft is cleared', () => {
    seed()
    const view = render(<Probe body="" />)
    view.rerender(<Probe body="h" />)
    view.rerender(<Probe body="he" />)
    expect(api.presenceTyping).toHaveBeenCalledTimes(1)
    expect(api.presenceTyping).toHaveBeenLastCalledWith(runID)
    act(() => void vi.advanceTimersByTime(typingThrottleMs))
    view.rerender(<Probe body="hel" />)
    expect(api.presenceTyping).toHaveBeenCalledTimes(2)
    view.rerender(<Probe body="" />)
    expect(api.presenceTyping).toHaveBeenLastCalledWith(runID, true)
    expect(api.presenceTyping).toHaveBeenCalledTimes(3)
  })

  it('stops on unmount after typing', () => {
    seed()
    const view = render(<Probe body="" />)
    view.rerender(<Probe body="h" />)
    view.unmount()
    expect(api.presenceTyping).toHaveBeenLastCalledWith(runID, true)
  })

  it('stays silent when nobody else watches, and for a restored draft', () => {
    seed([alice.id])
    const view = render(<Probe body="draft" />)
    view.rerender(<Probe body="draft!" />)
    view.unmount()
    expect(api.presenceTyping).not.toHaveBeenCalled()
  })
})
