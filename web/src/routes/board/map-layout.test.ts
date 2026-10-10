import type { Run } from '@/lib/types'
import { layoutRunMap } from '@/routes/board/map-layout'
import type { MapRect } from '@/routes/board/map-layout'
import type { BoardCard } from '@/routes/board/selectors'
import { toRecord } from '@/store/runs'
import { alice, bob, otherWorkspace, run, workspace } from '@/test/fixtures'

function card(overrides: Partial<Run>): BoardCard {
  const record = toRecord(run(overrides))
  return {
    run: record,
    owner: [alice, bob].find((member) => member.id === record.member_id),
    state: 'working', group: 'working', reason: 'Agent working', since: record.stateChangedAt, waitingSince: record.stateChangedAt, children: [],
  }
}

function expectContained(inner: MapRect, outer: MapRect) {
  expect(inner.x).toBeGreaterThanOrEqual(outer.x)
  expect(inner.y).toBeGreaterThanOrEqual(outer.y)
  expect(inner.x + inner.width).toBeLessThanOrEqual(outer.x + outer.width)
  expect(inner.y + inner.height).toBeLessThanOrEqual(outer.y + outer.height)
}

function expectDisjoint(rects: MapRect[]) {
  rects.forEach((a, index) => {
    for (const b of rects.slice(index + 1)) {
      expect(a.x + a.width <= b.x || b.x + b.width <= a.x || a.y + a.height <= b.y || b.y + b.height <= a.y).toBe(true)
    }
  })
}

