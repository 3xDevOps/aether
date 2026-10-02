import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { lookupRoute } from '@/routes/registry'
import { runTabs } from '@/routes/terminal/tabs'
import '@/routes/browser'
import '@/routes/diff'
import '@/routes/run'
import '@/routes/terminal'
import '@/routes/terminal/events'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import type { Run } from '@/lib/types'
import type * as apiModule from '@/lib/api'
import { alice, approval, run, serverInfo, workspace } from '@/test/fixtures'
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

  it.each(tabs)('keeps execution and pending approval independent on the %s tab', (name) => {
    seed()
    useStore.setState({ inbox: { [workspace.id]: [approval()] } })
    const bar = runHeader(name)

    expect(within(bar).getByText('Working')).toBeDefined()
    expect(within(bar).getByRole('button', { name: /Needs input: 1 approval/ })).toBeDefined()
    expect(within(bar).queryByText('Idle')).toBeNull()
  })

  it('updates outstanding native requests independently of work and ignores a stale route snapshot', () => {
    const question = { id: 'same', session_id: 'foreground', kind: 'question' as const }
    const permission = { id: 'same', session_id: 'background', kind: 'permission' as const }
    seed({ pending_inputs: [question, permission] })
    const bar = runHeader('run')
    const oldSnapshot = { ...useStore.getState().runs.run_1 }
    expect(within(bar).getByText('Working')).toBeDefined()
    expect(within(bar).getByRole('button', { name: /Needs input: 1 question in Terminal; 1 permission request in Terminal/ })).toBeDefined()

    act(() => useStore.getState().applyRunInput('run_1', [permission]))
    fireEvent.click(within(bar).getByRole('button', { name: /Needs input: 1 permission request/ }))
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: 'run_1' } })
    act(() => useStore.getState().applyRunInput('run_1', []))
    act(() => useStore.getState().upsertRun(oldSnapshot))
    expect(within(bar).queryByRole('button', { name: /Needs input:/ })).toBeNull()
    expect(within(bar).getByText('Working')).toBeDefined()

    act(() => useStore.getState().applyRunStatus('run_1', 'needs-attention', undefined, '2026-08-14T12:00:00Z'))
    expect(within(bar).getByText('Idle')).toBeDefined()
    expect(within(bar).queryByRole('button', { name: /Needs input:/ })).toBeNull()
  })

  it.each(tabs)('shows the finished state on the %s tab', (name) => {
    seed({ status: 'merged' })
    const bar = runHeader(name)

    expect(within(bar).getByText('Done')).toBeDefined()
  })

  it.each(tabs)('shows the provided run reason once on the %s tab', (name) => {
    const reason = 'stalled: no output or file changes for 15s'
    seed({ status: 'needs-attention', reason })
    const bar = runHeader(name)

    expect(within(bar).getByText(reason)).toBeDefined()
    expect(screen.getAllByText(reason)).toHaveLength(1)
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

  // A swarm worker's prompt is a whole brief; without a terminal title the
  // heading must stay short and the brief stays behind the disclosure.
  it('keeps an untitled run with a long prompt to a short heading', () => {
    const task = `Goal: fix conflict communication for mission runs.\n${'x'.repeat(2000)}`
    seed({ title: '', task })
    const bar = runHeader('terminal')
    const heading = within(bar).getByRole('heading', { level: 1 })

    expect(heading.textContent).toBe('Goal: fix conflict communication for mission runs.')
    expect(heading.className).toContain('line-clamp-2')
    expect(heading.getAttribute('title')).toBe(heading.textContent)
    expect(within(bar).getByText('View full task')).toBeDefined()
    expect(bar.textContent).toContain('x'.repeat(2000))
  })

  // The shield used to sit on the Overview alone, which is not the tab
  // anyone reaches for the steer button it is warning about.
  it('warns that a run is protected on the tab that steers it', () => {
    seed({ protected: true })
    const bar = runHeader('terminal')

    expect(within(bar).getByTitle(/^Protected:/)).toBeDefined()
  })
})
