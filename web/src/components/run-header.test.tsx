import { render, screen, within } from '@testing-library/react'
import { lookupRoute } from '@/routes/registry'
import { runTabs } from '@/routes/terminal/tabs'
import '@/routes/diff'
import '@/routes/run'
import '@/routes/terminal'
import '@/routes/terminal/events'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import type { Run } from '@/lib/types'
import { alice, approval, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

// jsdom has no layout engine, so the terminal's fit addon has nothing to
// observe; the header is what these tests are about, not the grid.
class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

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

function header(name: string) {
  renderTab(name)
  return screen.getByRole('banner')
}

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
})

afterEach(() => vi.unstubAllGlobals())

describe('run header', () => {
  it.each(tabs)('names the run state on the %s tab', (name) => {
    seed()
    const bar = header(name)

    expect(within(bar).getByText('Working')).toBeDefined()
    expect(bar.querySelector('.working-dots')).not.toBeNull()
  })

  it.each(tabs)('shows the pending approval as needs-you on the %s tab', (name) => {
    seed()
    useStore.setState({ inbox: { [workspace.id]: [approval()] } })
    const bar = header(name)

    // The domain status still reads `running`; only the presentation state
    // knows the agent is parked on a question.
    expect(within(bar).getByText('Needs you')).toBeDefined()
    expect(bar.querySelector('.working-dots')).toBeNull()
  })

  it.each(tabs)('drops the bounce on the %s tab once the run has finished', (name) => {
    seed({ status: 'merged' })
    const bar = header(name)

    expect(within(bar).getByText('Done')).toBeDefined()
    expect(bar.querySelector('.working-dots')).toBeNull()
  })

  it.each(tabs)('marks the %s tab as the open one in the strip', (name) => {
    seed()
    renderTab(name)
    const strip = within(screen.getByRole('navigation', { name: 'Run tabs' }))

    for (const tab of runTabs) {
      const button = strip.getByRole('button', { name: tab.label })
      expect(button.getAttribute('aria-current')).toBe(
        tab.route === name ? 'page' : null,
      )
    }
  })

  // The shield used to sit on the Overview alone, which is not the tab
  // anyone reaches for the steer button it is warning about.
  it('warns that a run is protected on the tab that steers it', () => {
    seed({ protected: true })
    const bar = header('terminal')

    expect(within(bar).getByTitle(/^Protected:/)).toBeDefined()
  })
})
