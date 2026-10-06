import type { Member } from '@/lib/types'
import type { RunRow } from '@/store/selectors'

export const mapCardWidth = 320
export const mapCardHeight = 140
const cardGap = 32
const rowGap = 52
const unitGap = cardGap
const groupGap = 64
const groupInset = 32
const worldInset = 48

export interface MapRect {
  x: number
  y: number
  width: number
  height: number
}

export interface MapNode extends MapRect {
  key: string
  card: RunRow
  role: 'standalone' | 'integrator' | 'worker'
  parentKey?: string
}

export interface MapUnit extends MapRect {
  key: string
  label: string
  missionId?: string
  nodes: MapNode[]
}

export interface MapMemberGroup extends MapRect {
  key: string
  label: string
  member?: Member
  units: MapUnit[]
  count: number
}

export interface MapPoint {
  x: number
  y: number
}

export interface MapConnector {
  from: string
  to: string
  crossMember: boolean
  points: MapPoint[]
}

export interface RunMapLayout {
  width: number
  height: number
  groups: MapMemberGroup[]
  nodes: MapNode[]
  connectors: MapConnector[]
}

const compare = (a: string, b: string) => a < b ? -1 : a > b ? 1 : 0
const runKey = (card: RunRow) => JSON.stringify([card.run.workspace_id, card.run.id])

/** Tall units go first, then identity; live state never changes shelf order. */
function pack<T extends MapRect & { key: string }>(rects: T[], gap: number, aspect: number) {
  if (!rects.length) return { width: 0, height: 0 }
  const order = [...rects].sort((a, b) => b.height - a.height || compare(a.key, b.key))
  const minimum = Math.max(...rects.map((rect) => rect.width))
  const area = rects.reduce((sum, rect) => sum + rect.width * rect.height, 0)
  const candidates = new Set([minimum, Math.max(minimum, Math.sqrt(area * aspect))])
  let span = -gap
  for (const rect of order) {
    span += rect.width + gap
    candidates.add(Math.max(minimum, span))
  }
  let bestScore = Infinity
  let bestArea = Infinity
  let bestTarget = minimum
  const arrange = (target: number, apply: boolean) => {
    const shelves: { y: number; height: number; right: number }[] = []
    let width = 0
    let height = 0
    for (const rect of order) {
      let shelf = shelves.find((row) => row.right + gap + rect.width <= target)
      const x = shelf ? shelf.right + gap : 0
      if (!shelf) {
        shelf = { y: shelves.length ? height + gap : 0, height: rect.height, right: 0 }
        shelves.push(shelf)
        height = shelf.y + shelf.height
      }
      if (apply) {
        rect.x = x
        rect.y = shelf.y
      }
      shelf.right = x + rect.width
      width = Math.max(width, shelf.right)
    }
    return { width, height }
  }
  for (const target of candidates) {
    const bounds = arrange(target, false)
    // Score the occupied bounds, not the guessed shelf width. Prefer compact
    // owner boxes, then a landscape world; equal-fit ties use less empty area.
    const score = Math.max(bounds.width / aspect, bounds.height)
    const occupiedArea = bounds.width * bounds.height
    if (score < bestScore || (score === bestScore && occupiedArea < bestArea)) {
      bestScore = score
      bestArea = occupiedArea
      bestTarget = target
    }
  }
  return arrange(bestTarget, true)
}

function makeUnit(key: string, cards: RunRow[], parents: Map<string, string>): MapUnit {
  const integrator = cards.find((card) => card.run.mission_role === 'integrator')
  const workers = cards.filter((card) => card !== integrator)
  const standalone = !integrator && cards[0].run.mission_role !== 'worker'
  const columns = Math.min(3, Math.max(1, workers.length))
  const width = columns * mapCardWidth + (columns - 1) * cardGap
  const nodes: MapNode[] = []
  const add = (card: RunRow, x: number, y: number, role: MapNode['role']) => {
    const nodeKey = runKey(card)
    nodes.push({
      key: nodeKey, card, x, y, width: mapCardWidth, height: mapCardHeight,
      role, parentKey: parents.get(nodeKey),
    })
  }
  if (standalone) {
    add(cards[0], 0, 0, 'standalone')
  } else {
    if (integrator) add(integrator, (width - mapCardWidth) / 2, 44, 'integrator')
    const top = integrator ? 44 + mapCardHeight + rowGap : 64
    workers.forEach((card, index) => add(
      card,
      (index % columns) * (mapCardWidth + cardGap),
      top + Math.floor(index / columns) * (mapCardHeight + rowGap),
      'worker',
    ))
  }
  return {
    key, x: 0, y: 0, width,
    height: Math.max(...nodes.map((node) => node.y + node.height)),
    label: standalone ? 'Standalone' : integrator ? 'Swarm' : parents.has(runKey(cards[0]))
      ? 'Swarm · workers' : 'Workers · integrator not visible',
    missionId: cards[0].run.mission_id,
    nodes,
  }
}

