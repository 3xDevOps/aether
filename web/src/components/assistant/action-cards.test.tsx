import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AppShell } from '@/components/shell/app-shell'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { AssistantActionRecord, AssistantConfirmation, AssistantStatus } from '@/lib/assistant-types'
import type { SessionItem } from '@/lib/session-types'
import { useStore } from '@/store'
import { item, resetItems, ScriptedSession, say, tool } from '@/test/acp-stream'
import { alice, mission, run, runRecords, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

Element.prototype.scrollIntoView = vi.fn()

class SizedObserver {
  constructor(private readonly callback: ResizeObserverCallback) {}
  observe(target: Element) {
    const height = (target as HTMLElement).style.overflowY ? 4000 : 24
    const box = [{ blockSize: height, inlineSize: 400 }]
    this.callback([{ target, contentRect: { height, width: 400 }, borderBoxSize: box, contentBoxSize: box } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
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

const ready = [{ name: 'claude', display_name: 'Claude Code', ready: true }]

function status(over: Partial<AssistantStatus> = {}): AssistantStatus {
  return { state: 'idle', agent: 'claude', agents: ready, confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true, ...over }
}

const launch: AssistantConfirmation = {
  id: 'c1',
  method: 'run.launch',
  summary: 'Launch claude (acp) in main-repo: "Fix the login test"',
  params: { workspace_id: workspace.id, harness: 'claude', task: 'Fix the login test' },
  created_at: '2026-10-14T10:00:00Z',
  expires_at: '2099-10-14T10:10:00Z',
}

const fixLogin = run({ id: 'run_fix', title: 'fix-login', status: 'running' })

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', SizedObserver)
  StubSocket.install()
  resetItems()
  localStorage.clear()
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice },
    workspaces: { [workspace.id]: workspace },
    runs: runRecords(fixLogin),
    hydrated: true,
    route: { name: 'board', params: {} },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] },
    onboarded: true,
  }, true)
  vi.mocked(api.assistantStatus).mockResolvedValue(status())
})

