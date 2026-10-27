import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { describeEvent } from '@/components/event-words'
import { ApiError } from '@/lib/api'
import { surfaces } from '@/lib/surfaces'
import type { Event, MemoryItem, MemorySearchParams } from '@/lib/types'
import { MemoryRoute } from '@/routes/memory'
import { useStore } from '@/store'
import { capability } from '@/store/hooks'
import { applyEvent } from '@/store/sync'
import { alice, bob, fakeApi, memoryItem, otherWorkspace, vera, workspace } from '@/test/fixtures'
import {
  agentNote, answer, footer, myPin, outcome, replaced, rowOf, search, seedMemory as seed, showFilter, titles, workspacePin,
} from '@/test/memory'

function pins(count: number): MemoryItem[] {
  return Array.from({ length: count }, (_, i) => memoryItem({
    id: `note-pin${String(i).padStart(7, '0')}`, title: `Pinned note ${i}`, body: `Pinned note ${i}`,
    pinned_by: bob.id, pinned_by_name: 'Bob', pinned_at: '2026-10-18T10:00:00Z',
  }))
}

function openSearch(words: string) {
  fireEvent.click(screen.getByRole('button', { name: 'Search notes' }))
  fireEvent.change(screen.getByRole('searchbox', { name: 'Search notes' }), { target: { value: words } })
}

