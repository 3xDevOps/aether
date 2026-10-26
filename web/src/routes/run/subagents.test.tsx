import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { SessionHistory, SessionItem } from '@/lib/session-types'
import { CenterView } from '@/components/shell/center-view'
import { lookupRoute } from '@/routes/registry'
import '@/routes/run'
import { useStore } from '@/store'
import { item, resetItems, ScriptedSession, say, tool } from '@/test/acp-stream'
import { alice, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'
import { atViewport } from '@/test/viewport'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

function Harness() {
  const View = lookupRoute('run')!
  const params = useStore((s) => s.route.params)
  return <View params={params} />
}

function open(agent?: string) {
  useStore.getState().upsertRun(run({ mode: 'acp', acp: true }))
  useStore.setState({ route: { name: 'run', params: { runId: 'run_1', view: 'session', ...(agent ? { agent } : {}) } } })
  return render(<Harness />)
}

function acpSocket(): ScriptedSession {
  return new ScriptedSession(StubSocket.opened.filter((s) => s.url.includes('/ws/acp/run_1')).at(-1)!)
}

class SizedObserver {
  /** What every observed element measures, the run's row included. */
  static width = 736
  static reports: (() => void)[] = []
  static resize(width: number) {
    SizedObserver.width = width
    act(() => SizedObserver.reports.forEach((report) => report()))
  }
  constructor(private readonly callback: ResizeObserverCallback) {}
  observe(target: Element) {
    const report = () => {
      const height = (target as HTMLElement).style.overflowY ? 4000 : 24
      const width = SizedObserver.width
      const box = [{ blockSize: height, inlineSize: width }]
      this.callback([{ target, contentRect: { height, width }, borderBoxSize: box, contentBoxSize: box } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
    }
    SizedObserver.reports.push(report)
    report()
  }
  unobserve() {}
  disconnect() {}
}

const offsetParent = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')!
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', { configurable: true, get() { return this.parentElement } })
})
afterAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', offsetParent)
})

beforeEach(() => {
  SizedObserver.width = 736
  SizedObserver.reports = []
  vi.stubGlobal('ResizeObserver', SizedObserver)
  StubSocket.install()
  resetItems()
  useStore.setState({
    runs: {},
    info: serverInfo,
    members: { [alice.id]: alice },
    workspaces: { [workspace.id]: workspace },
    roomMessages: {},
    acpSessions: {},
    expandedRows: {},
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
  })
})

