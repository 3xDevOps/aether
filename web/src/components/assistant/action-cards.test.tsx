import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AppShell } from '@/components/shell/app-shell'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { AssistantConfirmation, AssistantStatus } from '@/lib/assistant-types'
import type { SessionItem } from '@/lib/session-types'
import { useStore } from '@/store'
import { item, resetItems, ScriptedSession, say, tool } from '@/test/acp-stream'
import { alice, run, runRecords, serverInfo, workspace } from '@/test/fixtures'
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
    expect(aside.querySelector('[data-slot="live-row"]')?.textContent).toBe('Waiting for your approval')

    act(() => useStore.getState().upsertRun(run({ id: 'run_9', title: 'login-test', status: 'running' })))
    assistantSocket().send({ status: status({ state: 'running' }) }).items(
      item('notice', 1, { notice: { severity: 'info', title: `Approved: ${launch.summary}`, description: 'run.launch passed after you approved its card.' } }),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_9' } } }),
    )
    const done = await card(aside, /^Launched run on main-repo with Claude Code\. Working in the background · approved by you$/)
    expect(within(aside).queryByText(`Approved: ${launch.summary}`)).toBeNull()

    done.focus()
    fireEvent.keyDown(done, { key: 'Enter' })
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
    const declined = await card(aside, /^Stop run fix-login\. Declined$/)
    expect(within(declined).getByRole('alert').textContent).toBe('run.kill was not approved: the member declined this action')

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
    expect(await card(aside, /^Message run fix-login\. Expired: no answer in time$/)).toBeDefined()
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

  it('opens a merged pull request in a new tab and a swarm from the result link', async () => {
    const opened = vi.fn()
    vi.stubGlobal('open', opened)
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Merge and start a swarm'),
      aether(1, 't1', 'aether_pr_merge', 'completed', { raw_input: { run_id: 'run_fix' }, content: [{ type: 'text', text: JSON.stringify({ number: 42, url: 'https://github.com/acme/api/pull/42' }) }] }),
      aether(1, 't2', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { link: '?page=missions&id=mis_7', ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const merged = await card(aside, /^Merged PR #42\. Done$/)
    await userEvent.click(within(merged).getByRole('button', { name: 'Open pull request' }))
    expect(opened).toHaveBeenCalledWith('https://github.com/acme/api/pull/42', '_blank', 'noopener,noreferrer')
    await userEvent.click(within(await card(aside, /^Launched swarm on main-repo\. Done$/)).getByRole('button', { name: 'Open swarm' }))
    expect(useStore.getState().route).toEqual({ name: 'missions', params: { missionId: 'mis_7' } })
  })

  it("shows a launch receipt and a watch's state, conditions and last delivery", async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Launch and watch it'),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: {
        run: { id: 'run_fix' },
        launch: { kind: 'run', workspace_id: workspace.id, harness: 'claude', model: 'opus', account: { name: 'Bob', shared: true }, requested_account: { name: 'Alice' }, link: '?run=run_fix' },
      } }),
      aether(1, 't2', 'aether_watch_create', 'completed', { raw_input: { run_id: 'run_fix' }, raw_output: {
        watch: { state: 'active', conditions: ['needs_you', 'finished'], last_notification: { deliveries: [{ channel: 'push', status: 'no_destination' }] } },
        link: '/?page=settings&id=watches&watch=wat-1',
      } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched run on main-repo with Claude Code\./)
    expect(within(launched).getByText("run · main-repo · Claude Code opus · Bob's account, shared · fell back from Alice")).toBeDefined()
    const watching = await card(aside, /^Started watching run fix-login\. Done$/)
    expect(within(watching).getByText('active · on needs you, finished · last notice: push no destination')).toBeDefined()
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
    await card(aside, /^Message run fix-login\. Declined$/)
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    const list = await screen.findByRole('list', { name: 'Actions' })
    const entries = within(list).getAllByRole('button')
    expect(entries.map((entry) => entry.textContent)).toEqual([
      expect.stringMatching(/^Message run fix-loginDeclined · opens the run/),
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

describe('status words', () => {
  it("names the assistant's own turn Generating, apart from runs working in the background", async () => {
    const aside = await openPanel([item('turn_start', 1), say(1, 'user', 'hi')])
    assistantSocket().send({ status: status({ state: 'running' }) })
    expect(await within(aside).findByText('Claude Code · Generating…')).toBeDefined()
  })
})
