import type { Member } from '@/lib/types'
import type { MapCard } from '@/routes/board/selectors'
import type { RunRecord } from '@/store/runs'
import type { TextSize } from '@/store/ui'

export interface MapMetrics {
  width: number
  height: number
}

const cardSizes: Record<TextSize, MapMetrics> = {
  default: { width: 288, height: 128 },
  large: { width: 304, height: 140 },
  larger: { width: 320, height: 152 },
}

/** Card geometry is fixed per text size; a coarse pointer adds room for 44px actions. */
export function mapMetrics(textSize: TextSize, coarse: boolean): MapMetrics {
  const size = cardSizes[textSize] ?? cardSizes.default
  return { width: size.width, height: size.height + (coarse ? 20 : 0) }
}

const siblingGap = 40
const levelGap = 40
const trunk = 19
const railInset = 20
const railIndent = 32
const railGap = 12
const elbowDrop = 16
const columns = 4
export const captionHeight = 28
const unitGap = 40
const shelfGap = 24
const sectionGap = 48
const sectionInset = 24
export const sectionHeader = 36
const worldInset = 32
const aspect = 1.6

export interface MapRect {
  x: number
  y: number
  width: number
  height: number
}

export interface MapPoint {
  x: number
  y: number
}

export interface MapNode extends MapRect {
  key: string
  card: MapCard
  role: 'standalone' | 'integrator' | 'worker'
  /** The owner differs from the section's, or several owners share an unframed map. */
  showOwner: boolean
}

/** An orthogonal connector in unit coordinates, drawn with `radius` corners. */
export interface MapLine {
  points: MapPoint[]
  radius: number
}

export interface MapUnit extends MapRect {
  key: string
  /** A family is a run with child runs, laid out like a swarm. */
  kind: 'standalone' | 'swarm' | 'family' | 'orphan'
  missionId?: string
  /** Every node, world coordinates, in tab order: the root, then workers by creation. */
  nodes: MapNode[]
  /** All workers in the input, shown or folded away. */
  workers: number
  /** Workers a fold hides, in creation order. */
  hidden: MapCard[]
  lines: MapLine[]
  dots: MapPoint[]
}

export interface MapSection extends MapRect {
  key: string
  kind: 'owner' | 'visiting'
  /** Owner sections draw their frame and header only when there are two or more; Needs you elsewhere always does. */
  framed: boolean
  label: string
  member?: Member
  count: number
  units: MapUnit[]
}

export interface RunMapLayout {
  width: number
  height: number
  /** Bumped on every repack; the fast path keeps it, so a glide runs only when geometry changed. */
  revision: number
  sections: MapSection[]
  units: MapUnit[]
  nodes: MapNode[]
  structure: string
}

export interface MapLayoutInput {
  viewerID: string | null
  folded: Readonly<Record<string, true>>
  metrics: MapMetrics
}

const compare = (a: string, b: string) => a < b ? -1 : a > b ? 1 : 0
export const runKey = ({ run }: { run: RunRecord }) => JSON.stringify([run.workspace_id, run.id])
const byCreated = (a: MapCard, b: MapCard) =>
  compare(a.run.created_at, b.run.created_at) || compare(runKey(a), runKey(b))

/**
 * Each run's parent key. Only explicit IDs establish a relationship: a worker's
 * integrator in the same mission, or the parent that started a child run.
 */
export function runParents<T extends { run: RunRecord }>(items: T[]): Map<string, string> {
  const byKey = new Map(items.map((item) => [runKey(item), item.run]))
  const parents = new Map<string, string>()
  for (const { run } of items) {
    if (run.mission_role === 'worker') {
      if (!run.mission_id || !run.integrator_run_id) continue
      const parentKey = JSON.stringify([run.workspace_id, run.integrator_run_id])
      const parent = byKey.get(parentKey)
      if (parent?.mission_role === 'integrator' && parent.mission_id === run.mission_id) parents.set(runKey({ run }), parentKey)
      continue
    }
    if (!run.parent_run_id) continue
    const parentKey = JSON.stringify([run.workspace_id, run.parent_run_id])
    if (byKey.has(parentKey)) parents.set(runKey({ run }), parentKey)
  }
  return parents
}

/** The top of a run's tree among `parents`; a cycle stops where it closes. */
export function rootKey(key: string, parents: Map<string, string>): string {
  const seen = new Set([key])
  for (let parent = parents.get(key); parent !== undefined && !seen.has(parent); parent = parents.get(parent)) {
    seen.add(parent)
    key = parent
  }
  return key
}