export function layoutRunMap(cards: RunRow[]): RunMapLayout {
  const byKey = new Map(cards.map((card) => [runKey(card), card]))
  const ordered = [...byKey.values()].sort((a, b) => compare(runKey(a), runKey(b)))
  const parents = new Map<string, string>()
  for (const card of ordered) {
    const run = card.run
    if (run.mission_role !== 'worker' || !run.mission_id || !run.integrator_run_id) continue
    const parentKey = JSON.stringify([run.workspace_id, run.integrator_run_id])
    const parent = byKey.get(parentKey)?.run
    if (parent?.mission_role === 'integrator' && parent.mission_id === run.mission_id) {
      parents.set(runKey(card), parentKey)
    }
  }

  const owners = new Map<string, RunRow[]>()
  for (const card of ordered) {
    const memberId = card.run.member_id
    const owned = owners.get(memberId)
    if (owned) owned.push(card)
    else owners.set(memberId, [card])
  }
  const groups: MapMemberGroup[] = []
  for (const [memberId, owned] of [...owners].sort(([a], [b]) => compare(a, b))) {
    const unitsByKey = new Map<string, RunRow[]>()
    for (const card of owned) {
      const nodeKey = runKey(card)
      const parentKey = parents.get(nodeKey)
      const unitKey = parentKey ?? (card.run.mission_role === 'worker'
        ? JSON.stringify([card.run.workspace_id, 'missing', card.run.mission_id, card.run.integrator_run_id || card.run.id])
        : nodeKey)
      const unit = unitsByKey.get(unitKey)
      if (unit) unit.push(card)
      else unitsByKey.set(unitKey, [card])
    }
    const units = [...unitsByKey].map(([key, unitCards]) => makeUnit(key, unitCards, parents))
    const bounds = pack(units, unitGap, 1)
    const member = owned.find((card) => card.owner?.id === memberId)?.owner
    groups.push({
      key: memberId, label: member?.display_name || memberId || 'Unknown member', member,
      x: 0, y: 0, width: bounds.width + groupInset * 2, height: bounds.height + 48 + groupInset,
      units, count: owned.length,
    })
  }
  const bounds = pack(groups, groupGap, 1.5)
  const nodes: MapNode[] = []
  const locations = new Map<string, { node: MapNode; unit: MapUnit; group: MapMemberGroup }>()
  for (const group of groups) {
    group.x += worldInset
    group.y += worldInset
    for (const unit of group.units) {
      unit.x += group.x + groupInset
      unit.y += group.y + 48
      for (const node of unit.nodes) {
        node.x += unit.x
        node.y += unit.y
        nodes.push(node)
        locations.set(node.key, { node, unit, group })
      }
    }
  }

  const connectors: MapConnector[] = []
  for (const node of nodes) {
    if (!node.parentKey) continue
    const source = locations.get(node.parentKey)
    const destination = locations.get(node.key)
    if (!source || !destination) continue
    const start = { x: source.node.x + mapCardWidth / 2, y: source.node.y + mapCardHeight }
    const end = { x: node.x + mapCardWidth / 2, y: node.y }
    const busY = end.y - rowGap / 2
    const sourceBusY = start.y + rowGap / 2
    let points: MapPoint[]
    if (source.unit === destination.unit) {
      points = busY === sourceBusY
        ? [start, { x: start.x, y: busY }, { x: end.x, y: busY }, end]
        : [
          start, { x: start.x, y: sourceBusY }, { x: source.unit.x - 12, y: sourceBusY },
          { x: source.unit.x - 12, y: busY }, { x: end.x, y: busY }, end,
        ]
    } else {
      // Exit each unit into shelf gutters. The world-left trunk never crosses
      // another owner's box, even when the source and target occupy different rows.
      const sourceLane = source.unit.x - 12
      const sourceTop = source.unit.y - 12
      const targetTop = destination.unit.y - 12
      const sourceOuter = source.group.y - 24
      const targetOuter = destination.group.y - 24
      const targetLane = destination.unit.x - 12
      points = [
        start, { x: start.x, y: sourceBusY }, { x: sourceLane, y: sourceBusY },
        { x: sourceLane, y: sourceTop }, { x: source.group.x + 12, y: sourceTop },
        { x: source.group.x + 12, y: sourceOuter },
        ...(sourceOuter === targetOuter ? [] : [{ x: 16, y: sourceOuter }, { x: 16, y: targetOuter }]),
        { x: destination.group.x + 12, y: targetOuter },
        { x: destination.group.x + 12, y: targetTop }, { x: targetLane, y: targetTop },
        { x: targetLane, y: busY }, { x: end.x, y: busY }, end,
      ]
    }
    connectors.push({ from: source.node.key, to: node.key, crossMember: source.group !== destination.group, points })
  }
  return {
    width: Math.max(1, bounds.width + worldInset * 2),
    height: Math.max(1, bounds.height + worldInset * 2),
    groups, nodes, connectors,
  }
}