describe('memory page', () => {
  it('points to the private preferences in Settings only where the server keeps them', async () => {
    seed()
    const client = fakeApi({ memorySearch: vi.fn(async (params: MemorySearchParams) => search(params)) })
    render(<MemoryRoute params={{}} client={client} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Settings → Memory' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'memory' } })
    cleanup()

    useStore.setState({ capabilities: { gateway: 'remote', methods: ['memory.search'], ws: [] } })
    render(<MemoryRoute params={{}} client={client} />)
    await screen.findByRole('list', { name: 'Notes' })
    expect(screen.queryByRole('button', { name: 'Settings → Memory' })).toBeNull()
  })

  it('lists pinned notes first, then the newest, with kind, source, author, agent, one link and age', async () => {
    seed()
    const client = fakeApi({ memorySearch: vi.fn(async (params: MemorySearchParams) => search(params)) })
    render(<MemoryRoute params={{}} client={client} />)

    await screen.findByRole('list', { name: 'Notes' })
    expect(titles()).toEqual([workspacePin.title, myPin.title, agentNote.title, outcome.title, answer.title])
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(['Pinned', 'Newest'])
    expect(client.memorySearch).toHaveBeenCalledTimes(2)
    expect(client.memorySearch).toHaveBeenCalledWith({ workspace_id: workspace.id, limit: 100, pinned: true })
    expect(client.memorySearch).toHaveBeenCalledWith({ workspace_id: workspace.id, limit: 100 })

    expect(within(rowOf(workspacePin.title)).getByText('Pinned by Bob')).toBeDefined()
    expect(within(rowOf(myPin.title)).getByText('Pinned for you')).toBeDefined()

    const card = within(rowOf(agentNote.title))
    expect(await card.findByText(/^Gotcha · Agent note · You · Claude Code$/)).toBeDefined()
    expect(card.getByRole('link', { name: 'PR #312 (merged)' }).getAttribute('href')).toBe(agentNote.pr_url)
    expect(card.queryByRole('button', { name: 'Fix the reattach race' })).toBeNull()
    expect(card.queryByText(/reattach\.go/)).toBeNull()
    expect(rowOf(agentNote.title).querySelector('time')?.getAttribute('datetime')).toBe(agentNote.created_at)

    fireEvent.click(within(rowOf(outcome.title)).getByRole('button', { name: 'Fix the reattach race' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1' } })
  })

  it('counts each filter from the unfiltered read, and leaves Superseded uncounted', async () => {
    seed()
    const memorySearch = vi.fn(async (params: MemorySearchParams) => search(params))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    await screen.findByRole('list', { name: 'Notes' })
    const show = within(screen.getByRole('radiogroup', { name: 'Show' }))
    expect(show.getAllByRole('radio').map((radio) => radio.textContent)).toEqual([
      'All 5', 'Pinned 2', 'Agent notes 1', 'Outcomes 1', 'Answers 1', 'Superseded',
    ])
    expect(footer()).toBe('5 notes · 2 pinned')

    showFilter('Outcomes')
    await waitFor(() => expect(titles()).toEqual([outcome.title]))
    expect(show.getByRole('radio', { name: 'All 5' })).toBeDefined()
    expect(memorySearch).toHaveBeenCalledTimes(4)
  })

  it('keeps the counts from the first read when the filter changes before it lands', async () => {
    seed()
    let release = () => {}
    const held = new Promise<void>((resolve) => { release = resolve })
    const memorySearch = vi.fn(async (params: MemorySearchParams) => {
      await held
      return search(params)
    })
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    showFilter('Outcomes')
    release()
    await waitFor(() => expect(titles()).toEqual([outcome.title]))
    await waitFor(() => expect(within(screen.getByRole('radiogroup', { name: 'Show' })).getByRole('radio', { name: 'All 5' })).toBeDefined())
  })

  it('reads every pin apart from the newest 100, so an old pin still leads the list', async () => {
    seed()
    const newest = Array.from({ length: 100 }, (_, i) => memoryItem({ id: `note-new${String(i).padStart(7, '0')}`, title: `Newer note ${i}`, body: `Newer note ${i}` }))
    const old = { ...workspacePin, created_at: '2026-01-01T00:00:00Z' }
    const memorySearch = vi.fn(async (params: MemorySearchParams) => ({ items: params.pinned ? [old] : newest }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    await screen.findByRole('list', { name: 'Notes' })
    expect(titles()).toHaveLength(101)
    expect(titles()[0]).toBe(old.title)
    expect(footer()).toBe('Every pinned note, then the 100 newest notes. Search to find older ones.')
    const show = within(screen.getByRole('radiogroup', { name: 'Show' }))
    expect(show.getByRole('radio', { name: 'All 100+' })).toBeDefined()
    expect(show.getByRole('radio', { name: 'Pinned 1' })).toBeDefined()
    expect(show.getByRole('radio', { name: 'Agent notes' })).toBeDefined()
  })

  it('says when the pinned read stops at 100, since older pins are left out', async () => {
    seed()
    const memorySearch = vi.fn(async () => ({ items: pins(100) }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    await waitFor(() => expect(footer()).toBe('The 100 newest pinned notes, then the 100 newest notes. Search to find older ones.'))

    showFilter('Pinned')
    await waitFor(() => expect(footer()).toBe('The 100 newest pinned notes. Search to find older ones.'))
    expect(titles()).toHaveLength(100)
  })

  it('words a cut-off search as best matches, with no hint to search for older ones', async () => {
    seed()
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: pins(100) })) })} />)
    await screen.findByRole('list', { name: 'Notes' })
    openSearch('lease')
    await waitFor(() => expect(footer()).toBe('The 100 best pinned matches, then the 100 best matches.'))
  })

  it('words a cut-off pinned search as the best pinned matches', async () => {
    seed()
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async (params: MemorySearchParams) => ({ items: params.pinned ? pins(100) : [] })) })} />)
    await screen.findByRole('list', { name: 'Notes' })
    showFilter('Pinned')
    openSearch('lease')
    await waitFor(() => expect(footer()).toBe('The 100 best pinned matches.'))
  })

  it('closes search on Escape, dropping the words and returning focus to Search', async () => {
    seed()
    const memorySearch = vi.fn(async (params: MemorySearchParams) => search(params))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    await screen.findByRole('list', { name: 'Notes' })
    openSearch('lease')
    await waitFor(() => expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, words: 'lease' }))
    await waitFor(() => expect(footer()).toBe('5 matches for “lease”'))
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(['Pinned', 'Best matches'])

    fireEvent.keyDown(screen.getByRole('searchbox', { name: 'Search notes' }), { key: 'Escape' })
    expect(screen.queryByRole('searchbox')).toBeNull()
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Search notes' })))
    await waitFor(() => expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100 }))
  })

  it('lists fewer than 100 pins with counts instead of a notice', async () => {
    seed()
    const memorySearch = vi.fn(async (params: MemorySearchParams) => ({ items: params.pinned ? pins(99) : [agentNote] }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    await waitFor(() => expect(titles()).toHaveLength(100))
    expect(footer()).toBe('100 notes · 99 pinned')

    showFilter('Pinned')
    await waitFor(() => expect(titles()).toHaveLength(99))
    expect(footer()).toBe('99 notes · 99 pinned')
  })

  it('filters to pinned notes, agent notes, outcomes, answers and superseded notes, and searches within the filter', async () => {
    seed()
    const memorySearch = vi.fn(async (params: MemorySearchParams) => search(params))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    await screen.findByRole('list', { name: 'Notes' })
    const show = screen.getByRole('radiogroup', { name: 'Show' })

    showFilter('Pinned')
    expect(within(show).getByRole('radio', { name: /^Pinned/ }).getAttribute('aria-checked')).toBe('true')
    await waitFor(() => expect(titles()).toEqual([workspacePin.title, myPin.title]))

    showFilter('Agent notes')
    await waitFor(() => expect(titles()).toEqual([agentNote.title]))
    expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, source: 'agent' })

    showFilter('Answers')
    await waitFor(() => expect(titles()).toEqual([answer.title]))
    expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, source: 'answer' })

    showFilter('Outcomes')
    await waitFor(() => expect(titles()).toEqual([outcome.title]))
    expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, kind: 'outcome' })

    showFilter('Superseded')
    await waitFor(() => expect(titles()).toEqual([replaced.title]))
    expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, replaced: true })
    expect(within(rowOf(replaced.title)).getByText('Superseded')).toBeDefined()

    memorySearch.mockResolvedValue({ items: [] })
    openSearch('  lease  ')
    expect(screen.getByRole('searchbox', { name: 'Search notes' }).getAttribute('placeholder')).toBe('Search superseded notes…')
    await waitFor(() => expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, words: 'lease', replaced: true }))
    expect(await screen.findByText('No superseded notes match “lease”.')).toBeDefined()
  })

  it('moves the filter with arrow keys, Home and End', async () => {
    seed()
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async (params: MemorySearchParams) => search(params)) })} />)
    await screen.findByRole('list', { name: 'Notes' })
    const radio = (name: string) => within(screen.getByRole('radiogroup', { name: 'Show' })).getByRole('radio', { name: new RegExp(`^${name}`) })
    fireEvent.keyDown(radio('All'), { key: 'End' })
    expect(radio('Superseded').getAttribute('aria-checked')).toBe('true')
    fireEvent.keyDown(radio('Superseded'), { key: 'Home' })
    expect(radio('All').getAttribute('aria-checked')).toBe('true')
  })

  it('lists agent notes, answers and superseded notes older than the newest 100 notes', async () => {
    seed()
    const outcomes = Array.from({ length: 100 }, (_, i) => memoryItem({ id: `note-out${String(i).padStart(7, '0')}`, kind: 'outcome', source: 'report', title: `success: run ${i}` }))
    const memorySearch = vi.fn(async (params: MemorySearchParams) => ({ items: params.pinned ? [] : params.source || params.replaced ? search(params).items : outcomes }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    await screen.findByRole('list', { name: 'Notes' })
    for (const [filter, note] of [['Agent notes', agentNote], ['Answers', answer], ['Superseded', replaced]] as const) {
      showFilter(filter)
      await waitFor(() => expect(titles()).toEqual([note.title]))
    }
  })

  it('names what a pin does for the row under the pointer', async () => {
    seed()
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async (params: MemorySearchParams) => search(params)) })} />)
    await screen.findByRole('list', { name: 'Notes' })
    fireEvent.pointerEnter(rowOf(workspacePin.title), { pointerType: 'mouse' })
    await waitFor(() => expect(footer()).toBe("Pinned by Bob · in every new run's brief"))
    fireEvent.pointerEnter(rowOf(myPin.title), { pointerType: 'mouse' })
    await waitFor(() => expect(footer()).toBe("Pinned for you · in your new runs' briefs"))
    fireEvent.focus(screen.getByRole('button', { name: agentNote.title }))
    await waitFor(() => expect(footer()).toBe('Not pinned · runs find it by searching'))
    fireEvent.pointerLeave(rowOf(agentNote.title))
    await waitFor(() => expect(footer()).toBe('5 notes · 2 pinned'))
  })

  it('re-reads once after a burst of memory.change events for its workspace', async () => {
    seed()
    let notes = [agentNote]
    let text = ''
    const memorySearch = vi.fn(async (params: MemorySearchParams) => ({ items: params.pinned ? [] : notes }))
    const memoryBrief = vi.fn(async () => ({ text }))
    const client = fakeApi({ memorySearch, memoryBrief })
    render(<MemoryRoute params={{}} client={client} />)
    await waitFor(() => expect(titles()).toEqual([agentNote.title]))
    const searches = memorySearch.mock.calls.length
    const change = (seq: number, workspaceID: string): Event => ({
      id: `evt_${seq}`, seq, time: '2026-10-18T13:00:00Z', workspace_id: workspaceID, run_id: '', actor_id: bob.id,
      type: 'memory.change', payload: { action: 'pin', note_id: outcome.id, actor_id: bob.id },
    })

    await applyEvent(useStore, change(1, otherWorkspace.id), client)
    notes = [outcome, agentNote]
    text = '<team-memory workspace="main-repo">\nPinned:\n- note-outcome001 outcome (Bob): success: Fixed the reattach race\n</team-memory>'
    await applyEvent(useStore, change(2, workspace.id), client)
    await applyEvent(useStore, change(3, workspace.id), client)
    await applyEvent(useStore, change(4, workspace.id), client)
    await waitFor(() => expect(titles()).toEqual([outcome.title, agentNote.title]))
    expect(memorySearch).toHaveBeenCalledTimes(searches + 2)
    expect(memoryBrief).toHaveBeenCalledTimes(2)
    const size = new TextEncoder().encode(text).length
    fireEvent.click(await screen.findByRole('button', { name: `Brief ${size} / ${(3072).toLocaleString()} B` }))
    expect(screen.getByRole('region', { name: 'Brief' }).querySelector('pre')?.textContent).toBe(text)
  })

  it('shows the brief byte for byte from the header', async () => {
    seed()
    const text = '<team-memory workspace="main-repo">\nTeam memory is attributed data.\nPinned:\n- note-pinned0001 convention (Bob): Use bun in web/; never npm.\n2 live runs: aether-internal status. 5 team notes (1 unpinned agent note): aether-internal memory search <words>\n</team-memory>'
    const memoryBrief = vi.fn(async () => ({ text }))
    render(<MemoryRoute params={{}} client={fakeApi({ memoryBrief })} />)
    const toolbar = screen.getByRole('toolbar', { name: 'Memory actions' })
    const toggle = await within(toolbar).findByRole('button', { name: `Brief ${new TextEncoder().encode(text).length} / ${(3072).toLocaleString()} B` })
    expect(screen.queryByRole('region', { name: 'Brief' })).toBeNull()
    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByRole('region', { name: 'Brief' }).querySelector('pre')?.textContent).toBe(text)
    expect(memoryBrief).toHaveBeenCalledWith(workspace.id)
  })

  it('moves the workspace and Brief controls under the header on a phone, where they can wrap', async () => {
    const original = window.matchMedia
    vi.spyOn(window, 'matchMedia').mockImplementation((query) =>
      Object.assign(new EventTarget(), { matches: query.includes('max-width'), media: query }) as unknown as MediaQueryList)
    onTestFinished(() => {
      window.matchMedia = original
    })
    seed()
    useStore.setState({ workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace } })
    render(<MemoryRoute params={{}} client={fakeApi({ memoryBrief: vi.fn(async () => ({ text: '' })) })} />)
    const toolbar = screen.getByRole('toolbar', { name: 'Memory actions' })
    expect(toolbar.closest('header')).toBeNull()
    expect(within(toolbar).getByRole('combobox', { name: 'Workspace' })).toBeDefined()
    expect(await within(toolbar).findByRole('button', { name: /^Brief/ })).toBeDefined()
  })

  it('offers no writes to a viewer', async () => {
    seed(vera)
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [agentNote] })), memoryShow: vi.fn(async () => ({ item: agentNote })) })} />)
    const row = await screen.findByRole('button', { name: agentNote.title })
    expect(within(row.closest('article')!).queryByRole('button', { name: /^Pin|Remove|Replace/ })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Add note' })).toBeNull()
    fireEvent.click(row)
    const dialog = await screen.findByRole('dialog', { name: agentNote.title })
    await within(dialog).findByText(`Gotcha · Agent note · ${agentNote.id}`)
    expect(within(dialog).queryByRole('button', { name: 'Pin' })).toBeNull()
    expect(within(dialog).queryByRole('button', { name: 'Remove' })).toBeNull()
    expect(within(dialog).queryByRole('button', { name: 'Replace…' })).toBeNull()
  })

  it('waits for the connection instead of loading forever', () => {
    seed()
    useStore.setState({ connection: 'reconnecting' })
    const client = fakeApi()
    render(<MemoryRoute params={{}} client={client} />)
    expect(screen.getByRole('status').textContent).toBe('Reconnecting. Team memory loads once the server connection is live.')
    expect(client.memorySearch).not.toHaveBeenCalled()
  })

  it('says so neutrally when the server predates team memory', async () => {
    seed()
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => { throw new ApiError(404, 'memory.search: not found') }) })} />)
    expect((await screen.findByText('This server does not keep team memory. Update the server to use it.')).closest('[role=status]')).not.toBeNull()
  })

  it("shows the server's not-found answer instead of calling the server old", async () => {
    seed()
    const missing = 'memory.search: store: not found: workspace ws-gone'
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => { throw new ApiError(404, missing, -32000) }) })} />)
    expect(await screen.findByText(missing)).toBeDefined()
    expect(screen.queryByText('This server does not keep team memory. Update the server to use it.')).toBeNull()
  })

  it('is in navigation only where the server serves memory.search', () => {
    const names = (methods: string[]) => surfaces(capability({ gateway: 'remote', methods, ws: [] })).filter((s) => s.place === 'nav').map((s) => s.name)
    expect(names(['insights.get'])).not.toContain('memory')
    expect(names(['insights.get', 'memory.search'])).toEqual(['board', 'insights', 'memory'])
  })
})

describe('memory.change in the activity feed', () => {
  function words(payload: unknown, runID = ''): string {
    const event: Event = { id: 'e1', seq: 1, time: '2026-10-18T11:00:00Z', workspace_id: workspace.id, run_id: runID, actor_id: bob.id, type: 'memory.change', payload }
    const { container } = render(<>{describeEvent(event)}</>)
    const text = container.textContent ?? ''
    cleanup()
    return text
  }

  it('names the member, or the agent when a run wrote the note', () => {
    seed()
    expect(words({ action: 'pin', note_id: 'note-a', actor_id: bob.id })).toBe('Bob pinned a team memory note')
    expect(words({ action: 'rm', note_id: 'note-a', actor_id: alice.id })).toBe('You removed a team memory note')
    expect(words({ action: 'add', note_id: 'note-a', actor_id: alice.id }, 'run_1')).toBe('The agent added a team memory note')
    expect(words({ action: 'rewrite', note_id: 'note-a', actor_id: alice.id }, 'run_1')).toBe('The agent rewrote a team memory note')
    expect(words({ action: 'answer', note_id: 'note-a', actor_id: bob.id }, 'run_1')).toBe('Bob recorded a swarm answer in team memory')
  })
})