afterEach(async () => {
  cleanup()
  await Promise.resolve()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

function assistantSocket(): ScriptedSession {
  const socket = StubSocket.opened.filter((s) => s.url.includes('/ws/assistant')).at(-1)
  if (!socket) throw new Error('no /ws/assistant socket opened')
  return new ScriptedSession(socket)
}

async function openPanel(items: SessionItem[]) {
  render(<AppShell />)
  await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
  const aside = await screen.findByRole('complementary', { name: 'Assistant' })
  assistantSocket().open({}, items)
  return aside
}

const aether = (turn: number, id: string, name: string, status: string, extra: Partial<NonNullable<SessionItem['tool_call']>> = {}) =>
  tool(turn, id, 'other', name, status, { tool_name: `mcp__aether__${name}`, ...extra })

const card = (aside: HTMLElement, name: RegExp) => within(aside).findByRole('group', { name }) as Promise<HTMLElement>

const launchInput = { workspace_id: workspace.id, harness: 'claude', task: 'Fix the login test' }

describe('action cards', () => {
  it('follows a launch from approval to a run working in the background, and opens it', async () => {
    const aside = await openPanel([item('turn_start', 1), say(1, 'user', 'Fix the login test'), aether(1, 't1', 'aether_launch_run', 'in_progress', { raw_input: launchInput })])
    assistantSocket().send({ status: status({ state: 'running', confirmations: [launch] }) })
    const pending = await card(aside, /^Launching run on main-repo with Claude Code\. Waiting for your approval$/)
    expect(pending.dataset.state).toBe('approval')
    expect(pending.textContent).toBe('Launching run on main-repo with Claude Code · Waiting for your approval below')
    expect(within(pending).queryAllByRole('button')).toHaveLength(0)
    expect(aside.querySelector('[data-slot="live-row"]')).toBeNull()

    act(() => useStore.getState().upsertRun(run({ id: 'run_9', title: 'login-test', status: 'running' })))
    assistantSocket().send({ status: status({ state: 'running' }) }).items(
      item('notice', 1, { notice: { severity: 'info', title: `Approved: ${launch.summary}`, description: 'run.launch passed after you approved its card.' } }),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_9' } } }),
    )
    const done = await card(aside, /^Launched run on main-repo with Claude Code\. Working in the background · approved by you$/)
    expect(within(aside).queryByText(`Approved: ${launch.summary}`)).toBeNull()

    expect(done.tabIndex).toBe(-1)
    within(aside).getByRole('textbox').focus()
    const primary = within(done).getByRole('button', { name: 'Open run' })
    while (document.activeElement !== primary) await userEvent.tab()
    await userEvent.keyboard('{Enter}')
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_9' } })

    act(() => useStore.getState().upsertRun(run({ id: 'run_9', title: 'login-test', status: 'completed' })))
    expect(await card(aside, /^Launched run on main-repo with Claude Code\. Done · approved by you$/)).toBeDefined()
  })

  it('says a declined action was declined and retries only into the composer', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      aether(1, 't1', 'aether_stop_run', 'failed', { raw_input: { run_id: 'run_fix' }, content: [{ type: 'text', text: 'run.kill was not approved: the member declined this action' }] }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const declined = await card(aside, /^Stop run fix-login\. You declined$/)
    expect(within(declined).queryByRole('alert')).toBeNull()
    expect(within(declined).queryByText(/not approved/)).toBeNull()
    await userEvent.click(within(declined).getByRole('button', { name: 'Details' }))
    expect(within(declined).getByText('run.kill was not approved: the member declined this action')).toBeDefined()

    await userEvent.click(within(declined).getByRole('button', { name: 'Retry' }))
    expect(useStore.getState().assistantDraft).toBe('Retry: Stop run fix-login')
    expect(document.activeElement).toBe(within(aside).getByRole('textbox'))
    expect(api.assistantPrompt).not.toHaveBeenCalled()

    await userEvent.click(within(declined).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_fix' } })
  })

  it('tells an expired card from a failure, shows the failure verbatim and says when the run is gone', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Message both'),
      aether(1, 't1', 'aether_message_run', 'failed', { raw_input: { run_id: 'run_fix', message: 'rebase' }, raw_output: 'run.inject was not approved: the member did not answer within 10m0s' }),
      aether(1, 't2', 'aether_message_run', 'failed', { raw_input: { run_id: 'run_gone', message: 'rebase' }, raw_output: 'run.inject: run run_gone not found (-32004)' }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const expired = await card(aside, /^Message run fix-login\. No answer in 10 minutes$/)
    expect(within(expired).queryByRole('alert')).toBeNull()
    expect(within(expired).getByRole('button', { name: 'Retry' })).toBeDefined()
    const failed = await card(aside, /^Message run run_gone\. Failed$/)
    expect(within(failed).getByRole('alert').textContent).toBe('run.inject: run run_gone not found (-32004)')
    expect(within(failed).getByText('This run no longer exists')).toBeDefined()
    expect(within(failed).queryByRole('button', { name: 'Open run' })).toBeNull()
    expect(within(failed).getByRole('button', { name: 'Retry' })).toBeDefined()
  })

  it('marks an auto-approved action and keeps the note of a call it cannot match', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Save it'),
      aether(1, 't1', 'aether_save_environment', 'in_progress'),
      item('notice', 1, { notice: { severity: 'info', title: 'Auto-approved: Save your environment', description: 'env.save passed without a card: auto-approval is on.' } }),
      item('notice', 1, { notice: { severity: 'info', title: 'Auto-approved: Stop run run_x', description: 'run.kill passed without a card: auto-approval is on.' } }),
      aether(1, 't1', 'aether_save_environment', 'completed', { raw_output: { image: 'img_1' } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const saved = await card(aside, /^Saved your environment\. Done · auto-approved$/)
    await userEvent.click(within(saved).getByRole('button', { name: 'Open environment' }))
    expect(useStore.getState().route).toEqual({ name: 'environment', params: {} })
    expect(within(aside).queryByText('Auto-approved: Save your environment')).toBeNull()
    expect(within(aside).getByText('Auto-approved: Stop run run_x')).toBeDefined()
  })

  it('opens a merged pull request in a new tab and a swarm from a dashboard link on another host in the app', async () => {
    const opened = vi.fn()
    vi.stubGlobal('open', opened)
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Merge and start a swarm'),
      aether(1, 't1', 'aether_pr_merge', 'completed', { raw_input: { run_id: 'run_fix' }, content: [{ type: 'text', text: JSON.stringify({ number: 42, url: 'https://github.com/acme/api/pull/42' }) }] }),
      aether(1, 't2', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { url: 'https://aether.example/?page=missions&id=mis_7', ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const merged = await card(aside, /^Merged PR #42\. Done$/)
    await userEvent.click(within(merged).getByRole('button', { name: 'Open pull request' }))
    expect(opened).toHaveBeenCalledWith('https://github.com/acme/api/pull/42', '_blank', 'noopener,noreferrer')
    await userEvent.click(within(await card(aside, /^Launched swarm on main-repo\. Working in the background$/)).getByRole('button', { name: 'Open swarm' }))
    expect(useStore.getState().route).toEqual({ name: 'missions', params: { missionId: 'mis_7' } })
  })

  it("shows a launch receipt and a watch's state, conditions and last delivery", async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Launch and watch it'),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: {
        run: { id: 'run_fix' },
        launch: { kind: 'run', id: 'run_fix', workspace_id: workspace.id, harness: 'claude', model: 'opus', mode: 'acp', mode_name: 'Enhanced', account: { member_id: 'mem_bob', name: 'Bob', access: 'shared' }, requested_account: { member_id: alice.id, name: 'Alice', access: 'own' }, fallback: { policy: 'ordered', order: ['mem_bob', alice.id] }, url: '?run=run_fix' },
      } }),
      aether(1, 't2', 'aether_watch', 'completed', { raw_input: { run_id: 'run_fix' }, raw_output: {
        watch_id: 'wat-1', state: 'active', conditions: ['needs_you', 'finished'],
        url: '/?page=settings&id=watches&watch=wat-1',
      } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched run on main-repo with Claude Code\./)
    expect(within(launched).getByText("run · main-repo · Claude Code opus · Bob's account, shared · fell back from your account · then mem_bob, your account")).toBeDefined()
    const watching = await card(aside, /^Started watching run fix-login\. Done$/)
    expect(within(watching).getByText('active · on needs you, finished')).toBeDefined()
    await userEvent.click(within(watching).getByRole('button', { name: 'Open watch' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'watches', watch: 'wat-1' } })
  })

  it('shows an Aether tool it has no words for by name with its inputs', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Frobnicate'),
      tool(1, 't1', 'other', 'aether.aether_frobnicate', 'completed', { tool_name: 'aether_frobnicate', raw_input: { widget_id: 'w1', depth: 2 }, raw_output: { ok: true } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const generic = await card(aside, /^Used aether_frobnicate\. Done$/)
    expect(within(generic).getByText('Widget').nextElementSibling?.textContent).toBe('w1')
    expect(within(generic).getByText('Depth').nextElementSibling?.textContent).toBe('2')
  })

  it('collapses consecutive reads into one line that expands to each read', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'How are my runs?'),
      aether(1, 'r1', 'aether_get_run', 'completed', { raw_input: { run_id: 'a' }, raw_output: { id: 'a' } }),
      aether(1, 'r2', 'aether_get_run', 'completed', { raw_input: { run_id: 'b' }, raw_output: { id: 'b' } }),
      aether(1, 'r3', 'aether_list_workspaces', 'completed', { raw_output: { workspaces: [] } }),
      aether(1, 'r4', 'aether_get_run', 'failed', { raw_input: { run_id: 'c' }, raw_output: 'run.get: run c not found' }),
      say(1, 'assistant', 'Two runs are fine.'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const line = await within(aside).findByRole('button', { name: /^Read 3 runs and 1 workspace list\s*· 1 failed$/ })
    expect(within(aside).queryAllByRole('group', { name: /Done$/ })).toHaveLength(0)
    expect(line.getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(line)
    expect(line.getAttribute('aria-expanded')).toBe('true')
    expect(within(aside).getByText('Run c')).toBeDefined()
    expect(within(aside).getByText('run.get: run c not found')).toBeDefined()
  })
})

describe('action history', () => {
  it('lists the actions newest first, moves with the arrows and opens a target', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Launch, then message'),
      aether(1, 'r1', 'aether_list_runs', 'completed', { raw_output: { runs: [] } }),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_fix' } } }),
      aether(1, 't2', 'aether_message_run', 'failed', { raw_input: { run_id: 'run_fix', message: 'go' }, raw_output: 'run.inject was not approved: the member declined this action' }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    await card(aside, /^Message run fix-login\. You declined$/)
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    const list = await screen.findByRole('list', { name: 'Actions' })
    const entries = within(list).getAllByRole('button')
    expect(entries.map((entry) => entry.textContent)).toEqual([
      expect.stringMatching(/^Message run fix-loginYou declined · opens the run/),
      expect.stringMatching(/^Launched run on main-repo with Claude CodeWorking in the background · opens the run/),
    ])
    expect(document.activeElement).toBe(entries[0])
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(entries[1])
    await userEvent.keyboard('{Enter}')
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_fix' } })
    await waitFor(() => expect(screen.queryByRole('list', { name: 'Actions' })).toBeNull())
  })

  it('says when there is nothing to list', async () => {
    const aside = await openPanel([item('turn_start', 1), say(1, 'user', 'hi'), say(1, 'assistant', 'Hello'), item('turn_end', 1, { stop_reason: 'end_turn' })])
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    expect(await screen.findByText('No actions yet. Reads are not listed.')).toBeDefined()
  })
})

describe('review fixes', () => {
  it('adds the retry under text already typed instead of replacing it', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      aether(1, 't1', 'aether_stop_run', 'failed', { raw_input: { run_id: 'run_fix' }, raw_output: 'run.kill was not approved: the member declined this action' }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    act(() => useStore.getState().setAssistantDraft('also check CI'))
    await userEvent.click(within(await card(aside, /^Stop run fix-login\. You declined$/)).getByRole('button', { name: 'Retry' }))
    expect(useStore.getState().assistantDraft).toBe('also check CI\n\nRetry: Stop run fix-login')
    expect(api.assistantPrompt).not.toHaveBeenCalled()
  })

  it('opens the run a launch approved by an older server names only in its text result', async () => {
    useStore.setState({ route: { name: 'run', params: { runId: 'run_fix' } } })
    const aside = await openPanel([item('turn_start', 1), say(1, 'user', 'Fix this'), aether(1, 't1', 'aether_launch_run', 'in_progress', { raw_input: launchInput })])
    assistantSocket().send({ status: status({ state: 'running', confirmations: [launch] }) })
    await card(aside, /Waiting for your approval$/)
    const text = `Launched run run_9.\n${JSON.stringify({ run_id: 'run_9', state: 'queued', workspace_id: workspace.id, harness: 'claude', url: '?run=run_9' })}`
    assistantSocket().send({ status: status({ state: 'running' }) }).items(
      item('notice', 1, { notice: { severity: 'info', title: `Approved: ${launch.summary}`, description: 'run.launch passed after you approved its card.' } }),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, content: [{ type: 'text', text }] }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    )
    const done = await card(aside, /^Launched run on main-repo with Claude Code\. .*approved by you$/)
    await userEvent.click(within(done).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_9' } })
  })

  it('keeps Open run on a launch whose run the store has not heard of yet', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Launch'),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_new' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched run on main-repo with Claude Code\. Done$/)
    expect(within(launched).queryByText('This run no longer exists')).toBeNull()
    await userEvent.click(within(launched).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_new' } })
  })

  it('reads the whole item behind a cut-down call when its details open', async () => {
    const head = [item('turn_start', 1), say(1, 'user', 'Message')]
    const cut = { ...aether(1, 't1', 'aether_message_run', 'completed', { raw_input: { run_id: 'run_fix' } }), truncated: true }
    vi.mocked(api.assistantItem).mockResolvedValue({ ...cut, truncated: false, tool_call: { ...cut.tool_call!, raw_input: { run_id: 'run_fix', message: 'the whole message' }, raw_output: { delivered: true } } })
    const aside = await openPanel([...head, cut, item('turn_end', 1, { stop_reason: 'end_turn' })])
    await userEvent.click(within(await card(aside, /^Messaged run fix-login\. Done$/)).getByRole('button', { name: 'Details' }))
    expect(api.assistantItem).toHaveBeenCalledWith(cut.seq)
    expect(await within(aside).findByText(/the whole message/)).toBeDefined()
  })

  it('stops saying it is loading once the whole item is read, even if the call still reads as cut', async () => {
    const head = [item('turn_start', 1), say(1, 'user', 'Message')]
    const cut = { ...aether(1, 't1', 'aether_message_run', 'completed', { raw_input: { run_id: 'run_fix' } }), truncated: true }
    vi.mocked(api.assistantItem).mockResolvedValue(cut)
    const aside = await openPanel([...head, cut, item('turn_end', 1, { stop_reason: 'end_turn' })])
    const messaged = await card(aside, /^Messaged run fix-login\. Done$/)
    await userEvent.click(within(messaged).getByRole('button', { name: 'Details' }))
    await waitFor(() => expect(api.assistantItem).toHaveBeenCalledWith(cut.seq))
    await waitFor(() => expect(within(messaged).queryByText('Loading the whole call…')).toBeNull())
  })

  it('opens a dashboard link in an assistant message in the app, and any other link in a new tab', async () => {
    const opened = vi.spyOn(window, 'open').mockReturnValue(null)
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Briefing'),
      say(1, 'assistant', 'Shipped: [fix-login](?run=run_fix). Docs: [guide](https://example.com/guide)'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const external = await within(aside).findByRole('link', { name: 'guide' })
    expect(external.getAttribute('target')).toBe('_blank')
    await userEvent.click(within(aside).getByRole('link', { name: 'fix-login' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_fix' } })
    expect(opened).not.toHaveBeenCalled()
    opened.mockRestore()
  })

  it('opens the watch a notice links to in the app', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Watch it'),
      item('notice', 1, { notice: { severity: 'info', title: 'Watching run fix-login', description: 'Aether tells you when it finishes. [Stop or change this watch](?page=settings&id=watches&watch=wat-1)' } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    await userEvent.click(await within(aside).findByRole('link', { name: 'Stop or change this watch' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'watches', watch: 'wat-1' } })
  })

  it('loads earlier actions into the history when the conversation has more', async () => {
    const older = [item('turn_start', 1), say(1, 'user', 'Launch'), aether(1, 'o1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_fix' } } }), item('turn_end', 1, { stop_reason: 'end_turn' })]
    const recent = [item('turn_start', 2), say(2, 'user', 'Read'), aether(2, 'r1', 'aether_get_run', 'completed', { raw_input: { run_id: 'run_fix' } }), item('turn_end', 2, { stop_reason: 'end_turn' })]
    vi.mocked(api.assistantHistory).mockResolvedValue({ frames: older.map((it) => ({ seq: it.seq, item: it })), oldest_seq: 1 })
    render(<AppShell />)
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    const aside = await screen.findByRole('complementary', { name: 'Assistant' })
    assistantSocket().open({ oldest_seq: recent[0]!.seq }, recent)
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    expect(await screen.findByText('No actions in the loaded messages. Reads are not listed.')).toBeDefined()
    await userEvent.click(screen.getByRole('button', { name: 'Show earlier actions' }))
    const list = await screen.findByRole('list', { name: 'Actions' })
    expect(within(list).getByRole('button').textContent).toMatch(/^Launched run on main-repo with Claude Code/)
  })
})

describe('agents and turn ends', () => {
  it("reads a Codex call, which has no tool name and wraps its arguments and result", async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      tool(1, 'c1', 'execute', 'mcp.aether.aether_stop_run', 'completed', {
        raw_input: { server: 'aether', tool: 'aether_stop_run', arguments: { run_id: 'run_fix' } },
        raw_output: { result: { content: [{ type: 'text', text: '{"url":"https://aether.example/?run=run_fix"}' }] }, error: null },
      }),
      tool(1, 'c2', 'execute', 'mcp.aether.aether_message_run', 'failed', {
        raw_input: { server: 'aether', tool: 'aether_message_run', arguments: { run_id: 'run_fix', message: 'go' } },
        raw_output: { result: null, error: { message: 'run.inject was not approved: the member declined this action' } },
      }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const stopped = await card(aside, /^Stopped run fix-login\. Done$/)
    await userEvent.click(within(stopped).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_fix' } })
    expect(await card(aside, /^Message run fix-login\. You declined$/)).toBeDefined()
  })

  it('says a call the turn ended under did not finish, rather than done', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      aether(1, 't1', 'aether_stop_run', 'in_progress', { raw_input: { run_id: 'run_fix' } }),
      item('turn_end', 1, { stop_reason: 'cancelled' }),
    ])
    const open = await card(aside, /^Stop run fix-login\. Did not finish: the turn ended first$/)
    expect(within(open).getByRole('button', { name: 'Retry' })).toBeDefined()
  })
})

describe('status words', () => {
  it("names the assistant's own turn Generating, apart from runs working in the background", async () => {
    const aside = await openPanel([item('turn_start', 1), say(1, 'user', 'hi')])
    assistantSocket().send({ status: status({ state: 'running' }) })
    expect(await within(aside).findByText('Claude Code · Generating…')).toBeDefined()
  })
})

describe('quiet outcomes and names', () => {
  it('says a withdrawn card in its own words, without an error or a retry', async () => {
    const withdrawn: AssistantActionRecord = { id: 'a1', method: 'run.kill', summary: 'Stop run fix-login', target: { run_id: 'run_fix' }, outcome: 'withdrawn', confirmation_id: 'c1', created_at: '2026-10-26T10:00:00Z' }
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      aether(1, 't1', 'aether_stop_run', 'in_progress', { raw_input: { run_id: 'run_fix' } }),
      item('notice', 1, { notice: { severity: 'info', title: 'Withdrawn: Stop run fix-login', action: withdrawn } }),
      aether(1, 't1', 'aether_stop_run', 'failed', { raw_input: { run_id: 'run_fix' }, raw_output: 'run.kill was not approved: the turn or session ended before the member answered' }),
      item('turn_end', 1, { stop_reason: 'cancelled' }),
    ])
    const stopped = await card(aside, /^Stop run fix-login\. Withdrawn: the turn ended before you answered$/)
    expect(within(stopped).queryByRole('alert')).toBeNull()
    expect(within(stopped).queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(within(aside).queryByText('Withdrawn: Stop run fix-login')).toBeNull()
  })

  it('names a run by its task when it has no title', async () => {
    useStore.getState().upsertRun(run({ id: 'run_untitled', title: '', task: 'Fix the flaky login test\nThen push', status: 'running' }))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop it'),
      aether(1, 't1', 'aether_stop_run', 'completed', { raw_input: { run_id: 'run_untitled' } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    expect(await card(aside, /^Stopped run Fix the flaky login test\. Done$/)).toBeDefined()
  })

  it('reads a launched swarm it has not loaded, working until the swarm says it finished', async () => {
    let finish!: (value: Awaited<ReturnType<typeof api.missionShow>>) => void
    vi.mocked(api.missionShow).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Start a swarm'),
      aether(1, 't1', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    expect(await card(aside, /^Launched swarm on main-repo\. Working in the background$/)).toBeDefined()
    expect(api.missionShow).toHaveBeenCalledWith('mis_7')
    act(() => finish({ mission: mission({ id: 'mis_7', phase: 'completed' }), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [] }))
    expect(await card(aside, /^Launched swarm on main-repo\. Done$/)).toBeDefined()
    act(() => useStore.setState({ missions: {} }))
    expect(await card(aside, /^Launched swarm on main-repo\. Done$/)).toBeDefined()
  })

  it('shows why a launched swarm could not be read instead of calling it working', async () => {
    vi.mocked(api.missionShow).mockRejectedValue(new Error('mission.get: mission mis_7 not found (-32004)'))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Start a swarm'),
      aether(1, 't1', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched swarm on main-repo\. Done$/)
    expect(within(launched).getByText('Could not read swarm mis_7: mission.get: mission mis_7 not found (-32004)')).toBeDefined()
  })

  it('reads a launched swarm again on each of its changes, so the card and the history agree', async () => {
    vi.mocked(api.missionShow).mockResolvedValue({ mission: mission({ id: 'mis_7', phase: 'active' }), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [] })
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Start a swarm'),
      aether(1, 't1', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    expect(await card(aside, /^Launched swarm on main-repo\. Working in the background$/)).toBeDefined()
    await waitFor(() => expect(useStore.getState().missions.mis_7?.phase).toBe('active'))

    vi.mocked(api.missionShow).mockResolvedValue({ mission: mission({ id: 'mis_7', phase: 'completed' }), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [] })
    act(() => useStore.getState().bumpWorkflowSignal('missionSeq', 'mis_7'))
    expect(await card(aside, /^Launched swarm on main-repo\. Done$/)).toBeDefined()
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    const history = await screen.findByRole('dialog', { name: 'Actions in this conversation' })
    expect(within(history).getByText(/^Done · opens the swarm$/)).toBeDefined()
  })

  it('says what a launch started without a receipt, and how the member hears back', async () => {
    useStore.setState({ members: { [alice.id]: alice, mem_bob: { id: 'mem_bob', display_name: 'Bob', color: '#000', role: 'collaborator' } } })
    act(() => useStore.getState().upsertRun(run({ id: 'run_9', title: 'login-test', status: 'running' })))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Fix the login test on Bob'),
      aether(1, 't1', 'aether_launch_run', 'completed', {
        raw_input: { ...launchInput, model: 'opus', account_member_id: 'mem_bob', no_fallback: true },
        content: [{ type: 'text', text: `Launched run run_9.\n${JSON.stringify({ run_id: 'run_9', workspace_id: workspace.id, harness: 'claude', url: '?run=run_9' })}` }],
      }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched run on main-repo with Claude Code\. Working in the background$/)
    expect(within(launched).getByText("run · main-repo · Claude Code opus · Bob's account, shared · no fallback")).toBeDefined()
    expect(within(launched).getByText(/^Aether notifies you here/)).toBeDefined()
    await userEvent.click(within(launched).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_9' } })
  })
})
