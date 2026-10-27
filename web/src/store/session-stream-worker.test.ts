import { createRootStore } from '@/store'
import { requestSessionControl, subscribeSession } from '@/store/session-stream'
import { ScriptedSession } from '@/test/acp-stream'
import { alice, run, serverInfo } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', () => ({ api: { acpSocket: (runID: string) => `ws://localhost/ws/acp/${runID}` } }))

type Frame = { type?: string; write?: boolean; control_generation?: number }

const allFrames = () => StubSocket.opened.flatMap((socket) => socket.frames() as Frame[])

// agent-terminal.ts passes automaticWrite=false for a mission worker.
function openWorker() {
  const store = createRootStore()
  store.setState({ info: serverInfo })
  store.getState().upsertRun(run({ mode: 'acp', acp: true, mission_role: 'worker', controller_member_id: '' }))
  const stop = subscribeSession(store, 'run_1', false)
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

describe('an Enhanced swarm worker opened in the dashboard', () => {
  it('never asks for write control without a click: open, reconnect, wake, freed lease', async () => {
    const { store, stop, session } = openWorker()
    try {
      session.open({ has_control: false, control_generation: 0 })
      session.close(1006)
      await vi.advanceTimersByTimeAsync(0)
      ScriptedSession.last().open({ has_control: false })
      window.dispatchEvent(new Event('online'))
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(0)
      ScriptedSession.last().open({ has_control: false })
      store.getState().applyRunController('run_1', alice.id)
      store.getState().applyRunController('run_1', '')
      await vi.advanceTimersByTimeAsync(60_000)
      expect(StubSocket.opened.length).toBeGreaterThan(2)
      expect(allFrames().filter((frame) => frame.write === true)).toEqual([])
    } finally {
      stop()
    }
  })

  it('re-requests write in the reconnect header once the viewer holds control', async () => {
    const { stop, session } = openWorker()
    try {
      session.open({ has_control: false })
      requestSessionControl('run_1', true)
      session.send({ type: 'control', request_id: 1, ok: true, has_control: true, control_generation: 3 })
      session.close(1006)
      await vi.advanceTimersByTimeAsync(0)
      const reconnected = ScriptedSession.last().open({ has_control: true, control_generation: 3 })
      expect(reconnected.socket).not.toBe(session.socket)
      expect(reconnected.header()).toMatchObject({ write: true, control_generation: 3 })
    } finally {
      stop()
    }
  })

  it('repro: releases a held worker when the run page unmounts, not only on pagehide', () => {
    const { stop, session } = openWorker()
    session.open({ has_control: false })
    requestSessionControl('run_1', true)
    session.send({ type: 'control', request_id: 1, ok: true, has_control: true, control_generation: 3 })
    stop()
    expect(session.socket.closed).toBe(true)
    expect(session.socket.frames().at(-1)).toMatchObject({ type: 'control', write: false, control_generation: 3 })
  })
})
