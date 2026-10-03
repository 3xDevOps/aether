import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Api } from '@/lib/api'
import { RunRoom } from '@/routes/terminal/run-room'
import type { ControlMetadata } from '@/routes/terminal/attach'
import { applyEvent } from '@/store/sync'
import { useStore } from '@/store'
import type { EvidencePatchResult, Event, RoomMessageListResult, RoomPostResult, RoomStatusResult, Run } from '@/lib/types'
import { alice, bob, evidencePacket, fakeApi, roomMessage, run, workspace } from '@/test/fixtures'
import { EvidenceDrawer } from '@/routes/terminal/evidence-drawer'
import { atViewport } from '@/test/viewport'


const control: ControlMetadata = {
  control_session_id: 'session-alice',
  control_generation: 7,
  has_control: true,
}

function status(over: Partial<RoomStatusResult> = {}): RoomStatusResult {
  return { ...roomStatus(), ...over }
}

function roomStatus(): RoomStatusResult {
  return {
    workspace_id: workspace.id,
    run_id: 'run_1',
    protected: false,
    watchers: [alice.id, bob.id],
    queued_steers: 0,
  }
}

function mount(over: Partial<Run> = {}, options: { status?: RoomStatusResult; control?: ControlMetadata; client?: Api } = {}) {
  const client = options.client ?? fakeApi()
  useStore.setState({
    members: { [alice.id]: alice, [bob.id]: bob },
    roomMessages: {},
    roomPagination: {},
    roomStatus: { run_1: options.status ?? status() },
    roomLoading: {},
    roomStatusControl: {},
    roomStatusError: {},
    roomError: {},
    roomActionError: {},
    evidencePackets: {},
    evidencePagination: {},
    evidenceNextBefore: {},
    evidenceLoading: {},
    evidenceError: {},
    selectedEvidence: {},
  })
  render(<RunRoom run={run(over)} client={client} selfID={alice.id} control={options.control} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
  return client
}

beforeEach(() => {
  useStore.setState({
    members: {},
    roomMessages: {},
    roomPagination: {},
    roomStatus: {},
    roomLoading: {},
    roomStatusControl: {},
    roomStatusError: {},
    roomError: {},
    roomActionError: {},
    evidencePackets: {},
    evidencePagination: {},
    evidenceNextBefore: {},
    evidenceLoading: {},
    evidenceError: {},
    selectedEvidence: {},
  })
})

describe('Run Room', () => {
  it.each(['ctrlKey', 'metaKey'] as const)('toggles with %s+Shift+M without terminal input and preserves draft and focus', (modifier) => {
    const terminalInput = vi.fn()
    render(
      <>
        <div className="xterm"><textarea aria-label="Terminal input" onKeyDown={terminalInput} /></div>
        <RunRoom run={run()} client={fakeApi()} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />
      </>,
    )
    const terminal = screen.getByRole('textbox', { name: 'Terminal input' })
    terminal.focus()
    const chord = { key: 'M', shiftKey: true, [modifier]: true }
    expect(fireEvent.keyDown(terminal, chord)).toBe(false)
    expect(terminalInput).not.toHaveBeenCalled()
    const composer = screen.getByRole('textbox', { name: 'Run Room message' })
    expect(document.activeElement).toBe(composer)
    fireEvent.change(composer, { target: { value: 'keep this draft' } })
    fireEvent.keyDown(composer, chord)
    expect(screen.queryByRole('complementary', { name: 'Run Room' })).toBeNull()
    expect(document.activeElement).toBe(terminal)
    fireEvent.keyDown(terminal, chord)
    expect(screen.getByRole('textbox', { name: 'Run Room message' })).toHaveProperty('value', 'keep this draft')
    fireEvent.click(screen.getByRole('button', { name: 'Close Run Room' }))
    expect(document.activeElement).toBe(terminal)
  })

  it('describes the keyboard-focused opener and restores focus after a mouse-opened room closes', async () => {
    render(<RunRoom run={run()} client={fakeApi()} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
    const opener = screen.getByRole('button', { name: 'Open Run Room' })
    fireEvent.keyDown(document.body, { key: 'Tab' })
    act(() => opener.focus())
    const tooltip = await screen.findByRole('tooltip')
    expect(tooltip.textContent).toContain('Toggle Run Room')
    expect(opener.getAttribute('aria-describedby')?.split(' ')).toContain(tooltip.id)
    fireEvent.click(opener)
    fireEvent.click(screen.getByRole('button', { name: 'Close Run Room' }))
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Open Run Room' }))
  })

  it('keeps phone keyboard focus inside the sheet and restores the opener without losing the draft', async () => {
    atViewport(390, { pointer: 'coarse' })
    render(
      <>
        <button>Outside Room</button>
        <RunRoom run={run()} client={fakeApi()} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />
      </>,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    const room = await screen.findByRole('dialog', { name: 'Run Room' })
    expect(screen.queryByRole('button', { name: 'Outside Room' })).toBeNull()
    for (let index = 0; index < 12; index++) {
      await userEvent.tab()
      expect(room.contains(document.activeElement)).toBe(true)
    }
    await userEvent.tab({ shift: true })
    expect(room.contains(document.activeElement)).toBe(true)
    const composer = screen.getByRole('textbox', { name: 'Run Room message' })
    fireEvent.change(composer, { target: { value: 'keep this phone draft' } })
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Run Room' })).toBeNull())
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Open Run Room' }))
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    const reopened = screen.getByRole('textbox', { name: 'Run Room message' })
    expect(reopened).toHaveProperty('value', 'keep this phone draft')
    fireEvent.keyDown(reopened, { key: 'M', ctrlKey: true, shiftKey: true })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Run Room' })).toBeNull())
  })

  it.each(['dialog', 'alertdialog', 'menu', 'listbox'])('leaves the room shortcut to an open %s', (role) => {
    render(
      <>
        <div role={role} data-state="open"><input aria-label="Overlay field" /></div>
        <RunRoom run={run()} client={fakeApi()} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />
      </>,
    )
    expect(fireEvent.keyDown(screen.getByLabelText('Overlay field'), { key: 'm', ctrlKey: true, shiftKey: true })).toBe(true)
    expect(screen.queryByRole('complementary', { name: 'Run Room' })).toBeNull()
    fireEvent.keyDown(document.body, { key: 'm', ctrlKey: true, shiftKey: true })
    expect(screen.queryByRole('complementary', { name: 'Run Room' })).toBeNull()
  })

  it('ignores composing and already claimed room shortcuts', () => {
    render(<RunRoom run={run()} client={fakeApi()} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
    fireEvent.keyDown(window, { key: 'm', ctrlKey: true, shiftKey: true, isComposing: true })
    const claimed = new KeyboardEvent('keydown', { key: 'm', ctrlKey: true, shiftKey: true, cancelable: true })
    claimed.preventDefault()
    fireEvent(window, claimed)
    expect(screen.queryByRole('complementary', { name: 'Run Room' })).toBeNull()
  })

  it('refreshes collapsed presence independently of history and keeps stale status on failure', async () => {
    atViewport(390, { pointer: 'coarse' })
    vi.useFakeTimers()
    try {
      const initial = status({ controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' } })
      const next = status({ watchers: [alice.id] })
      const statusCall = vi.fn().mockResolvedValueOnce(initial).mockRejectedValueOnce(new Error('presence service unavailable')).mockResolvedValue(next)
      const client = fakeApi({ runRoomStatus: statusCall })
      render(<RunRoom run={run()} client={client} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
      await act(async () => { await Promise.resolve() })
      expect(useStore.getState().roomStatus.run_1).toEqual(initial)
      expect(client.runRoomList).not.toHaveBeenCalled()
      await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
      expect(useStore.getState().roomStatus.run_1).toEqual(initial)
      expect(useStore.getState().roomStatusError.run_1).toBe('presence service unavailable')
      fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
      await act(async () => { await Promise.resolve() })
      expect(screen.getByRole('status').textContent).toContain('Presence is stale: presence service unavailable')
      expect(useStore.getState().roomError.run_1).toBeUndefined()
      await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
      expect(useStore.getState().roomStatus.run_1).toEqual(next)
      expect(useStore.getState().roomStatusError.run_1).toBeUndefined()
      expect(client.runRoomList).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('applies slow presence responses and exposes slow failures without starving on the refresh interval', async () => {
    atViewport(390, { pointer: 'coarse' })
    vi.useFakeTimers()
    try {
      useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob } })
      const response = Promise.withResolvers<RoomStatusResult>()
      const failure = Promise.withResolvers<RoomStatusResult>()
      const occupied = status({ controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' } })
      const client = fakeApi({
        runRoomStatus: vi.fn().mockReturnValueOnce(response.promise).mockReturnValueOnce(failure.promise),
      })
      render(<RunRoom run={run()} client={client} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
      await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
      await act(async () => { response.resolve(occupied); await response.promise })
      fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
      expect(screen.getByText(`Controller: ${bob.display_name}`)).toBeDefined()
      await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
      await act(async () => { failure.reject(new Error('slow presence failure')); await Promise.resolve() })
      expect(screen.getByRole('status').textContent).toContain('Presence is stale: slow presence failure')
      expect(screen.getByText(`Controller: ${bob.display_name}`)).toBeDefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it.each(['success', 'failure'] as const)('recovers after a hung presence request and ignores its late %s', async (outcome) => {
    atViewport(390, { pointer: 'coarse' })
    vi.useFakeTimers()
    try {
      const hung = Promise.withResolvers<RoomStatusResult>()
      const recovery = Promise.withResolvers<RoomStatusResult>()
      const occupied = status({ controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' } })
      const recovered = status({ watchers: [alice.id] })
      useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob }, roomStatus: { run_1: occupied } })
      const statusCall = vi.fn().mockReturnValueOnce(hung.promise).mockReturnValueOnce(recovery.promise)
      const client = fakeApi({ runRoomStatus: statusCall })
      const view = render(<RunRoom run={run()} client={client} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
      fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
      await act(async () => { await vi.advanceTimersByTimeAsync(14_999) })
      expect(statusCall).toHaveBeenCalledTimes(1)
      expect(useStore.getState().roomStatusError.run_1).toBeUndefined()
      await act(async () => { await vi.advanceTimersByTimeAsync(1) })
      expect(statusCall.mock.calls[0][1].aborted).toBe(true)
      expect(screen.getByRole('status').textContent).toContain('Presence is stale: Presence request timed out after 15 seconds')
      expect(screen.getByText(`Controller: ${bob.display_name}`)).toBeDefined()
      await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
      expect(statusCall).toHaveBeenCalledTimes(2)
      await act(async () => { recovery.resolve(recovered); await recovery.promise })
      expect(useStore.getState().roomStatus.run_1).toEqual(recovered)
      expect(useStore.getState().roomStatusError.run_1).toBeUndefined()
      expect(screen.queryByText(/Presence is stale/)).toBeNull()
      await act(async () => {
        if (outcome === 'success') hung.resolve(occupied)
        else hung.reject(new Error('late presence failure'))
        await Promise.resolve()
      })
      expect(useStore.getState().roomStatus.run_1).toEqual(recovered)
      expect(useStore.getState().roomStatusError.run_1).toBeUndefined()
      view.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('ignores a pending presence response when acknowledged control changes', async () => {
    const oldResponse = Promise.withResolvers<RoomStatusResult>()
    const currentResponse = Promise.withResolvers<RoomStatusResult>()
    const statusCall = vi.fn().mockReturnValueOnce(oldResponse.promise).mockReturnValueOnce(currentResponse.promise)
    const client = fakeApi({ runRoomStatus: statusCall })
    const props = { run: run(), client, selfID: alice.id, onTakeControl: vi.fn(), onReleaseControl: vi.fn() }
    const view = render(<RunRoom {...props} />)
    view.rerender(<RunRoom {...props} control={control} />)
    expect(statusCall.mock.calls[0][1].aborted).toBe(true)
    expect(statusCall.mock.calls[1][1].aborted).toBe(false)
    const current = status({ watchers: [alice.id] })
    await act(async () => { currentResponse.resolve(current); await currentResponse.promise })
    await act(async () => { oldResponse.resolve(status({ watchers: [bob.id] })); await oldResponse.promise })
    expect(useStore.getState().roomStatus.run_1).toEqual(current)
  })

  it('ignores presence responses from an old run and after unmount', async () => {
    const oldResponse = Promise.withResolvers<RoomStatusResult>()
    const currentResponse = Promise.withResolvers<RoomStatusResult>()
    const statusCall = vi.fn().mockImplementationOnce(() => oldResponse.promise).mockImplementationOnce(() => currentResponse.promise)
    const client = fakeApi({ runRoomStatus: statusCall })
    const view = render(<RunRoom run={run()} client={client} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
    view.rerender(<RunRoom run={run({ id: 'run_2' })} client={client} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
    expect(statusCall.mock.calls[0][1].aborted).toBe(true)
    await act(async () => { oldResponse.resolve(status()); await oldResponse.promise })
    expect(useStore.getState().roomStatus.run_1).toBeUndefined()
    view.unmount()
    expect(statusCall.mock.calls[1][1].aborted).toBe(true)
    await act(async () => { currentResponse.resolve(status({ run_id: 'run_2' })); await currentResponse.promise })
    expect(useStore.getState().roomStatus.run_2).toBeUndefined()
  })

  it('keeps presence and protection in the phone sheet without duplicating desktop metadata', async () => {
    const resize = atViewport(1440, { pointer: 'coarse' })
    const occupied = status({ protected: true, controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' } })
    const client = fakeApi({ runRoomStatus: vi.fn(async () => occupied) })
    mount({}, { client })
    await waitFor(() => expect(useStore.getState().roomStatus.run_1).toEqual(occupied))
    expect(screen.queryByText(/Controller:/)).toBeNull()
    expect(screen.queryByText(/Viewing:/)).toBeNull()
    expect(screen.queryByText('Protected', { exact: true })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Take control' })).toBeNull()
    expect(screen.queryByRole('button', { name: /Evidence/ })).toBeNull()
    resize(390)
    expect(screen.getByText(`Controller: ${bob.display_name}`)).toBeDefined()
    expect(screen.getByText(`Viewing: ${alice.display_name}, ${bob.display_name}`)).toBeDefined()
    expect(screen.getByText('Protected', { exact: true })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Take control' })).toHaveProperty('disabled', false)
    resize(1440)
    expect(screen.queryByText(/Controller:/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Take control' })).toBeNull()
  })

  it('keeps room control available for a needs-attention run', () => {
    atViewport(390, { pointer: 'coarse' })
    const take = vi.fn()
    useStore.setState({
      members: { [alice.id]: alice, [bob.id]: bob },
      roomStatus: { run_1: status() },
      roomMessages: {},
    })
    render(<RunRoom run={run({ status: 'needs-attention' })} client={fakeApi()} selfID={alice.id} onTakeControl={take} onReleaseControl={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    const button = screen.getByRole('button', { name: 'Take control' })
    expect(button).toHaveProperty('disabled', false)
  })
  it('disables taking control for a completed run', () => {
    atViewport(390, { pointer: 'coarse' })
    const take = vi.fn()
    const client = fakeApi()
    useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob }, roomStatus: { run_1: status() }, roomMessages: {} })
    render(<RunRoom run={run({ status: 'completed' })} client={client} selfID={alice.id} onTakeControl={take} onReleaseControl={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    const button = screen.getByRole('button', { name: 'Take control' })
    expect(button).toHaveProperty('disabled', true)
    fireEvent.click(button)
    expect(take).not.toHaveBeenCalled()
  })
  it('disables stale release control state for a completed run', () => {
    atViewport(390, { pointer: 'coarse' })
    const release = vi.fn()
    const occupied = status({ controller: { member_id: alice.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' } })
    const client = fakeApi({ runRoomStatus: vi.fn(async () => occupied) })
    useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob }, roomStatus: { run_1: occupied }, roomMessages: {} })
    render(<RunRoom run={run({ status: 'completed' })} client={client} selfID={alice.id} control={control} onTakeControl={vi.fn()} onReleaseControl={release} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    const button = screen.getByRole('button', { name: 'Release control' })
    expect(button).toHaveProperty('disabled', true)
    fireEvent.click(button)
    expect(release).not.toHaveBeenCalled()
  })

  it('loads an empty room once and settles after the first response', async () => {
    const client = mount()
    await waitFor(() => expect(client.runRoomList).toHaveBeenCalledTimes(1))
    expect(client.runRoomStatus).toHaveBeenCalledTimes(1)
    expect(screen.getByText(/No messages yet/)).toBeDefined()
  })

  it('uses the immutable actor snapshot after a member is removed', () => {
    const message = roomMessage({
      actor_id: 'member-removed',
      actor_display_name: 'Former participant',
      body: 'historical note',
    })
    useStore.setState({
      members: { [alice.id]: alice, [bob.id]: bob },
      roomMessages: { run_1: [message] },
      roomStatus: { run_1: status() },
    })
    render(
      <RunRoom
        run={run()}
        client={fakeApi()}
        selfID={alice.id}
        onTakeControl={vi.fn()}
        onReleaseControl={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    expect(screen.getByText('Former participant')).toBeDefined()
  })
  it('shows the exact initial-load error and retries without losing a draft', async () => {
    const list = vi.fn()
      .mockRejectedValueOnce(new Error('room unavailable'))
      .mockResolvedValue({ messages: [] })
    const client = fakeApi({ runRoomList: list })
    mount({}, { client })

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('room unavailable')
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'keep this draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Retry room' }))

    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
    expect((screen.getByRole('textbox', { name: 'Run Room message' }) as HTMLTextAreaElement).value).toBe('keep this draft')
  })
  it('reconciles a room event that arrives during the first page load', async () => {
    const initial = Promise.withResolvers<RoomMessageListResult>()
    const incoming = roomMessage({ id: 'event-message', body: 'arrived during load' })
    const list = vi.fn()
      .mockImplementationOnce(() => initial.promise)
      .mockResolvedValue({ messages: [incoming] })
    const client = fakeApi({ runRoomList: list })
    useStore.setState({
      workspaces: { [workspace.id]: workspace },
      members: { [alice.id]: alice },
      roomMessages: {},
    })
    mount({}, { client })
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1))
    const event: Event = {
      id: 'event-1',
      seq: 1,
      time: '2026-08-14T10:00:01Z',
      workspace_id: workspace.id,
      run_id: 'run_1',
      actor_id: alice.id,
      type: 'workspace.room_message',
      payload: {},
    }
    const applied = applyEvent(useStore, event, client)
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    initial.resolve({ messages: [] })
    await applied
    expect(await screen.findByText('arrived during load')).toBeDefined()
  })

  it('loads older room history from the current cursor', async () => {
    const newest = roomMessage({ id: 'newest', body: 'newest' })
    const oldest = roomMessage({ id: 'oldest', body: 'oldest', created_at: '2026-08-14T09:00:00Z' })
    const list = vi.fn()
      .mockResolvedValueOnce({ messages: [newest], next_before: 'cursor-1' })
      .mockResolvedValueOnce({ messages: [oldest] })
    const client = fakeApi({ runRoomList: list })
    mount({}, { client })
    await waitFor(() => expect(client.runRoomList).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(client.runRoomList).toHaveBeenCalledTimes(2))
    expect(list.mock.calls[1][0]).toMatchObject({ before: 'cursor-1' })
    expect(screen.getByText('oldest')).toBeDefined()
  })

  it('walks every disconnected room page until cached history overlaps', async () => {
    vi.useFakeTimers()
    try {
      const message = (index: number) => roomMessage({
        id: `message-${String(index).padStart(3, '0')}`,
        body: `message ${index}`,
        created_at: new Date(Date.UTC(2026, 7, 14, 10, 0, index)).toISOString(),
      })
      const cached = Array.from({ length: 100 }, (_, index) => message(index))
      const arrivals = Array.from({ length: 150 }, (_, index) => message(index + 100))
      const list = vi.fn()
        .mockResolvedValueOnce({ messages: cached, next_before: 'cached-cursor' })
        .mockResolvedValueOnce({ messages: arrivals.slice(50), next_before: 'burst-cursor-1' })
        .mockResolvedValueOnce({ messages: arrivals.slice(0, 100), next_before: 'burst-cursor-2' })
        .mockResolvedValueOnce({ messages: cached })
      const client = fakeApi({ runRoomList: list })
      mount({}, { client })
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0)
      })
      expect(list).toHaveBeenCalledTimes(1)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000)
      })
      expect(list).toHaveBeenCalledTimes(4)

      expect(list.mock.calls.slice(1).map(([params]) => params.before)).toEqual([
        undefined,
        'burst-cursor-1',
        'burst-cursor-2',
      ])
      expect(useStore.getState().roomMessages.run_1.map((item) => item.id)).toEqual(
        cached.concat(arrivals).map((item) => item.id),
      )
    } finally {
      vi.useRealTimers()
    }
  })

  it('refreshes controller status without resetting the open room on control loss', async () => {
    atViewport(390, { pointer: 'coarse' })
    vi.useFakeTimers()
    try {
      const initial = status({ controller: { member_id: alice.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' } })
      const takeover = status({ controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:01:00Z' } })
      const statusCall = vi.fn()
        .mockResolvedValueOnce(initial)
        .mockResolvedValue(takeover)
      const client = fakeApi({ runRoomStatus: statusCall })
      useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob }, roomStatus: { run_1: initial }, roomMessages: {} })
      const view = render(
        <RunRoom
          run={run()}
          client={client}
          selfID={alice.id}
          control={control}
          onTakeControl={vi.fn()}
          onReleaseControl={vi.fn()}
        />,
      )
      fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
      await act(async () => { await Promise.resolve() })
      expect(statusCall).toHaveBeenCalledTimes(1)

      const composer = screen.getByRole('textbox', { name: 'Run Room message' }) as HTMLTextAreaElement
      fireEvent.change(composer, { target: { value: 'keep this draft' } })
      view.rerender(
        <RunRoom
          run={run()}
          client={client}
          selfID={alice.id}
          control={{ ...control, has_control: false }}
          onTakeControl={vi.fn()}
          onReleaseControl={vi.fn()}
        />,
      )
      await act(async () => { await Promise.resolve() })
      expect(statusCall).toHaveBeenCalledTimes(2)
      expect(screen.getByText('Controller: Bob')).toBeDefined()
      expect((screen.getByRole('textbox', { name: 'Run Room message' }) as HTMLTextAreaElement).value).toBe('keep this draft')

      await act(async () => {
        vi.advanceTimersByTime(5000)
        await Promise.resolve()
      })
      expect(statusCall).toHaveBeenCalledTimes(3)
    } finally {
      vi.useRealTimers()
    }
  })
  it('reconciles the newest room page every ten seconds without clearing a draft', async () => {
    vi.useFakeTimers()
    try {
      const first = roomMessage({ id: 'first', body: 'first' })
      const later = roomMessage({ id: 'later', body: 'later', created_at: '2026-08-14T10:01:00Z' })
      const list = vi.fn()
        .mockResolvedValueOnce({ messages: [first] })
        .mockResolvedValue({ messages: [first, later] })
      const client = fakeApi({ runRoomList: list })
      mount({}, { client })
      await act(async () => {
        await Promise.resolve()
        await Promise.resolve()
        await Promise.resolve()
      })
      fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'keep draft' } })
      await act(async () => {
        vi.advanceTimersByTime(10_000)
        await Promise.resolve()
        await Promise.resolve()
      })
      expect(list).toHaveBeenCalledTimes(2)
      expect(screen.getByText('later')).toBeDefined()
      expect((screen.getByRole('textbox', { name: 'Run Room message' }) as HTMLTextAreaElement).value).toBe('keep draft')
    } finally {
      vi.useRealTimers()
    }
  })


  it('posts a comment and a steer request with different kinds and stable keys', async () => {
    const client = mount()
    const post = vi.mocked(client.runRoomPost)
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'A useful note' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][0]).toMatchObject({ kind: 'comment', body: 'A useful note' })
    expect(await screen.findByText('Posted')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'Please inspect the failing test' } })
    fireEvent.click(screen.getByRole('button', { name: 'Queue steer' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(post.mock.calls[1][0]).toMatchObject({ kind: 'steer_request', body: 'Please inspect the failing test' })
    expect(post.mock.calls[0][0].idempotency_key).not.toBe(post.mock.calls[1][0].idempotency_key)
  })

  it('preserves a draft edited while its post is pending', async () => {
    const response = Promise.withResolvers<RoomPostResult>()
    const post = vi.fn(() => response.promise)
    const client = fakeApi({ runRoomPost: post })
    mount({}, { client })
    const composer = screen.getByRole('textbox', { name: 'Run Room message' })
    fireEvent.change(composer, { target: { value: 'first draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))

    fireEvent.change(composer, { target: { value: 'newer draft' } })
    await act(async () => {
      response.resolve({ message: roomMessage({ id: 'posted', body: 'first draft' }) })
      await response.promise
    })
    await waitFor(() => expect((screen.getByRole('textbox', { name: 'Run Room message' }) as HTMLTextAreaElement).value).toBe('newer draft'))
  })

  it('keeps an exact post error visible when history reconciliation succeeds', async () => {
    const post = vi.fn().mockRejectedValue(new Error('server refused this action'))
    const client = mount({}, { client: fakeApi({ runRoomPost: post }) })
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), {
      target: { value: 'must remain visible' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    const actionAlert = await screen.findByRole('alert', { name: 'Run Room action error' })
    expect(actionAlert.textContent).toContain('server refused this action')

    await act(async () => {
      await applyEvent(useStore, {
        id: 'room-after-action-error',
        seq: 1,
        time: '2026-08-14T10:01:00Z',
        workspace_id: workspace.id,
        run_id: 'run_1',
        actor_id: alice.id,
        type: 'workspace.room_message',
        payload: {},
      }, client)
    })
    expect(screen.getByRole('alert', { name: 'Run Room action error' }).textContent).toContain(
      'server refused this action',
    )
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByRole('alert', { name: 'Run Room action error' })).toBeNull()
  })

  it.each([
    { member_id: alice.id, connected: false },
    { member_id: bob.id, connected: true },
  ])('keeps acknowledged local authority despite stale presence from $member_id (connected=$connected)', async (snapshot) => {
    atViewport(390, { pointer: 'coarse' })
    const queued = roomMessage({ id: 'steer_1', kind: 'steer_request', state: 'queued', body: 'Run the focused test' })
    const occupied = status({ controller: { ...snapshot, acquired_at: '2026-08-14T10:00:00Z' } })
    const client = fakeApi({
      runRoomStatus: vi.fn(async () => occupied),
      runRoomList: vi.fn(async () => ({ messages: [queued] })),
    })
    const release = vi.fn()
    useStore.setState({ roomMessages: { run_1: [queued] }, roomStatus: { run_1: occupied }, members: { [alice.id]: alice, [bob.id]: bob } })
    render(<RunRoom run={run()} client={client} selfID={alice.id} control={control} onTakeControl={vi.fn()} onReleaseControl={release} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    expect(screen.getByText(`Controller: ${alice.display_name}`)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Approve now' }))
    await waitFor(() => expect(client.runRoomDecide).toHaveBeenCalledWith(expect.objectContaining({
      message_id: queued.id, decision: 'approve', control_session_id: control.control_session_id, control_generation: control.control_generation,
    })))
    fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'Inspect the result' } })
    fireEvent.click(screen.getByRole('button', { name: 'Queue steer' }))
    await waitFor(() => expect(client.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'steer_request', control_session_id: control.control_session_id, control_generation: control.control_generation,
    })))
  })

  it.each([
    { label: 'without metadata', metadata: undefined, selfID: alice.id, runStatus: 'running' as const },
    { label: 'with a mirror acknowledgement', metadata: { ...control, has_control: false }, selfID: alice.id, runStatus: 'running' as const },
    { label: 'without local identity', metadata: control, selfID: null, runStatus: 'running' as const },
    { label: 'after the run completes', metadata: control, selfID: alice.id, runStatus: 'completed' as const },
  ])('does not grant moderation or controller steering from presence $label', async ({ metadata, selfID, runStatus }) => {
    const queued = roomMessage({ id: 'steer_1', kind: 'steer_request', state: 'queued', body: 'Run the focused test' })
    const occupied = status({ controller: { member_id: alice.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' } })
    const client = fakeApi({
      runRoomStatus: vi.fn(async () => occupied),
      runRoomList: vi.fn(async () => ({ messages: [queued] })),
    })
    useStore.setState({ roomMessages: { run_1: [queued] }, roomStatus: { run_1: occupied } })
    render(<RunRoom run={run({ status: runStatus })} client={client} selfID={selfID} control={metadata} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    expect(screen.queryByRole('button', { name: 'Approve now' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Deny' })).toBeNull()
    if (runStatus === 'running') expect(screen.queryByRole('button', { name: 'Release control' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'Inspect the result' } })
    fireEvent.click(screen.getByRole('button', { name: 'Queue steer' }))
    await waitFor(() => expect(client.runRoomPost).toHaveBeenCalledOnce())
    const request = vi.mocked(client.runRoomPost).mock.calls[0][0]
    expect(request.control_session_id).toBeUndefined()
    expect(request.control_generation).toBeUndefined()
  })

  it('shows the live countdown and lets the controller approve or deny', async () => {
    const queued = roomMessage({ id: 'steer_1', kind: 'steer_request', state: 'queued', body: 'Run the focused test', deliver_after: new Date(Date.now() + 45_000).toISOString() })
    useStore.setState({ roomMessages: { run_1: [queued] }, roomStatus: { run_1: status({ controller: { member_id: alice.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' }, queued_steers: 1 }) }, members: { [alice.id]: alice, [bob.id]: bob } })
    const client = fakeApi()
    render(<RunRoom run={run()} client={client} selfID={alice.id} control={control} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    expect(screen.getByText(/before delivery/)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Approve now' }))
    await waitFor(() => expect(client.runRoomDecide).toHaveBeenCalledWith(expect.objectContaining({ message_id: 'steer_1', decision: 'approve', control_session_id: 'session-alice', control_generation: 7 })))
  })
  it('explains why protected runs cannot send to the agent', () => {
    const protectedStatus = status({ protected: true })
    const client = fakeApi({ runRoomStatus: vi.fn(async () => protectedStatus) })
    mount({}, { status: protectedStatus, client })
    expect(screen.getByRole('button', { name: 'Send to agent' })).toHaveProperty('disabled', true)
    expect(screen.getByText('Protected runs do not accept steering requests.')).toBeDefined()
  })

  it('keeps a newly protected agent draft without allowing keyboard submission', async () => {
    const client = mount()
    await act(async () => { await Promise.resolve() })
    fireEvent.click(screen.getByRole('button', { name: 'Send to agent' }))
    const composer = screen.getByRole('textbox', { name: 'Run Room message' })
    fireEvent.change(composer, { target: { value: 'keep pending instruction' } })
    act(() => useStore.getState().setRoomStatus('run_1', status({ protected: true }), undefined))
    fireEvent.keyDown(composer, { key: 'Enter', ctrlKey: true })
    expect(client.runRoomPost).not.toHaveBeenCalled()
    expect(composer).toHaveProperty('value', 'keep pending instruction')
    fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
    fireEvent.keyDown(composer, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(client.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({ kind: 'comment', body: 'keep pending instruction' })))
  })

  it('uploads an image before posting and includes its server reference', async () => {
    const client = mount()
    const input = screen.getByLabelText('Attach image') as HTMLInputElement
    fireEvent.change(input, { target: { files: [new File(['image'], 'shot.png', { type: 'image/png' })] } })
    await waitFor(() => expect(client.uploadTerminalImage).toHaveBeenCalled())
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'See this' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(client.runRoomPost).toHaveBeenCalled())
    expect(client.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({ attachments: ['/home/alice/.aether/uploads/image.png'] }))
  })
  it('enforces eight attachments, disables add at the limit, and clears without losing the draft', async () => {
    const client = mount()
    const composer = screen.getByRole('textbox', { name: 'Run Room message' }) as HTMLTextAreaElement
    const input = screen.getByLabelText('Attach image') as HTMLInputElement
    fireEvent.change(composer, { target: { value: 'keep this text' } })

    for (let index = 0; index < 8; index += 1) {
      fireEvent.change(input, {
        target: { files: [new File([`image-${index}`], `image-${index}.png`, { type: 'image/png' })] },
      })
      await waitFor(() => expect(client.uploadTerminalImage).toHaveBeenCalledTimes(index + 1))
    }

    expect(input.disabled).toBe(true)
    expect(screen.getByText('Attachments: 8/8')).toBeDefined()
    expect(screen.getByText('Attachment limit reached')).toBeDefined()

    fireEvent.change(input, {
      target: { files: [new File(['overflow'], 'overflow.png', { type: 'image/png' })] },
    })
    expect(client.uploadTerminalImage).toHaveBeenCalledTimes(8)

    fireEvent.click(screen.getByRole('button', { name: 'Clear attachments' }))
    expect(input.disabled).toBe(false)
    expect(screen.getByText('Attachments: 0/8')).toBeDefined()
    expect(composer.value).toBe('keep this text')

    fireEvent.change(input, {
      target: { files: [new File(['recovered'], 'recovered.png', { type: 'image/png' })] },
    })
    await waitFor(() => expect(client.uploadTerminalImage).toHaveBeenCalledTimes(9))
  })
  it('uses a new idempotency key when a retry adds an attachment', async () => {
    const post = vi.fn()
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValue({ message: roomMessage({ id: 'posted', attachments: ['/new.png'] }) })
    const client = fakeApi({ runRoomPost: post })
    mount({}, { client })
    const composer = screen.getByRole('textbox', { name: 'Run Room message' })
    fireEvent.change(composer, { target: { value: 'same note' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect((await screen.findByRole('alert')).textContent).toContain('offline')
    expect(screen.queryByRole('button', { name: 'Retry room' })).toBeNull()


    fireEvent.change(screen.getByLabelText('Attach image'), {
      target: { files: [new File(['image'], 'new.png', { type: 'image/png' })] },
    })
    await waitFor(() => expect(client.uploadTerminalImage).toHaveBeenCalled())
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(post.mock.calls[1][0]).toMatchObject({ body: 'same note', attachments: ['/home/alice/.aether/uploads/image.png'] })
    expect(post.mock.calls[1][0].idempotency_key).not.toBe(post.mock.calls[0][0].idempotency_key)
  })

  it('does not render a stale patch after selecting another packet', async () => {
    const packetA = evidencePacket({ id: 'packet_a', objective: 'A' })
    const packetB = evidencePacket({ id: 'packet_b', objective: 'B' })
    const first = Promise.withResolvers<EvidencePatchResult>()
    const second = Promise.withResolvers<EvidencePatchResult>()
    const client = fakeApi({
      runEvidenceList: vi.fn(async () => ({ packets: [packetA, packetB] })),
      runEvidenceGet: vi.fn(async ({ packet_id }) => ({ packet: packet_id === packetA.id ? packetA : packetB })),
      runEvidencePatch: vi.fn()
        .mockImplementationOnce(() => first.promise)
        .mockImplementationOnce(() => second.promise),
    })
    render(<EvidenceDrawer runID="run_1" workspaceID={workspace.id} client={client} onAnswer={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: /Evidence/ }))
    const packets = await screen.findAllByRole('button', { name: /report capture/ })
    fireEvent.click(packets[0])
    fireEvent.click(await screen.findByRole('tab', { name: 'Patch' }))
    await waitFor(() => expect(client.runEvidencePatch).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: 'Back to packets' }))
    fireEvent.click((await screen.findAllByRole('button', { name: /report capture/ }))[1])
    fireEvent.click(await screen.findByRole('tab', { name: 'Patch' }))
    await waitFor(() => expect(client.runEvidencePatch).toHaveBeenCalledTimes(2))
    await act(async () => {
      first.resolve({ packet: packetA, patch: 'patch A', truncated: false })
      await first.promise
    })
    expect(screen.queryByText('patch A')).toBeNull()
    await act(async () => {
      second.resolve({ packet: packetB, patch: 'patch B', truncated: false })
      await second.promise
    })
    expect(await screen.findByText('patch B')).toBeDefined()
  })

  it('keeps the selected evidence view when another packet refreshes the list', async () => {
    const packet = evidencePacket({ id: 'packet_selected' })
    const newer = evidencePacket({ id: 'packet_newer', captured_at: '2026-08-14T10:01:00Z' })
    const list = vi.fn()
      .mockResolvedValueOnce({ packets: [packet] })
      .mockResolvedValueOnce({ packets: [packet, newer] })
    const client = fakeApi({
      runEvidenceList: list,
      runEvidenceGet: vi.fn(async () => ({ packet })),
      runEvidencePatch: vi.fn(async () => ({ packet, patch: 'stable patch', truncated: false })),
    })
    render(<EvidenceDrawer runID="run_1" workspaceID={workspace.id} client={client} onAnswer={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: /Evidence/ }))
    fireEvent.click(await screen.findByRole('button', { name: /report capture/ }))
    fireEvent.click(await screen.findByRole('tab', { name: 'Patch' }))
    expect(await screen.findByText('stable patch')).toBeDefined()

    const event: Event = {
      id: 'evidence-refresh',
      seq: 1,
      time: '2026-08-14T10:01:00Z',
      workspace_id: workspace.id,
      run_id: 'run_1',
      actor_id: alice.id,
      type: 'workspace.evidence_packet',
      payload: {},
    }
    await act(async () => {
      await applyEvent(useStore, event, client)
    })

    expect(screen.getByRole('tab', { name: 'Patch' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByText('stable patch')).toBeDefined()
  })

  it('offers an explicit retry after the evidence list fails', async () => {
    const packet = evidencePacket()
    const list = vi.fn()
      .mockRejectedValueOnce(new Error('evidence unavailable'))
      .mockResolvedValue({ packets: [packet] })
    const client = fakeApi({ runEvidenceList: list })
    render(<EvidenceDrawer runID="run_1" workspaceID={workspace.id} client={client} onAnswer={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: /Evidence/ }))
    await screen.findByRole('alert')
    fireEvent.click(screen.getByRole('button', { name: 'Retry evidence' }))
    expect(await screen.findByRole('button', { name: /report capture/ })).toBeDefined()
  })


  it('answers an open question with a correlated reply', async () => {
    const question = roomMessage({ id: 'question_1', kind: 'question', body: 'Which API should we use?' })
    useStore.setState({ roomMessages: { run_1: [question] }, roomStatus: { run_1: status() }, members: { [alice.id]: alice, [bob.id]: bob } })
    const client = fakeApi()
    render(<RunRoom run={run()} client={client} selfID={alice.id} onTakeControl={vi.fn()} onReleaseControl={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'Use the existing API client.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(client.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({ kind: 'reply', correlation_id: 'question_1' })))
  })
  it('consumes each evidence request once, reopening and focusing a contextual comment without submitting', async () => {
    const question = roomMessage({ id: 'question_1', kind: 'question', body: 'Which API?' })
    const client = fakeApi({ runRoomList: vi.fn(async () => ({ messages: [question] })) })
    const props = { run: run(), client, selfID: alice.id, onTakeControl: vi.fn(), onReleaseControl: vi.fn() }
    const view = render(<RunRoom {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Answer' }))
    fireEvent.change(screen.getByLabelText('Attach image'), { target: { files: [new File(['image'], 'shot.png', { type: 'image/png' })] } })
    await screen.findByText('Attachments: 1/8')
    fireEvent.click(screen.getByRole('button', { name: 'Close Run Room' }))
    const evidenceAnswer = { fact: 'Which branch should be released?' }
    view.rerender(<RunRoom {...props} evidenceAnswer={evidenceAnswer} />)
    const composer = screen.getByRole('textbox', { name: 'Run Room message' })
    expect(composer).toHaveProperty('value', evidenceAnswer.fact)
    expect(document.activeElement).toBe(composer)
    expect(screen.queryByText('Replying to question')).toBeNull()
    expect(screen.getByText('Attachments: 1/8')).toBeDefined()
    expect(client.runRoomPost).not.toHaveBeenCalled()
    fireEvent.change(composer, { target: { value: 'edited answer' } })
    view.rerender(<RunRoom {...props} evidenceAnswer={evidenceAnswer} />)
    expect(composer).toHaveProperty('value', 'edited answer')
    view.rerender(<RunRoom {...props} evidenceAnswer={{ fact: evidenceAnswer.fact }} />)
    expect(composer).toHaveProperty('value', evidenceAnswer.fact)
    expect(document.activeElement).toBe(composer)
    expect(client.runRoomPost).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(client.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'comment', body: evidenceAnswer.fact, correlation_id: undefined,
      attachments: ['/home/alice/.aether/uploads/image.png'],
    })))
  })

  it('preserves an evidence answer and attachments when an older post completes', async () => {
    const response = Promise.withResolvers<RoomPostResult>()
    const client = fakeApi({ runRoomPost: vi.fn(() => response.promise) })
    const props = { run: run(), client, selfID: alice.id, onTakeControl: vi.fn(), onReleaseControl: vi.fn() }
    const view = render(<RunRoom {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open Run Room' }))
    fireEvent.change(screen.getByLabelText('Attach image'), { target: { files: [new File(['image'], 'shot.png', { type: 'image/png' })] } })
    await screen.findByText('Attachments: 1/8')
    fireEvent.change(screen.getByRole('textbox', { name: 'Run Room message' }), { target: { value: 'original comment' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    view.rerender(<RunRoom {...props} evidenceAnswer={{ fact: 'new evidence question' }} />)
    await act(async () => {
      response.resolve({ message: roomMessage({ body: 'original comment' }) })
      await response.promise
    })
    expect(screen.getByRole('textbox', { name: 'Run Room message' })).toHaveProperty('value', 'new evidence question')
    expect(screen.getByText('Attachments: 1/8')).toBeDefined()
    expect(client.runRoomPost).toHaveBeenCalledTimes(1)
  })


  it('inspects retained patch and transcript evidence', async () => {
    const packet = evidencePacket()
    const client = fakeApi({
      runEvidenceList: vi.fn(async () => ({ packets: [packet] })),
      runEvidenceGet: vi.fn(async () => ({ packet })),
      runEvidencePatch: vi.fn(async () => ({ packet, patch: 'diff --git a/src/app.ts', truncated: false })),
      runEvidenceTranscript: vi.fn(async () => ({ packet, data_base64: btoa('agent output'), truncated: false })),
    })
    render(<EvidenceDrawer runID="run_1" workspaceID={workspace.id} client={client} onAnswer={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: /Evidence/ }))
    fireEvent.click(await screen.findByRole('button', { name: /report capture/ }))
    fireEvent.click(await screen.findByRole('tab', { name: 'Patch' }))
    expect(await screen.findByText('diff --git a/src/app.ts')).toBeDefined()
    fireEvent.click(screen.getByRole('tab', { name: 'Transcript' }))
    expect(await screen.findByText('agent output')).toBeDefined()
  })

})
