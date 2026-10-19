import { fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { Api } from '@/lib/api'
import type { InsightsParams } from '@/lib/types'
import { InsightsRoute } from '@/routes/insights'
import { useStore } from '@/store'
import { alice, fakeApi, insights, insightsRun, otherWorkspace, serverInfo, teamInsights, workspace } from '@/test/fixtures'

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    activeWorkspace: '',
    info: { ...serverInfo, member: alice },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    connection: 'live',
    hydrated: true,
  })
}

const empty = { launched: 0, completed: 0, merged: 0, failed: 0, interrupted: 0, abandoned: 0, killed: 0, active: 0, finished: 0 }

const sections = { mine: 'My statistics', everyone: 'Team statistics' } as const

function byScope(params: InsightsParams) {
  return params.scope === 'mine' ? insights() : teamInsights()
}

function renderPage(over: Partial<Api> = {}) {
  seed()
  return render(<InsightsRoute params={{}} client={fakeApi({ insightsGet: vi.fn(async (p: InsightsParams) => byScope(p)), ...over })} />)
}

describe('insights page', () => {
  it('reads the last seven days for me and for everyone in the browser zone', async () => {
    const insightsGet = vi.fn(async (params: InsightsParams) => byScope(params))
    renderPage({ insightsGet })
    await screen.findByRole('region', { name: 'Finished work' })

    const calls = insightsGet.mock.calls.map(([p]) => p)
    expect(calls.map((p) => p.scope).sort()).toEqual(['everyone', 'mine'])
    for (const call of calls) {
      expect(call.workspace_id).toBe('')
      expect(call.time_zone).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone)
      expect(Date.parse(call.to) - Date.parse(call.from)).toBeGreaterThanOrEqual(7 * 86_400_000 - 3_600_000)
    }
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)
    expect(headings.slice(0, 2)).toEqual(['My statistics', 'Team statistics'])
  })

  it('shows the four boxes for me and for the team from the server numbers', async () => {
    renderPage()
    const mine = await screen.findByRole('region', { name: sections.mine })
    const completed = within(mine).getByRole('article', { name: 'Runs / swarms completed' })
    expect(within(completed).getByText('7 runs / 1 swarm')).toBeDefined()
    expect(within(completed).getByText('10 runs launched')).toBeDefined()
    const rate = within(mine).getByRole('article', { name: 'Success rate' })
    expect(within(rate).getByText('40%')).toBeDefined()
    expect(within(rate).getByText('Runs: 3 of 9 runs merged a pull request')).toBeDefined()
    expect(within(rate).getByText('Swarms: 1 of 1 swarm merged a pull request')).toBeDefined()
    const tasks = within(mine).getByRole('article', { name: 'Tasks completed' })
    expect(within(tasks).getByText('4')).toBeDefined()
    expect(within(tasks).getByText('5 agents, 4 tasks accepted')).toBeDefined()
    const used = within(mine).getByRole('article', { name: 'Total tokens used' })
    expect(within(used).getByText('129K tokens')).toBeDefined()
    expect(within(used).getByText('Claude Code used 78% of the 129K tokens')).toBeDefined()
    expect(within(used).getByText('6 runs did not report tokens')).toBeDefined()

    const team = screen.getByRole('region', { name: sections.everyone })
    expect(within(within(team).getByRole('article', { name: 'Runs / swarms completed' })).getByText('10 runs / 1 swarm')).toBeDefined()
    expect(within(within(team).getByRole('article', { name: 'Success rate' })).getByText('40%')).toBeDefined()
    expect(within(within(team).getByRole('article', { name: 'Tasks completed' })).getByText('6')).toBeDefined()
  })

  it('draws seven days, dims the others on hover and switches to swarms', async () => {
    renderPage()
    const mine = await screen.findByRole('region', { name: sections.mine })
    const days = within(mine).getAllByRole('img')
    expect(days).toHaveLength(7)
    const wed = within(mine).getByRole('img', { name: 'Wed, Oct 14: runs 5 done, 1 failed, 0 abandoned' })

    fireEvent.mouseEnter(wed)
    expect(wed.hasAttribute('data-active')).toBe(true)
    expect(days.filter((d) => d.hasAttribute('data-dimmed'))).toHaveLength(6)
    expect(within(mine).getByRole('tooltip').textContent).toContain('Wed, Oct 14')
    fireEvent.mouseLeave(wed.parentElement!.parentElement!)
    expect(days.some((d) => d.hasAttribute('data-dimmed') || d.hasAttribute('data-active'))).toBe(false)
    expect(within(mine).queryByRole('tooltip')).toBeNull()

    fireEvent.click(within(mine).getByRole('button', { name: 'Swarms' }))
    expect(within(mine).getByRole('img', { name: 'Thu, Oct 15: swarms 1 done, 0 failed, 0 abandoned' })).toBeDefined()
    expect(within(mine).getByText('1 swarm ended in the last 7 days')).toBeDefined()
  })

  it('stacks the team chart by member and names them in the legend and tooltip', async () => {
    renderPage()
    const team = await screen.findByRole('region', { name: sections.everyone })
    const legend = within(team).getByRole('list', { name: 'Members' })
    expect(within(legend).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Alice', 'Bob'])

    const wed = within(team).getByRole('img', { name: 'Wed, Oct 14: runs 7 done (Alice 5, Bob 2), 2 failed (Alice 1, Bob 1), 0 abandoned' })
    fireEvent.focus(wed)
    expect(within(within(team).getByRole('tooltip')).getAllByText('Bob')).toHaveLength(2)

    const mine = screen.getByRole('region', { name: sections.mine })
    expect(within(mine).queryByRole('list', { name: 'Members' })).toBeNull()
  })

  it('says so when nothing ended and still draws the week', async () => {
    const zero = { done: 0, failed: 0, abandoned: 0 }
    const quiet = insights({
      totals: empty,
      recent: [],
      needs_you: 0,
      heatmap: [],
      swarms: { launched: 0, completed: 0, cancelled: 0, agents: 0, tasks_accepted: 0 },
      cost: { metered_runs: 0, unmetered_runs: 0, cost_usd: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0, by_agent: [] },
      success: { runs: { succeeded: 0, finished: 0 }, swarms: { succeeded: 0, finished: 0 } },
      outcomes: { runs: zero, swarms: zero, members: [], series: insights().outcomes.series.map((d) => ({ ...d, runs: zero, swarms: zero })) },
    })
    renderPage({ insightsGet: vi.fn(async () => quiet) })
    const mine = await screen.findByRole('region', { name: sections.mine })
    expect(within(within(mine).getByRole('article', { name: 'Runs / swarms completed' })).getByText('0 runs / 0 swarms')).toBeDefined()
    expect(within(within(mine).getByRole('article', { name: 'Success rate' })).getByText('—')).toBeDefined()
    expect(within(within(mine).getByRole('article', { name: 'Total tokens used' })).getByText('0 tokens')).toBeDefined()
    expect(within(mine).getByText('No runs ended in the last 7 days.')).toBeDefined()
    expect(within(mine).getAllByRole('img')).toHaveLength(7)
  })

  it('links the runs that need you to the board', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: '2 runs need you' }))
    expect(useStore.getState().route.name).toBe('board')
  })

  it('waits for the connection instead of loading forever', () => {
    seed()
    useStore.setState({ connection: 'reconnecting' })
    const client = fakeApi()
    render(<InsightsRoute params={{}} client={client} />)
    expect(screen.getByRole('status').textContent).toBe('Reconnecting. Insights load once the server connection is live.')
    expect(client.insightsGet).not.toHaveBeenCalled()
  })

  it('opens a finished run and names its outcome, agent and pull request', async () => {
    seed()
    render(<InsightsRoute params={{}} client={fakeApi()} />)
    const list = await screen.findByRole('region', { name: 'Finished work' })
    const merged = within(list).getByRole('button', { name: 'Add the export button' }).closest('article')!
    expect(within(merged).getByText('Merged')).toBeDefined()
    expect(within(merged).getByRole('link', { name: 'PR #42 (merged)' }).getAttribute('href')).toBe('https://github.com/acme/app/pull/42')
    const interrupted = within(list).getByRole('button', { name: 'Retry flaky upload' }).closest('article')!
    expect(within(interrupted).getByText('Interrupted')).toBeDefined()
    expect(within(interrupted).getByText('docs-site')).toBeDefined()

    fireEvent.click(within(list).getByRole('button', { name: 'Fix the checkout flow' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1' } })
  })

  it('labels every heatmap day with its date and count', async () => {
    seed()
    render(<InsightsRoute params={{}} client={fakeApi()} />)
    const day = await screen.findByRole('img', { name: 'Thursday, October 15, 2026: 3 runs' })
    expect(screen.getByRole('img', { name: 'Friday, April 17, 2026: 0 runs' })).toBeDefined()
    expect(screen.getAllByRole('img', { name: /2026: \d+ runs?$/ })).toHaveLength(182)
    expect(day.getAttribute('tabindex')).toBe('0')
    fireEvent.keyDown(day, { key: 'ArrowUp' })
    expect(document.activeElement).toBe(screen.getByRole('img', { name: 'Wednesday, October 14, 2026: 6 runs' }))
  })

  it('keeps cost behind a collapsed disclosure and says it is a floor', async () => {
    seed()
    render(<InsightsRoute params={{}} client={fakeApi()} />)
    const toggle = await screen.findByRole('button', { name: 'Usage' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('at least $4.12')).toBeNull()
    fireEvent.click(toggle)
    expect(screen.getByText('at least $4.12')).toBeDefined()
    expect(screen.getByText(/metered for 3 of 9 runs \(headless Claude reports cost; other agents do not\)/)).toBeDefined()
  })

  it('says how much of the time is measured', async () => {
    seed()
    render(<InsightsRoute params={{}} client={fakeApi()} />)
    const time = await screen.findByRole('region', { name: 'Time' })
    expect(within(time).getByText('21m')).toBeDefined()
    expect(within(time).getByText('1h 8m')).toBeDefined()
    expect(within(time).getByText(/measured for 6 of 9 runs; Aether records this from Oct 12, 2026 on\./)).toBeDefined()
  })

  it('shows the server error and retries', async () => {
    seed()
    let failed = false
    const insightsGet = vi.fn(async (params: InsightsParams) => {
      if (params.scope === 'everyone' && !failed) {
        failed = true
        throw new ApiError(500, 'insights.get: database is locked')
      }
      return byScope(params)
    })
    render(<InsightsRoute params={{}} client={fakeApi({ insightsGet })} />)
    expect((await screen.findByRole('alert')).textContent).toContain('insights.get: database is locked')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await within(screen.getByRole('region', { name: sections.everyone })).findByRole('list', { name: 'Members' })).toBeDefined()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(within(screen.getByRole('region', { name: sections.mine })).getAllByRole('article')).toHaveLength(4)
  })

  it('says so neutrally when the server predates insights', async () => {
    seed()
    render(<InsightsRoute params={{}} client={fakeApi({ insightsGet: vi.fn(async () => { throw new ApiError(404, 'insights.get: not found') }) })} />)
    expect((await screen.findByRole('status')).textContent).toBe('This server does not report insights. Update the server to see them.')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('refreshes open pull requests one at a time and re-reads once when one merged', async () => {
    seed()
    const open = [
      insightsRun({ run_id: 'run_a', title: 'Merged since', pr_url: 'https://github.com/acme/app/pull/7', pr_state: 'open' }),
      insightsRun({ run_id: 'run_b', title: 'Token expired', pr_url: 'https://github.com/acme/app/pull/8', pr_state: 'open' }),
    ]
    const merged = insightsRun({ ...open[0], pr_state: 'merged' })
    let mineReads = 0
    const insightsGet = vi.fn(async (params: InsightsParams) => {
      if (params.scope === 'everyone') return teamInsights()
      return insights({ recent: mineReads++ === 0 ? open : [merged, open[1]] })
    })
    const runPRStatus = vi.fn<Api['runPRStatus']>(async ({ run_id }) => {
      if (run_id === 'run_b') throw new ApiError(502, 'run.pr.status: github: 401 Bad credentials')
      return { identity: 'alice', account_member_id: alice.id, pull_request: { number: 7, url: open[0].pr_url, state: 'merged', title: '', draft: false, repository: 'acme/app', base_branch: '', head_repository: '', head_branch: '', head_oid: '' }, output: { exit_code: 0, truncated: false } }
    })
    render(<InsightsRoute params={{}} client={fakeApi({ insightsGet, runPRStatus })} />)

    expect(await screen.findByRole('link', { name: 'PR #7 (merged)' })).toBeDefined()
    expect(runPRStatus.mock.calls.map(([p]) => p)).toEqual([{ run_id: 'run_a' }, { run_id: 'run_b' }])
    expect(insightsGet).toHaveBeenCalledTimes(3)
    expect(screen.getByRole('link', { name: 'PR #8 (open)' }).getAttribute('title')).toBe('Could not refresh this pull request: run.pr.status: github: 401 Bad credentials')
  })
})
