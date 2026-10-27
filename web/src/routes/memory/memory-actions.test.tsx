import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { StrictMode } from 'react'
import { toast } from 'sonner'
import { api, ApiError, type Api } from '@/lib/api'
import type { MemoryAddResult, MemoryItem, MemorySearchParams } from '@/lib/types'
import { MemoryRoute } from '@/routes/memory'
import { undoMs } from '@/routes/memory/use-removal'
import { alice, bob, fakeApi, memoryItem, workspace } from '@/test/fixtures'
import { agentNote, footer, liveNotes, myPin, outcome, replaced, rowOf, search, seedMemory as seed, showFilter, titles, workspacePin } from '@/test/memory'

function composer(name = 'Add a note') {
  return within(screen.getByRole('region', { name }))
}

describe('memory page writes', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it("shows the server's refusal word for word and keeps the note to fix", async () => {
    seed()
    const refusal = 'memory.add: rejected, gitleaks rule github-pat at 0:22; remove the secret and retry'
    const answers: Record<string, unknown> = { 'memory.search': { items: liveNotes }, 'memory.brief': { text: '' }, 'agent.list': { agents: [] } }
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const method = input.split('/api/v1/')[1]
      if (method === 'memory.add') return { ok: false, status: 403, statusText: 'Forbidden', json: async () => ({ error: { code: -32001, message: refusal } }) }
      return { ok: true, status: 200, json: async () => answers[method] ?? {} }
    }))
    try {
      render(<MemoryRoute params={{}} client={api} />)
      fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
      const dock = composer()
      fireEvent.change(dock.getByLabelText('Note'), { target: { value: 'token ghp_example' } })
      fireEvent.click(dock.getByRole('button', { name: 'Add note' }))

      expect((await dock.findByRole('alert')).textContent).toBe(refusal)
      expect((dock.getByLabelText('Note') as HTMLTextAreaElement).value).toBe('token ghp_example')
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('adds a pinned note for the member only, and reports a refused pin as the server words it', async () => {
    seed()
    const budget = 'memory.add: note-new0000001 is stored but not pinned: memory.pin: your pinned member notes use 480 of 512 bytes; this note needs 92; unpin one first'
    const memoryAdd = vi.fn<Api['memoryAdd']>(async () => { throw new ApiError(409, budget, -32003) })
    const memorySearch = vi.fn(async () => ({ items: liveNotes }))
    const memoryBrief = vi.fn(async () => ({ text: '' }))
    render(<MemoryRoute params={{}} client={fakeApi({ memoryAdd, memorySearch, memoryBrief })} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
    const dock = composer()
    expect(dock.getByRole('radio', { name: 'Decision' }).getAttribute('aria-checked')).toBe('true')
    fireEvent.click(within(dock.getByRole('radiogroup', { name: 'Kind' })).getByRole('radio', { name: 'Gotcha' }))
    fireEvent.change(dock.getByLabelText('Note'), { target: { value: 'Run make lint before pushing.' } })
    fireEvent.click(dock.getByRole('checkbox', { name: 'Pin to my runs only' }))
    fireEvent.click(dock.getByRole('checkbox', { name: 'Pin' }))
    expect(dock.getByText('Your new runs get its title in the brief. Everyone in the workspace can still read it.')).toBeDefined()
    const reads = memorySearch.mock.calls.length
    fireEvent.click(dock.getByRole('button', { name: 'Add note' }))

    expect((await dock.findByRole('alert')).textContent).toBe(budget)
    expect(memoryAdd).toHaveBeenCalledWith({ workspace_id: workspace.id, kind: 'gotcha', text: 'Run make lint before pushing.', scope: 'member', pin: true })
    await waitFor(() => expect(memorySearch.mock.calls.length).toBe(reads + 2))
    expect(memoryBrief).toHaveBeenCalledTimes(2)
  })

  it('adds with the keyboard, says so in the footer and hands focus back to Add note', async () => {
    seed()
    const note = memoryItem({ id: 'note-new0000002', title: 'Ship on Fridays', body: 'Ship on Fridays' })
    let notes = [agentNote]
    const memoryAdd = vi.fn<Api['memoryAdd']>(async () => {
      notes = [note, agentNote]
      return { item: note }
    })
    render(<MemoryRoute params={{}} client={fakeApi({ memoryAdd, memorySearch: vi.fn(async () => ({ items: notes })) })} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
    const field = composer().getByLabelText('Note')
    expect(document.activeElement).toBe(field)
    fireEvent.change(field, { target: { value: 'Ship on Fridays' } })
    await waitFor(() => expect(footer()).toMatch(/adds · Esc cancels · 15 of 1024 bytes$/))
    fireEvent.keyDown(field, { key: 'Enter', ctrlKey: true })

    await waitFor(() => expect(footer()).toBe(`Added ${note.id}`))
    expect(screen.queryByRole('region', { name: 'Add a note' })).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add note' }))
    await waitFor(() => expect(titles()).toEqual([note.title, agentNote.title]))
  })

  it('finishes an add under StrictMode, as the development build mounts the composer', async () => {
    seed()
    const note = memoryItem({ id: 'note-new0000003', title: 'Ship on Fridays', body: 'Ship on Fridays' })
    const memoryAdd = vi.fn<Api['memoryAdd']>(async () => ({ item: note }))
    render(<StrictMode><MemoryRoute params={{}} client={fakeApi({ memoryAdd, memorySearch: vi.fn(async () => ({ items: [agentNote] })) })} /></StrictMode>)
    fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
    fireEvent.change(composer().getByLabelText('Note'), { target: { value: note.body } })
    fireEvent.click(composer().getByRole('button', { name: 'Add note' }))

    await waitFor(() => expect(footer()).toBe(`Added ${note.id}`))
    expect(screen.queryByRole('region', { name: 'Add a note' })).toBeNull()
  })

  it('says a duplicate is already kept, and Escape cancels the composer', async () => {
    seed()
    const memoryAdd = vi.fn<Api['memoryAdd']>(async () => ({ item: agentNote, duplicate: true }))
    render(<MemoryRoute params={{}} client={fakeApi({ memoryAdd, memorySearch: vi.fn(async () => ({ items: [agentNote] })) })} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
    fireEvent.change(composer().getByLabelText('Note'), { target: { value: agentNote.body } })
    fireEvent.click(composer().getByRole('button', { name: 'Add note' }))
    await waitFor(() => expect(footer()).toBe(`Already kept as ${agentNote.id}`))

    fireEvent.click(screen.getByRole('button', { name: 'Add note' }))
    fireEvent.keyDown(composer().getByLabelText('Note'), { key: 'Escape' })
    expect(screen.queryByRole('region', { name: 'Add a note' })).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add note' }))
  })

  it('opens a note with its provenance and supersede chain', async () => {
    seed()
    // Written in the same second as the note it replaces: only the server's order tells them apart.
    const replacement = memoryItem({ id: 'note-newer00001', title: 'Use bun in web/ and docs/.', created_at: agentNote.created_at })
    const shown = { ...agentNote, superseded_by: replacement.id, invalid_at: agentNote.created_at }
    const older = { ...replaced, superseded_by: shown.id }
    const memoryShow = vi.fn<Api['memoryShow']>(async (_, id) => (id === shown.id
      ? { item: shown, chain: [older, replacement] }
      : { item: replacement, chain: [older, shown] }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [agentNote] })), memoryShow })} />)
    fireEvent.click(await screen.findByRole('button', { name: agentNote.title }))

    const dialog = await screen.findByRole('dialog', { name: agentNote.title })
    expect(within(dialog).getByText(`Gotcha · Agent note · ${agentNote.id}`)).toBeDefined()
    const provenance = within(dialog.querySelector<HTMLElement>('dl[aria-label="Provenance"]')!)
    expect(await provenance.findByText('You · Claude Code')).toBeDefined()
    expect(provenance.getByText('run_1')).toBeDefined()
    expect(provenance.getByText('1a2b3c4d5e6f')).toBeDefined()
    expect(provenance.getByText('internal/scheduler/lease.go')).toBeDefined()
    expect(provenance.getByRole('link', { name: 'PR #312 (merged)' })).toBeDefined()

    const chain = within(within(dialog).getByRole('region', { name: 'Supersede chain' }))
    expect(chain.getAllByRole('listitem').map((li) => li.lastElementChild?.textContent)).toEqual([
      older.title, `${agentNote.title} (this note)`, replacement.title,
    ])
    fireEvent.click(chain.getByRole('button', { name: replacement.title }))
    expect(await screen.findByRole('dialog', { name: replacement.title })).toBeDefined()
    expect(memoryShow).toHaveBeenLastCalledWith(workspace.id, replacement.id)
  })

  it('pins and unpins a note from its detail', async () => {
    seed()
    const pinned = { ...agentNote, pinned_by: alice.id, pinned_by_name: 'Alice', pinned_at: '2026-10-18T14:00:00Z' }
    const memoryPin = vi.fn<Api['memoryPin']>(async () => ({ item: pinned }))
    const memoryUnpin = vi.fn<Api['memoryUnpin']>(async () => ({ item: agentNote }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [agentNote] })), memoryShow: vi.fn(async () => ({ item: agentNote })), memoryPin, memoryUnpin })} />)
    fireEvent.click(await screen.findByRole('button', { name: agentNote.title }))
    const dialog = await screen.findByRole('dialog', { name: agentNote.title })

    fireEvent.click(await within(dialog).findByRole('button', { name: 'Pin' }))
    expect(await within(dialog).findByText('Pinned by you')).toBeDefined()
    expect(memoryPin).toHaveBeenCalledWith(workspace.id, agentNote.id)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Unpin' }))
    expect(await within(dialog).findByRole('button', { name: 'Pin for me only' })).toBeDefined()
    expect(memoryUnpin).toHaveBeenCalledWith(workspace.id, agentNote.id)
  })

  it('pins a row in its own scope and moves it among the pinned notes', async () => {
    seed()
    const mine = { ...myPin, pinned_by: undefined, pinned_by_name: undefined, pinned_at: undefined }
    const pinned = { ...agentNote, pinned_by: alice.id, pinned_by_name: 'Alice', pinned_at: '2026-10-18T14:00:00Z' }
    let notes = [workspacePin, mine, agentNote]
    const memoryPin = vi.fn<Api['memoryPin']>(async () => {
      notes = [workspacePin, mine, pinned]
      return { item: pinned }
    })
    const memorySearch = vi.fn(async () => ({ items: notes }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch, memoryPin })} />)
    await screen.findByRole('list', { name: 'Notes' })
    expect(within(rowOf(mine.title)).getByRole('button', { name: 'Pin for your new runs' })).toBeDefined()
    expect(within(rowOf(workspacePin.title)).getByRole('button', { name: 'Unpin' })).toBeDefined()

    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Pin for every new run' }))
    await waitFor(() => expect(titles()).toEqual([workspacePin.title, agentNote.title, mine.title]))
    expect(memoryPin).toHaveBeenCalledWith(workspace.id, agentNote.id)
    expect(within(rowOf(agentNote.title)).getByText('Pinned by you')).toBeDefined()
  })

  it('re-reads the filter on screen when a row pin lands after the viewer moved on', async () => {
    seed()
    let finish: (result: { item: MemoryItem }) => void = () => {}
    const memoryPin = vi.fn<Api['memoryPin']>(() => new Promise((resolve) => { finish = resolve }))
    const memorySearch = vi.fn(async (params: MemorySearchParams) => search(params))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch, memoryPin })} />)
    await screen.findByRole('list', { name: 'Notes' })
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Pin for every new run' }))
    showFilter('Outcomes')
    await waitFor(() => expect(titles()).toEqual([outcome.title]))

    const reads = memorySearch.mock.calls.length
    await act(async () => finish({ item: { ...agentNote, pinned_by: alice.id, pinned_at: '2026-10-18T14:00:00Z' } }))
    await waitFor(() => expect(memorySearch.mock.calls.length).toBe(reads + 4))
    expect(memorySearch.mock.calls[reads][0]).toMatchObject({ kind: 'outcome' })
    await waitFor(() => expect(screen.getByRole('list', { name: 'Notes' }).parentElement!.className).not.toContain('opacity-50'))
    expect(titles()).toEqual([outcome.title])
  })

  it('moves focus to a neighbouring note when the focused row or its detail removes it', async () => {
    seed()
    const memoryRm = vi.fn<Api['memoryRm']>(async (_, id) => ({ ids: [id] }))
    const memorySearch = vi.fn(async () => ({ items: [workspacePin, agentNote, outcome] }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch, memoryShow: vi.fn(async () => ({ item: outcome })), memoryRm })} />)
    await screen.findByRole('list', { name: 'Notes' })
    const remove = within(rowOf(agentNote.title)).getByRole('button', { name: 'Remove' })
    remove.focus()
    fireEvent.click(remove)
    expect(document.activeElement).toBe(screen.getByRole('button', { name: outcome.title }))

    fireEvent.click(screen.getByRole('button', { name: outcome.title }))
    const dialog = await screen.findByRole('dialog', { name: outcome.title })
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: workspacePin.title })))
  })

  it('keeps a typed note when Replace is asked of another row', async () => {
    seed()
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [agentNote] })) })} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
    fireEvent.change(composer().getByLabelText('Note'), { target: { value: 'Half a thought' } })
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Replace' }))
    expect((composer().getByLabelText('Note') as HTMLTextAreaElement).value).toBe('Half a thought')
    expect(screen.queryByRole('region', { name: `Replace ${agentNote.id}` })).toBeNull()
  })

  it('keeps a replacement whose only edit is its files when Replace is asked of another row', async () => {
    seed()
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [workspacePin, agentNote] })) })} />)
    await screen.findByRole('list', { name: 'Notes' })
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Replace' }))
    fireEvent.change(composer(`Replace ${agentNote.id}`).getByLabelText('Files'), { target: { value: 'internal/scheduler/sweep.go' } })
    fireEvent.click(within(rowOf(workspacePin.title)).getByRole('button', { name: 'Replace' }))
    expect((composer(`Replace ${agentNote.id}`).getByLabelText('Files') as HTMLTextAreaElement).value).toBe('internal/scheduler/sweep.go')
    expect(screen.queryByRole('region', { name: `Replace ${workspacePin.id}` })).toBeNull()
  })

  it('shows a refused row pin in full as a toast', async () => {
    seed()
    const refusal = 'memory.pin: pinned workspace notes use 980 of 1024 bytes; this note needs 120; unpin one first'
    const error = vi.spyOn(toast, 'error')
    const memoryPin = vi.fn<Api['memoryPin']>(async () => { throw new ApiError(409, refusal, -32003) })
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [agentNote] })), memoryPin })} />)
    await screen.findByRole('list', { name: 'Notes' })
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Pin for every new run' }))
    await waitFor(() => expect(error).toHaveBeenCalledWith(refusal))
  })

  it('pins a member-scope note for the pinning member only, as aether memory pin does', async () => {
    seed()
    const mine = { ...myPin, pinned_by: undefined, pinned_by_name: undefined, pinned_at: undefined }
    const memoryPin = vi.fn<Api['memoryPin']>(async () => ({ item: myPin }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [mine] })), memoryShow: vi.fn(async () => ({ item: mine })), memoryPin })} />)
    fireEvent.click(await screen.findByRole('button', { name: mine.title }))
    const dialog = await screen.findByRole('dialog', { name: mine.title })
    const only = await within(dialog).findByRole('button', { name: 'Pin for me only' })
    expect(within(dialog).queryByRole('button', { name: 'Pin' })).toBeNull()
    fireEvent.click(only)
    expect(await within(dialog).findByText('Pinned for you')).toBeDefined()
    expect(memoryPin).toHaveBeenCalledWith(workspace.id, mine.id, 'member')
  })

  it("shows a refused pin in the detail as the server's text", async () => {
    seed()
    const refusal = 'memory.pin: pinned workspace notes use 980 of 1024 bytes; this note needs 120; unpin one first'
    const memoryPin = vi.fn<Api['memoryPin']>(async () => { throw new ApiError(409, refusal, -32003) })
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [agentNote] })), memoryShow: vi.fn(async () => ({ item: agentNote })), memoryPin })} />)
    fireEvent.click(await screen.findByRole('button', { name: agentNote.title }))
    const dialog = await screen.findByRole('dialog', { name: agentNote.title })
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Pin' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe(refusal)
  })

  it('removes a note only once the undo window closes, and Undo keeps it', async () => {
    seed()
    const memoryRm = vi.fn<Api['memoryRm']>(async () => ({ ids: [agentNote.id] }))
    const memorySearch = vi.fn(async () => ({ items: [workspacePin, agentNote] }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch, memoryShow: vi.fn(async () => ({ item: agentNote })), memoryRm })} />)
    await screen.findByRole('list', { name: 'Notes' })

    vi.useFakeTimers()
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Remove' }))
    expect(titles()).toEqual([workspacePin.title])
    expect(footer()).toBe(`Removed “${agentNote.title}”Undo`)
    act(() => vi.advanceTimersByTime(undoMs - 1))
    expect(memoryRm).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Undo' }))
    expect(titles()).toEqual([workspacePin.title, agentNote.title])
    act(() => vi.advanceTimersByTime(undoMs))
    expect(memoryRm).not.toHaveBeenCalled()

    vi.useRealTimers()
    fireEvent.click(screen.getByRole('button', { name: agentNote.title }))
    const dialog = await screen.findByRole('dialog', { name: agentNote.title })
    const remove = await within(dialog).findByRole('button', { name: 'Remove' })
    vi.useFakeTimers()
    fireEvent.click(remove)
    expect(screen.queryByRole('dialog')).toBeNull()
    const reads = memorySearch.mock.calls.length
    await act(async () => vi.advanceTimersByTime(undoMs))
    expect(memoryRm).toHaveBeenCalledWith(workspace.id, agentNote.id)
    vi.useRealTimers()
    await waitFor(() => expect(memorySearch.mock.calls.length).toBeGreaterThan(reads))
    expect(titles()).toEqual([workspacePin.title])
  })

  it('keeps a note whose text is added again while its removal waits', async () => {
    seed()
    const memoryRm = vi.fn<Api['memoryRm']>(async (_, id) => ({ ids: [id] }))
    const memoryAdd = vi.fn<Api['memoryAdd']>(async () => ({ item: agentNote, duplicate: true }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [workspacePin, agentNote] })), memoryAdd, memoryRm })} />)
    await screen.findByRole('list', { name: 'Notes' })

    vi.useFakeTimers()
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Remove' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add note' }))
    fireEvent.change(composer().getByLabelText('Note'), { target: { value: agentNote.body } })
    await act(async () => fireEvent.click(composer().getByRole('button', { name: 'Add note' })))

    expect(footer()).toBe(`Already kept as ${agentNote.id}`)
    expect(titles()).toEqual([workspacePin.title, agentNote.title])
    await act(async () => vi.advanceTimersByTime(undoMs))
    expect(memoryRm).not.toHaveBeenCalled()
  })

  it('sends a waiting removal at once on a second removal or on leaving the page', async () => {
    seed()
    const memoryRm = vi.fn<Api['memoryRm']>(async (_, id) => ({ ids: [id] }))
    const { unmount } = render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [workspacePin, agentNote, outcome] })), memoryRm })} />)
    await screen.findByRole('list', { name: 'Notes' })
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Remove' }))
    fireEvent.click(within(rowOf(outcome.title)).getByRole('button', { name: 'Remove' }))
    expect(memoryRm).toHaveBeenCalledTimes(1)
    expect(memoryRm).toHaveBeenLastCalledWith(workspace.id, agentNote.id)
    await waitFor(() => expect(footer()).toBe(`Removed “${outcome.title}”Undo`))
    unmount()
    expect(memoryRm).toHaveBeenCalledTimes(2)
    expect(memoryRm).toHaveBeenLastCalledWith(workspace.id, outcome.id)
  })

  it('brings back a note whose removal the server refused, with its words', async () => {
    seed()
    const refusal = 'memory.rm: note-agent00001 is not yours to remove'
    const error = vi.spyOn(toast, 'error')
    const memoryRm = vi.fn<Api['memoryRm']>(async () => { throw new ApiError(403, refusal, -32001) })
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [workspacePin, agentNote, outcome] })), memoryRm })} />)
    await screen.findByRole('list', { name: 'Notes' })
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Remove' }))
    fireEvent.click(within(rowOf(outcome.title)).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(error).toHaveBeenCalledWith(refusal))
    expect(titles()).toEqual([workspacePin.title, agentNote.title])
  })

  it('replaces a note from its detail with one pre-filled from it, then opens the replacement', async () => {
    seed()
    const note = { ...agentNote, scope: 'member' as const }
    const text = 'Reattach races the lease sweeper\nTake the lease, then read the container state, then sweep.'
    const replacement = memoryItem({ id: 'note-replace001', kind: 'gotcha', scope: 'member', title: 'Reattach races the lease sweeper', body: text })
    const memoryAdd = vi.fn<Api['memoryAdd']>(async () => ({ item: replacement }))
    const memoryShow = vi.fn<Api['memoryShow']>(async (_, id) => (id === note.id ? { item: note } : { item: replacement, chain: [{ ...note, superseded_by: replacement.id }] }))
    const memorySearch = vi.fn(async () => ({ items: [note] }))
    const memoryBrief = vi.fn(async () => ({ text: '' }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch, memoryShow, memoryAdd, memoryBrief })} />)
    fireEvent.click(await screen.findByRole('button', { name: note.title }))
    fireEvent.click(await within(await screen.findByRole('dialog', { name: note.title })).findByRole('button', { name: 'Replace…' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const dock = composer(`Replace ${note.id}`)
    expect(dock.getByRole('radio', { name: 'Gotcha' }).getAttribute('aria-checked')).toBe('true')
    expect((dock.getByLabelText('Note') as HTMLTextAreaElement).value).toBe(note.body)
    expect((dock.getByLabelText('Files') as HTMLTextAreaElement).value).toBe('internal/scheduler/reattach.go\ninternal/scheduler/lease.go')
    expect(dock.getByRole('checkbox', { name: 'Pin to my runs only' }).getAttribute('aria-checked')).toBe('true')

    fireEvent.change(dock.getByLabelText('Note'), { target: { value: text } })
    fireEvent.change(dock.getByLabelText('Files'), { target: { value: ' internal/scheduler/reattach.go \n\n internal/scheduler/sweep.go\n' } })
    const reads = memorySearch.mock.calls.length
    fireEvent.keyDown(dock.getByLabelText('Files'), { key: 'Enter', metaKey: true })

    expect(await screen.findByText(`Gotcha · Member note · ${replacement.id}`)).toBeDefined()
    expect(memoryAdd).toHaveBeenCalledWith({
      workspace_id: workspace.id, kind: 'gotcha', text, files: ['internal/scheduler/reattach.go', 'internal/scheduler/sweep.go'], supersedes: note.id, scope: 'member',
    })
    expect(screen.queryByRole('region', { name: `Replace ${note.id}` })).toBeNull()
    expect(memoryShow).toHaveBeenLastCalledWith(workspace.id, replacement.id)
    expect(footer()).toBe(`Replaced ${note.id} with ${replacement.id}`)
    await waitFor(() => expect(memorySearch.mock.calls.length).toBeGreaterThan(reads))
    expect(memoryBrief).toHaveBeenCalledTimes(2)
  })

  it('only re-reads when a cancelled replacement finishes saving', async () => {
    seed()
    const replacement = memoryItem({ id: 'note-replace002', title: 'Replaced text', body: 'Replaced text' })
    let finish: (result: MemoryAddResult) => void = () => {}
    const memoryAdd = vi.fn<Api['memoryAdd']>(() => new Promise((resolve) => { finish = resolve }))
    const memorySearch = vi.fn(async () => ({ items: [workspacePin, agentNote] }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch, memoryShow: vi.fn(async () => ({ item: workspacePin })), memoryAdd })} />)
    await screen.findByRole('list', { name: 'Notes' })
    fireEvent.click(within(rowOf(agentNote.title)).getByRole('button', { name: 'Replace' }))
    fireEvent.click(composer(`Replace ${agentNote.id}`).getByRole('button', { name: 'Replace note' }))
    await waitFor(() => expect(memoryAdd).toHaveBeenCalled())
    fireEvent.click(composer(`Replace ${agentNote.id}`).getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByRole('button', { name: workspacePin.title }))
    await screen.findByRole('dialog', { name: workspacePin.title })

    const reads = memorySearch.mock.calls.length
    await act(async () => finish({ item: replacement }))
    expect(screen.getByRole('dialog', { name: workspacePin.title })).toBeDefined()
    expect(screen.queryByRole('dialog', { name: replacement.title })).toBeNull()
    await waitFor(() => expect(memorySearch.mock.calls.length).toBeGreaterThan(reads))
  })

  it("replaces an outcome note as a decision, keeps a pinned note's pin and scope, and shows a refusal word for word", async () => {
    seed()
    const pinned = { ...outcome, scope: 'member' as const, pinned_by: alice.id, pinned_by_name: 'Alice', pinned_at: '2026-10-18T12:00:00Z' }
    const refusal = 'memory.add: file 1 is 600 bytes; a cited path holds at most 512'
    const memoryAdd = vi.fn<Api['memoryAdd']>(async () => { throw new ApiError(400, refusal, -32602) })
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [pinned] })), memoryAdd })} />)
    fireEvent.click(within(await screen.findByRole('article')).getByRole('button', { name: 'Replace' }))

    const dock = composer(`Replace ${pinned.id}`)
    expect(dock.getByRole('radio', { name: 'Decision' }).getAttribute('aria-checked')).toBe('true')
    expect(dock.queryByRole('checkbox', { name: 'Pin' })).toBeNull()
    const onlyMe = dock.getByRole('checkbox', { name: 'Pin to my runs only' }) as HTMLButtonElement
    expect(onlyMe.getAttribute('aria-checked')).toBe('true')
    expect(onlyMe.disabled).toBe(true)
    expect(dock.getByText("The pin moves to the replacement, at the note's scope.")).toBeDefined()
    fireEvent.click(dock.getByRole('button', { name: 'Files' }))
    const long = `web/${'x'.repeat(596)}`
    fireEvent.change(dock.getByLabelText('Files'), { target: { value: long } })
    fireEvent.click(dock.getByRole('button', { name: 'Replace note' }))

    expect((await dock.findByRole('alert')).textContent).toBe(refusal)
    expect(memoryAdd).toHaveBeenCalledWith({ workspace_id: workspace.id, kind: 'decision', text: pinned.body, files: [long], supersedes: pinned.id, scope: 'member' })
    expect((dock.getByLabelText('Files') as HTMLTextAreaElement).value).toBe(long)
  })

  it("offers a collaborator no action on another member's member note, and every action on a workspace note", async () => {
    seed(bob)
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [myPin, workspacePin] })), memoryShow: vi.fn(async () => ({ item: myPin })) })} />)
    const theirs = within(rowOf((await screen.findByRole('button', { name: myPin.title })).textContent!))
    expect(theirs.queryByRole('button', { name: /^(Pin|Unpin|Replace|Remove)/ })).toBeNull()
    const shared = within(rowOf(workspacePin.title))
    for (const name of ['Unpin', 'Replace', 'Remove']) expect(shared.getByRole('button', { name })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: myPin.title }))
    const dialog = await screen.findByRole('dialog', { name: myPin.title })
    expect(await within(dialog).findByText(myPin.body)).toBeDefined()
    expect(within(dialog).queryByRole('button', { name: /^(Pin|Unpin|Replace|Remove)/ })).toBeNull()
  })

  it('offers neither pin nor replace on a superseded note', async () => {
    seed()
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [replaced] })), memoryShow: vi.fn(async () => ({ item: replaced })) })} />)
    const row = within(rowOf((await screen.findByRole('button', { name: replaced.title })).textContent!))
    expect(row.queryByRole('button', { name: /^Pin|Replace/ })).toBeNull()
    expect(row.getByRole('button', { name: 'Remove' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: replaced.title }))
    const dialog = await screen.findByRole('dialog', { name: replaced.title })
    expect(await within(dialog).findByRole('button', { name: 'Remove' })).toBeDefined()
    expect(within(dialog).queryByRole('button', { name: 'Replace…' })).toBeNull()
  })
})
