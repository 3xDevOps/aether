import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { StatusBar } from '@/components/shell/status-bar'
import { ApiError } from '@/lib/api'
import type { PresenceEntry } from '@/lib/types'
import { Board } from '@/routes/board'
import { TeamStatus } from '@/routes/team'
import { ApprovalInbox, ApprovalStatus } from '@/routes/team/approvals'
import { BudgetStatus } from '@/routes/team/budget'
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
import { hintOn } from '@/test/tooltip'

const watching: PresenceEntry = {
  member_id: bob.id,
  state: 'watching',
  watching: ['run_1'],
  last_seen: '2026-08-14T10:04:00Z',
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: { run_1: toRecord(run()) },
    inbox: {},
    inboxError: null,
    presence: [],
    budgets: {},
    showDecided: false,
    acked: {},
    pausedRuns: {},
    hydrated: true,
    hydrationError: null,
    lastSeq: 0,
    route: { name: 'board', params: {} },
    ...extra,
  })
}

describe('team status bar', () => {
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
    seed({ route: { name: 'terminal', params: { runId: 'run_1' } } })
    render(<TeamStatus client={client} />)

    expect(await screen.findByText('1 waiting')).toBeDefined()
    expect(screen.getByText('$0.50')).toBeDefined()
    // A budget warns and reports being past its cap. It never stops a run,
    // so nothing here may say that it did.
    expect(screen.getByText('nearing the cap')).toBeDefined()
    expect(screen.getByLabelText('Bob')).toBeDefined()
    expect(client.presenceHeartbeat).toHaveBeenCalledWith(workspace.id)
  })

  // Workspaces are few and long-lived, and both of these readouts claim a
  // whole-deployment fact - the worst budget state anywhere, and the size of
  // the shared queue - so the refresh covers every workspace, including the
  // ones with nothing running in them.
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
    seed({ route: { name: 'terminal', params: { runId: 'run_1' } } })
    render(<TeamStatus client={fakeApi({ approvalList, presenceHeartbeat })} />)

    expect(await screen.findByText('1 waiting')).toBeDefined()
    const reads = approvalList.mock.calls.length
    const beats = presenceHeartbeat.mock.calls.length

    // A pocketed phone froze both timers: presence has already expired
    // server-side and an approval may have arrived while it was away.
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })

    expect(approvalList.mock.calls.length).toBeGreaterThan(reads)
    expect(presenceHeartbeat.mock.calls.length).toBeGreaterThan(beats)
  })

  it('says nothing when no workspace is chosen at all', async () => {
    const client = fakeApi()
    seed({ activeWorkspace: '', route: { name: 'board', params: {} } })

    await heartbeat(useStore, client)

    expect(client.presenceHeartbeat).not.toHaveBeenCalled()
  })

  // A workspace does not stop being over its cap when its last run finishes.
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
    render(<TeamStatus client={client} />)

    expect(await screen.findByText('past the cap')).toBeDefined()
    // $0.50 from the live workspace and $14 from the finished one.
    expect(screen.getByText('$14.50')).toBeDefined()
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
    render(<BudgetStatus />)

    // $0.50 + $1.00, and the trailing + because one run reported no usage
    // at all: the total is a floor, not a measurement.
    expect(screen.getByText('$1.50+')).toBeDefined()
    expect(screen.getByText('past the cap')).toBeDefined()
  })

  it('lights the disk gauge from the read server.info cannot carry', async () => {
    const client = fakeApi()
    seed({ info: { ...serverInfo, disk: undefined } })
    render(<StatusBar />)
    expect(screen.queryByLabelText('Disk usage')).toBeNull()

    await act(async () => {
      await refreshTeam(useStore, client)
    })

    expect(screen.getByLabelText('Disk usage').textContent).toContain(
      '512 MB / 2.0 GB',
    )
  })

  it('breaks the disk gauge down into what an operator can reclaim', async () => {
    const client = fakeApi()
    seed({ info: { ...serverInfo, disk: undefined } })
    render(<StatusBar />)

    await act(async () => {
      await refreshTeam(useStore, client)
    })

    // The four directories that grow without bound; a bare filesystem
    // total says the disk is filling but not what is filling it.
    const detail = screen.getByLabelText('Disk usage').getAttribute('title')
    expect(detail).toContain('Worktrees 256 MB')
    expect(detail).toContain('Transcripts 128 MB')
    expect(detail).toContain('Database 64 MB')
    expect(detail).toContain('Repos 512 MB')
  })

  // An upgraded server starts reporting a component while the filesystem
  // totals sit unchanged. Comparing only the totals would keep the stored
  // reading and leave the tooltip a component short until the disk moved.
  it('takes a new breakdown even when the totals have not moved', async () => {
    const client = fakeApi()
    const stale = await client.disk()
    seed({ info: { ...serverInfo, disk: { ...stale, repo_bytes: undefined } } })
    render(<StatusBar />)
    expect(
      screen.getByLabelText('Disk usage').getAttribute('title'),
    ).not.toContain('Repos')

    await act(async () => {
      await refreshTeam(useStore, client)
    })

    expect(screen.getByLabelText('Disk usage').getAttribute('title')).toContain(
      'Repos 512 MB',
    )
  })

  // The readout itself can only fit "queue unreadable"; the server's own
  // refusal is what an operator needs, so the hint carries it verbatim.
  it('keeps the server refusal on the queue readout', async () => {
    seed({ inboxError: 'approval.list: database is locked' })
    render(<ApprovalStatus />)

    expect(await hintOn(screen.getByRole('button', { name: 'queue unreadable' }))).toBe(
      'approval.list: database is locked',
    )
  })
})

describe('run card contributions', () => {
  it('carries the approval badge and the watcher avatars', async () => {
    seed({ inbox: { [workspace.id]: [approval()] }, presence: [watching] })
    render(<Board />)

    const card = screen.getByRole('article')
    expect(await hintOn(within(card).getByRole('button', { name: '1' }))).toBe(
      '1 waiting on a decision',
    )
    expect(within(card).getByTitle('Watching: Bob')).toBeDefined()
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

  it('opens the run behind a request on its terminal tab', async () => {
    const client = fakeApi({ approvalList: vi.fn(async () => [approval()]) })
    seed({ inbox: { [workspace.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    fireEvent.click(await screen.findByText('rewrite the checkout flow'))

    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: 'run_1' },
    })
  })

  // The row names the scope the request belongs to, so a shared queue says
  // which workspace each decision is about.
  it('names the workspace each request came from', async () => {
    const client = fakeApi({ approvalList: vi.fn(async () => [approval()]) })
    seed({ inbox: { [workspace.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    expect(await screen.findByText(workspace.name)).toBeDefined()
  })

  it('is titled the word its nav entry uses', async () => {
    const client = fakeApi({ approvalList: vi.fn(async () => [approval()]) })
    seed({ inbox: { [workspace.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    expect(await screen.findByRole('heading', { name: 'Approvals' })).toBeDefined()
  })

  it('reports a failed read instead of saying nothing is waiting', async () => {
    // A swallowed 403 used to render as an empty queue, which reads as "no
    // agent is blocked" on the surface a member now watches for exactly that.
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
    // The request is still open, so the buttons are still there.
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
  })
})
