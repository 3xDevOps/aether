import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { NoteMessage, NoteThread } from '@/routes/run/note-thread'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, bob, roomMessage, run, serverInfo, workspace } from '@/test/fixtures'
import type { RoomMessage } from '@/lib/types'

const record = toRecord(run())

const room = (over: Partial<RunRoom> = {}): RunRoom => ({
  post: vi.fn(), note: vi.fn(), decide: vi.fn(), loadOlder: vi.fn(), refresh: vi.fn(), error: undefined, errorFromComposer: false, clearError: vi.fn(), ...over,
})

function note(n: number, over: Partial<RoomMessage> = {}): RoomMessage {
  return roomMessage({
    id: `n${n}`, actor_id: n % 2 ? alice.id : bob.id, body: `note ${n}`,
    created_at: `2026-08-14T10:${String(n).padStart(2, '0')}:00Z`, ...over,
  })
}

function seed(messages: RoomMessage[], exhausted = true) {
  useStore.setState({
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    roomMessages: { [record.id]: messages },
    roomPagination: { [record.id]: { initialized: true, exhausted } },
    roomLoading: {},
    roomError: {},
    failedNotes: {},
  })
}

const reveal = vi.fn()
const thread = (props: Partial<RunRoom> = {}) => <NoteThread run={record} room={room(props)} nav={{ reveal } as never} />

afterEach(() => vi.restoreAllMocks())

