import type { Run } from '@/lib/types'
import { layoutRunMap, mapMetrics, runKey, type MapLayoutInput, type MapRect, type RunMapLayout } from '@/routes/board/map-layout'
import type { MapCard } from '@/routes/board/selectors'
import { toRecord } from '@/store/runs'
import { alice, bob, otherWorkspace, run, workspace } from '@/test/fixtures'

const input: MapLayoutInput = { viewerID: alice.id, folded: {}, metrics: mapMetrics('default', false) }

function card(overrides: Partial<Run>, visiting = false): MapCard {
  const record = toRecord(run(overrides))
  return {
    run: record,
    owner: [alice, bob].find((member) => member.id === record.member_id),
    state: 'working', group: 'working', reason: 'Agent working', since: record.stateChangedAt, waitingSince: record.stateChangedAt, children: [],
    visiting,
  }
}

const at = (minute: number) => new Date(Date.UTC(2026, 7, 14, 10, minute)).toISOString()
const key = (id: string, ws = workspace.id) => JSON.stringify([ws, id])

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

function expectSound(layout: RunMapLayout) {
  const world = { x: 0, y: 0, width: layout.width, height: layout.height }
  expectDisjoint(layout.sections)
  expectDisjoint(layout.nodes)
  for (const section of layout.sections) {
    expectContained(section, world)
    expectDisjoint(section.units)
    for (const unit of section.units) {
      expectContained(unit, section)
      unit.nodes.forEach((node) => expectContained(node, unit))
      for (const line of unit.lines) {
        const points = line.points.map((point) => ({ x: point.x + unit.x, y: point.y + unit.y }))
        points.slice(1).forEach((b, index) => {
          const a = points[index]
          expect(a.x === b.x || a.y === b.y).toBe(true)
          expectContained({ ...a, width: 0, height: 0 }, unit)
          for (const node of layout.nodes) {
            const crosses = a.x === b.x
              ? a.x > node.x && a.x < node.x + node.width && Math.max(a.y, b.y) > node.y && Math.min(a.y, b.y) < node.y + node.height
              : a.y > node.y && a.y < node.y + node.height && Math.max(a.x, b.x) > node.x && Math.min(a.x, b.x) < node.x + node.width
            expect(crosses).toBe(false)
          }
        })
      }
    }
  }
}

const geometry = (layout: RunMapLayout) => layout.nodes.map(({ key, x, y, width, height }) => ({ key, x, y, width, height }))

