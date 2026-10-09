import { createRootStore } from '@/store'
import { allowSessionAutoWrite, requestSessionControl, requestSessionTakeover, subscribeSession } from '@/store/session-stream'
import { ScriptedSession } from '@/test/acp-stream'
import { alice, run, serverInfo } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', () => ({ api: { acpSocket: (runID: string) => `ws://localhost/ws/acp/${runID}` } }))

const controlFrames = (session: ScriptedSession) =>
  session.socket.frames().filter((f) => (f as { type?: string }).type === 'control') as { write: boolean; request_id: number }[]

function setup(autoWrite = true) {
  const store = createRootStore()
  store.setState({ info: serverInfo })
  store.getState().upsertRun(run({ mode: 'acp', acp: true, controller_member_id: alice.id }))
  const stop = subscribeSession(store, 'run_1', autoWrite)
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

  it('asks for the lease only once automatic control is allowed', () => {
    const { stop, session } = setup(false)
    session.open({ has_control: false })
    expect(controlFrames(session)).toHaveLength(0)
    allowSessionAutoWrite('run_1')
    expect(controlFrames(session)).toMatchObject([{ write: true }])
    allowSessionAutoWrite('run_1')
    expect(controlFrames(session)).toHaveLength(1)
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
    requestSessionControl('run_1', true)
    session.send({ type: 'control', request_id: 1, ok: false, error: 'stale lease' })
    await vi.runAllTimersAsync()
    expect(store.getState().acpSessions.run_1?.controlError).toBe('stale lease')
    stop()
  })

  it('keeps a deliberate release read-only when presence changes or Session is shown again', async () => {
    const { store, stop, session } = setup()
    try {
      session.open({ has_control: false })
      session.send({ type: 'control', request_id: 1, ok: true, has_control: true, control_generation: 3 })
      await vi.advanceTimersByTimeAsync(0)
      requestSessionControl('run_1', false)
      expect(controlFrames(session).at(-1)).toMatchObject({ write: false, control_generation: 3 })
      session.send({ type: 'control', request_id: 2, ok: true, has_control: false, control_generation: 3 })
      await vi.advanceTimersByTimeAsync(0)
      store.getState().applyRunController('run_1', '')
      allowSessionAutoWrite('run_1')
      await vi.advanceTimersByTimeAsync(60_000)
      expect(controlFrames(session).map((frame) => frame.write)).toEqual([true, false])
      expect(store.getState().acpSessions.run_1?.control).toMatchObject({ has_control: false, loss: 'release' })
    } finally {
      stop()
    }
  })

  it('does not race a deliberate takeover with an automatic acquisition retry', async () => {
    const { stop, session } = setup()
    try {
      session.open({ has_control: false })
      session.send({ type: 'control', request_id: 1, ok: false, error: 'run control is held by another session' })
      requestSessionTakeover('run_1', 'start', 'deliberate-takeover')
      await vi.advanceTimersByTimeAsync(60_000)
      expect(controlFrames(session)).toHaveLength(1)
      expect(session.socket.frames().at(-1)).toMatchObject({ type: 'takeover', action: 'start', takeover_id: 'deliberate-takeover' })
    } finally {
      stop()
    }
  })

  it('keeps a displaced controller read-only after the new controller releases', async () => {
    const { store, stop, session } = setup()
    try {
      session.open({ has_control: false })
      session.send({ type: 'control', request_id: 1, ok: true, has_control: true, control_generation: 3 })
      session.send({
        type: 'control', has_control: false, control_generation: 3,
        control_session_id: session.header().control_session_id, revocation_reason: 'takeover',
      })
      await vi.advanceTimersByTimeAsync(0)
      store.getState().applyRunController('run_1', 'mem_bob')
      store.getState().applyRunController('run_1', '')
      await vi.advanceTimersByTimeAsync(60_000)
      expect(controlFrames(session)).toHaveLength(1)
      expect(store.getState().acpSessions.run_1?.control).toMatchObject({ has_control: false, loss: 'takeover' })
    } finally {
      stop()
    }
  })

  it.each([
    { reason: 'permission', generation: 3, currentSession: true },
    { reason: 'revoked', generation: 3, currentSession: true },
    { reason: 'takeover', generation: 2, currentSession: true },
    { reason: 'takeover', generation: 3, currentSession: false },
  ])('does not animate $reason for generation $generation, current session $currentSession', ({ reason, generation, currentSession }) => {
    const { store, stop, session } = setup(false)
    try {
      session.open({ has_control: true, control_generation: 3 })
      session.send({
        type: 'control', has_control: false, control_generation: generation,
        control_session_id: currentSession ? session.header().control_session_id : 'another-tab',
        revocation_reason: reason,
      })
      expect(store.getState().acpSessions.run_1?.control).toMatchObject({ has_control: false })
      expect(store.getState().acpSessions.run_1?.control?.loss).toBeUndefined()
    } finally {
      stop()
    }
  })

  it('resets the stream and lease when the last viewer leaves', async () => {
    const { store, stop, session } = setup()
    session.open({ has_control: true, control_generation: 2 })
    stop()
    await vi.runAllTimersAsync()
    expect(store.getState().acpSessions.run_1).toMatchObject({ stream: 'connecting', control: undefined, takeover: undefined })
  })
})
