import { toast } from 'sonner'
import { ApiError } from '@/lib/api'
import type { GatewayCapabilities } from '@/lib/types'
import { createRootStore } from '@/store'
import { watchOutcomeSeen } from '@/store/outcome-seen'
import { toRecord } from '@/store/runs'
import { alice, bob, fakeApi, run, serverInfo } from '@/test/fixtures'

vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

const everyMethod: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }
const reported = run({ status: 'completed', outcome_unseen: true, member_id: alice.id })

function fakeDocument(hidden = false) {
  return Object.assign(new EventTarget(), { hidden }) as unknown as Document & { hidden: boolean }
}

function setup(self = alice, caps: GatewayCapabilities = everyMethod) {
  const store = createRootStore()
  store.setState({
    info: { ...serverInfo, member: self },
    capabilities: caps,
    runs: { [reported.id]: toRecord(reported) },
    route: { name: 'board', params: {} },
  })
  return store
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0))

describe('watchOutcomeSeen', () => {
  beforeEach(() => vi.mocked(toast.error).mockClear())

  it('asks once when the owner opens the run, and clears only on the server answer', async () => {
    const store = setup()
    const answer = deferred<ReturnType<typeof run>>()
    const client = fakeApi({ runSeen: vi.fn(() => answer.promise) })
    const stop = watchOutcomeSeen(store, client, fakeDocument())

    store.getState().navigate('run', { runId: reported.id })
    store.getState().navigate('run', { runId: reported.id, view: 'changes' })
    store.getState().applyRunTitle(reported.id, 'a later write')

    expect(client.runSeen).toHaveBeenCalledTimes(1)
    expect(client.runSeen).toHaveBeenCalledWith(reported.id)
    // The tab-local ack does not move the card.
    expect(store.getState().runs[reported.id].outcome_unseen).toBe(true)

    answer.resolve({ ...reported, outcome_unseen: false })
    await settle()
    expect(store.getState().runs[reported.id].outcome_unseen).toBe(false)
    stop()
  })

  it.each(['tui', 'acp'] as const)('reviews repeated %s reports without closing the live session', async (mode) => {
    const store = setup()
    const initial = run({ mode, acp: mode === 'acp', status: 'running', finished_at: null })
    store.setState({ runs: { [initial.id]: toRecord(initial) } })
    const client = fakeApi({ runSeen: vi.fn(async () => ({
      ...store.getState().runs[initial.id], outcome_unseen: false,
    })) })
    const stop = watchOutcomeSeen(store, client, fakeDocument())
    store.getState().navigate('run', { runId: initial.id })
    for (const outcome of ['success', 'failure'] as const) {
      store.getState().applyRunStatus(initial.id, 'needs-attention', `agent reported ${outcome}`, '2026-08-14T11:00:00Z', true)
      expect(store.getState().runs[initial.id].outcome_unseen).toBe(true)
      await settle()
      const seen = store.getState().runs[initial.id]
      expect(seen.outcome_unseen).toBe(false)
      expect(seen.status).toBe('needs-attention')
      expect(seen.finished_at).toBeNull()
      expect(seen.container_retained_until).toBeUndefined()
      const question = { id: 'question_1', session_id: 'session_1', kind: 'question' as const }
      store.getState().applyRunInput(initial.id, [question])
      expect(store.getState().runs[initial.id].pending_inputs).toEqual([question])
      store.getState().applyRunInput(initial.id, [])
      store.getState().applyRunStatus(initial.id, 'running', 'agent resumed', '2026-08-14T11:01:00Z')
      expect(store.getState().runs[initial.id].outcome_unseen).toBe(false)
    }
    expect(client.runSeen).toHaveBeenCalledTimes(2)
    stop()
  })

  it('changes nothing when someone other than the owner opens the run', async () => {
    const store = setup(bob)
    const client = fakeApi()
    const stop = watchOutcomeSeen(store, client, fakeDocument())

    store.getState().navigate('run', { runId: reported.id })
    await settle()

    expect(client.runSeen).not.toHaveBeenCalled()
    expect(store.getState().runs[reported.id].outcome_unseen).toBe(true)
    stop()
  })

  it('asks when the outcome lands on a run the owner is viewing, once the tab is visible', async () => {
    const store = setup()
    store.setState({ runs: { [reported.id]: toRecord({ ...reported, status: 'running', outcome_unseen: false }) } })
    const doc = fakeDocument(true)
    const client = fakeApi()
    const stop = watchOutcomeSeen(store, client, doc)
    store.getState().navigate('run', { runId: reported.id })

    store.getState().applyRunStatus(reported.id, 'completed', 'agent reported success', '2026-08-14T11:00:00Z', true)
    expect(client.runSeen).not.toHaveBeenCalled()

    doc.hidden = false
    doc.dispatchEvent(new Event('visibilitychange'))
    expect(client.runSeen).toHaveBeenCalledTimes(1)
    stop()
  })

  it('reports a refusal once and does not retry until the run is opened again', async () => {
    const store = setup()
    const client = fakeApi({
      runSeen: vi.fn(async () => {
        throw new ApiError(403, 'run.seen: only the run owner can mark it seen')
      }),
    })
    const stop = watchOutcomeSeen(store, client, fakeDocument())

    store.getState().navigate('run', { runId: reported.id })
    await settle()
    store.getState().applyRunTitle(reported.id, 'a later write')
    await settle()

    expect(client.runSeen).toHaveBeenCalledTimes(1)
    expect(toast.error).toHaveBeenCalledTimes(1)
    expect(vi.mocked(toast.error).mock.calls[0][0]).toContain('only the run owner can mark it seen')
    expect(store.getState().runs[reported.id].outcome_unseen).toBe(true)

    store.getState().navigate('board')
    store.getState().navigate('run', { runId: reported.id })
    expect(client.runSeen).toHaveBeenCalledTimes(2)
    stop()
  })

  it('does not call a gateway that lacks run.seen', () => {
    const store = setup(alice, { gateway: 'remote', methods: ['run.get'], ws: [] })
    const client = fakeApi()
    const stop = watchOutcomeSeen(store, client, fakeDocument())

    store.getState().navigate('run', { runId: reported.id })

    expect(client.runSeen).not.toHaveBeenCalled()
    stop()
  })
})
