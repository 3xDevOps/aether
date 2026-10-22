import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { PresenceEntry } from '@/lib/types'
import { useTeamRefresh } from '@/routes/team'
import { ApprovalInbox } from '@/routes/team/approvals'
import { TeamSummary } from '@/routes/team/budget'
import { StorageSection } from '@/routes/insights/system/storage'
import type { Api } from '@/lib/api'
import { pendingApprovals } from '@/store/approvals'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { heartbeat, refreshInbox, refreshTeam } from '@/routes/team/sync'
import { useStore, type RootState } from '@/store'
import { toRecord } from '@/store/runs'
import {
  alice,
  approval,
  bob,
  budget,
  fakeApi,
  otherWorkspace,
  run,
  serverInfo,
  workspace,
} from '@/test/fixtures'
import { fire } from '@/test/wake'

const watching: PresenceEntry = {
  member_id: bob.id,
  state: 'watching',
  watching: ['run_1'],
  last_seen: '2026-08-14T10:04:00Z',
}

function TeamRefresh({ client }: { client: Api }) {
  useTeamRefresh(client)
  return null
}

const waiting = () => pendingApprovals(useStore.getState().inbox).length

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: { run_1: toRecord(run()) },
    inbox: {},
    inboxError: null,
    presence: [],
    budgets: {},
    showDecided: false,
    pausedRuns: {},
    hydrated: true,
    hydrationError: null,
    lastSeq: 0,
    route: { name: 'board', params: {} },
    ...extra,
  })
}

