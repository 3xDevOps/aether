import type { Run } from '@/lib/types'
import { layoutRunMap, mapMetrics, type MapLayoutInput, type MapRect, type RunMapLayout } from '@/routes/board/map-layout'
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

it('keeps cross-owner child runs in their parent tree and links only a parent in the same workspace', () => {
  const parent = card({ id: 'root', member_id: alice.id })
  const child = card({ id: 'child', member_id: bob.id, parent_run_id: 'root' })
  const elsewhere = card({ ...child.run, id: 'elsewhere', workspace_id: otherWorkspace.id })
  const standalone = card({ id: 'standalone', member_id: bob.id, task: 'child of root' })
  const layout = layoutRunMap([child, parent, elsewhere, standalone, child], input)
  expect(layout.nodes.map((node) => node.card.run.id).sort()).toEqual(['child', 'elsewhere', 'root', 'standalone'])
  const family = layout.units.find((unit) => unit.key === key('root'))!
  expect(family).toMatchObject({ kind: 'family', childRuns: 1 })
  expect(family.nodes.map((node) => [node.card.run.id, node.role])).toEqual([['root', 'parent'], ['child', 'child']])
  expect(layout.sections[0].key).toBe(alice.id)
  expect(layout.sections[0].units).toContain(family)
  expect(family.nodes.map((node) => node.showOwner)).toEqual([false, true])
  expect(layout.nodes.find((node) => node.card.run.id === 'elsewhere')?.role).toBe('standalone')
  expect(layout.nodes.find((node) => node.card.run.id === 'standalone')?.role).toBe('standalone')
  expectSound(layout)
})

it('lays a run out with its child runs at any depth', () => {
  const parent = card({ id: 'parent', created_at: at(0) })
  const child = card({ id: 'child', parent_run_id: 'parent', created_at: at(1) })
  const grandchild = card({ id: 'grandchild', parent_run_id: 'child', created_at: at(2) })
  const layout = layoutRunMap([grandchild, child, parent], input)
  const family = layout.units.find((unit) => unit.key === key('parent'))!
  expect(family).toMatchObject({ kind: 'family', childRuns: 2 })
  expect(family.nodes.map((node) => [node.card.run.id, node.role])).toEqual([['parent', 'parent'], ['child', 'child'], ['grandchild', 'child']])
  expect(layoutRunMap([parent, child, grandchild], { ...input, folded: { [key('parent')]: true } }).units[0]).toMatchObject({ kind: 'family', hidden: [child, grandchild] })
  expectSound(layout)
})

it('keeps a child run whose parent is not visible and does not invent legacy relationships', () => {
  const orphan = card({ id: 'child', member_id: 'missing-member', parent_run_id: 'archived-root' })
  const legacy = card({ id: 'legacy', task: 'Parent coordinating child' })
  const layout = layoutRunMap([orphan, legacy], input)
  expect(layout.units.flatMap((unit) => unit.lines)).toEqual([])
  expect(layout.nodes.map((node) => [node.card.run.id, node.role]).sort()).toEqual([['child', 'standalone'], ['legacy', 'standalone']])
  expect(layout.sections.find((section) => section.key === 'missing-member')).toMatchObject({ label: 'missing-member', count: 1 })
})

