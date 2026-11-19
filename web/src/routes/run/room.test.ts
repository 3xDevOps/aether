import { act, renderHook, waitFor } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import { useRunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { alice, bob, fakeApi, roomMessage, run, serverInfo } from '@/test/fixtures'

const conflict = new ApiError(200, 'room message already resolved with a different outcome', -32003)

function held(id: string) {
  return useStore.getState().roomMessages.run_1?.find((m) => m.id === id)
}

async function mounted(resolve: () => Promise<{ message: never }>) {
  useStore.setState({ info: serverInfo })
  const note = roomMessage({ id: 'n1', kind: 'comment', actor_id: bob.id, body: 'keep me' })
  const client = fakeApi({ runRoomList: vi.fn(async () => ({ messages: [note] })), runRoomResolve: vi.fn(resolve) })
  const { result } = renderHook(() => useRunRoom(run(), undefined, client))
  await waitFor(() => expect(held('n1')).toBeDefined())
  return { result, note }
}

describe('resolving a note', () => {
  it('reopens a note the server refused and shows its error', async () => {
    const { result } = await mounted(() => Promise.reject(conflict))
    let outcome: { ok: boolean; error?: string } | undefined
    await act(async () => { outcome = await result.current.resolve('n1', 'deleted') })
    expect(outcome).toEqual({ ok: false, error: expect.stringContaining('already resolved') })
    expect(held('n1')?.resolution).toBeUndefined()
  })

  it('keeps another member\'s resolution that landed before the refusal', async () => {
    let refuse!: (cause: unknown) => void
    const { result, note } = await mounted(() => new Promise((_, reject) => { refuse = reject }))
    let pending!: Promise<{ ok: boolean; error?: string }>
    act(() => { pending = result.current.resolve('n1', 'deleted') })
    expect(held('n1')).toBeUndefined()
    const winner = { ...note, resolution: 'done' as const, resolved_by: alice.id, resolved_at: '2026-08-14T11:00:00Z' }
    act(() => useStore.getState().upsertRoomMessage(winner))
    let outcome: { ok: boolean; error?: string } | undefined
    await act(async () => {
      refuse(conflict)
      outcome = await pending
    })
    expect(outcome?.error).toContain('already resolved')
    expect(held('n1')).toMatchObject({ resolution: 'done', resolved_by: alice.id })
  })

  it('carries a refusal that lands after the deadline, instead of leaving only the timeout text', async () => {
    let refuse!: (cause: unknown) => void
    const { result } = await mounted(() => new Promise((_, reject) => { refuse = reject }))
    vi.useFakeTimers()
    try {
      let pending!: Promise<{ ok: boolean; error?: string; refused?: Promise<string> }>
      act(() => { pending = result.current.resolve('n1', 'deleted') })
      await act(async () => { await vi.advanceTimersByTimeAsync(optimisticDeadlineMs) })
      const outcome = await pending
      expect(outcome.ok).toBe(false)
      expect(outcome.error).toContain('did not answer')
      expect(outcome.refused).toBeDefined()
      refuse(conflict)
      await expect(outcome.refused).resolves.toContain('already resolved')
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('loading the room', () => {
  const noAnswer = () => new TypeError('Failed to fetch')

  async function load(list: () => Promise<{ messages: ReturnType<typeof roomMessage>[] }>) {
    useStore.setState({ info: serverInfo })
    const runRoomList = vi.fn(list)
    vi.useFakeTimers()
    try {
      renderHook(() => useRunRoom(run(), undefined, fakeApi({ runRoomList })))
      await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    } finally {
      vi.useRealTimers()
    }
    return { runRoomList, error: useStore.getState().roomError.run_1 }
  }

  it('retries a list that got no answer, without showing an error', async () => {
    const note = roomMessage({ id: 'n1', kind: 'comment', actor_id: bob.id, body: 'after the blip' })
    let calls = 0
    const { runRoomList, error } = await load(async () => {
      calls += 1
      if (calls === 1) throw noAnswer()
      return { messages: [note] }
    })
    expect(runRoomList).toHaveBeenCalledTimes(2)
    expect(error).toBeUndefined()
    expect(held('n1')).toBeDefined()
  })

  it('shows a server refusal at once', async () => {
    const { runRoomList, error } = await load(() => Promise.reject(new ApiError(403, 'not a member of this workspace', -32003)))
    expect(runRoomList).toHaveBeenCalledTimes(1)
    expect(error).toContain('not a member')
  })

  it('stops retrying once the session view closes', async () => {
    useStore.setState({ info: serverInfo })
    const runRoomList = vi.fn(() => Promise.reject(noAnswer()))
    vi.useFakeTimers()
    try {
      const { unmount } = renderHook(() => useRunRoom(run(), undefined, fakeApi({ runRoomList })))
      await act(async () => { await vi.advanceTimersByTimeAsync(0) })
      unmount()
      await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    } finally {
      vi.useRealTimers()
    }
    expect(runRoomList).toHaveBeenCalledTimes(1)
  })

  it('shows the error once its retries also get no answer', async () => {
    const { runRoomList, error } = await load(() => Promise.reject(noAnswer()))
    expect(runRoomList).toHaveBeenCalledTimes(4)
    expect(error).toContain('Failed to fetch')
  })
})