describe('team refresh and summary', () => {
  it('reads the roster, the queue and the budget, and renders all three', async () => {
    const client = fakeApi({
      presenceRoster: vi.fn(async () => [watching]),
      approvalList: vi.fn(async () => [approval()]),
      budgetGet: vi.fn(async (id: string) =>
        budget(id, {
          state: 'warn',
          budget: { workspace_id: id, limit_usd: 1, warn_usd: 0.4 },
        }),
      ),
    })
    seed({ route: { name: 'run', params: { runId: 'run_1' } } })
    render(<><TeamRefresh client={client} /><TeamSummary /></>)

    await vi.waitFor(() => expect(waiting()).toBe(1))
    // A budget never stops a run, so nothing here may say that it did.
    const line = await screen.findByText('1 online · $0.50 spent, nearing the cap')
    expect(line.getAttribute('title')).toBe('Online: Bob')
    expect(client.presenceHeartbeat).toHaveBeenCalledWith(workspace.id)
  })

  it('reads once per connect, after hydration lists the workspaces, and again on a reconnect', async () => {
    const client = fakeApi()
    seed({ hydrated: false, workspaces: {}, connection: 'connecting' })
    render(<TeamRefresh client={client} />)
    act(() => useStore.setState({ connection: 'live' }))
    act(() => useStore.setState({ workspaces: { [workspace.id]: workspace } }))
    expect(client.presenceRoster).not.toHaveBeenCalled()
    act(() => useStore.setState({ hydrated: true }))
    await vi.waitFor(() => expect(client.budgetGet).toHaveBeenCalledTimes(1))
    expect(client.presenceRoster).toHaveBeenCalledTimes(1)
    expect(client.approvalList).toHaveBeenCalledTimes(1)

    act(() => useStore.setState({ connection: 'offline' }))
    act(() => useStore.setState({ connection: 'live' }))
    await vi.waitFor(() => expect(client.presenceRoster).toHaveBeenCalledTimes(2))
    expect(client.budgetGet).toHaveBeenCalledTimes(2)
  })

  // Both readouts claim a whole-deployment fact, so idle workspaces count too.
  it('refreshes every workspace, not just the ones with live runs', async () => {
    const client = fakeApi()
    seed({
      workspaces: {
        [workspace.id]: workspace,
        [otherWorkspace.id]: otherWorkspace,
      },
      runs: {
        run_1: toRecord(run()),
        run_done: toRecord(
          run({
            id: 'run_done',
            workspace_id: otherWorkspace.id,
            status: 'merged',
          }),
        ),
      },
    })

    await refreshTeam(useStore, client)

    expect(client.approvalList).toHaveBeenCalledWith(workspace.id, false)
    expect(client.approvalList).toHaveBeenCalledWith(otherWorkspace.id, false)
    expect(client.budgetGet).toHaveBeenCalledWith(workspace.id)
    expect(client.budgetGet).toHaveBeenCalledWith(otherWorkspace.id)
  })

  // Presence is keyed on (member, workspace): beating every workspace would
  // report the user online in workspaces they have never opened.
  it('beats only the workspace the view is pointed at', async () => {
    const client = fakeApi()
    seed({
      workspaces: {
        [workspace.id]: workspace,
        [otherWorkspace.id]: otherWorkspace,
      },
      activeWorkspace: '',
      route: { name: 'workspace', params: { workspaceId: otherWorkspace.id } },
    })

    await heartbeat(useStore, client)

    expect(client.presenceHeartbeat).toHaveBeenCalledTimes(1)
    expect(client.presenceHeartbeat).toHaveBeenCalledWith(otherWorkspace.id)
  })

  it('re-reads the queue and beats presence when the tab returns', async () => {
    const approvalList = vi.fn(async () => [approval()])
    const presenceHeartbeat = vi.fn(async () => 90)
    seed({ route: { name: 'run', params: { runId: 'run_1' } } })
    render(<TeamRefresh client={fakeApi({ approvalList, presenceHeartbeat })} />)

    await vi.waitFor(() => expect(waiting()).toBe(1))
    const reads = approvalList.mock.calls.length
    const beats = presenceHeartbeat.mock.calls.length
    const mounted = Date.now()

    // Within the floor, flipping between apps must not fan out a request per
    // workspace; the single heartbeat still goes.
    await act(async () => {
      fire('visibilitychange')
    })
    expect(approvalList.mock.calls.length).toBe(reads)
    expect(presenceHeartbeat.mock.calls.length).toBeGreaterThan(beats)

    // Past the floor, a pocketed phone gets the read it came back for: an
    // approval may have arrived while its timers were frozen.
    const clock = vi.spyOn(Date, 'now').mockReturnValue(mounted + 3_000)
    try {
      await act(async () => {
        fire('visibilitychange')
      })
      expect(approvalList.mock.calls.length).toBeGreaterThan(reads)
    } finally {
      clock.mockRestore()
    }
  })

  it('says nothing when no workspace is chosen at all', async () => {
    const client = fakeApi()
    seed({ activeWorkspace: '', route: { name: 'board', params: {} } })

    await heartbeat(useStore, client)

    expect(client.presenceHeartbeat).not.toHaveBeenCalled()
  })

  it('keeps an over-cap workspace in the readout after its last run finishes', async () => {
    const client = fakeApi({
      budgetGet: vi.fn(async (id: string) =>
        id === otherWorkspace.id
          ? budget(id, {
              state: 'exceeded',
              budget: { workspace_id: id, limit_usd: 1 },
              spend: {
                runs: 1,
                metered_runs: 1,
                unmetered_runs: 0,
                input_tokens: 10,
                output_tokens: 5,
                cost_usd: 14,
              },
            })
          : budget(id),
      ),
    })
    seed({
      workspaces: {
        [workspace.id]: workspace,
        [otherWorkspace.id]: otherWorkspace,
      },
      runs: {
        run_1: toRecord(run()),
        run_done: toRecord(
          run({
            id: 'run_done',
            workspace_id: otherWorkspace.id,
            status: 'merged',
          }),
        ),
      },
    })
    render(<><TeamRefresh client={client} /><TeamSummary /></>)

    // $0.50 from the live workspace and $14 from the finished one.
    expect(await screen.findByText('$14.50 spent, past the cap')).toBeDefined()
  })

  it('rolls spend up across workspaces, worst state and unmetered floor first', () => {
    seed({
      workspaces: {
        [workspace.id]: workspace,
        [otherWorkspace.id]: otherWorkspace,
      },
      budgets: {
        [workspace.id]: budget(workspace.id, {
          budget: { workspace_id: workspace.id, limit_usd: 10 },
        }),
        [otherWorkspace.id]: budget(otherWorkspace.id, {
          state: 'exceeded',
          budget: { workspace_id: otherWorkspace.id, limit_usd: 1 },
          spend: {
            runs: 2,
            metered_runs: 1,
            unmetered_runs: 1,
            input_tokens: 10,
            output_tokens: 5,
            cost_usd: 1,
          },
        }),
      },
    })
    render(<TeamSummary />)

    // One run reported no usage, so the total is a floor.
    expect(screen.getByText('at least $1.50 spent, past the cap')).toBeDefined()
  })

  it.each([
    [0, 'no spend yet'],
    [1, 'no spend reported'],
  ])('words a zero spend with %i unmetered runs as "%s"', (unmetered, words) => {
    seed({
      budgets: {
        [workspace.id]: budget(workspace.id, {
          spend: { runs: unmetered, metered_runs: 0, unmetered_runs: unmetered, input_tokens: 0, output_tokens: 0, cost_usd: 0 },
        }),
      },
    })
    render(<TeamSummary />)

    expect(screen.getByText(words)).toBeDefined()
  })

  it('lights the disk gauge from the read server.info cannot carry', async () => {
    const client = fakeApi()
    seed({ info: { ...serverInfo, disk: undefined } })
    render(<StorageSection />)
    expect(screen.queryByLabelText('Disk usage')).toBeNull()

    await act(async () => {
      await refreshTeam(useStore, client)
    })

    expect(screen.getByLabelText('Disk usage').textContent).toContain(
      '512 MB of 2.0 GB used',
    )
  })

  it('breaks the disk gauge down into attributed components', async () => {
    const client = fakeApi()
    seed({ info: { ...serverInfo, disk: undefined } })
    render(<StorageSection />)

    await act(async () => {
      await refreshTeam(useStore, client)
    })

    const detail = screen.getByRole('list', { name: 'Aether data' }).textContent
    expect(detail).toContain('Worktrees256 MB')
    expect(detail).toContain('Transcripts128 MB')
    expect(detail).toContain('Database64 MB')
    expect(detail).toContain('Repositories512 MB')
  })

  // An upgraded server can add a component while the filesystem totals stay put.
  it('takes a new breakdown even when the totals have not moved', async () => {
    const client = fakeApi()
    const stale = await client.disk()
    const current = {
      ...stale,
      docker: { images_bytes: 512, error: 'Docker volume accounting unavailable' },
      warnings: ['Evidence measurement incomplete'],
    }
    client.disk = vi.fn(async () => current)
    seed({ info: { ...serverInfo, disk: { ...stale, repo_bytes: undefined } } })
    render(<StorageSection />)
    expect(screen.getByRole('list', { name: 'Aether data' }).textContent).not.toContain('Repositories')

    await act(async () => {
      await refreshTeam(useStore, client)
    })

    expect(screen.getByRole('list', { name: 'Aether data' }).textContent).toContain('Repositories512 MB')
    expect(useStore.getState().info?.disk).toEqual(current)

    const failure = new Error('disk RPC unavailable')
    client.disk = vi.fn(async () => { throw failure })
    await act(async () => {
      await refreshTeam(useStore, client)
    })
    expect(useStore.getState().info?.disk).toBeUndefined()
    expect(screen.queryByRole('meter', { name: 'Disk used' })).toBeNull()
    expect(screen.getByText(failure.message)).toBeDefined()

    client.disk = vi.fn(async () => current)
    await act(async () => {
      await refreshTeam(useStore, client)
    })
    expect(useStore.getState().info?.disk).toEqual(current)
    expect(screen.queryByText(failure.message)).toBeNull()
  })

  // Stuck reads would fill the browser's six HTTP/1.1 connections to the gateway.
  it('keeps one disk read in flight and gives up on it after 10 s', async () => {
    const disk = vi.fn<Api['disk']>((signal) => new Promise((_, reject) => {
      signal?.addEventListener('abort', () => reject(signal.reason))
    }))
    const client = fakeApi({ disk })
    seed({ info: { ...serverInfo, disk: undefined } })

    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const reads = Promise.all([refreshTeam(useStore, client), refreshTeam(useStore, client)])
      expect(disk).toHaveBeenCalledTimes(1)
      vi.advanceTimersByTime(10_000)
      await reads
      expect(useStore.getState().info?.diskError).toBe('Disk usage request timed out after 10 seconds')

      disk.mockResolvedValueOnce(serverInfo.disk!)
      await refreshTeam(useStore, client)
      expect(disk).toHaveBeenCalledTimes(2)
      expect(useStore.getState().info?.disk).toEqual(serverInfo.disk)
    } finally {
      vi.useRealTimers()
    }
  })

  it('lists what each container holds outside its mounted folders', () => {
    seed({ info: { ...serverInfo, disk: { ...serverInfo.disk!, containers: undefined } } })
    const { unmount } = render(<StorageSection />)
    expect(screen.queryByText('Container writable layers')).toBeNull()
    unmount()

    seed({
      info: {
        ...serverInfo,
        disk: {
          ...serverInfo.disk!,
          containers: [
            { owner_kind: 'run', owner_id: 'run_1', bytes: 3 * 1024 * 1024 * 1024 },
            { owner_kind: 'member', owner_id: bob.id, bytes: 1024 * 1024 * 1024 },
          ],
          containers_measured_at: '2026-08-14T10:04:00Z',
          containers_error: 'Docker container sizes unavailable: request timed out.',
        },
      },
    })
    render(<StorageSection />)
    expect(screen.getByText(/2 containers hold 4\.0 GB/)).toBeDefined()
    const items = screen.getAllByRole('button').map((item) => item.textContent)
    expect(items).toHaveLength(1)
    expect(items[0]).toContain('(run_1)3.0 GB')
    expect(screen.getByText(`environment ${bob.display_name} (${bob.id})`)).toBeDefined()
    expect(screen.getByText('Docker container sizes unavailable: request timed out.')).toBeDefined()
  })
})

