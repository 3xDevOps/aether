import { fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { Api } from '@/lib/api'
import type { InsightsParams } from '@/lib/types'
import { InsightsRoute } from '@/routes/insights'
import { rfc3339 } from '@/routes/insights/model'
import { useStore } from '@/store'
import { alice, fakeApi, insights, insightsRun, otherWorkspace, serverInfo, workspace } from '@/test/fixtures'

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

describe('insights page', () => {
  it('reads the browser zone and scope, and compares against the period before in raw numbers', async () => {
    seed()
    const client = fakeApi()
    render(<InsightsRoute params={{}} client={client} />)

    expect(await screen.findByText(/^Last 7 days: 7 runs finished and 1 swarm completed across 2 workspaces\./)).toBeDefined()
    expect(client.insightsGet).toHaveBeenCalledWith(expect.objectContaining({
      workspace_id: '',
      scope: 'mine',
      time_zone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    }))
    const finished = screen.getByRole('article', { name: 'Runs finished' })
    expect(within(finished).getByText('Up from 4 the 7 days before')).toBeDefined()
    const rate = screen.getByRole('article', { name: 'Success rate' })
    expect(within(rate).getByText('77%')).toBeDefined()
    expect(within(rate).getByText('7 finished of 9 ended runs')).toBeDefined()
    expect(within(rate).getByText('66% (4 of 6) the 7 days before')).toBeDefined()
    expect(within(screen.getByRole('article', { name: 'Pull requests opened' })).getByText('1 merged')).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Everyone' }))
    expect(client.insightsGet).toHaveBeenLastCalledWith(expect.objectContaining({ scope: 'everyone' }))
  })

  it('links the runs that need you to the board', async () => {
    seed()
    render(<InsightsRoute params={{}} client={fakeApi()} />)
    fireEvent.click(await screen.findByRole('button', { name: '2 runs need you' }))
    expect(useStore.getState().route.name).toBe('board')
  })

  it('explains pull requests until Aether has recorded one', async () => {
    seed()
    render(<InsightsRoute params={{}} client={fakeApi({ insightsGet: vi.fn(async () => insights({ coverage: { state_times_since: '', pull_requests_since: '' } })) })} />)
    const tile = await screen.findByRole('article', { name: 'Pull requests opened' })
    expect(within(tile).getByText('—')).toBeDefined()
    expect(within(tile).getByText(/None recorded yet/)).toBeDefined()
    expect(screen.getByText(/Working and waiting time is not recorded yet/)).toBeDefined()
  })

  it('reads the chosen bar on its own and clears it', async () => {
    seed()
    const older = insightsRun({ run_id: 'run_9', title: 'Older run past the newest 50', finished_at: '2026-10-14T03:00:00Z' })
    const insightsGet = vi.fn(async (params: InsightsParams) => params.from === rfc3339(new Date(2026, 9, 14)) ? insights({ recent: [older] }) : insights())
    render(<InsightsRoute params={{}} client={fakeApi({ insightsGet })} />)
    const list = await screen.findByRole('region', { name: 'Finished work' })
    expect(within(list).getAllByRole('article')).toHaveLength(3)

    const bar = screen.getByRole('button', { name: /^Wed, Oct 14: 6 runs launched, 5 done, 1 failed, 0 abandoned$/ })
    fireEvent.click(bar)
    expect(bar.getAttribute('aria-pressed')).toBe('true')
    expect(await within(list).findByRole('button', { name: 'Older run past the newest 50' })).toBeDefined()
    expect(within(list).getAllByRole('article')).toHaveLength(1)
    expect(within(list).getByText('Showing runs that ended on Wed, Oct 14.')).toBeDefined()
    expect(insightsGet).toHaveBeenLastCalledWith(expect.objectContaining({ from: rfc3339(new Date(2026, 9, 14)), to: rfc3339(new Date(2026, 9, 15)), scope: 'mine' }))

    fireEvent.keyDown(bar, { key: 'ArrowRight' })
    const next = screen.getByRole('button', { name: /^Thu, Oct 15:/ })
    expect(document.activeElement).toBe(next)
    fireEvent.keyDown(next, { key: 'Enter' })
    expect(insightsGet).toHaveBeenLastCalledWith(expect.objectContaining({ from: rfc3339(new Date(2026, 9, 15)), to: rfc3339(new Date(2026, 9, 16)) }))

    fireEvent.click(within(list).getByRole('button', { name: 'Show every day' }))
    expect(within(list).getAllByRole('article')).toHaveLength(3)
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

  it('offers the next step when the period is empty', async () => {
    seed()
    const result = insights({ totals: empty, previous: empty, recent: [], needs_you: 0, swarms: { launched: 0, completed: 0, cancelled: 0, agents: 0, tasks_accepted: 0 } })
    render(<InsightsRoute params={{}} client={fakeApi({ insightsGet: vi.fn(async () => result) })} />)
    expect(await screen.findByRole('heading', { name: 'No runs in this period.' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'New run' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Show the last 12 months' })).toBeDefined()
  })

  it('shows the server error and retries', async () => {
    seed()
    const insightsGet = vi.fn()
      .mockRejectedValueOnce(new ApiError(500, 'insights.get: database is locked'))
      .mockResolvedValue(insights())
    render(<InsightsRoute params={{}} client={fakeApi({ insightsGet })} />)
    expect((await screen.findByRole('alert')).textContent).toContain('insights.get: database is locked')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('region', { name: 'Finished work' })).toBeDefined()
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
    const insightsGet = vi.fn()
      .mockResolvedValueOnce(insights({ recent: open }))
      .mockResolvedValue(insights({ recent: [merged, open[1]] }))
    const runPRStatus = vi.fn<Api['runPRStatus']>(async ({ run_id }) => {
      if (run_id === 'run_b') throw new ApiError(502, 'run.pr.status: github: 401 Bad credentials')
      return { identity: 'alice', account_member_id: alice.id, pull_request: { number: 7, url: open[0].pr_url, state: 'merged', title: '', draft: false, repository: 'acme/app', base_branch: '', head_repository: '', head_branch: '', head_oid: '' }, output: { exit_code: 0, truncated: false } }
    })
    render(<InsightsRoute params={{}} client={fakeApi({ insightsGet, runPRStatus })} />)

    expect(await screen.findByRole('link', { name: 'PR #7 (merged)' })).toBeDefined()
    expect(runPRStatus.mock.calls.map(([p]) => p)).toEqual([{ run_id: 'run_a' }, { run_id: 'run_b' }])
    expect(insightsGet).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('link', { name: 'PR #8 (open)' }).getAttribute('title')).toBe('Could not refresh this pull request: run.pr.status: github: 401 Bad credentials')
  })
})
