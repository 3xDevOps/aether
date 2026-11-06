import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { NotesRail, NotesStrip } from '@/routes/run/session-notes'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, bob, roomMessage, run, serverInfo, workspace } from '@/test/fixtures'
import type { RoomMessage, RoomResolution } from '@/lib/types'

const record = toRecord(run())

function note(n: number, over: Partial<RoomMessage> = {}): RoomMessage {
  return roomMessage({
    id: `n${n}`, actor_id: bob.id, body: `note ${n}`,
    created_at: `2026-08-14T10:${String(n).padStart(2, '0')}:00Z`, ...over,
  })
}

function seed(messages: RoomMessage[]) {
  useStore.setState({
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    roomMessages: { [record.id]: messages },
  })
}

function fakeRoom(resolve: RunRoom['resolve']): RunRoom {
  return {
    post: vi.fn(), note: vi.fn(), decide: vi.fn(), resolve,
    loadOlder: vi.fn(), refresh: vi.fn(), error: undefined, errorFromComposer: false, clearError: vi.fn(),
  }
}

/** Mimics the real optimistic apply: flip the message's resolution in the store. */
function applyingResolve(outcome: 'ok' | 'refuse') {
  return vi.fn(async (id: string, resolution: RoomResolution) => {
    if (outcome === 'refuse') return { ok: false as const, error: 'The server refused.' }
    act(() => useStore.getState().setRoomMessageResolution(record.id, id, resolution, alice.id, '2026-08-14T11:00:00Z'))
    return { ok: true as const }
  })
}

/** The real optimistic shape: apply, then the server refuses and the store puts the note back. */
function refusingAfterApply(): RunRoom['resolve'] {
  return async (id: string, outcome: RoomResolution) => {
    const before = useStore.getState().roomMessages[record.id].find((m) => m.id === id)!
    act(() => useStore.getState().setRoomMessageResolution(record.id, id, outcome, alice.id, '2026-08-14T11:00:00Z'))
    await Promise.resolve()
    act(() => useStore.getState().upsertRoomMessage(before))
    return { ok: false, error: 'conflict from server' }
  }
}

afterEach(() => vi.restoreAllMocks())

