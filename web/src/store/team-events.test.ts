import type { Event } from '@/lib/types'
import { refreshInbox } from '@/routes/team/sync'
import { createRootStore } from '@/store'
import { applyEvent } from '@/store/sync'
import { alice, approval, budget, fakeApi, run, workspace } from '@/test/fixtures'

function event(seq: number, type: string, payload: unknown, over: Partial<Event> = {}): Event {
  return {
    id: `evt_${seq}`,
    seq,
    time: '2026-08-14T11:00:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: '',
    type,
    payload,
    ...over,
  }
}

function seeded() {
  const store = createRootStore()
  store.setState({ workspaces: { [workspace.id]: workspace }, members: { [alice.id]: alice } })
  store.getState().setRuns([run()])
  return store
}

describe('team state from events', () => {
  it('decides a known request in place and reads the list only for an unknown one', async () => {
    const store = seeded()
    store.getState().setInbox(workspace.id, [approval({ id: 'apr_1' })])
    const approvalList = vi.fn(async () => [approval({ id: 'apr_1' }), approval({ id: 'apr_2' })])
    const client = fakeApi({ approvalList })

    await applyEvent(store, event(1, 'workspace.approval', { request_id: 'apr_2', action: 'Bash', decision: 'requested' }), client)
    expect(approvalList).toHaveBeenCalledTimes(1)
    await vi.waitFor(() => expect(store.getState().approvalsByRun.run_1.map((a) => a.id)).toEqual(['apr_1', 'apr_2']))

    await applyEvent(
      store,
      event(2, 'workspace.approval', { request_id: 'apr_1', action: 'Bash', decision: 'approved' }, { actor_id: alice.id }),
      client,
    )
    expect(approvalList).toHaveBeenCalledTimes(1)
    expect(store.getState().inbox[workspace.id].map((a) => a.id)).toEqual(['apr_2'])
    expect(store.getState().approvalsByRun.run_1.map((a) => a.id)).toEqual(['apr_2'])

    // A decision about a request this client never held changes nothing it shows.
    await applyEvent(store, event(3, 'workspace.approval', { request_id: 'apr_9', action: 'Bash', decision: 'denied' }), client)
    expect(approvalList).toHaveBeenCalledTimes(1)
  })

  it('keeps a decided request on screen while decided requests are shown', async () => {
    const store = seeded()
    store.setState({ showDecided: true })
    store.getState().setInbox(workspace.id, [approval({ id: 'apr_1' })])

    await applyEvent(
      store,
      event(1, 'workspace.approval', { request_id: 'apr_1', action: 'Bash', decision: 'denied' }, { actor_id: alice.id }),
      fakeApi(),
    )
    expect(store.getState().inbox[workspace.id][0]).toMatchObject({
      decision: 'denied',
      decided_by: alice.id,
      decided_at: '2026-08-14T11:00:00Z',
    })
    expect(store.getState().approvalsByRun.run_1).toBeUndefined()
  })

  it('does not let a full read that started before an event overwrite it, and reads that workspace again', async () => {
    const store = seeded()
    store.getState().setInbox(workspace.id, [approval({ id: 'apr_1' })])
    const stale = Promise.withResolvers<ReturnType<typeof approval>[]>()
    const fresh = Promise.withResolvers<ReturnType<typeof approval>[]>()
    const approvalList = vi
      .fn()
      .mockImplementationOnce(() => stale.promise)
      .mockImplementation(() => fresh.promise)
    const read = refreshInbox(store, fakeApi({ approvalList }))

    await applyEvent(store, event(1, 'workspace.approval', { request_id: 'apr_1', action: 'Bash', decision: 'approved' }), fakeApi())
    stale.resolve([approval({ id: 'apr_1' })])
    await read
    expect(store.getState().inbox[workspace.id]).toEqual([])
    fresh.resolve([approval({ id: 'apr_2' })])
    await vi.waitFor(() => expect(store.getState().inbox[workspace.id].map((a) => a.id)).toEqual(['apr_2']))
    expect(approvalList).toHaveBeenCalledTimes(2)
  })

  it('keeps retrying a failed list read with backoff while the stream is live', async () => {
    vi.useFakeTimers()
    try {
      const store = seeded()
      store.setState({ connection: 'live' })
      const approvalList = vi.fn(async () => {
        throw new Error('approval.list: database is locked')
      })
      const client = fakeApi({ approvalList })

      await applyEvent(store, event(1, 'workspace.approval', { request_id: 'apr_2', action: 'Bash', decision: 'requested' }), client)
      await vi.advanceTimersByTimeAsync(0)
      expect(approvalList).toHaveBeenCalledTimes(1)
      let calls = 1
      for (const wait of [5000, 10_000, 20_000, 40_000, 60_000, 60_000]) {
        await vi.advanceTimersByTimeAsync(wait - 1)
        expect(approvalList).toHaveBeenCalledTimes(calls)
        await vi.advanceTimersByTimeAsync(1)
        expect(approvalList).toHaveBeenCalledTimes(++calls)
      }

      store.getState().setInboxError(null)
      await vi.advanceTimersByTimeAsync(120_000)
      expect(approvalList).toHaveBeenCalledTimes(calls)
    } finally {
      vi.useRealTimers()
    }
  })

  it('applies a budget event and re-reads the budget after a metered result', async () => {
    vi.useFakeTimers()
    try {
      const store = seeded()
      store.getState().setBudget(budget(workspace.id))
      const budgetGet = vi.fn(async (id: string) => budget(id, { state: 'exceeded' }))
      const client = fakeApi({ budgetGet })

      await applyEvent(store, event(1, 'workspace.budget', { state: 'warn', spend_usd: 4, limit_usd: 5, warn_usd: 3, unmetered_runs: 1 }, { run_id: '' }), client)
      expect(store.getState().budgets[workspace.id]).toMatchObject({
        state: 'warn',
        budget: { limit_usd: 5, warn_usd: 3 },
        spend: { cost_usd: 4, unmetered_runs: 1 },
        advisory: true,
      })
      expect(budgetGet).not.toHaveBeenCalled()

      await applyEvent(store, event(2, 'run.cost', { input_tokens: 10, output_tokens: 5, cost_usd: 0.2, metered: true }), client)
      await vi.advanceTimersByTimeAsync(1500)
      expect(budgetGet).toHaveBeenCalledWith(workspace.id)
      expect(store.getState().budgets[workspace.id].state).toBe('exceeded')
    } finally {
      vi.useRealTimers()
    }
  })

  it('reads the budget once after a burst of results, without holding later events', async () => {
    vi.useFakeTimers()
    try {
      const store = seeded()
      const budgetGet = vi.fn(async (id: string) => budget(id))
      const client = fakeApi({ budgetGet })
      const cost = (seq: number) => event(seq, 'run.cost', { metered: true })

      for (let seq = 1; seq <= 5; seq++) {
        await applyEvent(store, cost(seq), client)
        await vi.advanceTimersByTimeAsync(1000)
      }
      await applyEvent(store, event(6, 'run.status', { to: 'failed' }), client)
      expect(store.getState().runs.run_1.status).toBe('failed')
      expect(budgetGet).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(500)
      expect(budgetGet).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(5000)
      expect(budgetGet).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('drops a budget read that a budget event overtook', async () => {
    vi.useFakeTimers()
    try {
      const store = seeded()
      const late = Promise.withResolvers<ReturnType<typeof budget>>()
      const client = fakeApi({ budgetGet: () => late.promise })

      await applyEvent(store, event(1, 'run.cost', { metered: true }), client)
      await vi.advanceTimersByTimeAsync(1500)
      await applyEvent(store, event(2, 'workspace.budget', { state: 'exceeded', spend_usd: 6, limit_usd: 5 }, { run_id: '' }), client)
      late.resolve(budget(workspace.id, { state: 'ok' }))
      await vi.advanceTimersByTimeAsync(0)
      expect(store.getState().budgets[workspace.id].state).toBe('exceeded')
    } finally {
      vi.useRealTimers()
    }
  })

  it('reads the list again when an event lands while it is in flight', async () => {
    const store = seeded()
    const first = Promise.withResolvers<ReturnType<typeof approval>[]>()
    const approvalList = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementation(async () => [])
    const client = fakeApi({ approvalList })

    await applyEvent(store, event(1, 'workspace.approval', { request_id: 'apr_2', action: 'Bash', decision: 'requested' }), client)
    await applyEvent(store, event(2, 'workspace.approval', { request_id: 'apr_2', action: 'Bash', decision: 'approved' }), client)
    first.resolve([approval({ id: 'apr_2' })])

    await vi.waitFor(() => expect(approvalList).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => expect(store.getState().inbox[workspace.id]).toEqual([]))
  })

  it('clears the error a failed list read set once a later read succeeds', async () => {
    const store = seeded()
    const approvalList = vi
      .fn()
      .mockRejectedValueOnce(new Error('approval.list: database is locked'))
      .mockImplementation(async () => [approval({ id: 'apr_3' })])
    const client = fakeApi({ approvalList })

    await applyEvent(store, event(1, 'workspace.approval', { request_id: 'apr_2', action: 'Bash', decision: 'requested' }), client)
    await vi.waitFor(() => expect(store.getState().inboxError).toContain('locked'))

    await applyEvent(store, event(2, 'workspace.approval', { request_id: 'apr_3', action: 'Bash', decision: 'requested' }), client)
    await vi.waitFor(() => expect(store.getState().inboxError).toBeNull())
    expect(store.getState().approvalsByRun.run_1.map((a) => a.id)).toEqual(['apr_3'])
  })

  it('re-reads the roster on a presence event', async () => {
    const store = seeded()
    const presenceRoster = vi.fn(async () => [{ member_id: alice.id, state: 'online' as const, last_seen: '2026-08-14T11:00:00Z' }])
    await applyEvent(store, event(1, 'workspace.presence', { state: 'online' }, { run_id: '', actor_id: alice.id }), fakeApi({ presenceRoster }))
    await vi.waitFor(() => expect(store.getState().presence.map((p) => p.member_id)).toEqual([alice.id]))
  })

  it('adds live events to an open feed as its filters select them', async () => {
    const store = seeded()
    const client = fakeApi()
    store.getState().setFeedFilters({ workspaceID: workspace.id })
    const status = (seq: number) => event(seq, 'run.status', { to: 'running' })

    const before = store.getState()
    const persisted = vi.spyOn(Storage.prototype, 'setItem')
    store.getState().appendLiveEvent(status(1))
    expect(store.getState()).toBe(before)
    expect(persisted).not.toHaveBeenCalled()
    persisted.mockRestore()
    await applyEvent(store, status(1), client)
    expect(store.getState().feed).toEqual([])

    const release = store.getState().holdFeed()
    await applyEvent(store, status(2), client)
    await applyEvent(store, event(3, 'run.diff', { files: [], tree: 'b', parent_tree: 'a' }), client)
    await applyEvent(store, event(4, 'run.status', { to: 'running' }, { workspace_id: 'other' }), client)
    expect(store.getState().feed.map((e) => e.seq)).toEqual([2])
    expect(store.getState().feedCursor).toBe(2)

    store.getState().setFeedFilters({ type: 'run.diff' })
    await applyEvent(store, event(5, 'run.diff', { files: [], tree: 'c', parent_tree: 'b' }), client)
    expect(store.getState().feed.map((e) => e.seq)).toEqual([2, 5])

    release()
    await applyEvent(store, event(6, 'run.diff', { files: [], tree: 'd', parent_tree: 'c' }), client)
    expect(store.getState().feed.map((e) => e.seq)).toEqual([2, 5])
  })

  it('shows mail addressed to the run a feed is filtered to', () => {
    const store = seeded()
    store.getState().setFeedFilters({ workspaceID: workspace.id, runID: 'run_1' })
    const release = store.getState().holdFeed()
    const mail = (seq: number, to: string) =>
      event(seq, 'coord.message', { message_id: `msg_${seq}`, from_run_id: 'run_worker', to_run_id: to, kind: 'message' }, { run_id: 'run_worker' })
    store.getState().appendLiveEvent(mail(1, 'run_1'))
    store.getState().appendLiveEvent(mail(2, 'run_other'))
    store.getState().appendLiveEvent(event(3, 'run.status', { to: 'running' }, { run_id: 'run_worker' }))
    store.getState().appendLiveEvent(
      event(4, 'coord.message.acked', { message_id: 'msg_1', to_run_id: 'run_1', acked_at: '2026-08-14T11:00:00Z' }),
    )
    expect(store.getState().feed.map((e) => e.seq)).toEqual([1])
    store.getState().setFeedFilters({ type: 'coord.message.acked' })
    store.getState().appendLiveEvent(
      event(5, 'coord.message.acked', { message_id: 'msg_2', to_run_id: 'run_1', acked_at: '2026-08-14T11:00:00Z' }),
    )
    expect(store.getState().feed.map((e) => e.seq)).toEqual([1, 5])
    release()
  })
})
