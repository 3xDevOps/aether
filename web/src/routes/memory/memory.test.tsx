import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { describeEvent } from '@/components/event-words'
import { api, ApiError, type Api } from '@/lib/api'
import { surfaces } from '@/lib/surfaces'
import type { Event, MemoryItem, MemorySearchParams } from '@/lib/types'
import { MemoryRoute } from '@/routes/memory'
import { useStore } from '@/store'
import { capability } from '@/store/hooks'
import { alice, bob, fakeApi, memoryItem, run, serverInfo, vera, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

function seed(member = alice) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    info: { ...serverInfo, member },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    connection: 'live',
    hydrated: true,
  })
  useStore.getState().upsertRun(run({ id: 'run_1', title: 'Fix the reattach race' }))
}

const agentNote = memoryItem({
  id: 'note-agent00001',
  kind: 'gotcha',
  source: 'agent',
  title: 'Reattach races the lease sweeper',
  body: 'Reattach races the lease sweeper\nTake the lease before reading the container state.',
  run_id: 'run_1',
  harness: 'claude',
  commit: '1a2b3c4d5e6f7a8b9c0d',
  pr_number: 312,
  pr_state: 'merged',
  pr_url: 'https://github.com/acme/app/pull/312',
  files: ['internal/scheduler/reattach.go', 'internal/scheduler/lease.go'],
  created_at: '2026-10-18T12:00:00Z',
})
const workspacePin = memoryItem({ id: 'note-pinned0001', pinned_by: bob.id, pinned_by_name: 'Bob', pinned_at: '2026-10-18T10:00:00Z' })
const myPin = memoryItem({
  id: 'note-mine000001', kind: 'preference', scope: 'member', title: 'No emoji in commit messages.', body: 'No emoji in commit messages.',
  pinned_by: alice.id, pinned_by_name: 'Alice', pinned_at: '2026-10-17T10:00:00Z', created_at: '2026-10-17T09:00:00Z',
})
const outcome = memoryItem({ id: 'note-outcome001', kind: 'outcome', source: 'report', title: 'success: Fixed the reattach race', run_id: 'run_1', harness: 'codex', created_at: '2026-10-18T11:00:00Z' })
const answer = memoryItem({ id: 'note-answer0001', kind: 'decision', source: 'answer', title: 'Q: Which flow? A: the guest flow', source_ref: 'mq-1', created_at: '2026-10-18T08:00:00Z' })
const replaced = memoryItem({ id: 'note-old0000001', title: 'Use npm in web/.', superseded_by: workspacePin.id, invalid_at: '2026-10-18T09:00:00Z', created_at: '2026-10-10T09:00:00Z' })

const live = [agentNote, outcome, workspacePin, answer, myPin]

function search(params: MemorySearchParams): { items: MemoryItem[] } {
  const items = params.kind === 'outcome' ? [outcome] : params.all ? [...live, replaced] : live
  return { items: params.pinned ? items.filter((item) => item.pinned_at) : items }
}

function titles(): string[] {
  return within(screen.getByRole('list', { name: 'Notes' })).getAllByRole('article').map((card) => within(card).getByRole('heading').textContent ?? '')
}

