import { render } from '@testing-library/react'
import type { SessionItem } from '@/lib/session-types'
import { createRootStore } from '@/store'
import { batchNotifications } from '@/store/batch'
import { rowsOfTurn } from '@/store/session-rows'
import { agentTurn, extrasSig } from '@/store/subagents'
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

  it('streams 20 concurrent subagents without re-deriving the main turns', () => {
    const store = createRootStore()
    const agents = Array.from({ length: 20 }, (_, i) => `agent${i}`)
    const launches = [
      item('turn_start', 1),
      ...agents.map((id) => tool(1, id, 'other', `Agent ${id}`, 'in_progress', { tool_name: 'Agent', subagent: { description: id, type: 'Explore' } })),
    ]
    const children = Array.from({ length: 245 }, (_, n) => agents.map((id) => {
      const call = Math.floor(n / 2)
      return tool(1, `${id}-${call}`, 'read', `Read f${call}.go`, n % 2 ? 'completed' : 'in_progress', { parent_id: id })
    })).flat()
    const batch = 25
    const started = performance.now()
    store.getState().acpFrames('run_1', frames(launches))
    const turns = store.getState().acpSessions.run_1!.turns
    for (let i = 0; i < children.length; i += batch) {
      store.getState().acpFrames('run_1', frames(children.slice(i, i + batch)))
      expect(store.getState().acpSessions.run_1!.turns).toBe(turns)
      expect(extrasSig(store.getState().acpSessions.run_1!.agents, 0)).toBe('')
    }
    store.getState().acpFrames('run_1', frames([tool(1, 'agent0', 'other', 'Agent agent0', 'completed', { tool_name: 'Agent', subagent: { description: 'agent0' } })]))
    const agent = store.getState().acpSessions.run_1!.agents.agents.agent0!
    const rows = rowsOfTurn(agentTurn(agent))
    const took = performance.now() - started
    expect(agent.items).toHaveLength(245)
    expect(rows).toBe(rowsOfTurn(agentTurn(agent)))
    expect(took).toBeLessThan(1500)
    process.stdout.write(`${launches.length + children.length} items across 20 subagents: ${took.toFixed(0)} ms of work\n`)
  })
})
