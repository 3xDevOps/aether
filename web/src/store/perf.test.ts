// A micro-benchmark, skipped unless RUN_PERF=1: RUN_PERF=1 bun run test src/store/perf.test.ts
import type { Event } from '@/lib/types'
import { createRootStore } from '@/store'
import { connect } from '@/store/sync'
import { fakeApi, run, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const runs = 500
const events = 200

describe.runIf(process.env.RUN_PERF === '1')('store under an event burst', () => {
  beforeEach(() => StubSocket.install())
  afterEach(() => vi.unstubAllGlobals())

  it(`applies ${events} events over ${runs} runs in a few notifications`, async () => {
    const store = createRootStore()
    const seeded = Array.from({ length: runs }, (_, i) => run({ id: `run_${i}` }))
    const stop = connect(store, fakeApi({ runList: vi.fn(async () => seeded) }))
    try {
      await vi.waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
      const socket = StubSocket.last()
      socket.onopen?.()
      socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
      await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))

      let heard = 0
      store.subscribe(() => heard++)
      const started = performance.now()
      for (let seq = 1; seq <= events; seq++) {
        const ev: Event = {
          id: `evt_${seq}`,
          seq,
          time: new Date(Date.UTC(2026, 7, 14, 11, 0, seq)).toISOString(),
          workspace_id: workspace.id,
          run_id: `run_${seq % runs}`,
          actor_id: '',
          type: seq % 2 ? 'run.diff' : 'run.status',
          payload: seq % 2
            ? { files: [{ path: 'src/a.ts', additions: seq, deletions: 0 }], tree: `t${seq}`, parent_tree: `t${seq - 1}` }
            : { to: seq % 4 ? 'needs-attention' : 'running' },
        }
        socket.onmessage?.({ data: JSON.stringify(ev) })
      }
      await vi.waitFor(() => expect(store.getState().lastSeq).toBe(events), { interval: 1 })
      const elapsed = performance.now() - started

      expect(heard).toBeLessThan(10)
      expect(elapsed).toBeLessThan(250)
    } finally {
      stop()
    }
  })
})