/** Everything that decides geometry; live state, titles and timestamps stay out of it. */
function structureOf(cards: Map<string, MapCard>, input: MapLayoutInput): string {
  const runs = [...cards].sort(([a], [b]) => compare(a, b)).map(([key, { run, visiting }]) => [
    key, run.member_id, run.mission_id, run.mission_role, run.integrator_run_id, run.parent_run_id, visiting, input.folded[key] === true,
  ])
  return JSON.stringify([input.viewerID, input.metrics, runs])
}

interface Draft {
  key: string
  kind: MapUnit['kind']
  root: MapCard
  workers: MapCard[]
}

function makeUnit(draft: Draft, folded: boolean, m: MapMetrics): MapUnit {
  const { root, workers, kind } = draft
  const shown = folded && kind !== 'orphan' ? [] : workers
  const nodes: MapNode[] = []
  const lines: MapLine[] = []
  const dots: MapPoint[] = []
  const add = (card: MapCard, x: number, y: number, role: MapNode['role']) => {
    const key = runKey(card)
    nodes.push({ key, card, x, y, width: m.width, height: m.height, role, showOwner: false })
  }
  const top = captionHeight
  let width = m.width
  if (kind === 'orphan') {
    const count = Math.min(columns, workers.length)
    width = count * m.width + (count - 1) * siblingGap
    workers.forEach((card, index) => add(
      card,
      (index % columns) * (m.width + siblingGap),
      top + Math.floor(index / columns) * (m.height + levelGap),
      'worker',
    ))
  } else if (!shown.length) {
    add(root, 0, top, kind === 'standalone' ? 'standalone' : 'integrator')
  } else {
    const rail = shown.length > columns
    const count = Math.min(columns, shown.length)
    const columnWidth = rail ? railIndent + m.width : m.width
    const rowWidth = count * columnWidth + (count - 1) * siblingGap
    width = Math.max(m.width, rowWidth)
    const center = width / 2
    const bottom = top + m.height
    const childTop = bottom + levelGap
    const busY = bottom + trunk
    const left = (width - rowWidth) / 2
    add(root, center - m.width / 2, top, 'integrator')
    dots.push({ x: center, y: bottom })
    shown.forEach((card, index) => add(
      card,
      left + (index % columns) * (columnWidth + siblingGap) + (rail ? railIndent : 0),
      childTop + Math.floor(index / columns) * (m.height + railGap),
      'worker',
    ))
    const drops: { x: number; end: number }[] = []
    for (let column = 0; column < count; column++) {
      const columnX = left + column * (columnWidth + siblingGap)
      if (!rail) {
        drops.push({ x: columnX + m.width / 2, end: childTop })
        continue
      }
      const railX = columnX + railInset
      const rows = Math.ceil((shown.length - column) / columns)
      const elbows = Array.from({ length: rows }, (_, row) => childTop + row * (m.height + railGap) + elbowDrop)
      drops.push({ x: railX, end: elbows[rows - 1] - 4 })
      for (const y of elbows) lines.push({ radius: 4, points: [{ x: railX, y: y - 8 }, { x: railX, y }, { x: columnX + railIndent, y }] })
    }
    const first = drops[0]
    const last = drops[drops.length - 1]
    if (drops.length === 1 && first.x === center) {
      lines.push({ radius: 0, points: [{ x: center, y: bottom }, { x: center, y: first.end }] })
    } else {
      lines.push({ radius: 0, points: [{ x: center, y: bottom }, { x: center, y: busY }] })
      lines.push({ radius: 6, points: [{ x: first.x, y: first.end }, { x: first.x, y: busY }, { x: last.x, y: busY }, { x: last.x, y: last.end }] })
      for (const drop of drops.slice(1, -1)) lines.push({ radius: 0, points: [{ x: drop.x, y: busY }, { x: drop.x, y: drop.end }] })
    }
  }
  return {
    key: draft.key, kind, missionId: root.run.mission_id || undefined,
    x: 0, y: 0, width, height: Math.max(...nodes.map((node) => node.y + node.height)),
    nodes, workers: kind === 'orphan' ? 0 : workers.length, hidden: shown === workers ? [] : workers, lines, dots,
  }
}

/**
 * Skyline packing in recency order: each unit takes the highest free spot,
 * leftmost on a tie, so the newest sit top-left and older ones fill the room
 * under short neighbours instead of leaving shelves half empty.
 */