it('flows sparse and large families without overlap and draws orthogonal connectors clear of cards', () => {
  const cards: MapCard[] = []
  for (let owner = 0; owner < 7; owner++) {
    cards.push(card({ id: `root-${owner}`, member_id: `member-${owner}`, created_at: at(owner) }))
    for (let child = 0; child < owner * 5; child++) {
      cards.push(card({
        id: `child-${owner}-${child}`, member_id: `member-${child % 2 ? owner : (owner + 1) % 7}`, created_at: at(owner + child),
        parent_run_id: `root-${owner}`,
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
    card({ id: 'root' }),
    card({ id: 'child-a', parent_run_id: 'root' }),
    card({ id: 'child-b', member_id: bob.id, parent_run_id: 'root' }),
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

it('places the newest unit first, child runs in spawn order, and never moves child runs when one is appended', () => {
  const singles = [3, 1, 2].map((minute) => card({ id: `single-${minute}`, created_at: at(minute) }))
  const root = card({ id: 'root', created_at: at(0) })
  const children = [5, 4, 6, 7, 8].map((minute) => card({
    id: `child-${minute}`, parent_run_id: 'root', created_at: at(minute),
  }))
  const before = layoutRunMap([...singles, root, ...children], input)
  expect(before.sections.map((section) => section.framed)).toEqual([false])
  expect(before.units.map((unit) => unit.nodes[0].card.run.id)).toEqual(['single-3', 'single-2', 'single-1', 'root'])
  expect(before.units[0]).toMatchObject({ x: Math.min(...before.units.map((unit) => unit.x)), y: Math.min(...before.units.map((unit) => unit.y)) })
  const family = (layout: RunMapLayout) => layout.units.find((unit) => unit.key === key('root'))!
  const local = (layout: RunMapLayout) => family(layout).nodes.map((node) => [node.card.run.id, node.x - family(layout).x, node.y - family(layout).y])
  expect(local(before).map(([id]) => id)).toEqual(['root', 'child-4', 'child-5', 'child-6', 'child-7', 'child-8'])
  const spawned = card({ id: 'child-9', parent_run_id: 'root', created_at: at(9) })
  const after = layoutRunMap([...singles, root, ...children, spawned], input, before)
  expect(after.revision).not.toBe(before.revision)
  expect(local(after).slice(0, -1)).toEqual(local(before))
  expectSound(after)
})

it('folds a family to its parent and repacks only when folds or card metrics change', () => {
  const cards = [
    card({ id: 'root' }),
    ...[1, 2, 3].map((n) => card({ id: `child-${n}`, parent_run_id: 'root', created_at: at(n) })),
  ]
  cards[2] = { ...cards[2], group: 'needs-you', state: 'needs-you' }
  const open = layoutRunMap(cards, input)
  const folded = layoutRunMap(cards, { ...input, folded: { [key('root')]: true } }, open)
  expect(folded.revision).not.toBe(open.revision)
  expect(folded.nodes.map((node) => node.card.run.id)).toEqual(['root'])
  expect(folded.units[0]).toMatchObject({ childRuns: 3, lines: [], dots: [] })
  expect(folded.units[0].hidden.map((entry) => entry.run.id)).toEqual(['child-1', 'child-2', 'child-3'])
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

it('repacks when ownership or parent relationships change', () => {
  const first = card({ id: 'first' })
  const second = card({ id: 'second' })
  const child = card({ id: 'child', member_id: bob.id, parent_run_id: first.run.id })
  let layout = layoutRunMap([first, second, child], input)
  const unitOf = (id: string) => layout.units.find((unit) => unit.nodes.some((node) => node.card.run.id === id))!
  expect(unitOf('child').key).toBe(key('first'))
  expect(layout.nodes.find((node) => node.card.run.id === 'child')?.showOwner).toBe(true)

  const moved = card({ ...child.run, member_id: alice.id })
  layout = layoutRunMap([first, second, moved], input, layout)
  expect(layout.sections.map((section) => [section.key, section.count])).toEqual([[alice.id, 3]])
  expect(layout.nodes.find((node) => node.card.run.id === 'child')?.showOwner).toBe(false)

  const reassigned = card({ ...moved.run, parent_run_id: second.run.id })
  layout = layoutRunMap([first, second, reassigned], input, layout)
  expect(unitOf('child').key).toBe(key('second'))

  const detached = card({ ...reassigned.run, parent_run_id: undefined })
  layout = layoutRunMap([first, second, detached], input, layout)
  expect(layout.nodes.find((node) => node.card.run.id === child.run.id)?.role).toBe('standalone')
  expectSound(layout)
})

it('never moves owner sections when Needs you runs from outside the scope come or go', () => {
  const owned = [
    card({ id: 'mine', created_at: at(1) }),
    card({ id: 'root', created_at: at(2) }),
    ...[3, 4, 5, 6, 7].map((minute) => card({
      id: `child-${minute}`, parent_run_id: 'root', created_at: at(minute),
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

