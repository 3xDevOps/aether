import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Run, WaitCondition } from '@/lib/types'
import { WaitConditions } from '@/routes/run/wait-conditions'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, bob, fakeApi, run, serverInfo, workspace } from '@/test/fixtures'

const minute = 60_000

function ago(ms: number): string {
  return new Date(Date.now() - ms).toISOString()
}

function wait(over: Partial<WaitCondition>): WaitCondition {
  return {
    id: 'wait_1', revision: 2, spec: 'pr:acme/api#582:checks', kind: 'pr_checks', repo: 'acme/api', number: 582,
    state: 'pending', observed: { pr_state: 'open', head_sha: '0123456789abcdef', checks: { passed: 14, failed: 0, pending: 3, cancelled: 0, required_only: true, required: { passed: 4, failed: 0, pending: 1, cancelled: 0 }, missing: ['deploy-preview'] } },
    summary: '', consecutive_failures: 0, delivery_attempts: 0, expires_at: ago(-7 * 24 * 60 * minute),
    created_at: ago(20 * minute), last_checked_at: ago(minute), source_checked_at: ago(minute), next_check_at: ago(-minute), ...over,
  }
}

function setUp(target: Run, methods = ['*']) {
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    runs: { [target.id]: toRecord(target), run_2: toRecord(run({ id: 'run_2', title: 'Migrate the store' })) },
    capabilities: { gateway: 'remote', methods, ws: [] },
  }, true)
}

describe('waiting until', () => {
  it('lists each condition with what the server saw, its errors and delivery, and cancels a pending one', async () => {
    const user = userEvent.setup()
    const waiting = run({
      wait_conditions: [
        wait({ id: 'wait_old', revision: 1, spec: 'run:run_2:finished', kind: 'run_finished', repo: undefined, number: undefined, run_id: 'run_2',
          state: 'resolved', outcome: 'completed', observed: {}, resolved_at: ago(30 * minute), delivered_at: ago(29 * minute), next_check_at: undefined }),
        wait({ consecutive_failures: 3, last_error: 'GitHub: 502 Bad Gateway', last_error_at: ago(minute), next_check_at: ago(5 * minute) }),
        wait({ id: 'wait_merge', spec: 'pr:acme/api#582:merged', kind: 'pr_merged', state: 'error', outcome: 'unreadable', observed: { checks: { passed: 0, failed: 0, pending: 0, cancelled: 0, required_only: false } },
          resolved_at: ago(2 * minute), delivery_attempts: 2, last_delivery_error: 'run inbox: run is paused', next_check_at: undefined }),
      ],
    })
    setUp(waiting)
    const client = fakeApi({ runGet: vi.fn(async () => run({ wait_conditions: [] })) })
    render(<WaitConditions run={toRecord(waiting)} inset={false} client={client} />)
    const section = screen.getByRole('region', { name: 'Waiting until' })
    const [checks, merge, old] = within(section).getAllByRole('listitem')
    expect(within(checks).getByRole('link', { name: 'acme/api#582' }).getAttribute('href')).toBe('https://github.com/acme/api/pull/582')
    expect(checks.textContent).toContain('Required checks4 passed, 0 failed, 1 running')
    expect(checks.textContent).toContain('Not reporteddeploy-preview')
    expect(checks.textContent).toContain('All checks14 passed, 0 failed, 3 running')
    expect(checks.textContent).toContain('3 in a row: GitHub: 502 Bad Gateway')
    expect(checks.textContent).toContain('Overdue since')
    expect(merge.textContent).toContain('Unreadable: unreadable')
    expect(merge.textContent).toContain('None reported yet')
    expect(merge.textContent).toContain('Not delivered after 2 attempts: run inbox: run is paused')
    expect(within(merge).queryByRole('button', { name: 'Cancel' })).toBeNull()
    expect(within(old).getByRole('button', { name: 'Migrate the store' })).toBeDefined()
    expect(old.textContent).toContain('Resolved: completed')
    expect(old.textContent).toContain('Delivered to the agent')

    await user.click(within(checks).getByRole('button', { name: 'Cancel' }))
    expect(client.runWaitCancel).toHaveBeenCalledWith('run_1', 'wait_1')
    await waitFor(() => expect(useStore.getState().runs.run_1?.wait_conditions).toEqual([]))
  })

  it('offers no Cancel on a server without run.wait.cancel, and nothing for a run that never waited', () => {
    const waiting = run({ wait_conditions: [wait({})] })
    setUp(waiting, ['run.get'])
    const { rerender } = render(<WaitConditions run={toRecord(waiting)} inset={false} client={fakeApi()} />)
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
    rerender(<WaitConditions run={toRecord(run())} inset={false} client={fakeApi()} />)
    expect(screen.queryByRole('region', { name: 'Waiting until' })).toBeNull()
  })
})