it('keeps cross-member workers in their actual owner box and links only the explicit workspace integrator', () => {
  const integrator = card({ id: 'root', member_id: alice.id, mission_id: 'mission', mission_role: 'integrator' })
  const worker = card({ id: 'worker', member_id: bob.id, mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root' })
  const elsewhere = card({ ...worker.run, id: 'elsewhere', workspace_id: otherWorkspace.id })
  const wrongMission = card({ ...worker.run, id: 'wrong-mission', mission_id: 'another' })
  const standalone = card({ id: 'standalone', member_id: bob.id, task: 'worker for root in mission' })
  const layout = layoutRunMap([worker, integrator, elsewhere, wrongMission, standalone, worker])
  expect(layout.nodes.map((node) => node.card.run.id).sort()).toEqual(['elsewhere', 'root', 'standalone', 'worker', 'wrong-mission'])
  expect(layout.connectors).toEqual([expect.objectContaining({
    from: JSON.stringify([workspace.id, 'root']), to: JSON.stringify([workspace.id, 'worker']), crossMember: true,
  })])
  for (const group of layout.groups) {
    for (const unit of group.units) {
      for (const node of unit.nodes) {
        expect(node.card.run.member_id).toBe(group.key)
        expectContained(node, group)
      }
    }
  }
  expect(layout.nodes.find((node) => node.card.run.id === 'standalone')?.role).toBe('standalone')
})

it('keeps visible workers without a visible integrator and does not invent legacy relationships', () => {
  const orphan = card({ id: 'worker', member_id: 'missing-member', mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'archived-root' })
  const legacy = card({ id: 'legacy', task: 'Integrator coordinating worker' })
  const layout = layoutRunMap([orphan, legacy])
  expect(layout.connectors).toEqual([])
  expect(layout.nodes.map((node) => [node.card.run.id, node.role]).sort()).toEqual([['legacy', 'standalone'], ['worker', 'worker']])
  expect(layout.groups.find((group) => group.key === 'missing-member')).toMatchObject({ label: 'missing-member', count: 1 })
})

it('contains sparse and large swarms, shelf packs without overlap, and routes orthogonal connectors clear of cards', () => {
  const cards: BoardCard[] = []
  for (let owner = 0; owner < 7; owner++) {
    cards.push(card({ id: `root-${owner}`, member_id: `member-${owner}`, mission_id: `mission-${owner}`, mission_role: 'integrator' }))
    for (let worker = 0; worker < owner * 4; worker++) {
      cards.push(card({
        id: `worker-${owner}-${worker}`, member_id: `member-${worker % 2 ? owner : (owner + 1) % 7}`,
        mission_id: `mission-${owner}`, mission_role: 'worker', integrator_run_id: `root-${owner}`,
      }))
    }
    cards.push(card({ id: `single-${owner}`, member_id: `member-${owner}` }))
  }
  const layout = layoutRunMap(cards)
  expect(layout.nodes.map((node) => node.card.run.id).sort()).toEqual(cards.map((entry) => entry.run.id).sort())
  expectDisjoint(layout.groups)
  expectDisjoint(layout.nodes)
  for (const group of layout.groups) {
    expectContained(group, { x: 0, y: 0, width: layout.width, height: layout.height })
    expectDisjoint(group.units)
    for (const unit of group.units) {
      expectContained(unit, group)
      unit.nodes.forEach((node) => expectContained(node, unit))
    }
  }
  for (const connector of layout.connectors) {
    connector.points.slice(1).forEach((b, index) => {
      const a = connector.points[index]
      expect(a.x === b.x || a.y === b.y).toBe(true)
      expectContained({ ...a, width: 0, height: 0 }, { x: 0, y: 0, width: layout.width, height: layout.height })
      for (const node of layout.nodes) {
        const crosses = a.x === b.x
          ? a.x > node.x && a.x < node.x + node.width && Math.max(a.y, b.y) > node.y && Math.min(a.y, b.y) < node.y + node.height
          : a.y > node.y && a.y < node.y + node.height && Math.max(a.x, b.x) > node.x && Math.min(a.x, b.x) < node.x + node.width
        expect(crosses).toBe(false)
      }
    })
  }
})

it('does not move nodes when input order, heartbeat timestamps or live presentation state changes', () => {
  const cards = [
    card({ id: 'root', mission_id: 'mission', mission_role: 'integrator' }),
    card({ id: 'worker-a', mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root' }),
    card({ id: 'worker-b', member_id: bob.id, mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root' }),
    card({ id: 'single', member_id: bob.id }),
  ]
  const before = layoutRunMap(cards)
  const after = layoutRunMap([...cards].reverse().map<BoardCard>((entry) => ({
    ...entry, state: 'needs-you',
    run: { ...entry.run, status: 'needs-attention', stateChangedAt: '2026-09-29T12:00:00Z' },
  })), before)
  expect(after.nodes.map(({ key, x, y, width, height }) => ({ key, x, y, width, height })))
    .toEqual(before.nodes.map(({ key, x, y, width, height }) => ({ key, x, y, width, height })))
  expect(after.connectors).toEqual(before.connectors)
})

it('updates cached owner boundaries and connectors when ownership or swarm relationships change', () => {
  const first = card({ id: 'first', mission_id: 'mission', mission_role: 'integrator' })
  const second = card({ id: 'second', mission_id: 'mission', mission_role: 'integrator' })
  const worker = card({ id: 'worker', member_id: bob.id, mission_id: 'mission', mission_role: 'worker', integrator_run_id: first.run.id })
  let layout = layoutRunMap([first, second, worker])
  expect(layout.connectors[0].crossMember).toBe(true)

  const moved = card({ ...worker.run, member_id: alice.id })
  layout = layoutRunMap([first, second, moved], layout)
  expect(layout.groups.map((group) => [group.key, group.count])).toEqual([[alice.id, 3]])
  expect(layout.connectors).toEqual([expect.objectContaining({ crossMember: false })])
  expectDisjoint(layout.nodes)

  const reassigned = card({ ...moved.run, integrator_run_id: second.run.id })
  layout = layoutRunMap([first, second, reassigned], layout)
  expect(layout.connectors).toEqual([expect.objectContaining({ from: JSON.stringify([workspace.id, second.run.id]) })])

  const detached = card({ ...reassigned.run, mission_id: 'another-mission' })
  layout = layoutRunMap([first, second, detached], layout)
  expect(layout.connectors).toEqual([])
  expect(layout.groups[0].units.find((unit) => unit.nodes.some((node) => node.card.run.id === worker.run.id))?.label)
    .toBe('Workers · integrator not visible')

  const standalone = card({ ...detached.run, mission_role: undefined })
  layout = layoutRunMap([first, second, standalone], layout)
  expect(layout.nodes.find((node) => node.card.run.id === worker.run.id)?.role).toBe('standalone')
})