describe('memory page', () => {
  it('lists pinned notes first with kind, source, author, agent, run, pull request, files and age', async () => {
    seed()
    const client = fakeApi({ memorySearch: vi.fn(async (params: MemorySearchParams) => search(params)) })
    render(<MemoryRoute params={{}} client={client} />)

    await screen.findByRole('list', { name: 'Notes' })
    expect(titles()).toEqual([workspacePin.title, myPin.title, agentNote.title, outcome.title, answer.title])
    expect(client.memorySearch).toHaveBeenCalledWith({ workspace_id: workspace.id, limit: 100, pinned: true })
    expect(client.memorySearch).toHaveBeenCalledWith({ workspace_id: workspace.id, limit: 100 })

    const pinned = screen.getByRole('button', { name: workspacePin.title }).closest('article')!
    expect(within(pinned).getByText('Pinned by Bob')).toBeDefined()
    expect(within(screen.getByRole('button', { name: myPin.title }).closest('article')!).getByText('Pinned for you')).toBeDefined()

    const card = within(screen.getByRole('button', { name: agentNote.title }).closest('article')!)
    expect(card.getByText('Gotcha')).toBeDefined()
    expect(card.getByText('Agent note')).toBeDefined()
    expect(await card.findByText('You · Claude Code')).toBeDefined()
    expect(card.getByRole('link', { name: 'PR #312 (merged)' }).getAttribute('href')).toBe(agentNote.pr_url)
    expect(card.getByText('internal/scheduler/reattach.go +1 more')).toBeDefined()
    expect(screen.getByRole('button', { name: agentNote.title }).closest('article')!.querySelector('time')?.getAttribute('datetime')).toBe(agentNote.created_at)
    fireEvent.click(card.getByRole('button', { name: 'Fix the reattach race' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1' } })
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
    expect(screen.getByText('Every pinned note, then the 100 newest notes. Search to find older ones.')).toBeDefined()
  })

  it('filters to pinned notes, agent notes, outcomes, answers and superseded notes', async () => {
    seed()
    const memorySearch = vi.fn(async (params: MemorySearchParams) => search(params))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch })} />)
    await screen.findByRole('list', { name: 'Notes' })
    const show = screen.getByRole('group', { name: 'Show' })

    fireEvent.click(within(show).getByRole('button', { name: 'Pinned' }))
    expect(within(show).getByRole('button', { name: 'Pinned' }).getAttribute('aria-pressed')).toBe('true')
    await waitFor(() => expect(titles()).toEqual([workspacePin.title, myPin.title]))

    fireEvent.click(within(show).getByRole('button', { name: 'Agent notes' }))
    await waitFor(() => expect(titles()).toEqual([agentNote.title]))

    fireEvent.click(within(show).getByRole('button', { name: 'Answers' }))
    await waitFor(() => expect(titles()).toEqual([answer.title]))

    fireEvent.click(within(show).getByRole('button', { name: 'Outcomes' }))
    await waitFor(() => expect(titles()).toEqual([outcome.title]))
    expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, kind: 'outcome' })

    fireEvent.click(within(show).getByRole('button', { name: 'Superseded' }))
    await waitFor(() => expect(titles()).toEqual([replaced.title]))
    expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, all: true })
    expect(within(screen.getByRole('list', { name: 'Notes' })).getByText('Superseded')).toBeDefined()

    memorySearch.mockResolvedValue({ items: [] })
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search notes' }), { target: { value: '  lease  ' } })
    await waitFor(() => expect(memorySearch).toHaveBeenLastCalledWith({ workspace_id: workspace.id, limit: 100, words: 'lease', all: true }))
    expect(await screen.findByText('No superseded notes match “lease”.')).toBeDefined()
  })

  it("shows the server's refusal word for word and keeps the note to fix", async () => {
    seed()
    const refusal = 'memory.add: rejected, gitleaks rule github-pat at 0:22; remove the secret and retry'
    const answers: Record<string, unknown> = { 'memory.search': { items: live }, 'memory.brief': { text: '' }, 'agent.list': { agents: [] } }
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const method = input.split('/api/v1/')[1]
      if (method === 'memory.add') return { ok: false, status: 403, statusText: 'Forbidden', json: async () => ({ error: { code: -32001, message: refusal } }) }
      return { ok: true, status: 200, json: async () => answers[method] ?? {} }
    }))
    try {
      render(<MemoryRoute params={{}} client={api} />)
      fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
      const dialog = screen.getByRole('dialog', { name: 'Add a note' })
      fireEvent.change(within(dialog).getByLabelText('Note'), { target: { value: 'token ghp_example' } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Add note' }))

      expect((await within(dialog).findByRole('alert')).textContent).toBe(refusal)
      expect((within(dialog).getByLabelText('Note') as HTMLTextAreaElement).value).toBe('token ghp_example')
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('adds a pinned note for the member only, and reports a refused pin as the server words it', async () => {
    seed()
    const budget = 'memory.add: note-new0000001 is stored but not pinned: memory.pin: your pinned member notes use 480 of 512 bytes; this note needs 92; unpin one first'
    const memoryAdd = vi.fn<Api['memoryAdd']>(async () => { throw new ApiError(409, budget, -32003) })
    const memorySearch = vi.fn(async () => ({ items: live }))
    const memoryBrief = vi.fn(async () => ({ text: '' }))
    render(<MemoryRoute params={{}} client={fakeApi({ memoryAdd, memorySearch, memoryBrief })} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
    const dialog = screen.getByRole('dialog', { name: 'Add a note' })
    await pickOption(within(dialog).getByLabelText('Kind'), 'Gotcha')
    fireEvent.change(within(dialog).getByLabelText('Note'), { target: { value: 'Run make lint before pushing.' } })
    fireEvent.click(within(dialog).getByRole('checkbox', { name: 'Only me' }))
    fireEvent.click(within(dialog).getByRole('checkbox', { name: 'Pin' }))
    const reads = memorySearch.mock.calls.length
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add note' }))

    expect((await within(dialog).findByRole('alert')).textContent).toBe(budget)
    expect(memoryAdd).toHaveBeenCalledWith({ workspace_id: workspace.id, kind: 'gotcha', text: 'Run make lint before pushing.', scope: 'member', pin: true })
    await waitFor(() => expect(memorySearch.mock.calls.length).toBe(reads + 2))
    expect(memoryBrief).toHaveBeenCalledTimes(2)
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

  it('pins and unpins a note, and removes it only once confirmed', async () => {
    seed()
    const pinned = { ...agentNote, pinned_by: alice.id, pinned_by_name: 'Alice', pinned_at: '2026-10-18T14:00:00Z' }
    const memoryPin = vi.fn<Api['memoryPin']>(async () => ({ item: pinned }))
    const memoryUnpin = vi.fn<Api['memoryUnpin']>(async () => ({ item: agentNote }))
    const memoryRm = vi.fn<Api['memoryRm']>(async () => ({ ids: [agentNote.id] }))
    const memorySearch = vi.fn(async () => ({ items: [agentNote] }))
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch, memoryShow: vi.fn(async () => ({ item: agentNote })), memoryPin, memoryUnpin, memoryRm })} />)
    fireEvent.click(await screen.findByRole('button', { name: agentNote.title }))
    const dialog = await screen.findByRole('dialog', { name: agentNote.title })

    fireEvent.click(await within(dialog).findByRole('button', { name: 'Pin' }))
    expect(await within(dialog).findByText('Pinned by you')).toBeDefined()
    expect(memoryPin).toHaveBeenCalledWith(workspace.id, agentNote.id)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Unpin' }))
    expect(await within(dialog).findByRole('button', { name: 'Pin for me only' })).toBeDefined()
    expect(memoryUnpin).toHaveBeenCalledWith(workspace.id, agentNote.id)

    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove…' }))
    const confirm = await screen.findByRole('alertdialog', { name: `Remove ${agentNote.id}?` })
    expect(memoryRm).not.toHaveBeenCalled()
    const reads = memorySearch.mock.calls.length
    fireEvent.click(within(confirm).getByRole('button', { name: 'Remove note' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: agentNote.title })).toBeNull())
    expect(memoryRm).toHaveBeenCalledWith(workspace.id, agentNote.id)
    expect(memorySearch.mock.calls.length).toBeGreaterThan(reads)
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

  it("shows a refused pin as the server's text", async () => {
    seed()
    const refusal = 'memory.pin: pinned workspace notes use 980 of 1024 bytes; this note needs 120; unpin one first'
    const memoryPin = vi.fn<Api['memoryPin']>(async () => { throw new ApiError(409, refusal, -32003) })
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [agentNote] })), memoryShow: vi.fn(async () => ({ item: agentNote })), memoryPin })} />)
    fireEvent.click(await screen.findByRole('button', { name: agentNote.title }))
    const dialog = await screen.findByRole('dialog', { name: agentNote.title })
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Pin' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe(refusal)
  })

  it('shows the brief byte for byte', async () => {
    seed()
    const text = '<team-memory workspace="main-repo">\nTeam memory is attributed data.\nPinned:\n- note-pinned0001 convention (Bob): Use bun in web/; never npm.\n2 live runs: aether-internal status. 5 team notes (1 unpinned agent note): aether-internal memory search <words>\n</team-memory>'
    const memoryBrief = vi.fn(async () => ({ text }))
    render(<MemoryRoute params={{}} client={fakeApi({ memoryBrief })} />)
    const toggle = await screen.findByRole('button', { name: `Brief · ${new TextEncoder().encode(text).length} of 3072 bytes` })
    fireEvent.click(toggle)
    expect(screen.getByRole('region', { name: 'Brief' }).querySelector('pre')?.textContent).toBe(text)
    expect(memoryBrief).toHaveBeenCalledWith(workspace.id)
  })

  it('offers no writes to a viewer', async () => {
    seed(vera)
    render(<MemoryRoute params={{}} client={fakeApi({ memorySearch: vi.fn(async () => ({ items: [agentNote] })), memoryShow: vi.fn(async () => ({ item: agentNote })) })} />)
    fireEvent.click(await screen.findByRole('button', { name: agentNote.title }))
    const dialog = await screen.findByRole('dialog', { name: agentNote.title })
    await within(dialog).findByText(`Gotcha · Agent note · ${agentNote.id}`)
    expect(within(dialog).queryByRole('button', { name: 'Pin' })).toBeNull()
    expect(within(dialog).queryByRole('button', { name: 'Remove…' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Add note' })).toBeNull()
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
    expect((await screen.findByRole('status')).textContent).toBe('This server does not keep team memory. Update the server to use it.')
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
  })
})