function flow(units: MapUnit[], wrap: number, apply: boolean) {
  let sky = [{ x: 0, width: wrap + unitGap, y: 0 }]
  let width = 0
  let height = 0
  for (const unit of units) {
    const span = unit.width + unitGap
    let spot = { x: 0, y: Infinity }
    for (let index = 0; index < sky.length && sky[index].x + span <= wrap + unitGap; index++) {
      let y = 0
      for (let next = index; next < sky.length && sky[next].x < sky[index].x + span; next++) y = Math.max(y, sky[next].y)
      if (y < spot.y) spot = { x: sky[index].x, y }
    }
    if (apply) {
      unit.x = spot.x
      unit.y = spot.y
    }
    width = Math.max(width, spot.x + unit.width)
    height = Math.max(height, spot.y + unit.height)
    const end = spot.x + span
    const step = { x: spot.x, width: span, y: spot.y + unit.height + shelfGap }
    // Spots start where a step starts, so every earlier step ends at or before it.
    sky = [
      ...sky.filter((segment) => segment.x < spot.x),
      step,
      ...sky.filter((segment) => segment.x + segment.width > end)
        .map((segment) => segment.x >= end ? segment : { x: end, width: segment.x + segment.width - end, y: segment.y }),
    ]
  }
  return { width, height }
}

function measure(sections: MapUnit[][], wrap: number, framed: boolean) {
  let width = 0
  let height = 0
  for (const units of sections) {
    const bounds = flow(units, wrap, false)
    width = Math.max(width, bounds.width + (framed ? sectionInset * 2 : 0))
    height += bounds.height + (framed ? sectionHeader + sectionInset : 0)
  }
  return { width, height: height + (sections.length - 1) * sectionGap }
}

/** One wrap width for every section, scored for a landscape world. */
function wrapWidth(sections: MapUnit[][], framed: boolean): number {
  const all = sections.flat()
  const widest = Math.max(...all.map((unit) => unit.width))
  const area = all.reduce((sum, unit) => sum + (unit.width + unitGap) * (unit.height + shelfGap), 0)
  // Wider than twice the ideal landscape width only scores worse.
  const limit = 2 * Math.sqrt(area * aspect)
  const candidates = new Set([widest])
  for (const units of sections) {
    let span = -unitGap
    for (const unit of units) {
      span += unit.width + unitGap
      if (span > limit) break
      candidates.add(Math.max(widest, span))
    }
  }
  let best = widest
  let bestScore = Infinity
  let bestArea = Infinity
  for (const wrap of candidates) {
    const bounds = measure(sections, wrap, framed)
    const score = Math.max(bounds.width / aspect, bounds.height)
    const occupied = bounds.width * bounds.height
    if (score < bestScore || (score === bestScore && occupied < bestArea)) {
      best = wrap
      bestScore = score
      bestArea = occupied
    }
  }
  return best
}

/** Moves a unit to world `x, y`; its nodes still hold unit coordinates until then. */
function placeUnit(unit: MapUnit, x: number, y: number) {
  unit.x = x
  unit.y = y
  for (const node of unit.nodes) {
    node.x += x
    node.y += y
  }
}

let revisions = 0

/**
 * Places the viewer's runs first, then other owners by their newest unit, then
 * Needs you runs from outside the scope. Units read newest first; workers in
 * spawn order. With the same structure the previous geometry is reused exactly.
 */
