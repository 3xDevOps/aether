import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { StatusBar } from '@/components/shell/status-bar'
import { ApiError } from '@/lib/api'
import type { PresenceEntry } from '@/lib/types'
import { Board } from '@/routes/board'
import { TeamStatus } from '@/routes/team'
import { ApprovalInbox } from '@/routes/team/approvals'
import { BudgetStatus } from '@/routes/team/budget'
import { heartbeat, refreshTeam } from '@/routes/team/sync'
import { useStore, type RootState } from '@/store'
import { toRecord } from '@/store/runs'
import {
  alice,
  approval,
  bob,
  budget,
  fakeApi,
  otherSession,
  run,
  serverInfo,
  session,
} from '@/test/fixtures'

const watching: PresenceEntry = {
  member_id: bob.id,
  state: 'watching',
  watching: ['run_1'],
  last_seen: '2026-08-14T10:04:00Z',
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    sessions: { [session.id]: session },
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: { run_1: toRecord(run()) },
    inbox: {},
    presence: [],
    budgets: {},
    showDecided: false,
    acked: {},
    pausedRuns: {},
    showIdle: false,
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
          budget: { session_id: id, limit_usd: 1, warn_usd: 0.4 },
        }),
      ),
    })
    seed({ route: { name: 'run', params: { runId: 'run_1' } } })
    render(<TeamStatus client={client} />)

    expect(await screen.findByText('1 waiting')).toBeDefined()
    expect(screen.getByText('$0.50')).toBeDefined()
    // A budget warns and reports being past its cap. It never stops a run,
    // so nothing here may say that it did.
    expect(screen.getByText('nearing the cap')).toBeDefined()
    expect(screen.getByLabelText('Bob')).toBeDefined()
    expect(client.presenceHeartbeat).toHaveBeenCalledWith(session.id)
  })

  // session.list returns every session that ever existed, and all three of
  // these reads are per session on the wire, so the recurring refresh - the
  // one that runs on every burst of events - has to bound what it asks
  // about, or the cost of having the dashboard open grows with the age of
  // the deployment rather than with what is happening on it.
  it('bounds the recurring refresh to live sessions', async () => {
    const client = fakeApi()
    const stale = { ...otherSession, id: 'ses_3', name: 'older still' }
    seed({
      sessions: {
        [session.id]: session,
        [otherSession.id]: otherSession,
        [stale.id]: stale,
      },
      runs: {
        run_1: toRecord(run()),
        run_done: toRecord(
          run({ id: 'run_done', session_id: otherSession.id, status: 'merged' }),
        ),
      },
    })

    await refreshTeam(useStore, client)

    expect(client.approvalList).toHaveBeenCalledTimes(1)
    expect(client.approvalList).toHaveBeenCalledWith(session.id, false)
    expect(client.budgetGet).toHaveBeenCalledTimes(1)
    expect(client.budgetGet).toHaveBeenCalledWith(session.id)

    // Nothing is revealed, so we are in no session and say so by beating
    // none of them.
    await heartbeat(useStore, client)
    expect(client.presenceHeartbeat).not.toHaveBeenCalled()
  })

  // An approval outlives its run: nothing decides it when the run fails, and
  // the decision can be made anywhere - over SSH, by a teammate. The session
  // stays in the refresh set until the request is decided, or the queue
  // count would keep claiming it for as long as the tab lives.
  it('keeps refreshing a session whose inbox still holds a pending request', async () => {
    const client = fakeApi()
    seed({
      sessions: { [session.id]: session, [otherSession.id]: otherSession },
      runs: {
        run_done: toRecord(
          run({ id: 'run_done', session_id: otherSession.id, status: 'failed' }),
        ),
      },
      inbox: {
        [otherSession.id]: [
          approval({ session_id: otherSession.id, run_id: 'run_done' }),
        ],
      },
    })

    await refreshTeam(useStore, client)

    expect(client.approvalList).toHaveBeenCalledWith(otherSession.id, false)
  })

  // The bounded refresh cannot answer a whole-deployment question, and the
  // budget readout asks one: a session does not stop being over its cap
  // when its last run finishes. The wide read on the session list is what
  // keeps it visible.
  it('keeps an over-cap session in the readout after its last run finishes', async () => {
    const client = fakeApi({
      budgetGet: vi.fn(async (id: string) =>
        id === otherSession.id
          ? budget(id, {
              state: 'exceeded',
              budget: { session_id: id, limit_usd: 1 },
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
      sessions: { [session.id]: session, [otherSession.id]: otherSession },
      runs: {
        run_1: toRecord(run()),
        run_done: toRecord(
          run({ id: 'run_done', session_id: otherSession.id, status: 'merged' }),
        ),
      },
    })
    render(<TeamStatus client={client} />)

    expect(await screen.findByText('past the cap')).toBeDefined()
    // $0.50 from the live session and $14 from the finished one.
    expect(screen.getByText('$14.50')).toBeDefined()
  })

  it('rolls spend up across sessions, worst state and unmetered floor first', () => {
    seed({
      sessions: { [session.id]: session, [otherSession.id]: otherSession },
      budgets: {
        [session.id]: budget(session.id, {
          budget: { session_id: session.id, limit_usd: 10 },
        }),
        [otherSession.id]: budget(otherSession.id, {
          state: 'exceeded',
          budget: { session_id: otherSession.id, limit_usd: 1 },
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

    // The three directories that grow without bound; a bare filesystem
    // total says the disk is filling but not what is filling it.
    const detail = screen.getByLabelText('Disk usage').getAttribute('title')
    expect(detail).toContain('Worktrees 256 MB')
    expect(detail).toContain('Transcripts 128 MB')
    expect(detail).toContain('Database 64 MB')
  })
})

describe('run card contributions', () => {
  it('carries the approval badge and the watcher avatars', () => {
    seed({ inbox: { [session.id]: [approval()] }, presence: [watching] })
    render(<Board />)

    const card = screen.getByRole('article')
    expect(within(card).getByTitle('1 waiting on a decision')).toBeDefined()
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
    seed({ inbox: { [session.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))

    expect(await screen.findByText(/Approved by Alice/, { selector: 'p' })).toBeDefined()
    expect(client.approvalDecide).toHaveBeenCalledWith('run_1', 'apr_1', true)
  })

  it('surfaces the server refusal instead of guessing at the capability', async () => {
    const client = fakeApi({
      approvalList: vi.fn(async () => [approval()]),
      approvalDecide: vi.fn(async () => {
        throw new ApiError(403, 'approval.decide: permission denied')
      }),
    })
    seed({ inbox: { [session.id]: [approval()] } })
    render(<ApprovalInbox params={{}} client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))

    expect(await screen.findByText(/permission denied/)).toBeDefined()
    // The request is still open, so the buttons are still there.
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
  })
})