describe('approval inbox', () => {
  it('decides through the gateway and shows who decided', async () => {
    const client = fakeApi({
      approvalList: vi.fn(async () => [approval()]),
      approvalDecide: vi.fn(async () =>
        approval({
          decision: 'approved',
          decided_by: alice.id,
          decided_at: '2026-08-14T10:06:00Z',
        }),
      ),
    })
    seed({ inbox: { [workspace.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))

    await screen.findByText('Alice')
    expect(
      screen.getByRole('listitem').textContent?.replace(/\s+/g, ' '),
    ).toContain('Approved by Alice')
    expect(client.approvalDecide).toHaveBeenCalledWith('run_1', 'apr_1', true)
  })

  it('shows Denying… on the pressed button and locks only that request', async () => {
    const other = approval({ id: 'apr_2', action: 'deploy' })
    const client = fakeApi({
      approvalList: vi.fn(async () => [approval(), other]),
      approvalDecide: vi.fn(() => new Promise<never>(() => {})),
    })
    seed({ inbox: { [workspace.id]: [approval(), other] } })
    render(<ApprovalInbox params={{}} client={client} />)

    const [first, second] = screen.getAllByRole('listitem').map((item) => within(item))
    fireEvent.click(first!.getByRole('button', { name: 'Deny' }))
    expect(first!.getByRole('button', { name: 'Denying…' })).toHaveProperty('disabled', true)
    expect(first!.getByRole('button', { name: 'Approve' })).toHaveProperty('disabled', true)
    expect(second!.getByRole('button', { name: 'Deny' })).toHaveProperty('disabled', false)
  })

  it('keeps a decision locked past the deadline with no Cancel, so no second decision races it', () => {
    const client = fakeApi({ approvalDecide: vi.fn(() => new Promise<never>(() => {})) })
    seed({ inbox: { [workspace.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      expect(screen.getByRole('button', { name: 'Still waiting for the server…' })).toHaveProperty('disabled', true)
      expect(screen.getByRole('button', { name: 'Deny' })).toHaveProperty('disabled', true)
      expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('opens the run behind a request on its terminal tab', async () => {
    const client = fakeApi({ approvalList: vi.fn(async () => [approval()]) })
    seed({ inbox: { [workspace.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    fireEvent.click(await screen.findByText('rewrite the checkout flow'))

    expect(useStore.getState().route).toEqual({
      name: 'run',
      params: { runId: 'run_1' },
    })
  })

  it('names the workspace each request came from', async () => {
    const client = fakeApi({ approvalList: vi.fn(async () => [approval()]) })
    seed({ inbox: { [workspace.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    expect(await screen.findByText(workspace.name)).toBeDefined()
  })

  it('reports a failed read instead of saying nothing is waiting', async () => {
    const client = fakeApi({
      approvalList: vi.fn(async () => {
        throw new ApiError(403, 'approval.list: permission denied')
      }),
    })
    seed({ inbox: {} })
    render(<ApprovalInbox params={{}} client={client} />)

    expect(await screen.findByText(/permission denied/)).toBeDefined()
    expect(screen.queryByText('Nothing is waiting on a decision.')).toBeNull()
  })

  it('lists the workspaces that answered beside the failure, then clears it', async () => {
    const approvalList = vi
      .fn()
      .mockImplementation(async (id: string) => {
        if (id === workspace.id) throw new ApiError(403, 'approval.list: denied')
        return [approval({ workspace_id: otherWorkspace.id })]
      })
    seed({
      workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    })
    render(<ApprovalInbox params={{}} client={fakeApi({ approvalList })} />)

    // A workspace that answered is still shown; the failure sits above it.
    expect(await screen.findByText(/denied/)).toBeDefined()
    expect(screen.getByText(otherWorkspace.name)).toBeDefined()

    approvalList.mockImplementation(async () => [])
    await act(() => refreshInbox(useStore, fakeApi({ approvalList })))

    expect(screen.queryByText(/denied/)).toBeNull()
    expect(screen.getByText('Nothing is waiting on a decision.')).toBeDefined()
  })

  it('cannot let a slow failed read overwrite a newer good one', async () => {
    seed()
    let reject: (e: Error) => void = () => {}
    const slow = fakeApi({
      approvalList: () => new Promise((_, r) => { reject = () => r(new Error('stale: denied')) }),
    })
    const first = refreshInbox(useStore, slow)
    await refreshInbox(useStore, fakeApi({ approvalList: async () => [approval()] }))
    expect(useStore.getState().inboxError).toBeNull()

    reject(new Error('stale: denied'))
    await first

    expect(useStore.getState().inboxError).toBeNull()
  })

  it('surfaces the server refusal instead of guessing at the capability', async () => {
    const client = fakeApi({
      approvalList: vi.fn(async () => [approval()]),
      approvalDecide: vi.fn(async () => {
        throw new ApiError(403, 'approval.decide: permission denied')
      }),
    })
    seed({ inbox: { [workspace.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))

    expect(await screen.findByText(/permission denied/)).toBeDefined()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
  })
})
