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

const tabs = runTabs.map((tab) => tab.route)
const reasonStates = ['needs-attention', 'failed'] as const

const reasonCases = tabs.flatMap((name) =>
  reasonStates.map((status) => [name, status] as const),
)


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

  it.each(tabs)('shows the pending approval as needs-you on the %s tab', (name) => {
    seed()
    useStore.setState({ inbox: { [workspace.id]: [approval()] } })
    const bar = runHeader(name)

    // The domain status still reads `running`; only the presentation state
    // knows the agent is parked on a question.
    expect(within(bar).getByText('Needs you')).toBeDefined()
  })

  it.each(tabs)('shows the finished state on the %s tab', (name) => {
    seed({ status: 'merged' })
    const bar = runHeader(name)

    expect(within(bar).getByText('Done')).toBeDefined()
  })

  it.each(reasonCases)(
    'shows a provided reason once on the %s tab for a %s run',
    (name, status) => {
      const reason = `${status} reason for this run: ${'context '.repeat(32)}`
      seed({ status, reason })
      const bar = runHeader(name)

      expect(within(bar).getByText(reason)).toBeDefined()
      expect(screen.getAllByText(reason)).toHaveLength(1)
      expect(within(bar).getByRole('toolbar')).toBeDefined()
    },
  )

  it.each(tabs)('marks the %s tab as the open one in the strip', (name) => {
    seed()
    renderTab(name)
    const strip = within(screen.getByRole('tablist', { name: 'Run tabs' }))

    for (const tab of runTabs) {
      const button = strip.getByRole('tab', { name: tab.label })
      expect(button.getAttribute('aria-selected')).toBe(String(tab.route === name))
    }
  })

  // The shield used to sit on the Overview alone, which is not the tab
  // anyone reaches for the steer button it is warning about.
  it('warns that a run is protected on the tab that steers it', () => {
    seed({ protected: true })
    const bar = runHeader('terminal')

    expect(within(bar).getByTitle(/^Protected:/)).toBeDefined()
  })
})
