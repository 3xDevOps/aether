// Finding 2: a refusal that lands during the scratch phase leaves the card collapsed.
// Finding 5: a refused delete still announces "Note deleted."
// Copy into web/src/routes/run/ on the PR #798 branch and run with vitest.
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { NotesRail } from '@/routes/run/session-notes'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, bob, roomMessage, run, serverInfo, workspace } from '@/test/fixtures'
import type { RoomResolution } from '@/lib/types'

const record = toRecord(run())

function seed() {
  useStore.setState({
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    roomMessages: { [record.id]: [roomMessage({ id: 'n1', actor_id: bob.id, body: 'keep me' })] },
  })
}

// The real optimistic shape: apply, then the server refuses and the store reverts.
function refusing(): RunRoom['resolve'] {
  return async (id: string, outcome: RoomResolution) => {
    act(() => useStore.getState().setRoomMessageResolution(record.id, id, outcome, alice.id, 'x'))
    await Promise.resolve()
    act(() => useStore.getState().setRoomMessageResolution(record.id, id, ''))
    return { ok: false, error: 'conflict from server' }
  }
}

const room = (resolve: RunRoom['resolve']) => ({
  post: vi.fn(), note: vi.fn(), decide: vi.fn(), resolve,
  loadOlder: vi.fn(), refresh: vi.fn(), error: undefined, errorFromComposer: false, clearError: vi.fn(),
}) as unknown as RunRoom

afterEach(() => vi.restoreAllMocks())

it('does not collapse the card after a refusal that lands during the scratch phase', async () => {
  let releaseScratch!: () => void
  const scratchDone = new Promise<void>((resolve) => { releaseScratch = resolve })
  const calls: { tag: string; keyframes: Keyframe[] }[] = []
  vi.spyOn(Element.prototype, 'animate').mockImplementation(function (this: Element, keyframes) {
    calls.push({ tag: this.tagName, keyframes: keyframes as Keyframe[] })
    const finished = this.tagName.toLowerCase() === 'path' ? scratchDone : Promise.resolve()
    return { finished, cancel() {} } as unknown as Animation
  })
  seed()
  render(<NotesRail run={record} room={room(refusing())} />)
  fireEvent.click(screen.getByRole('button', { name: 'Complete note' }))
  await waitFor(() => expect(screen.getByText('conflict from server')).toBeDefined())
  const before = calls.filter((c) => c.tag === 'LI').length
  await act(async () => {
    releaseScratch()
    await scratchDone
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  // Fails on the PR: the fade to opacity 0 and the height collapse run after the error shows.
  expect(calls.filter((c) => c.tag === 'LI').slice(before)).toEqual([])
})

it('does not announce a deletion the server refused', async () => {
  vi.spyOn(Element.prototype, 'animate').mockImplementation(function (this: Element & { __anims?: Animation[] }) {
    let reject!: (e: unknown) => void
    const finished = new Promise<Animation>((_, r) => { reject = r })
    const animation = { finished, cancel() { reject(new DOMException('cancelled', 'AbortError')) } } as unknown as Animation
    ;(this.__anims ??= []).push(animation)
    return animation
  })
  vi.spyOn(Element.prototype, 'getAnimations').mockImplementation(function (this: Element) {
    return (this as unknown as { __anims?: Animation[] }).__anims ?? []
  })
  seed()
  render(<NotesRail run={record} room={room(refusing())} />)
  fireEvent.click(screen.getByRole('button', { name: 'Delete note' }))
  await waitFor(() => expect(screen.getByText('conflict from server')).toBeDefined())
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
  // Fails on the PR: the status reads "Note deleted."
  expect(screen.getByRole('status').textContent).toBe('')
})