describe('note thread', () => {
  it('shows the newest five notes and expands the earlier ones on request', () => {
    seed(Array.from({ length: 8 }, (_, i) => note(i + 1)))
    render(thread())
    expect(screen.getAllByRole('article')).toHaveLength(5)
    expect(screen.queryByText('note 3')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show 3 earlier notes' }))
    expect(screen.getAllByRole('article')).toHaveLength(8)
    expect(screen.queryByRole('button', { name: /earlier/ })).toBeNull()
  })

  it('offers Show older only once every loaded note shows and history remains', () => {
    seed(Array.from({ length: 6 }, (_, i) => note(i + 1)), false)
    const loadOlder = vi.fn()
    render(thread({ loadOlder }))
    expect(screen.queryByRole('button', { name: 'Show older' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show 1 earlier note' }))
    fireEvent.click(screen.getByRole('button', { name: 'Show older' }))
    expect(loadOlder).toHaveBeenCalledOnce()
  })

  it('keeps Show older when the loaded pages hold no notes, and expands what it brings', () => {
    seed([roomMessage({ id: 's1', kind: 'steer_request', actor_id: alice.id, body: 'go' })], false)
    const loadOlder = vi.fn()
    render(thread({ loadOlder }))
    fireEvent.click(screen.getByRole('button', { name: 'Show older' }))
    expect(loadOlder).toHaveBeenCalledOnce()
    act(() => useStore.getState().upsertRoomMessage(note(1, { created_at: '2026-08-14T09:00:00Z' })))
    expect(screen.getByText('note 1')).toBeDefined()
  })

  it('shows Loading while an older page is on its way', () => {
    seed([note(1)], false)
    useStore.setState({ roomLoading: { [record.id]: true } })
    render(thread())
    expect(screen.getByRole('button', { name: 'Loading…' })).toHaveProperty('disabled', true)
  })

  it('shows the history error with a Retry that reads it again', () => {
    seed([], true)
    useStore.setState({ roomError: { [record.id]: 'run.room.list: 500 boom' } })
    const refresh = vi.fn()
    render(thread({ refresh }))
    const alert = screen.getByRole('alert')
    expect(alert.textContent).toContain('run.room.list: 500 boom')
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(refresh).toHaveBeenCalledOnce()
  })

  it('retries the older page when Show older failed', async () => {
    seed([note(1)], false)
    const loadOlder = vi.fn(async () => {
      useStore.setState({ roomError: { [record.id]: 'run.room.list: 500 boom' } })
    })
    const refresh = vi.fn()
    render(thread({ loadOlder, refresh }))
    fireEvent.click(screen.getByRole('button', { name: 'Show older' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(loadOlder).toHaveBeenCalledTimes(2)
    expect(refresh).not.toHaveBeenCalled()
  })

  it('nests a reply under its question and offers Reply on a teammate question nobody answered', () => {
    const answered = note(1, { id: 'q1', kind: 'question', actor_id: bob.id, body: 'which branch?' })
    const reply = note(2, { id: 'r1', kind: 'reply', actor_id: alice.id, body: 'main', correlation_id: 'q1' })
    const open = note(3, { id: 'q2', kind: 'question', actor_id: bob.id, body: 'which port?' })
    const own = note(4, { id: 'q3', kind: 'question', actor_id: alice.id, body: 'anyone?' })
    seed([answered, reply, open, own])
    render(thread())
    const [first] = screen.getAllByRole('article')
    expect(within(first).getByText('which branch?')).toBeDefined()
    expect(within(first).getByText('main')).toBeDefined()
    expect(screen.getAllByRole('article')).toHaveLength(4)
    expect(screen.getAllByRole('button', { name: 'Reply' })).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Reply' }))
    expect(reveal).toHaveBeenCalledWith('request-question-q2')
  })

  it('names a removed member by the name the message carried, and a connector post by its source', () => {
    seed([note(1, { actor_id: 'mem_gone', actor_display_name: 'Gone Person', via: 'ChatGPT' })])
    render(thread())
    expect(screen.getByText('Gone Person')).toBeDefined()
    expect(screen.getByText(/via ChatGPT/)).toBeDefined()
  })

  it('shows a note being sent in place of its time, muted', () => {
    seed([note(1, { id: 'key-1', idempotency_key: 'key-1' })])
    render(thread())
    expect(screen.getByText('Sending…')).toBeDefined()
    expect(screen.getByText('note 1').className).toContain('text-muted')
  })

  it('shows a failed note with its error and dismisses it', () => {
    seed([])
    useStore.setState({ failedNotes: { [record.id]: [{ text: 'lost', key: 'k', error: '403 forbidden', unanswered: false }] } })
    render(thread())
    expect(screen.getByText('lost')).toBeDefined()
    expect(screen.getByText('Not sent')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('lost')).toBeNull()
  })
})

describe('note arrival', () => {
  it('animates only notes that arrive after the thread settled, once, and announces a teammate\'s', () => {
    const animate = vi.spyOn(Element.prototype, 'animate')
    seed(Array.from({ length: 7 }, (_, i) => note(i + 1)))
    render(thread())
    expect(animate).not.toHaveBeenCalled()

    act(() => useStore.getState().upsertRoomMessage(note(8, { id: 'n8', actor_id: bob.id })))
    expect(screen.getByText('note 8')).toBeDefined()
    expect(animate).toHaveBeenCalled()
    expect(screen.getByRole('status').textContent).toBe('Bob added a note: note 8')

    animate.mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'Show 3 earlier notes' }))
    expect(animate).not.toHaveBeenCalled()
  })

  it('announces a teammate\'s reply under a question', () => {
    seed([note(1, { id: 'q1', kind: 'question', actor_id: alice.id, body: 'which branch?' })])
    render(thread())
    act(() => useStore.getState().upsertRoomMessage(note(2, { id: 'r1', kind: 'reply', actor_id: bob.id, correlation_id: 'q1', body: 'main' })))
    expect(screen.getByText('main')).toBeDefined()
    expect(screen.getByRole('status').textContent).toBe('Bob added a note: main')
  })

  it('treats older history as old even when the first page held no notes', () => {
    const animate = vi.spyOn(Element.prototype, 'animate')
    seed([roomMessage({ id: 's1', kind: 'steer_request', actor_id: alice.id, body: 'go', created_at: '2026-08-14T12:00:00Z' })], false)
    render(thread())
    act(() => useStore.getState().upsertRoomMessage(note(1, { actor_id: bob.id })))
    expect(screen.getByText('note 1')).toBeDefined()
    expect(animate).not.toHaveBeenCalled()
    expect(screen.getByRole('status').textContent).toBe('')
  })

  it('does not replay the entrance when a sent note is saved under a new id', () => {
    seed([note(1)])
    render(thread())
    const provisional = note(9, { id: 'key-9', idempotency_key: 'key-9', actor_id: alice.id, created_at: '2026-08-14T11:00:00Z' })
    act(() => useStore.getState().upsertRoomMessage(provisional))
    const animate = vi.spyOn(Element.prototype, 'animate')
    act(() => useStore.getState().upsertRoomMessage({ ...provisional, id: 'srv-9' }))
    expect(animate).not.toHaveBeenCalled()
    expect(screen.getAllByText('note 9')).toHaveLength(1)
    expect(screen.getByRole('status').textContent).toBe('')
  })

  it('stays still under reduced motion', () => {
    vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) => ({ matches: query.includes('prefers-reduced-motion'), addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList,
    )
    const animate = vi.spyOn(Element.prototype, 'animate')
    seed([note(1)])
    render(thread())
    act(() => useStore.getState().upsertRoomMessage(note(2)))
    expect(animate).not.toHaveBeenCalled()
  })
})

describe('note message', () => {
  it('marks a question with a chip', () => {
    seed([])
    render(<NoteMessage authorID={alice.id} question at="2026-08-14T10:00:00Z" body="which port?" />)
    expect(screen.getByText('question')).toBeDefined()
    expect(screen.getByRole('img', { name: 'Alice' })).toBeDefined()
  })
})