it('keeps cross-owner workers in their integrator tree and links only the explicit workspace integrator', () => {
  const integrator = card({ id: 'root', member_id: alice.id, mission_id: 'mission', mission_role: 'integrator' })
  const worker = card({ id: 'worker', member_id: bob.id, mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root' })
  const elsewhere = card({ ...worker.run, id: 'elsewhere', workspace_id: otherWorkspace.id })
  const wrongMission = card({ ...worker.run, id: 'wrong-mission', mission_id: 'another' })
  const standalone = card({ id: 'standalone', member_id: bob.id, task: 'worker for root in mission' })
  const layout = layoutRunMap([worker, integrator, elsewhere, wrongMission, standalone, worker], input)
  expect(layout.nodes.map((node) => node.card.run.id).sort()).toEqual(['elsewhere', 'root', 'standalone', 'worker', 'wrong-mission'])
  const swarm = layout.units.find((unit) => unit.key === key('root'))!
  expect(swarm).toMatchObject({ kind: 'swarm', workers: 1 })
  expect(swarm.nodes.map((node) => [node.card.run.id, node.role])).toEqual([['root', 'integrator'], ['worker', 'worker']])
  expect(layout.sections[0].key).toBe(alice.id)
  expect(layout.sections[0].units).toContain(swarm)
  expect(swarm.nodes.map((node) => node.showOwner)).toEqual([false, true])
  expect(layout.units.filter((unit) => unit.kind === 'orphan').flatMap((unit) => unit.nodes.map((node) => node.card.run.id)).sort())
    .toEqual(['elsewhere', 'wrong-mission'])
  expect(layout.nodes.find((node) => node.card.run.id === 'standalone')?.role).toBe('standalone')
  expectSound(layout)
})

it('lays a run out with its child runs at any depth, and a worker with its child runs inside the swarm', () => {
  const parent = card({ id: 'parent', created_at: at(0) })
  const child = card({ id: 'child', parent_run_id: 'parent', created_at: at(1) })
  const grandchild = card({ id: 'grandchild', parent_run_id: 'child', created_at: at(2) })
  const integrator = card({ id: 'root', mission_id: 'mission', mission_role: 'integrator', created_at: at(3) })
  const worker = card({ id: 'worker', mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root', parent_run_id: 'root', created_at: at(4) })
  const review = card({ id: 'review', parent_run_id: 'worker', created_at: at(5) })
  const layout = layoutRunMap([grandchild, review, child, worker, parent, integrator], input)
  const family = layout.units.find((unit) => unit.key === key('parent'))!
  expect(family).toMatchObject({ kind: 'family', workers: 2 })
  expect(family.nodes.map((node) => [node.card.run.id, node.role])).toEqual([['parent', 'integrator'], ['child', 'worker'], ['grandchild', 'worker']])
  const swarm = layout.units.find((unit) => unit.key === key('root'))!
  expect(swarm.nodes.map((node) => node.card.run.id)).toEqual(['root', 'worker', 'review'])
  expect(layoutRunMap([parent, child, grandchild], { ...input, folded: { [key('parent')]: true } }).units[0]).toMatchObject({ kind: 'family', hidden: [child, grandchild] })
  expectSound(layout)
})

it('keeps visible workers without a visible integrator and does not invent legacy relationships', () => {
  const orphan = card({ id: 'worker', member_id: 'missing-member', mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'archived-root' })
  const legacy = card({ id: 'legacy', task: 'Integrator coordinating worker' })
  const layout = layoutRunMap([orphan, legacy], input)
  expect(layout.units.flatMap((unit) => unit.lines)).toEqual([])
  expect(layout.nodes.map((node) => [node.card.run.id, node.role]).sort()).toEqual([['legacy', 'standalone'], ['worker', 'worker']])
  expect(layout.sections.find((section) => section.key === 'missing-member')).toMatchObject({ label: 'missing-member', count: 1 })
})

it('flows sparse and large swarms without overlap and draws orthogonal connectors clear of cards', () => {
  const cards: MapCard[] = []
  for (let owner = 0; owner < 7; owner++) {
    cards.push(card({ id: `root-${owner}`, member_id: `member-${owner}`, mission_id: `mission-${owner}`, mission_role: 'integrator', created_at: at(owner) }))
    for (let worker = 0; worker < owner * 5; worker++) {
      cards.push(card({
        id: `worker-${owner}-${worker}`, member_id: `member-${worker % 2 ? owner : (owner + 1) % 7}`, created_at: at(owner + worker),
        mission_id: `mission-${owner}`, mission_role: 'worker', integrator_run_id: `root-${owner}`,
      }))
    }
    cards.push(card({ id: `single-${owner}`, member_id: `member-${owner}`, created_at: at(owner * 3) }))
  }
  const layout = layoutRunMap(cards, input)
  expect(layout.nodes.map((node) => node.card.run.id).sort()).toEqual(cards.map((entry) => entry.run.id).sort())
  expect(layout.units.find((unit) => unit.key === key('root-6'))?.lines.length).toBeGreaterThan(4)
  expectSound(layout)
})

it('does not move nodes when input order, heartbeat timestamps or live presentation state changes', () => {
  const cards = [
    card({ id: 'root', mission_id: 'mission', mission_role: 'integrator' }),
    card({ id: 'worker-a', mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root' }),
    card({ id: 'worker-b', member_id: bob.id, mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root' }),
    card({ id: 'single', member_id: bob.id }),
  ]
  const before = layoutRunMap(cards, input)
  const after = layoutRunMap([...cards].reverse().map<MapCard>((entry) => ({
    ...entry, state: 'needs-you', group: 'needs-you',
    run: { ...entry.run, status: 'needs-attention', title: 'Renamed', stateChangedAt: '2026-09-29T12:00:00Z' },
  })), input, before)
  expect(after.revision).toBe(before.revision)
  expect(geometry(after)).toEqual(geometry(before))
  expect(after.units.map((unit) => unit.lines)).toEqual(before.units.map((unit) => unit.lines))
  expect(after.nodes[0].card.run.title).toBe('Renamed')
})

it('places the newest unit first, workers in spawn order, and never moves workers when one is appended', () => {
  const singles = [3, 1, 2].map((minute) => card({ id: `single-${minute}`, created_at: at(minute) }))
  const root = card({ id: 'root', mission_id: 'mission', mission_role: 'integrator', created_at: at(0) })
  const workers = [5, 4, 6, 7, 8].map((minute) => card({
    id: `worker-${minute}`, mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root', created_at: at(minute),
  }))
  const before = layoutRunMap([...singles, root, ...workers], input)
  expect(before.sections.map((section) => section.framed)).toEqual([false])
  expect(before.units.map((unit) => unit.nodes[0].card.run.id)).toEqual(['single-3', 'single-2', 'single-1', 'root'])
  expect(before.units[0]).toMatchObject({ x: Math.min(...before.units.map((unit) => unit.x)), y: Math.min(...before.units.map((unit) => unit.y)) })
  const swarm = (layout: RunMapLayout) => layout.units.find((unit) => unit.key === key('root'))!
  const local = (layout: RunMapLayout) => swarm(layout).nodes.map((node) => [node.card.run.id, node.x - swarm(layout).x, node.y - swarm(layout).y])
  expect(local(before).map(([id]) => id)).toEqual(['root', 'worker-4', 'worker-5', 'worker-6', 'worker-7', 'worker-8'])
  const spawned = card({ id: 'worker-9', mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root', created_at: at(9) })
  const after = layoutRunMap([...singles, root, ...workers, spawned], input, before)
  expect(after.revision).not.toBe(before.revision)
  expect(local(after).slice(0, -1)).toEqual(local(before))
  expectSound(after)
})

it('folds a swarm to its integrator and repacks only when folds or card metrics change', () => {
  const cards = [
    card({ id: 'root', mission_id: 'mission', mission_role: 'integrator' }),
    ...[1, 2, 3].map((n) => card({ id: `worker-${n}`, mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root', created_at: at(n) })),
  ]
  cards[2] = { ...cards[2], group: 'needs-you', state: 'needs-you' }
  const open = layoutRunMap(cards, input)
  const folded = layoutRunMap(cards, { ...input, folded: { [key('root')]: true } }, open)
  expect(folded.revision).not.toBe(open.revision)
  expect(folded.nodes.map((node) => node.card.run.id)).toEqual(['root'])
  expect(folded.units[0]).toMatchObject({ workers: 3, lines: [], dots: [] })
  expect(folded.units[0].hidden.map((entry) => entry.run.id)).toEqual(['worker-1', 'worker-2', 'worker-3'])
  const live = layoutRunMap(cards.map((entry) => ({ ...entry, reason: 'Reading main.go' })), { ...input, folded: { [key('root')]: true } }, folded)
  expect(live.revision).toBe(folded.revision)
  expect(live.units[0].hidden[0].reason).toBe('Reading main.go')
  const larger = layoutRunMap(cards, { ...input, metrics: mapMetrics('larger', true) }, open)
  expect(larger.revision).not.toBe(open.revision)
  expect(larger.nodes[0]).toMatchObject({ width: 320, height: 172 })
  expectSound(larger)
})

it('puts the viewer first, other owners by their newest unit, and Needs you from outside the scope last', () => {
  const carol = 'mem_carol'
  const layout = layoutRunMap([
    card({ id: 'visiting', member_id: bob.id, created_at: at(30) }, true),
    card({ id: 'carol-new', member_id: carol, created_at: at(20) }),
    card({ id: 'bob-old', member_id: bob.id, created_at: at(10) }),
    card({ id: 'mine', created_at: at(1) }),
  ], input)
  expect(layout.sections.every((section) => section.framed)).toBe(true)
  expect(layout.sections.map((section) => [section.label, section.kind])).toEqual([
    ['Alice', 'owner'], [carol, 'owner'], ['Bob', 'owner'], ['Needs you elsewhere', 'visiting'],
  ])
  expect(layout.nodes.map((node) => [node.card.run.id, node.showOwner])).toEqual([
    ['mine', false], ['carol-new', false], ['bob-old', false], ['visiting', true],
  ])
  expect(layout.sections.map((section) => section.width)).toEqual(Array(4).fill(layout.sections[0].width))
  expectSound(layout)
})

it('repacks when ownership or swarm relationships change', () => {
  const first = card({ id: 'first', mission_id: 'mission', mission_role: 'integrator' })
  const second = card({ id: 'second', mission_id: 'mission', mission_role: 'integrator' })
  const worker = card({ id: 'worker', member_id: bob.id, mission_id: 'mission', mission_role: 'worker', integrator_run_id: first.run.id })
  let layout = layoutRunMap([first, second, worker], input)
  const unitOf = (id: string) => layout.units.find((unit) => unit.nodes.some((node) => node.card.run.id === id))!
  expect(unitOf('worker').key).toBe(key('first'))
  expect(layout.nodes.find((node) => node.card.run.id === 'worker')?.showOwner).toBe(true)

  const moved = card({ ...worker.run, member_id: alice.id })
  layout = layoutRunMap([first, second, moved], input, layout)
  expect(layout.sections.map((section) => [section.key, section.count])).toEqual([[alice.id, 3]])
  expect(layout.nodes.find((node) => node.card.run.id === 'worker')?.showOwner).toBe(false)

  const reassigned = card({ ...moved.run, integrator_run_id: second.run.id })
  layout = layoutRunMap([first, second, reassigned], input, layout)
  expect(unitOf('worker').key).toBe(key('second'))

  const detached = card({ ...reassigned.run, mission_id: 'another-mission' })
  layout = layoutRunMap([first, second, detached], input, layout)
  expect(layout.units.find((unit) => unit.nodes.some((node) => node.key === runKey(detached)))?.kind).toBe('orphan')

  const standalone = card({ ...detached.run, mission_role: undefined })
  layout = layoutRunMap([first, second, standalone], input, layout)
  expect(layout.nodes.find((node) => node.card.run.id === worker.run.id)?.role).toBe('standalone')
  expectSound(layout)
})

it('never moves owner sections when Needs you runs from outside the scope come or go', () => {
  const owned = [
    card({ id: 'mine', created_at: at(1) }),
    card({ id: 'root', mission_id: 'mission', mission_role: 'integrator', created_at: at(2) }),
    ...[3, 4, 5, 6, 7].map((minute) => card({
      id: `worker-${minute}`, mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root', created_at: at(minute),
    })),
  ]
  const visiting = (id: string, minute: number) => card({ id, member_id: bob.id, created_at: at(minute) }, true)
  const owner = (layout: RunMapLayout) => geometry(layout).filter((node) => !node.key.includes('visiting'))
  const alone = layoutRunMap(owned, input)
  const one = layoutRunMap([...owned, visiting('visiting-a', 20)], input, alone)
  const two = layoutRunMap([...owned, visiting('visiting-a', 20), visiting('visiting-b', 21)], input, one)
  expect(alone.sections.map((section) => section.framed)).toEqual([false])
  expect(two.sections.map((section) => [section.kind, section.framed])).toEqual([['owner', false], ['visiting', true]])
  expect(owner(one)).toEqual(owner(alone))
  expect(owner(two)).toEqual(owner(alone))

  const shared = [...owned, card({ id: 'bob-run', member_id: bob.id, created_at: at(9) })]
  const framed = layoutRunMap(shared, input)
  const visited = layoutRunMap([...shared, visiting('visiting-a', 20)], input, framed)
  expect(owner(visited)).toEqual(owner(framed))
  expectSound(visited)
})

