import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LaunchChildHint, RunChildNudge } from '@/components/child-nudge'
import { RunCommandConfirmation } from '@/components/run-command-confirmation'
import type { Child, ChildSettings, Run } from '@/lib/types'
import { ChildRuns, costText } from '@/routes/run/child-runs'
import { useStore } from '@/store'
import { useChildSettings } from '@/store/children'
import { toRecord } from '@/store/runs'
import { alice, bob, fakeApi, run, serverInfo, workspace } from '@/test/fixtures'

const settings: ChildSettings = { enabled: false, fallback: true, shared_when_missing: false, configured: false, nudge: 'show' }

function setUp(runs: Run[], methods = ['*']) {
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    runs: Object.fromEntries(runs.map((item) => [item.id, toRecord(item)])),
    capabilities: { gateway: 'remote', methods, ws: [] },
  }, true)
  useChildSettings.setState({ settings: null, error: null })
}

function child(over: Partial<Child>): Child {
  return {
    run_id: 'run_c1', parent_run_id: 'run_1', root_run_id: 'run_1', depth: 1, purpose: 'adversarial review of auth',
    harness: 'codex', mode: 'acp', model: 'gpt-5.5', owner_member_id: alice.id, account_member_id: bob.id,
    state: 'working', created_at: '2026-08-14T10:03:00Z', ...over,
  }
}

describe('child run usage', () => {
  it('never shows $0 for unmetered runs', () => {
    expect(costText({ cost_usd: 0, metered_runs: 0, unmetered_runs: 3 })).toBe('Cost not reported')
    expect(costText({ cost_usd: 1.5, metered_runs: 2, unmetered_runs: 0 })).toBe('$1.50')
    expect(costText({ cost_usd: 1.5, metered_runs: 2, unmetered_runs: 1 })).toBe('At least $1.50: 1 run reports no cost')
  })

  it('lists descendants with their account, cost and badges, and detaches one', async () => {
    const user = userEvent.setup()
    const parent = run()
    setUp([parent, run({ id: 'run_c1', parent_run_id: 'run_1', account_member_id: bob.id, requested_account_member_id: alice.id })])
    const client = fakeApi({
      runChildren: vi.fn(async () => ({
        root_run_id: 'run_1',
        children: [
          child({ cost_usd: 0.42, stale: true }),
          child({ run_id: 'run_c2', depth: 2, parent_run_id: 'run_c1', purpose: 'second opinion', state: 'queued', account_member_id: alice.id, detached: true }),
        ],
        limits: { max_live: 8, max_depth: 3, max_total: 100 },
        usage: {
          runs: 3, metered_runs: 2, unmetered_runs: 1, input_tokens: 12000, output_tokens: 3400, cost_usd: 1.42,
          by_account: [{ account_member_id: alice.id, runs: 1, cost_usd: 1 }, { account_member_id: bob.id, runs: 1, cost_usd: 0.42 }],
        },
      })),
    })
    render(<ChildRuns run={toRecord(parent)} inset={false} client={client} />)
    const list = await screen.findByRole('list', { name: /Child runs of/ })
    const [first, second] = within(list).getAllByRole('listitem')
    expect(first.textContent).toContain("On Bob's account (fallback from Alice)")
    expect(first.textContent).toContain('$0.42')
    expect(within(first).getByText('Stale review')).toBeDefined()
    expect(second.textContent).toContain('Cost not reported')
    expect(within(second).getByText('Detached')).toBeDefined()
    expect(within(second).queryByRole('button', { name: 'Detach' })).toBeNull()
    expect(screen.getByText(/At least \$1\.42/)).toBeDefined()
    expect(screen.getByText('Alice $1.00, Bob $0.42')).toBeDefined()
    expect(screen.getByText('1 of 8 busy, 1 queued, 2 of 100 started')).toBeDefined()

    await user.click(within(first).getByRole('button', { name: 'Detach' }))
    expect(client.runChildDetach).toHaveBeenCalledWith('run_c1')
    await waitFor(() => expect(client.runChildren).toHaveBeenCalledTimes(2))
  })
})

