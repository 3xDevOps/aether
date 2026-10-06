import { createRootStore } from '@/store'
import { requestSessionControl, subscribeSession } from '@/store/session-stream'
import { ScriptedSession } from '@/test/acp-stream'
import { alice, run, serverInfo } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', () => ({ api: { acpSocket: (runID: string) => `ws://localhost/ws/acp/${runID}` } }))

const controlFrames = (session: ScriptedSession) =>
  session.socket.frames().filter((f) => (f as { type?: string }).type === 'control') as { write: boolean; request_id: number }[]

function setup() {
  const store = createRootStore()
  store.setState({ info: serverInfo })
  store.getState().upsertRun(run({ mode: 'acp', acp: true, controller_member_id: alice.id }))
  const stop = subscribeSession(store, 'run_1', true)
  return { store, stop, session: ScriptedSession.last() }
}

beforeEach(() => {
  StubSocket.install()
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('the session stream owner', () => {
  it('retries a refused automatic lease request quietly until the old lease frees', () => {
    const { store, stop, session } = setup()
    session.open({ has_control: false })
    expect(controlFrames(session)).toHaveLength(1)
    session.send({ type: 'control', request_id: 1, ok: false, error: 'run control is held by another session' })
    expect(store.getState().acpSessions.run_1?.controlError).toBeUndefined()
    vi.advanceTimersByTime(1000)
    expect(controlFrames(session)).toHaveLength(2)
    session.send({ type: 'control', request_id: 2, ok: true, has_control: true, control_generation: 3 })
    expect(store.getState().acpSessions.run_1?.control).toMatchObject({ has_control: true, control_generation: 3 })
    stop()
  })

  it('asks again when run.controller reports the lease free', async () => {
    const { store, stop, session } = setup()
    store.getState().upsertRun(run({ mode: 'acp', acp: true, controller_member_id: 'mem_bob' }))
    session.open({ has_control: false })
    session.send({ type: 'control', request_id: 1, ok: false, error: 'run control is held by another session' })
    await vi.advanceTimersByTimeAsync(60_000)
    expect(controlFrames(session)).toHaveLength(1)
    store.getState().applyRunController('run_1', '')
    expect(controlFrames(session)).toHaveLength(2)
    stop()
  })

  it('shows a refused request the viewer made', async () => {
    const { store, stop, session } = setup()
    session.open({ has_control: true, control_generation: 2 })
    requestSessionControl('run_1', true, true)
    session.send({ type: 'control', request_id: 1, ok: false, error: 'stale lease' })
    await vi.runAllTimersAsync()
    expect(store.getState().acpSessions.run_1?.controlError).toBe('stale lease')
    stop()
  })

  it('resets the stream and lease when the last viewer leaves', async () => {
    const { store, stop, session } = setup()
    session.open({ has_control: true, control_generation: 2 })
    stop()
    await vi.runAllTimersAsync()
    expect(store.getState().acpSessions.run_1).toMatchObject({ stream: 'connecting', control: undefined, takeover: undefined })
  })
})
