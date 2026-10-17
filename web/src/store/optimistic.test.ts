import { act, renderHook } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import { optimistic, optimisticDeadlineMs, refusedByServer, usePastDeadline } from '@/store/optimistic'

describe('refusedByServer', () => {
  it('counts only a JSON-RPC error the server sent, never a gateway or edge transport failure', () => {
    expect(refusedByServer(new ApiError(200, 'member.invitation.create: octocat is already a member', -32003))).toBe(true)
    expect(refusedByServer(new TypeError('Failed to fetch'))).toBe(false)
    expect(refusedByServer(new ApiError(502, 'Bad Gateway'))).toBe(false)
    expect(refusedByServer(new ApiError(503, 'run.room.post: server unreachable: connection reset', -32004))).toBe(false)
    expect(refusedByServer(new ApiError(503, 'run.room.post: network unreachable: no route to host', -32004))).toBe(false)
    expect(refusedByServer(new ApiError(503, 'run.room.post: server did not answer run.room.post within 1m0s', -32004))).toBe(false)
    expect(refusedByServer(new ApiError(503, 'run.room.post: server unreachable: device pending approval', -32004, {
      refusal: { kind: 'device-pending', approval_code: 'ABCD-1234' },
    }))).toBe(false)
    expect(refusedByServer(new ApiError(200, 'edge refused the request', -32002, { refusal: { kind: 'server-not-connected' } }))).toBe(false)
  })
})

describe('usePastDeadline', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('turns true once a pending flag has held for the deadline, and resets when it clears', () => {
    const { result, rerender } = renderHook(({ pending }) => usePastDeadline(pending), { initialProps: { pending: true } })
    act(() => vi.advanceTimersByTime(optimisticDeadlineMs - 1))
    expect(result.current).toBe(false)
    act(() => vi.advanceTimersByTime(1))
    expect(result.current).toBe(true)
    rerender({ pending: false })
    expect(result.current).toBe(false)
    rerender({ pending: true })
    expect(result.current).toBe(false)
  })
})

describe('optimistic', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('reports a refusal that arrives after the deadline, without a second revert', async () => {
    let refuse = (_: Error) => {}
    const revert = vi.fn()
    const pending = optimistic({
      apply: () => {},
      send: () => new Promise((_, reject) => (refuse = reject)),
      confirm: () => {},
      revert,
    })
    vi.advanceTimersByTime(optimisticDeadlineMs)
    const outcome = await pending
    expect(outcome).toMatchObject({ ok: false, unanswered: true })
    refuse(new ApiError(403, '403 forbidden: not yours', -32001))
    expect(await (outcome.ok ? undefined : outcome.refused)).toContain('403 forbidden: not yours')
    expect(revert).toHaveBeenCalledTimes(1)
  })

  it('never reverts a retry once an earlier attempt of the same mutation was answered', async () => {
    const answers: ((result: string) => void)[] = []
    const revert = vi.fn()
    const pending = optimistic({
      apply: () => {},
      send: () => new Promise<string>((resolve) => answers.push(resolve)),
      confirm: () => {},
      revert,
    })
    vi.advanceTimersByTime(optimisticDeadlineMs)
    const first = await pending
    if (first.ok) throw new Error('expected the first attempt to time out')
    const retried = first.retry()
    answers[0]!('saved')
    await Promise.resolve()
    vi.advanceTimersByTime(optimisticDeadlineMs)
    expect(await retried).toEqual({ ok: true, result: 'saved' })
    expect(revert).toHaveBeenCalledTimes(1)
  })

  it('treats a failure with no JSON-RPC answer as unanswered, and never as a later refusal', async () => {
    const revert = vi.fn()
    const outcome = await optimistic({
      apply: () => {},
      send: () => Promise.reject(new TypeError('Failed to fetch')),
      confirm: () => {},
      revert,
    })
    expect(outcome).toMatchObject({ ok: false, unanswered: true, error: 'Failed to fetch' })
    expect(revert).toHaveBeenCalledTimes(1)

    let fail = (_: Error) => {}
    const pending = optimistic({
      apply: () => {},
      send: () => new Promise((_, reject) => (fail = reject)),
      confirm: () => {},
      revert: () => {},
    })
    vi.advanceTimersByTime(optimisticDeadlineMs)
    const late = await pending
    const refused = vi.fn()
    void (late.ok ? undefined : late.refused)?.then(refused)
    fail(new ApiError(503, 'run.room.post: server unreachable: connection reset', -32004))
    await Promise.resolve()
    await Promise.resolve()
    expect(refused).not.toHaveBeenCalled()
  })
})
