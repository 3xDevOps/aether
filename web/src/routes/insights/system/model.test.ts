import type { SystemContainer, SystemHost, SystemSample } from '@/lib/types'
import { appendHistory, assignSlots, breakdown, capacityOf, limitLevel, mergeSamples, slotCount } from '@/routes/insights/system/model'
import { systemContainer, systemStats } from '@/test/fixtures'

const host: SystemHost = { cpu_cores: 10, memory_total_bytes: 1000, swap_total_bytes: 0, uptime_seconds: 0 }

function sample(over: Partial<SystemSample> = {}): SystemSample {
  return { ...systemStats().samples[0]!, cpu_user: 40, cpu_system: 10, cpu_iowait: 0, cpu_steal: 0, memory_used_bytes: 600, ...over }
}

const named = (id: string, cpu: number, memory = 1): SystemContainer => systemContainer({ owner_id: id, title: id, cpu_cores: cpu, memory_bytes: memory })
const label = (c: SystemContainer) => c.title ?? c.owner_id

describe('breakdown', () => {
  const capacity = capacityOf(host, sample(), 'cpu')

  it('sizes capacity and use from the newest sample', () => {
    expect(capacity).toEqual({ capacity: 10, used: 5 })
    expect(capacityOf(host, sample(), 'memory')).toEqual({ capacity: 1000, used: 600 })
  })

  it('lists at most five containers of 2% or more and folds the rest into Other runs', () => {
    const containers = [3, 2, 1.5, 1, 0.8, 0.6, 0.19, 0.2].map((cpu, i) => named(`r${i}`, cpu))
    const { slices, folded } = breakdown(containers, capacity, 0, 'cpu', new Map(), label)

    expect(slices.filter((s) => s.kind === 'run').map((s) => s.label)).toEqual(['r0', 'r1', 'r2', 'r3', 'r4'])
    const other = slices.find((s) => s.kind === 'other')!
    expect(other.containers.map((c) => c.owner_id)).toEqual(['r5', 'r7', 'r6'])
    expect(other.value).toBeCloseTo(0.99)
    expect([...folded]).toEqual(['run:r5', 'run:r7', 'run:r6'])
  })

  it('has no Other slice when every container is listed', () => {
    const { slices } = breakdown([named('a', 2), named('b', 1)], capacity, 0, 'cpu', new Map(), label)
    expect(slices.map((s) => s.kind)).toEqual(['run', 'run', 'rest', 'free'])
  })

  it('leaves host use outside the containers as Everything else and the remainder as Free', () => {
    const { slices, rest } = breakdown([named('a', 2), named('b', 1)], capacity, 0, 'cpu', new Map(), label)
    expect(rest).toBeCloseTo(2)
    expect(slices.map((s) => s.value)).toEqual([2, 1, 2, 5])
  })

  it('never goes negative when containers sum past the host figure', () => {
    const { slices } = breakdown([named('a', 8)], { capacity: 10, used: 5 }, 0, 'cpu', new Map(), label)
    expect(slices.map((s) => s.value)).toEqual([8, 0, 2])
  })

  it('keeps hidden containers in Everything else when the host sample reads lower', () => {
    const { slices, rest } = breakdown([named('a', 4)], { capacity: 10, used: 4 }, 1.5, 'cpu', new Map(), label)
    expect(rest).toBe(1.5)
    expect(slices.map((s) => s.value)).toEqual([4, 1.5, 4.5])
  })
})

describe('assignSlots', () => {
  it('keeps a run on its slot while it is present, across polls and folds', () => {
    const first = assignSlots(new Map(), ['run:a', 'run:b'], new Set(['run:a', 'run:b', 'run:c']))
    expect([...first]).toEqual([['run:a', 0], ['run:b', 1]])

    const reordered = assignSlots(first, ['run:b', 'run:c', 'run:a'], new Set(['run:a', 'run:b', 'run:c']))
    expect(reordered.get('run:a')).toBe(0)
    expect(reordered.get('run:b')).toBe(1)
    expect(reordered.get('run:c')).toBe(2)

    const folded = assignSlots(reordered, ['run:b'], new Set(['run:a', 'run:b', 'run:c']))
    expect(assignSlots(folded, ['run:a', 'run:b'], new Set(['run:a', 'run:b', 'run:c'])).get('run:a')).toBe(0)
  })

  it('keeps the slot of a folded run that is still present', () => {
    const present = new Set(['run:a', 'run:b', 'run:c'])
    const first = assignSlots(new Map(), ['run:a', 'run:b'], present)
    const aFolded = assignSlots(first, ['run:b', 'run:c'], present)
    expect(aFolded.get('run:c')).toBe(2)
    const back = assignSlots(aFolded, ['run:c', 'run:a'], present)
    expect(back.get('run:a')).toBe(0)
    expect(back.get('run:c')).toBe(2)
  })

  it('takes a hidden run\'s slot only when every slot is held', () => {
    const keys = Array.from({ length: slotCount }, (_, i) => `run:${i}`)
    const present = new Set([...keys, 'run:new'])
    const full = assignSlots(new Map(), keys, present)
    const next = assignSlots(full, ['run:new', 'run:1'], present)
    expect(next.get('run:new')).toBe(0)
    expect(next.has('run:0')).toBe(false)
    expect(next.get('run:1')).toBe(1)
  })

  it('keeps colors when the metric switches', () => {
    const containers = [named('a', 3, 10), named('b', 2, 400), named('c', 1, 300)]
    const cpu = breakdown(containers, { capacity: 10, used: 6 }, 0, 'cpu', new Map(), label)
    const memory = breakdown(containers, { capacity: 1000, used: 800 }, 0, 'memory', cpu.slots, label)
    for (const key of ['run:a', 'run:b', 'run:c']) expect(memory.slots.get(key)).toBe(cpu.slots.get(key))
  })

  it('releases the slot of a run that disappears and gives it to the next arrival', () => {
    const first = assignSlots(new Map(), ['run:a', 'run:b'], new Set(['run:a', 'run:b']))
    const gone = assignSlots(first, ['run:b'], new Set(['run:b']))
    expect(gone.has('run:a')).toBe(false)
    expect(assignSlots(gone, ['run:b', 'run:d'], new Set(['run:b', 'run:d'])).get('run:d')).toBe(0)
  })
})

describe('limits', () => {
  it('flags use near and at the limit', () => {
    expect([undefined, 0.5, 0.8, 0.94, 0.95].map(limitLevel)).toEqual([undefined, undefined, 'near', 'near', 'critical'])
  })
})

describe('history', () => {
  it('appends each container snapshot, trims to 15 minutes and drops absent containers', () => {
    const t = 10 * 60_000 * 3
    let history = appendHistory(new Map(), [named('a', 1), named('b', 2)], t)
    history = appendHistory(history, [named('a', 2)], t + 5000)
    history = appendHistory(history, [named('a', 3)], t + 16 * 60_000)
    expect(history.has('run:b')).toBe(false)
    expect(history.get('run:a')!.cpu).toEqual([{ t: t + 16 * 60_000, v: 3 }])
  })

  it('merges only newer samples and keeps the retained window', () => {
    const at = (t_ms: number) => sample({ t_ms })
    const merged = mergeSamples([at(1000), at(2000)], [at(2000), at(3000), at(4000)], 2500)
    expect(merged.map((s) => s.t_ms)).toEqual([2000, 3000, 4000])
  })
})
