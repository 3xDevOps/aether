import { act, renderHook, waitFor } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import { useRunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
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
})
