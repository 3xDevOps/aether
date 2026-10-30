import { render, screen, within } from '@testing-library/react'
import { RunCommandConfirmation } from '@/components/run-command-confirmation'
import type { Child, Run } from '@/lib/types'
import { ChildRuns, costText } from '@/routes/run/child-runs'
import { useStore } from '@/store'
import { useChildSettings } from '@/store/children'
import { toRecord } from '@/store/runs'
import { alice, bob, fakeApi, run, serverInfo, workspace } from '@/test/fixtures'

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
    run_id: 'run_c1', parent_run_id: 'run_1', root_run_id: 'run_1', purpose: 'adversarial review of auth',
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

  it('lists descendants with their account and cost', async () => {
    const parent = run()
    setUp([parent, run({ id: 'run_c1', parent_run_id: 'run_1', account_member_id: bob.id, requested_account_member_id: alice.id })])
    const client = fakeApi({
      runChildren: vi.fn(async () => ({
        root_run_id: 'run_1',
        children: [
          child({ cost_usd: 0.42 }),
          child({ run_id: 'run_c2', parent_run_id: 'run_c1', purpose: 'second opinion', state: 'queued', account_member_id: alice.id, detached: true }),
        ],
        limits: { max_live: 8 },
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
    expect(second.textContent).toContain('Cost not reported')
    expect(within(second).getByText('Detached')).toBeDefined()
    expect(screen.getByText(/At least \$1\.42/)).toBeDefined()
    expect(screen.getByText('Alice $1.00, Bob $0.42')).toBeDefined()
    expect(screen.getByText('1 of 8 busy, 1 queued, 2 started')).toBeDefined()
  })
})

describe('kill confirmation', () => {
  const confirmation = { title: 'Kill this run?', body: 'The agent stops immediately.', action: 'Kill run', stopsChildren: true }

  it('says unfinished child runs stop too', () => {
    const parent = run()
    setUp([
      parent,
      run({ id: 'run_c1', parent_run_id: 'run_1' }),
      run({ id: 'run_c2', parent_run_id: 'run_1', status: 'completed' }),
      run({ id: 'run_c3', parent_run_id: 'run_1', detached: true }),
    ])
    render(<RunCommandConfirmation run={toRecord(parent)} confirmation={confirmation} onConfirm={vi.fn()} onClose={() => {}} />)
    const dialog = screen.getByRole('alertdialog')
    expect(within(dialog).getByText('Its unfinished child run stops too, with any runs it started.')).toBeDefined()
  })
})
