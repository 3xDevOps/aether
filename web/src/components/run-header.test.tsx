import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { lookupRoute } from '@/routes/registry'
import { runTabs } from '@/routes/terminal/tabs'
import '@/routes/browser'
import '@/routes/diff'
import '@/routes/terminal'
import '@/routes/terminal/events'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import type { Run } from '@/lib/types'
import type * as apiModule from '@/lib/api'
import { alice, approval, bob, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const tabs = runTabs.map((tab) => tab.route)

function seed(over: Partial<Run> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice },
    runs: { run_1: toRecord(run(over)) },
    info: serverInfo,
    inbox: {},
    approvalsByRun: {},
  })
}

function renderTab(name: string) {
  const View = lookupRoute(name)
  if (!View) throw new Error(`route not registered: ${name}`)
  return render(<View params={{ runId: 'run_1' }} />).container
}

function runHeader(name: string) {
  const view = renderTab(name)
  const title = within(view).getByRole('heading', { level: 1 })
  const bar = title.closest('header')
  if (!bar) throw new Error('RunHeader heading is not inside a header')
  return bar
}

beforeEach(() => {
  StubSocket.install()
})

afterEach(() => vi.unstubAllGlobals())

describe('run header', () => {
  it.each(tabs)('names the run state on the %s tab', (name) => {
    seed()
    const bar = runHeader(name)

    expect(within(bar).getByText('Working')).toBeDefined()
  })

  it.each(tabs)('says the viewer is needed for a pending approval on the %s tab', (name) => {
    seed()
    useStore.getState().setInbox(workspace.id, [approval()])
    const bar = runHeader(name)

    expect(within(bar).getByText('Needs you')).toBeDefined()
    expect(within(bar).getByText('Permission: write src/checkout.ts')).toBeDefined()

    fireEvent.click(within(bar).getByRole('button', { name: /Requests: 1 approval/ }))
    expect(useStore.getState().route).toEqual({ name: 'approvals', params: {} })
  })

  it("says whose turn it is on another member's waiting run", () => {
    seed({ member_id: bob.id, status: 'needs-attention' })
    useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob } })
    const bar = runHeader('events')

    expect(within(bar).getByText('Working')).toBeDefined()
    expect(within(bar).getByText('Waiting for Bob')).toBeDefined()
  })

  it('updates outstanding native requests and ignores a stale route snapshot', () => {
    const question = { id: 'same', session_id: 'foreground', kind: 'question' as const }
    const permission = { id: 'same', session_id: 'background', kind: 'permission' as const }
    seed({ pending_inputs: [question, permission] })
    const bar = runHeader('events')
    const oldSnapshot = { ...useStore.getState().runs.run_1 }
    expect(within(bar).getByText('Needs you')).toBeDefined()
    expect(within(bar).getByRole('button', { name: /Requests: 1 question in Terminal; 1 permission request in Terminal/ })).toBeDefined()

    act(() => useStore.getState().applyRunInput('run_1', [permission]))
    fireEvent.click(within(bar).getByRole('button', { name: /Requests: 1 permission request/ }))
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: 'run_1' } })
    act(() => useStore.getState().applyRunInput('run_1', []))
    act(() => useStore.getState().upsertRun(oldSnapshot))
    expect(within(bar).queryByRole('button', { name: /Requests:/ })).toBeNull()
    expect(within(bar).getByText('Working')).toBeDefined()

    act(() => useStore.getState().applyRunStatus('run_1', 'needs-attention', undefined, '2026-08-14T12:00:00Z'))
    expect(within(bar).getByText('Needs you')).toBeDefined()
    expect(within(bar).getByText(/^Agent idle for/)).toBeDefined()
  })

  it.each(tabs)('shows the finished state on the %s tab', (name) => {
    seed({ status: 'merged' })
    const bar = runHeader(name)

    expect(within(bar).getByText('Done')).toBeDefined()
  })

  it.each(tabs)('says the reason in plain words once, keeping the raw one in details, on the %s tab', (name) => {
    const reason = 'stalled: no output or file changes for 15s'
    seed({ status: 'needs-attention', reason })
    const bar = runHeader(name)

    expect(within(bar).getAllByText(/^No activity for/)).toHaveLength(1)
    expect(within(bar).getByText(`needs-attention - ${reason}`)).toBeDefined()
  })

  it.each(tabs)('marks the %s tab as the open one in the strip', (name) => {
    seed()
    renderTab(name)
    const strip = within(screen.getByRole('tablist', { name: 'Run tabs' }))

    for (const tab of runTabs) {
      const button = strip.getByRole('tab', { name: tab.label })
      expect(button.getAttribute('aria-selected')).toBe(String(tab.route === name))
    }
  })

  it.each(tabs)('discloses the complete task and metadata on the %s tab', async (name) => {
    const task = `Goal: fix conflict communication for mission runs.\n${'x'.repeat(2000)}`
    const now = Date.now()
    const commit = 'abcdef0123456789abcdef0123456789abcdef01'
    seed({
      title: '',
      task,
      account_member_id: bob.id,
      created_at: new Date(now - 7_200_000).toISOString(),
      started_at: new Date(now - 3_600_000).toISOString(),
      last_commit: commit,
      last_commit_at: new Date(now - 1_800_000).toISOString(),
    })
    useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob } })
    const bar = runHeader(name)
    const trigger = within(bar).getByText('Task and details')
    const disclosure = trigger.closest('details')!

    expect(disclosure.open).toBe(false)
    expect(within(bar).getByRole('heading', { level: 1 }).textContent).toBe(
      'Goal: fix conflict communication for mission runs.',
    )
    await userEvent.click(trigger)

    expect(disclosure.open).toBe(true)
    expect(disclosure.querySelector('p')?.textContent).toBe(task)
    const details = within(disclosure)
    expect(details.getByText('Owner').nextElementSibling?.textContent).toContain('Alice')
    expect(details.getByText('Agent account').nextElementSibling?.textContent).toContain('Bob')
    expect(details.getByText('Created').nextElementSibling?.textContent).toBe('2 hours ago')
    expect(details.getByText('Changed').nextElementSibling?.textContent).toBe('1 hour ago')
    expect(details.getByText('Last commit').nextElementSibling?.textContent).toBe(
      'abcdef01 30 minutes ago',
    )
    expect(details.getByTitle(commit).textContent).toBe('abcdef01')

    await userEvent.click(trigger)
    expect(disclosure.open).toBe(false)
  })

  it('keeps details and the branch available for a titled run without a task', async () => {
    seed({ task: '', title: 'Terminal title', member_id: 'missing-owner', account_member_id: 'missing-account' })
    const bar = runHeader('events')
    expect(within(bar).getByText('aether/run-1-checkout')).toBeDefined()
    const trigger = within(bar).getByText('Task and details')

    await userEvent.click(trigger)

    const disclosure = trigger.closest('details')!
    expect(disclosure.open).toBe(true)
    const details = within(disclosure)
    expect(details.getByText('Owner').nextElementSibling?.textContent).toContain('missing-owner')
    expect(details.getByText('Agent account').nextElementSibling?.textContent).toContain('missing-account')
  })

  it.each([undefined, alice.id])('omits a redundant agent account (%s)', async (account_member_id) => {
    seed({ account_member_id })
    const bar = runHeader('events')
    const trigger = within(bar).getByText('Task and details')

    await userEvent.click(trigger)

    const details = within(trigger.closest('details')!)
    expect(details.getByText('Owner').nextElementSibling?.textContent).toContain('Alice')
    expect(details.queryByText('Agent account')).toBeNull()
  })

  it('omits a last commit without its timestamp', async () => {
    seed({ last_commit: 'a'.repeat(40), last_commit_at: null })
    const bar = runHeader('events')
    const trigger = within(bar).getByText('Task and details')

    await userEvent.click(trigger)

    expect(within(trigger.closest('details')!).queryByText('Last commit')).toBeNull()
  })

  it('warns that a run is protected on the tab that steers it', () => {
    seed({ protected: true })
    const bar = runHeader('terminal')

    expect(within(bar).getByTitle(/^Protected:/)).toBeDefined()
  })
})
