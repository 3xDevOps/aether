import { render } from '@testing-library/react'
import type { SessionItem } from '@/lib/session-types'
import { createRootStore } from '@/store'
import { batchNotifications } from '@/store/batch'
import { rowsOfTurn } from '@/store/session-rows'
import { item, resetItems, say, tool } from '@/test/acp-stream'

const perf = process.env.RUN_PERF === '1' ? describe : describe.skip

function turnItems(turn: number, size: number): SessionItem[] {
  const items = [item('turn_start', turn), say(turn, 'user', `step ${turn}`)]
  while (items.length < size - 2) {
    const id = `t${turn}-${items.length}`
    items.push(tool(turn, id, 'read', `Read f${items.length}.go`, 'completed', { locations: [{ path: `f${items.length}.go` }] }))
  }
  items.push(say(turn, 'assistant', 'done'), item('turn_end', turn, { stop_reason: 'end_turn' }))
  return items
}

const frames = (items: SessionItem[]) => items.map((it) => ({ seq: it.seq, item: it }))

perf('session stream budget', () => {
  beforeEach(resetItems)

  it('streams 5,000 items with one render per batch and every closed turn derived once', async () => {
    const store = createRootStore()
    let renders = 0
    function Rows() {
      const session = store((s) => s.acpSessions.run_1)
      renders++
      return <span>{session?.turns.flatMap(rowsOfTurn).length ?? 0}</span>
    }
    render(<Rows />)
    const items = Array.from({ length: 100 }, (_, i) => turnItems(i + 1, 50)).flat()
    const batch = 25
    const started = performance.now()
    for (let i = 0; i < items.length; i += batch) {
      await batchNotifications(store, async () => store.getState().acpFrames('run_1', frames(items.slice(i, i + batch))))
      await new Promise((resolve) => setTimeout(resolve, 20))
    }
    const took = performance.now() - started - (items.length / batch) * 20
    const turns = store.getState().acpSessions.run_1!.turns
    expect(turns).toHaveLength(100)
    expect(rowsOfTurn(turns[0]!)).toBe(rowsOfTurn(turns[0]!))
    expect(renders).toBeLessThanOrEqual(items.length / batch + 1)
    process.stdout.write(`5000 items: ${renders} renders, ${took.toFixed(0)} ms of work\n`)
  })

  it('opens a 2,000-item session to its rows within 150 ms', () => {
    const store = createRootStore()
    const items = Array.from({ length: 40 }, (_, i) => turnItems(i + 1, 50)).flat()
    const started = performance.now()
    store.getState().acpFrames('run_1', frames(items))
    const rows = store.getState().acpSessions.run_1!.turns.flatMap(rowsOfTurn)
    const took = performance.now() - started
    expect(rows.length).toBeGreaterThan(0)
    expect(took).toBeLessThan(150)
    process.stdout.write(`2000 items to ${rows.length} rows: ${took.toFixed(1)} ms\n`)
  })
})