export function layoutRunMap(cards: MapCard[], input: MapLayoutInput, previous?: RunMapLayout): RunMapLayout {
  const byKey = new Map(cards.map((card) => [runKey(card), card]))
  const structure = structureOf(byKey, input)
  if (previous && previous.structure === structure) {
    const units: MapUnit[] = []
    const nodes: MapNode[] = []
    const sections = previous.sections.map((section) => {
      let member: Member | undefined
      const sectionUnits = section.units.map((unit) => {
        const next = {
          ...unit,
          hidden: unit.hidden.map((card) => byKey.get(runKey(card))!),
          nodes: unit.nodes.map((node) => {
            const card = byKey.get(node.key)!
            if (card.owner?.id === section.key) member ??= card.owner
            return { ...node, card }
          }),
        }
        units.push(next)
        nodes.push(...next.nodes)
        return next
      })
      return { ...section, units: sectionUnits, member, label: sectionLabel(section, member) }
    })
    return { ...previous, sections, units, nodes }
  }
  if (!byKey.size) return { width: 1, height: 1, revision: ++revisions, sections: [], units: [], nodes: [], structure }

  const ordered = [...byKey.values()].sort(byCreated)
  const parents = runParents(ordered)
  const unitOf = ({ run }: MapCard) => run.mission_role === 'worker'
    ? JSON.stringify([run.workspace_id, 'missing', run.mission_id, run.integrator_run_id || run.id])
    : runKey({ run })
  const drafts = new Map<string, Draft>()
  for (const card of ordered) {
    if (parents.has(runKey(card))) continue
    const unitKey = unitOf(card)
    const draft = drafts.get(unitKey)
    if (draft) draft.workers.push(card)
    else {
      const kind = card.run.mission_role === 'integrator' ? 'swarm' : card.run.mission_role === 'worker' ? 'orphan' : 'standalone'
      drafts.set(unitKey, { key: unitKey, kind, root: card, workers: kind === 'orphan' ? [card] : [] })
    }
  }
  for (const card of ordered) {
    const key = runKey(card)
    if (!parents.has(key)) continue
    const draft = drafts.get(unitOf(byKey.get(rootKey(key, parents))!))!
    if (draft.kind === 'standalone') draft.kind = 'family'
    draft.workers.push(card)
  }

  const grouped = new Map<string, { kind: MapSection['kind']; units: MapUnit[]; drafts: Draft[] }>()
  for (const draft of drafts.values()) {
    const unit = makeUnit(draft, input.folded[draft.key] === true, input.metrics)
    const key = draft.root.visiting ? '' : draft.root.run.member_id
    const group = grouped.get(key) ?? { kind: draft.root.visiting ? 'visiting' as const : 'owner' as const, units: [], drafts: [] }
    group.units.push(unit)
    group.drafts.push(draft)
    grouped.set(key, group)
  }
  const newest = new Map<string, MapCard>()
  for (const [key, group] of grouped) {
    const roots = new Map(group.drafts.map((draft) => [draft.key, draft.root]))
    group.units.sort((a, b) => byCreated(roots.get(b.key)!, roots.get(a.key)!))
    newest.set(key, roots.get(group.units[0].key)!)
  }
  const sectionOrder = [...grouped.keys()].sort((a, b) => {
    const rank = (key: string) => key === '' ? 2 : key === input.viewerID ? 0 : 1
    return rank(a) - rank(b) || byCreated(newest.get(b)!, newest.get(a)!)
  })

  // Needs you elsewhere stays out of the wrap and the framing decision, so its churn never reflows owner sections.
  const owned = sectionOrder.filter((key) => key !== '')
  const framed = owned.length > 1
  const wrap = wrapWidth((owned.length ? owned : ['']).map((key) => grouped.get(key)!.units), framed || !owned.length)
  const sections: MapSection[] = []
  let y = worldInset
  let width = 0
  for (const key of sectionOrder) {
    const { kind, units } = grouped.get(key)!
    const visiting = kind === 'visiting'
    const bounds = flow(units, visiting ? Math.max(wrap, ...units.map((unit) => unit.width)) : wrap, true)
    const inset = framed || visiting ? sectionInset : 0
    const header = framed || visiting ? sectionHeader : 0
    for (const unit of units) placeUnit(unit, unit.x + worldInset + inset, unit.y + y + header)
    const runs = units.flatMap((unit) => [...unit.nodes.map((node) => node.card), ...unit.hidden])
    const member = runs.find((card) => card.owner?.id === key)?.owner
    const section: MapSection = {
      key, kind, framed: framed || visiting, member, units, count: runs.length, label: '',
      x: worldInset, y, width: bounds.width + inset * 2, height: bounds.height + header + inset,
    }
    section.label = sectionLabel(section, member)
    sections.push(section)
    width = Math.max(width, section.width)
    y += section.height + sectionGap
  }
  // Frames share one width so stacked sections read as rows of one sheet.
  for (const section of sections) if (section.framed) section.width = width

  const owners = new Set(sections.filter((section) => section.kind === 'owner')
    .flatMap((section) => section.units.flatMap((unit) => unit.nodes.map((node) => node.card.run.member_id))))
  const units = sections.flatMap((section) => section.units)
  const nodes: MapNode[] = []
  for (const section of sections) {
    const owner = section.kind === 'visiting' ? input.viewerID : section.key
    for (const unit of section.units) {
      for (const node of unit.nodes) {
        node.showOwner = section.framed ? node.card.run.member_id !== owner : owners.size > 1
        nodes.push(node)
      }
    }
  }
  return {
    width: Math.max(1, width + worldInset * 2),
    height: Math.max(1, y - sectionGap + worldInset),
    revision: ++revisions,
    sections, units, nodes, structure,
  }
}

function sectionLabel(section: Pick<MapSection, 'key' | 'kind'>, member: Member | undefined): string {
  if (section.kind === 'visiting') return 'Needs you elsewhere'
  return member?.display_name || section.key || 'Unknown member'
}