afterEach(async () => {
  cleanup()
  await Promise.resolve()
  useStore.setState(useStore.getInitialState(), true)
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

const launch = (turn: number, id: string, description: string, status: string, extra: Record<string, unknown> = {}) =>
  tool(turn, id, 'other', description, status, { tool_name: 'Agent', subagent: { description, type: 'Explore' }, ...extra })

const child = (turn: number, id: string, parent: string, title: string) =>
  tool(turn, id, 'read', title, 'completed', { tool_name: 'Read', parent_id: parent })

/** A finished turn's block is shut; its header opens it. */
async function openSteps(log: HTMLElement, name: RegExp) {
  await userEvent.click(await within(log).findByRole('button', { name }))
}

function transcript(): SessionItem[] {
  return [
    item('turn_start', 1),
    say(1, 'user', 'Map the billing code'),
    launch(1, 'a1', 'Find usages', 'in_progress', { raw_input: { prompt: 'Search the repo for round()' } }),
    launch(1, 'a2', 'Check tests', 'in_progress', { raw_input: { prompt: 'Run the billing tests' } }),
    child(1, 'c1', 'a1', 'Read billing.js'),
    launch(1, 'a1', 'Find usages', 'completed', { raw_input: { prompt: 'Search the repo for round()' }, content: [{ type: 'text', text: 'round() is used in **two** places.' }] }),
    item('turn_end', 1, { stop_reason: 'end_turn' }),
  ]
}

describe('subagents in the session view', () => {
  it('groups consecutive subagents into cards and opens one in the inspector', async () => {
    open()
    acpSocket().open({}, transcript())
    const log = await screen.findByRole('log', { name: 'Session' })
    await openSteps(log, /^Delegated 2 tasks/)
    expect(await within(log).findByRole('button', { name: /2 subagents · 1 stopped/ })).toBeDefined()
    const card = within(log).getByRole('button', { name: 'Find usages, Explore, Done' })
    expect(within(card).getByText('round() is used in two places.')).toBeDefined()
    expect(within(log).getByRole('button', { name: 'Check tests, Explore, Stopped' })).toBeDefined()

    await userEvent.click(card)
    expect(useStore.getState().route.params).toEqual({ runId: 'run_1', view: 'session', agent: 'a1' })
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect(within(inspector).getByRole('heading', { name: 'Find usages' })).toBeDefined()
    expect(within(inspector).getByText('Search the repo for round()')).toBeDefined()
    expect(within(inspector).getByRole('button', { name: /Read 1 file/ })).toBeDefined()
    expect(within(inspector).getByRole('heading', { name: 'Report' }).nextElementSibling?.textContent).toBe('round() is used in two places.')
    expect(within(inspector).getByText('1 of 2')).toBeDefined()
    expect(within(log).getByRole('button', { name: 'Find usages, Explore, Done' }).getAttribute('aria-current')).toBe('true')

    await userEvent.click(within(inspector).getByRole('button', { name: 'Next subagent' }))
    expect(useStore.getState().route.params.agent).toBe('a2')
    expect(within(inspector).getByRole('heading', { name: 'Check tests' })).toBeDefined()
    expect(within(inspector).getByText('No tool calls were reported.')).toBeDefined()
    expect(within(inspector).getByRole('button', { name: 'Next subagent' })).toHaveProperty('disabled', true)

    await userEvent.click(within(inspector).getByRole('button', { name: 'Close subagent' }))
    expect(useStore.getState().route.params).toEqual({ runId: 'run_1', view: 'session' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Subagent' })).toBeNull())
  })

  it('opens an agent from the address and walks up the breadcrumbs from a nested one', async () => {
    open('n1')
    acpSocket().open({}, [
      item('turn_start', 1),
      launch(1, 'a1', 'Outer', 'in_progress'),
      launch(1, 'n1', 'Inner', 'in_progress', { parent_id: 'a1' }),
      child(1, 'c1', 'n1', 'Read inner.js'),
    ])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    const path = within(inspector).getByRole('navigation', { name: 'Subagent path' })
    expect(within(path).getByText('Inner').getAttribute('aria-current')).toBe('page')
    await userEvent.click(within(path).getByRole('button', { name: 'Outer' }))
    expect(useStore.getState().route.params.agent).toBe('a1')
    expect(await within(inspector).findByRole('button', { name: 'Inner, Explore, Running' })).toBeDefined()
    await userEvent.click(within(inspector).getByRole('button', { name: 'Session' }))
    expect(useStore.getState().route.params.agent).toBeUndefined()
  })

  it('reads a prompt the brief item dropped from the whole item', async () => {
    const start = item('turn_start', 1)
    const launched = { ...launch(1, 'a1', 'Find usages', 'completed'), truncated: true }
    vi.mocked(api.runACPItem).mockResolvedValue({
      ...launched,
      tool_call: { ...launched.tool_call!, raw_input: { prompt: 'Search the repo for round()' } },
      truncated: false,
    })
    open('a1')
    acpSocket().open({}, [start, launched])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect(await within(inspector).findByText('Search the repo for round()')).toBeDefined()
    expect(api.runACPItem).toHaveBeenCalledWith('run_1', launched.seq)
  })

  it('shows the real error when the whole item cannot be read', async () => {
    const start = item('turn_start', 1)
    const launched = { ...launch(1, 'a1', 'Find usages', 'completed'), truncated: true }
    vi.mocked(api.runACPItem).mockRejectedValue(new Error('acphost: requested session history has expired'))
    open('a1')
    acpSocket().open({}, [start, launched])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect((await within(inspector).findAllByRole('alert')).map((alert) => alert.textContent)).toEqual([
      'The prompt could not be read: acphost: requested session history has expired',
      'The report could not be read: acphost: requested session history has expired',
    ])
  })

  it('shows a report read error beside a held prompt, and retries it', async () => {
    const start = item('turn_start', 1)
    const launched = { ...launch(1, 'a1', 'Find usages', 'completed', { raw_input: { prompt: 'Search' } }), truncated: true }
    vi.mocked(api.runACPItem).mockClear().mockRejectedValue(new Error('acphost: requested session history has expired'))
    open('a1')
    acpSocket().open({}, [start, launched])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect(within(inspector).getByText('Search')).toBeDefined()
    expect((await within(inspector).findByRole('alert')).textContent).toBe('The report could not be read: acphost: requested session history has expired')
    await userEvent.click(within(inspector).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(api.runACPItem).toHaveBeenCalledTimes(2))
  })

  it('pages earlier history until the launch is loaded, and says when an agent is unknown', async () => {
    const [start, launched] = [item('turn_start', 1), launch(1, 'a1', 'Find usages', 'in_progress', { raw_input: { prompt: 'Search' } })]
    const later = child(1, 'c1', 'a1', 'Read billing.js')
    const page = Promise.withResolvers<SessionHistory>()
    vi.mocked(api.runACPHistory).mockClear().mockReturnValueOnce(page.promise)
    open('a1')
    acpSocket().open({ oldest_seq: later.seq }, [later])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect(await within(inspector).findByText('Loading earlier history…')).toBeDefined()
    await act(async () => page.resolve({ frames: [start, launched].map((it) => ({ seq: it.seq, item: it })), oldest_seq: start.seq }))
    expect(await within(inspector).findByRole('heading', { name: 'Find usages' })).toBeDefined()
    expect(within(inspector).queryByText('Loading earlier history…')).toBeNull()
    expect(api.runACPHistory).toHaveBeenCalledTimes(1)
  })

  it('says a run has no such subagent once there is no more history', async () => {
    open('missing')
    acpSocket().open({}, [item('turn_start', 1)])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect(await within(inspector).findByText('The loaded history has no subagent missing.')).toBeDefined()
  })

  it('shows the failure and retries when earlier history cannot be read', async () => {
    item('turn_start', 1)
    const later = child(1, 'c1', 'a1', 'Read billing.js')
    vi.mocked(api.runACPHistory).mockClear().mockRejectedValueOnce(new Error('acphost: history unavailable'))
    open('a1')
    acpSocket().open({ oldest_seq: later.seq }, [later])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect(await within(inspector).findByText('acphost: history unavailable')).toBeDefined()
    vi.mocked(api.runACPHistory).mockResolvedValueOnce({ frames: [], oldest_seq: 1 })
    await userEvent.click(within(inspector).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(api.runACPHistory).toHaveBeenCalledTimes(2))
  })

  // The session log behind the inspector requests one page on mount; the inspector's own requests count on top.
  const sessionViewPages = 1

  it('does not page history for an unknown subagent until asked', async () => {
    let page = 100
    vi.mocked(api.runACPHistory).mockClear().mockImplementation(async () => {
      const older = { ...child(1, `c${page}`, 'other', 'Read billing.js'), seq: --page }
      return { frames: [{ seq: older.seq, item: older }], oldest_seq: 1 }
    })
    open('missing')
    acpSocket().open({ oldest_seq: 100 }, [{ ...child(1, 'c100', 'other', 'Read billing.js'), seq: 100 }])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    const more = await within(inspector).findByRole('button', { name: 'Load earlier history' })
    expect(api.runACPHistory).toHaveBeenCalledTimes(sessionViewPages)
    await userEvent.click(more)
    await waitFor(() => expect(api.runACPHistory).toHaveBeenCalledTimes(sessionViewPages + 1))
  })

  it('stops auto-paging for a known subagent after five pages and offers the button', async () => {
    let page = 100
    vi.mocked(api.runACPHistory).mockClear().mockImplementation(async () => {
      const older = { ...child(1, `c${page}`, 'a1', 'Read billing.js'), seq: --page }
      return { frames: [{ seq: older.seq, item: older }], oldest_seq: 1 }
    })
    open('a1')
    acpSocket().open({ oldest_seq: 100 }, [{ ...child(1, 'c100', 'a1', 'Read billing.js'), seq: 100 }])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    await within(inspector).findByRole('button', { name: 'Load earlier history' })
    expect(api.runACPHistory).toHaveBeenCalledTimes(sessionViewPages + 5)
  })

  it('shows a subagent whose launch is not loaded as a Not loaded card', async () => {
    open()
    acpSocket().open({ oldest_seq: 2 }, [item('turn_start', 1), say(1, 'user', 'Map it'), child(1, 'c1', 'ghost', 'Read billing.js')])
    const log = await screen.findByRole('log', { name: 'Session' })
    expect(await within(log).findByRole('button', { name: 'Subagent, Not loaded' })).toBeDefined()
  })

  it('shows a not loaded subagent even when no main item of its turn is held', async () => {
    open()
    acpSocket().open({}, [child(1, 'c1', 'ghost', 'Read billing.js')])
    const log = await screen.findByRole('log', { name: 'Session' })
    const card = await within(log).findByRole('button', { name: 'Subagent, Not loaded' })
    expect(within(card).getByText('Its launch is not in the session history · Read billing.js')).toBeDefined()
  })

  it('previews a report without Latin letters', async () => {
    open()
    acpSocket().open({}, [item('turn_start', 1), launch(1, 'a1', 'Check', 'completed', { content: [{ type: 'text', text: '## 检查完成' }] })])
    const log = await screen.findByRole('log', { name: 'Session' })
    expect(await within(log).findByText('检查完成')).toBeDefined()
  })

  it('says when the server cut the launch input that held the prompt', async () => {
    open('a1')
    acpSocket().open({}, [item('turn_start', 1), launch(1, 'a1', 'Find usages', 'in_progress', { raw_input: '{"prompt":"Search the re' })])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect(await within(inspector).findByText('The launch input is longer than the session log keeps, so its prompt is not available.')).toBeDefined()
  })

  it('says the prompt is unavailable when the stored launch stays cut', async () => {
    const start = item('turn_start', 1)
    const cut = { ...launch(1, 'a1', 'Find usages', 'in_progress', { raw_input: '{"prompt":"Search the re' }), truncated: true }
    vi.mocked(api.runACPItem).mockResolvedValue(cut)
    open('a1')
    acpSocket().open({}, [start, cut])
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    expect(await within(inspector).findByText('The launch input is longer than the session log keeps, so its prompt is not available.')).toBeDefined()
    expect(api.runACPItem).toHaveBeenCalledWith('run_1', cut.seq)
  })

  it('closes an inline inspector when Details opens', async () => {
    SizedObserver.width = 1180
    open('a1')
    acpSocket().open({}, transcript())
    expect(await screen.findByRole('complementary', { name: 'Subagent' })).toBeDefined()
    await userEvent.click(screen.getByRole('button', { name: 'Show details' }))
    expect(useStore.getState().route.params.agent).toBeUndefined()
    expect(await screen.findByRole('complementary', { name: 'Run details' })).toBeDefined()
  })

  it('opens the inspector over the run where inline it would leave the run too narrow to read', async () => {
    SizedObserver.width = 1000
    open('a1')
    acpSocket().open({}, transcript())
    expect(await screen.findByRole('dialog', { name: 'Subagent' })).toBeDefined()
    expect(screen.queryByRole('complementary', { name: 'Subagent' })).toBeNull()
  })

  it('closes an inline inspector the docking assistant squeezes out, leaving focus where it was', async () => {
    atViewport(1500)
    SizedObserver.width = 1180
    useStore.getState().upsertRun(run({ mode: 'acp', acp: true }))
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1', view: 'session', agent: 'a1' } } })
    render(<main id="main"><CenterView /></main>)
    acpSocket().open({}, transcript())
    expect(await screen.findByRole('complementary', { name: 'Subagent' })).toBeDefined()
    const outside = document.body.appendChild(document.createElement('textarea'))
    outside.focus()
    act(() => useStore.getState().setAssistantOpen(true))
    SizedObserver.resize(1000)
    expect(useStore.getState().route.params.agent).toBeUndefined()
    expect(screen.queryByRole('complementary', { name: 'Subagent' })).toBeNull()
    expect(screen.queryByRole('dialog', { name: 'Subagent' })).toBeNull()
    expect(document.activeElement).toBe(outside)
    outside.remove()
  })

  it('moves an inline inspector to the side sheet when the run narrows for any other reason', async () => {
    SizedObserver.width = 1180
    open('a1')
    acpSocket().open({}, transcript())
    expect(await screen.findByRole('complementary', { name: 'Subagent' })).toBeDefined()
    SizedObserver.resize(1000)
    expect(await screen.findByRole('dialog', { name: 'Subagent' })).toBeDefined()
    expect(useStore.getState().route.params.agent).toBe('a1')
  })

  it('reads a background Agent as launched in the background, not done', async () => {
    const raw_input = { run_in_background: true, prompt: 'x' }
    open()
    acpSocket().open({}, [
      item('turn_start', 1),
      launch(1, 'a1', 'Watch CI', 'in_progress', { raw_input }),
      launch(1, 'a1', 'Watch CI', 'completed', { raw_input, content: [{ type: 'text', text: 'Async agent launched successfully.' }] }),
    ])
    const log = await screen.findByRole('log', { name: 'Session' })
    const card = await within(log).findByRole('button', { name: 'Watch CI, Explore, Launched' })
    expect(within(card).getByText('Launched in the background')).toBeDefined()
    expect(within(log).queryByText('Async agent launched successfully.')).toBeNull()
  })

  it('summarises a report by its first line without markdown marks', async () => {
    const report = (text: string) => [{ type: 'text', text }]
    open()
    acpSocket().open({}, [
      item('turn_start', 1),
      launch(1, 'a1', 'Coverage', 'completed', { content: report('## Coverage report\n\nbody') }),
      launch(1, 'a2', 'Listing', 'completed', { content: report('- **bold** item') }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const log = await screen.findByRole('log', { name: 'Session' })
    await openSteps(log, /^Delegated 2 tasks/)
    expect(within(await within(log).findByRole('button', { name: 'Coverage, Explore, Done' })).getByText('Coverage report')).toBeDefined()
    expect(within(within(log).getByRole('button', { name: 'Listing, Explore, Done' })).getByText('bold item')).toBeDefined()
  })

  it('adds no changed-files row for a subagent edit', async () => {
    open()
    acpSocket().open({}, [
      item('turn_start', 1),
      launch(1, 'a1', 'Edit it', 'in_progress'),
      tool(1, 'e1', 'edit', 'Edit a.go', 'completed', { parent_id: 'a1', diffs: [{ path: 'a.go', patch: '+x\n-y\n' }] }),
      tool(1, 'e2', 'edit', 'Edit a.go', 'completed', { diffs: [{ path: 'a.go', patch: '+z\n' }] }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const log = await screen.findByRole('log', { name: 'Session' })
    await openSteps(log, /^Delegated 1 task and edited 1 file/)
    await within(log).findByRole('button', { name: /Edit it/ })
    expect(log.textContent).not.toContain('Changed')
  })

  it('steps and closes subagents without history entries, and ties the group to its cards', async () => {
    open()
    acpSocket().open({}, transcript())
    const log = await screen.findByRole('log', { name: 'Session' })
    await openSteps(log, /^Delegated 2 tasks/)
    const group = await within(log).findByRole('button', { name: /2 subagents/ })
    const list = document.getElementById(group.getAttribute('aria-controls')!)
    expect(list?.contains(within(log).getByRole('button', { name: 'Find usages, Explore, Done' }))).toBe(true)

    await userEvent.click(within(log).getByRole('button', { name: 'Find usages, Explore, Done' }))
    const inspector = await screen.findByRole('dialog', { name: 'Subagent' })
    const push = vi.spyOn(window.history, 'pushState')
    const replace = vi.spyOn(window.history, 'replaceState')
    await userEvent.click(within(inspector).getByRole('button', { name: 'Next subagent' }))
    expect(useStore.getState().route.params.agent).toBe('a2')
    await userEvent.click(within(inspector).getByRole('button', { name: 'Close subagent' }))
    expect(useStore.getState().route.params.agent).toBeUndefined()
    expect(push).not.toHaveBeenCalled()
    expect(replace).toHaveBeenCalled()
  })
})