describe('child run nudge', () => {
  it('offers child runs on a refused run, and Not now and Don\'t show again never turn the setting on', async () => {
    const user = userEvent.setup()
    const refused = run({ child_refused: true })
    setUp([refused])
    const client = fakeApi({ childSettings: vi.fn(async () => settings) })
    const { unmount } = render(<RunChildNudge run={toRecord(refused)} client={client} />)
    const nudge = await screen.findByRole('group', { name: 'Let this run start child runs?' })
    await user.click(within(nudge).getByRole('button', { name: 'Not now' }))
    expect(client.childSettings).toHaveBeenLastCalledWith({ nudge: 'later' })
    expect((await screen.findByRole('status')).textContent).toContain('You can turn child runs on later')
    unmount()

    useChildSettings.setState({ settings: null })
    render(<RunChildNudge run={toRecord(refused)} client={client} />)
    await user.click(await screen.findByRole('button', { name: "Don't show again" }))
    expect(client.childSettings).toHaveBeenLastCalledWith({ nudge: 'never' })
    expect((await screen.findByRole('status')).textContent).toContain("We won't suggest this again")
    expect(vi.mocked(client.childSettings).mock.calls.some(([params]) => params?.enabled !== undefined)).toBe(false)
  })

  it('allows child runs for one run only', async () => {
    const user = userEvent.setup()
    const refused = run({ child_refused: true })
    setUp([refused])
    const client = fakeApi({ childSettings: vi.fn(async () => settings) })
    render(<RunChildNudge run={toRecord(refused)} client={client} />)
    await user.click(await screen.findByRole('button', { name: 'Allow for this run' }))
    expect(client.runChildrenAllow).toHaveBeenCalledWith('run_1', 'on')
    expect((await screen.findByRole('status')).textContent).toContain('allowed for this run')
  })

  it('stays away from runs the viewer does not own and from a snoozed nudge', async () => {
    const theirs = run({ child_refused: true, member_id: bob.id })
    setUp([theirs])
    const client = fakeApi({ childSettings: vi.fn(async () => settings) })
    render(<RunChildNudge run={toRecord(theirs)} client={client} />)
    await waitFor(() => expect(client.childSettings).toHaveBeenCalled())
    expect(screen.queryByRole('group')).toBeNull()

    setUp([])
    useChildSettings.setState({ settings: { ...settings, nudge: 'snoozed' } })
    render(<LaunchChildHint launchable={3} onLeave={() => {}} client={client} />)
    expect(screen.queryByText(/Agents can start each other/)).toBeNull()
  })

  it('hints in the launch dialog only when two agents could work together', async () => {
    const user = userEvent.setup()
    setUp([])
    const client = fakeApi({ childSettings: vi.fn(async () => settings) })
    const { rerender } = render(<LaunchChildHint launchable={1} onLeave={() => {}} client={client} />)
    await waitFor(() => expect(client.childSettings).toHaveBeenCalled())
    expect(screen.queryByText(/Agents can start each other/)).toBeNull()
    rerender(<LaunchChildHint launchable={2} onLeave={() => {}} client={client} />)
    await user.click(screen.getByRole('button', { name: 'Not now' }))
    expect(client.childSettings).toHaveBeenLastCalledWith({ nudge: 'later' })
    await waitFor(() => expect(screen.queryByText(/Agents can start each other/)).toBeNull())
  })
})

describe('kill confirmation', () => {
  const confirmation = { title: 'Kill this run?', body: 'The agent stops immediately.', action: 'Kill run', stopsChildren: true }

  it('says unfinished child runs stop too and can detach them first', async () => {
    const user = userEvent.setup()
    const parent = run()
    setUp([
      parent,
      run({ id: 'run_c1', parent_run_id: 'run_1' }),
      run({ id: 'run_c2', parent_run_id: 'run_1', status: 'completed' }),
      run({ id: 'run_c3', parent_run_id: 'run_1', detached: true }),
    ])
    const client = fakeApi()
    const onConfirm = vi.fn()
    render(<RunCommandConfirmation run={toRecord(parent)} confirmation={confirmation} onConfirm={onConfirm} onClose={() => {}} client={client} />)
    const dialog = screen.getByRole('alertdialog')
    expect(within(dialog).getByText('Its unfinished child run stops too, with any runs it started.')).toBeDefined()
    await user.click(within(dialog).getByRole('checkbox', { name: 'Detach them and keep them running' }))
    await user.click(within(dialog).getByRole('button', { name: 'Kill run' }))
    await waitFor(() => expect(onConfirm).toHaveBeenCalled())
    expect(client.runChildDetach).toHaveBeenCalledTimes(1)
    expect(client.runChildDetach).toHaveBeenCalledWith('run_c1')
    expect(vi.mocked(client.runChildDetach).mock.invocationCallOrder[0]).toBeLessThan(onConfirm.mock.invocationCallOrder[0])
  })

  it('does not stop the run when a detach is refused', async () => {
    const user = userEvent.setup()
    const parent = run()
    setUp([parent, run({ id: 'run_c1', parent_run_id: 'run_1' })])
    const client = fakeApi({ runChildDetach: vi.fn(async () => { throw new Error('not your run') }) })
    const onConfirm = vi.fn()
    render(<RunCommandConfirmation run={toRecord(parent)} confirmation={confirmation} onConfirm={onConfirm} onClose={() => {}} client={client} />)
    await user.click(screen.getByRole('checkbox', { name: 'Detach them and keep them running' }))
    await user.click(screen.getByRole('button', { name: 'Kill run' }))
    await waitFor(() => expect(client.runChildDetach).toHaveBeenCalled())
    expect(onConfirm).not.toHaveBeenCalled()
  })
})