describe('sticky note rail', () => {
  it('shows only open comments: a question, a done note and a deleted note are excluded', () => {
    seed([
      note(1, { body: 'open comment' }),
      note(2, { kind: 'question', body: 'a question' }),
      note(3, { body: 'done comment', resolution: 'done' }),
      note(4, { body: 'deleted comment', resolution: 'deleted' }),
    ])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('ok'))} />)
    expect(screen.getByText('open comment')).toBeDefined()
    expect(screen.queryByText('a question')).toBeNull()
    expect(screen.queryByText('done comment')).toBeNull()
    expect(screen.queryByText('deleted comment')).toBeNull()
  })

  it('stacks open notes oldest first', () => {
    seed([note(2, { body: 'second' }), note(1, { body: 'first', created_at: '2026-08-14T09:00:00Z' })])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('ok'))} />)
    const bodies = screen.getAllByText(/first|second/)
    expect(bodies.map((el) => el.textContent)).toEqual(['first', 'second'])
  })

  it('keeps creation order when an older note loads in beside an already-shown one', () => {
    seed([note(2, { body: 'second' })])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('ok'))} />)
    act(() => seed([note(1, { body: 'first', created_at: '2026-08-14T09:00:00Z' }), note(2, { body: 'second' })]))
    const bodies = screen.getAllByText(/first|second/)
    expect(bodies.map((el) => el.textContent)).toEqual(['first', 'second'])
  })

  it('completing a note plays the scratch strokes then removes it', async () => {
    const animate = vi.spyOn(Element.prototype, 'animate')
    seed([note(1, { body: 'finish me' })])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('ok'))} />)
    fireEvent.click(screen.getByRole('button', { name: 'Complete note' }))
    await waitFor(() => expect(screen.queryByText('finish me')).toBeNull())
    // 3 scratch strokes, then an opacity and a height animation for the collapse.
    expect(animate.mock.calls.length).toBeGreaterThanOrEqual(5)
    // Each stroke must hold its drawn-in end state, or it snaps back to
    // hidden before the collapse even starts.
    for (const call of animate.mock.calls.slice(0, 3)) {
      expect((call[1] as KeyframeAnimationOptions).fill).toBe('forwards')
    }
  })

  it('deleting a note fades and collapses it without a scratch, then removes it', async () => {
    const animate = vi.spyOn(Element.prototype, 'animate')
    seed([note(1, { body: 'drop me' })])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('ok'))} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete note' }))
    await waitFor(() => expect(screen.queryByText('drop me')).toBeNull())
    expect(animate.mock.calls.length).toBe(2)
  })

  it('shows who sent it, the connector it came through, and disables actions until it is saved', () => {
    seed([note(1, { body: 'typing…', id: 'prov1', idempotency_key: 'prov1', via: 'ChatGPT' })])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('ok'))} />)
    expect(screen.getByText('Sending…')).toBeDefined()
    expect(screen.getByText(/via ChatGPT/)).toBeDefined()
    expect((screen.getByRole('button', { name: 'Complete note' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Delete note' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('a refusal keeps the note and shows the real error', async () => {
    seed([note(1, { body: 'keep me' })])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('refuse'))} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete note' }))
    await waitFor(() => expect(screen.getByText('The server refused.')).toBeDefined())
    expect(screen.getByText('keep me')).toBeDefined()
  })

  it('shows the real refusal once it arrives after the timeout', async () => {
    seed([note(1, { body: 'keep me' })])
    let settleRefusal!: (reason: string) => void
    const refused = new Promise<string>((resolve) => { settleRefusal = resolve })
    const resolve = vi.fn(async () => ({ ok: false as const, error: 'The server did not answer in 5 seconds.', refused }))
    render(<NotesRail run={record} room={fakeRoom(resolve)} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete note' }))
    await waitFor(() => expect(screen.getByText('The server did not answer in 5 seconds.')).toBeDefined())
    await act(async () => {
      settleRefusal('The run no longer exists.')
      await refused
    })
    expect(screen.getByText('The run no longer exists.')).toBeDefined()
    expect(screen.queryByText('The server did not answer in 5 seconds.')).toBeNull()
  })

  it('keeps the card visible when a refusal lands during the scratch', async () => {
    let releaseScratch!: () => void
    const scratchDone = new Promise<void>((resolve) => { releaseScratch = resolve })
    const animated: string[] = []
    vi.spyOn(Element.prototype, 'animate').mockImplementation(function (this: Element) {
      animated.push(this.tagName)
      const finished = this.tagName.toLowerCase() === 'path' ? scratchDone : Promise.resolve()
      return { finished, cancel() {} } as unknown as Animation
    })
    seed([note(1, { body: 'keep me' })])
    render(<NotesRail run={record} room={fakeRoom(refusingAfterApply())} />)
    fireEvent.click(screen.getByRole('button', { name: 'Complete note' }))
    await waitFor(() => expect(screen.getByText('conflict from server')).toBeDefined())
    await act(async () => {
      releaseScratch()
      await scratchDone
    })
    // No fade or collapse of the card itself after the error showed.
    expect(animated.filter((tag) => tag === 'LI')).toEqual([])
    expect(screen.getByText('keep me')).toBeDefined()
  })

  it('offers no Retry when another member already resolved the note, and Dismiss removes it', async () => {
    seed([note(1, { body: 'theirs now' })])
    const resolve = async (id: string) => {
      act(() => useStore.getState().setRoomMessageResolution(record.id, id, 'done', bob.id, '2026-08-14T11:00:00Z'))
      return { ok: false, error: 'room message already resolved with a different outcome' }
    }
    render(<NotesRail run={record} room={fakeRoom(resolve)} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete note' }))
    await waitFor(() => expect(screen.getByText('room message already resolved with a different outcome')).toBeDefined())
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('theirs now')).toBeNull()
  })

  it('announces nothing for a deletion the server refused', async () => {
    seed([note(1, { body: 'keep me' })])
    render(<NotesRail run={record} room={fakeRoom(refusingAfterApply())} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete note' }))
    await waitFor(() => expect(screen.getByText('conflict from server')).toBeDefined())
    expect(screen.getByRole('status').textContent).toBe('')
  })

  it('reads the announcement for the last note, from outside the list', async () => {
    seed([note(1, { body: 'only one' })])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('ok'))} />)
    fireEvent.click(screen.getByRole('button', { name: 'Complete note' }))
    await waitFor(() => expect(screen.queryByRole('list', { name: 'Notes' })).toBeNull())
    expect(screen.getByRole('status').textContent).toBe('Note completed.')
  })

  it('stays still under reduced motion and still removes the note', async () => {
    vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) => ({ matches: query.includes('prefers-reduced-motion'), addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList,
    )
    const animate = vi.spyOn(Element.prototype, 'animate')
    seed([note(1, { body: 'fade only' })])
    render(<NotesRail run={record} room={fakeRoom(applyingResolve('ok'))} />)
    fireEvent.click(screen.getByRole('button', { name: 'Complete note' }))
    await waitFor(() => expect(screen.queryByText('fade only')).toBeNull())
    expect(animate.mock.calls.length).toBe(1)
  })
})

describe('notes strip', () => {
  it('starts collapsed and expands to the same stickies on request', () => {
    seed([note(1, { body: 'strip note' })])
    render(<NotesStrip run={record} room={fakeRoom(applyingResolve('ok'))} />)
    expect(screen.queryByText('strip note')).toBeNull()
    const toggle = screen.getByRole('button', { name: /1 note/ })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(toggle)
    expect(within(screen.getByRole('list', { name: 'Notes' })).getByText('strip note')).toBeDefined()
  })

  it('reads the announcement for the last note after the strip empties', async () => {
    seed([note(1, { body: 'strip note' })])
    render(<NotesStrip run={record} room={fakeRoom(applyingResolve('ok'))} />)
    fireEvent.click(screen.getByRole('button', { name: /1 note/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Delete note' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: /1 note/ })).toBeNull())
    expect(screen.getByRole('status').textContent).toBe('Note deleted.')
  })
})
