import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { type Run } from '@/lib/types'
import { RunMap } from '@/routes/board/run-map'
import { type MapCard } from '@/routes/board/selectors'
import '@/routes/diff/conflict-chips'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { type BoardMapViewport } from '@/store/ui'
import { alice, bob, run, workspace } from '@/test/fixtures'

let width = 956
const height = 588
const origin = { x: 40, y: 60 }
const frames = new Map<number, FrameRequestCallback>()
let frameId = 0


function card(overrides: Partial<Run> = {}): MapCard {
  const record = toRecord(run(overrides))
  return {
    run: record, owner: [alice, bob].find((member) => member.id === record.member_id), state: 'working', group: 'working',
    reason: 'Agent working', since: record.stateChangedAt, waitingSince: record.stateChangedAt, children: [], visiting: false,
  }
}

function canvas() {
  const element = screen.getByRole('region', { name: 'Map canvas' })
  if (!element.setPointerCapture) {
    const captures = new Set<number>()
    element.setPointerCapture = (id) => { captures.add(id) }
    element.hasPointerCapture = (id) => captures.has(id)
    element.releasePointerCapture = (id) => { captures.delete(id) }
  }
  return element
}

function camera(): BoardMapViewport {
  const world = document.querySelector<HTMLElement>('[data-run-map-world]')!
  const transform = /^translate\(([-\d.e+]+)px, ([-\d.e+]+)px\) scale\(([-\d.e+]+)\)$/.exec(world.style.transform)!
  return { x: Number(transform[1]), y: Number(transform[2]), zoom: Number(transform[3]) }
}

function expectCamera(expected: BoardMapViewport) {
  const actual = camera()
  expect(actual.x).toBeCloseTo(expected.x, 8)
  expect(actual.y).toBeCloseTo(expected.y, 8)
  expect(actual.zoom).toBeCloseTo(expected.zoom, 8)
}

function pointer(target: Element, type: string, x: number, y: number, id = 1, pointerType = 'mouse', buttons = type === 'pointerup' || type === 'pointercancel' ? 0 : 1) {
  // jsdom has MouseEvent coordinates but no PointerEvent constructor/capture.
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, buttons, clientX: x, clientY: y })
  Object.defineProperties(event, { pointerId: { value: id }, pointerType: { value: pointerType } })
  fireEvent(target, event)
}

function saveCamera() {
  act(() => {
    const pending = [...frames.values()]
    frames.clear()
    for (const callback of pending) callback(0)
  })
}

function seedCamera(value: BoardMapViewport, scope = workspace.id) {
  useStore.setState({ boardMapViewports: { ...useStore.getState().boardMapViewports, [scope]: value } })
}

function visibleCards() {
  const bounds = canvas().getBoundingClientRect()
  return screen.getAllByRole('article').filter((article) => {
    const rect = article.getBoundingClientRect()
    return rect.right > bounds.left && rect.left < bounds.right && rect.bottom > bounds.top && rect.top < bounds.bottom
  })
}

function expectAllCardsInView() {
  const bounds = canvas().getBoundingClientRect()
  for (const article of screen.getAllByRole('article')) {
    const rect = article.getBoundingClientRect()
    expect(rect.left).toBeGreaterThanOrEqual(bounds.left + 12)
    expect(rect.top).toBeGreaterThanOrEqual(bounds.top + 12)
    expect(rect.right).toBeLessThanOrEqual(bounds.right - 12)
    expect(rect.bottom).toBeLessThanOrEqual(bounds.bottom - 12)
  }
}

beforeEach(() => {
  width = 956
  frames.clear()
  useStore.setState({
    boardMapViewports: {}, boardMapFolded: {}, workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice, [bob.id]: bob }, runs: {}, pausedRuns: {}, overlaps: {},
    route: { name: 'board', params: {} },
  })
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    frames.set(++frameId, callback)
    return frameId
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => { frames.delete(id) })
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(function (this: HTMLElement) {
    return this.hasAttribute('data-run-map-canvas') ? width : 0
  })
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockImplementation(function (this: HTMLElement) {
    return this.hasAttribute('data-run-map-canvas') ? height : 0
  })
  const originalBounds = HTMLElement.prototype.getBoundingClientRect
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    if (this.hasAttribute('data-run-map-canvas')) return new DOMRect(origin.x, origin.y, width, height)
    if (this.tagName === 'ARTICLE' && this.closest('[data-run-map-world]')) {
      // Nodes sit inside unit and section containers; their offsets add up to world coordinates.
      const node = this.parentElement!
      let left = 0
      let top = 0
      for (let el: HTMLElement | null = node; el && !el.hasAttribute('data-run-map-world'); el = el.parentElement) {
        left += Number.parseFloat(el.style.left) || 0
        top += Number.parseFloat(el.style.top) || 0
      }
      const view = camera()
      return new DOMRect(
        origin.x + view.x + left * view.zoom,
        origin.y + view.y + top * view.zoom,
        Number.parseFloat(node.style.width) * view.zoom,
        Number.parseFloat(node.style.height) * view.zoom,
      )
    }
    return originalBounds.call(this)
  })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('drags the rendered canvas and stops moving when the pointer is released', () => {
  const initial = { x: 20, y: 30, zoom: 0.8 }
  seedCamera(initial)
  render(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  const surface = canvas()
  pointer(surface, 'pointerdown', 100, 100)
  pointer(surface, 'pointermove', 180, 145)
  expectCamera({ ...initial, x: 100, y: 75 })
  pointer(surface, 'pointerup', 180, 145)
  pointer(surface, 'pointermove', 300, 300)
  expectCamera({ ...initial, x: 100, y: 75 })
  saveCamera()
  expect(useStore.getState().boardMapViewports[workspace.id]).toEqual(camera())
})

it('zooms around the wheel pointer rather than the canvas origin or center', () => {
  const initial = { x: 20, y: 30, zoom: 0.8 }
  seedCamera(initial)
  render(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  const anchor = { x: 240, y: 150 }
  fireEvent.wheel(canvas(), {
    ctrlKey: true, deltaY: -Math.log(1.5) / 0.002,
    clientX: origin.x + anchor.x, clientY: origin.y + anchor.y,
  })
  const next = camera()
  expect(next.zoom).toBeCloseTo(1.2, 8)
  expect((anchor.x - next.x) / next.zoom).toBeCloseTo((anchor.x - initial.x) / initial.zoom, 8)
  expect((anchor.y - next.y) / next.zoom).toBeCloseTo((anchor.y - initial.y) / initial.zoom, 8)
})

it('pans and zooms by keyboard and fits displaced cards back into view', () => {
  seedCamera({ x: 20, y: 30, zoom: 0.8 })
  render(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  const surface = canvas()
  fireEvent.keyDown(surface, { key: 'ArrowRight' })
  fireEvent.keyDown(surface, { key: 'ArrowDown', shiftKey: true })
  const panned = { x: -20, y: -90, zoom: 0.8 }
  expectCamera(panned)
  fireEvent.keyDown(surface, { key: '+' })
  expectCamera({ x: width / 2 - (width / 2 - panned.x) * 1.2, y: height / 2 - (height / 2 - panned.y) * 1.2, zoom: 0.96 })
  fireEvent.keyDown(surface, { key: '-' })
  expectCamera(panned)
  fireEvent.wheel(surface, { deltaX: 5000, deltaY: 5000 })
  expect(visibleCards()).toEqual([])
  fireEvent.keyDown(surface, { key: 'Home' })
  expectAllCardsInView()
  const fitted = camera()
  fireEvent.wheel(surface, { deltaX: 5000 })
  fireEvent.click(screen.getByRole('button', { name: 'Fit map to view' }))
  expect(camera()).toEqual(fitted)
})

it('restores an exact intentionally blank camera on remount and scope round trips, even before a save frame', () => {
  const cards = [card()]
  seedCamera({ x: 13.25, y: 27.75, zoom: 0.73 })
  const first = render(<RunMap cards={cards} scope={workspace.id} agentNames={{}} />)
  fireEvent.wheel(canvas(), { deltaX: 5000.5, deltaY: 4000.25 })
  const blank = camera()
  expect(visibleCards()).toEqual([])
  first.unmount()
  expect(useStore.getState().boardMapViewports[workspace.id]).toEqual(blank)
  const alternate = { x: 31.125, y: 46.875, zoom: 1.17 }
  seedCamera(alternate, 'other')
  const second = render(<RunMap cards={[...cards]} scope={workspace.id} agentNames={{}} />)
  expect(camera()).toEqual(blank)
  second.rerender(<RunMap cards={cards} scope="other" agentNames={{}} />)
  expect(camera()).toEqual(alternate)
  second.rerender(<RunMap cards={[...cards]} scope={workspace.id} agentNames={{}} />)
  expect(camera()).toEqual(blank)
})

it('keeps a visible camera through card replacement and substantive layout changes', () => {
  const initial = { x: 17.5, y: 25.25, zoom: 0.65 }
  seedCamera(initial)
  const view = render(<RunMap cards={[card({ id: 'old' })]} scope={workspace.id} agentNames={{}} />)
  view.rerender(<RunMap cards={[card({ id: 'new-a' }), card({ id: 'new-b' })]} scope={workspace.id} agentNames={{}} />)
  expect(visibleCards().map((article) => article.getAttribute('data-run-id'))).toContain('new-a')
  expect(camera()).toEqual(initial)
})

it('does not refit an intentional blank pan for new arrays or live status and metadata updates', () => {
  const original = card({ id: 'stable' })
  const teammate = card({ id: 'teammate', member_id: bob.id })
  const view = render(<RunMap cards={[original, teammate]} scope={workspace.id} agentNames={{}} />)
  fireEvent.wheel(canvas(), { deltaX: 5000, deltaY: 5000 })
  const blank = camera()
  expect(visibleCards()).toEqual([])
  saveCamera()
  view.rerender(<RunMap cards={[original, teammate]} scope={workspace.id} agentNames={{}} />)
  expect(camera()).toEqual(blank)
  view.rerender(<RunMap cards={[{
    ...card({ id: 'stable', status: 'completed', task: 'Updated live task' }), state: 'completed',
    owner: { ...alice, display_name: 'Renamed member' },
  }, teammate]} scope={workspace.id} agentNames={{}} />)
  expect(screen.getByRole('button', { name: 'Updated live task' })).toBeDefined()
  expect(screen.getByRole('heading', { name: 'Renamed member' })).toBeDefined()
  expect(camera()).toEqual(blank)
  // A live replacement cannot reset an intentionally blank pan either.
  view.rerender(<RunMap cards={[card({ id: 'replacement' })]} scope={workspace.id} agentNames={{}} />)
  expect(camera()).toEqual(blank)
})

it('reveals an offscreen card when its real control receives keyboard focus', () => {
  seedCamera({ x: -1200, y: -900, zoom: 1 })
  render(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  expect(visibleCards()).toEqual([])
  const title = within(screen.getByRole('article')).getByRole('button', { name: run().task })
  fireEvent.keyDown(document.body, { key: 'Tab' })
  act(() => title.focus())
  expect(document.activeElement).toBe(title)
  expectAllCardsInView()
  // Native pointer-vs-keyboard :focus-visible heuristics require the browser
  // smoke; jsdom cannot prove that distinction and must not mock matches().
})

it('zooms from the Runs strip around the canvas center and fits back into view', () => {
  const initial = { x: 20, y: 30, zoom: 0.8 }
  seedCamera(initial)
  render(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  const header = screen.getByRole('heading', { name: 'Runs 1' }).closest('header')!
  fireEvent.click(within(header).getByRole('button', { name: 'Zoom in' }))
  expectCamera({
    x: width / 2 - (width / 2 - initial.x) * 1.2,
    y: height / 2 - (height / 2 - initial.y) * 1.2,
    zoom: 0.96,
  })
  fireEvent.click(within(header).getByRole('button', { name: 'Zoom out' }))
  expectCamera(initial)
  fireEvent.wheel(canvas(), { deltaX: 5000, deltaY: 5000 })
  expect(visibleCards()).toEqual([])
  fireEvent.click(within(header).getByRole('button', { name: 'Fit map to view' }))
  expectAllCardsInView()
})

it.each(['mouse', 'touch'])('pans from a card with %s without opening it, then permits a deliberate tap', (pointerType) => {
  seedCamera({ x: 0, y: 0, zoom: 1 })
  render(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  const surface = canvas()
  const title = screen.getByRole('button', { name: run().task })
  pointer(title, 'pointerdown', 160, 160, 1, pointerType)
  pointer(title, 'pointermove', 200, 190, 1, pointerType)
  pointer(surface, 'pointerup', 200, 190, 1, pointerType)
  fireEvent.click(title, { detail: 1 })
  expectCamera({ x: 40, y: 30, zoom: 1 })
  expect(useStore.getState().route.name).toBe('board')
  pointer(title, 'pointerdown', 200, 190, 1, pointerType)
  pointer(title, 'pointerup', 200, 190, 1, pointerType)
  fireEvent.click(title, { detail: 1 })
  expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: run().id } })
})

it('ends a card press released outside the canvas before capture, without hover panning or swallowing the next click', () => {
  const initial = { x: 0, y: 0, zoom: 1 }
  seedCamera(initial)
  render(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  const surface = canvas()
  const title = screen.getByRole('button', { name: run().task })
  pointer(title, 'pointerdown', 160, 160)
  pointer(document.body, 'pointerup', 1200, 160)
  pointer(surface, 'pointermove', 200, 190, 1, 'mouse', 0)
  pointer(surface, 'pointermove', 240, 220, 1, 'mouse', 0)
  expectCamera(initial)
  pointer(title, 'pointerdown', 160, 160)
  pointer(title, 'pointerup', 160, 160)
  fireEvent.click(title, { detail: 1 })
  expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: run().id } })
})

it('keeps the world under the pinch midpoint, suppresses its click, and stops after cancellation', () => {
  seedCamera({ x: 20, y: 30, zoom: 0.8 })
  render(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  const surface = canvas()
  pointer(surface, 'pointerdown', 140, 160, 1, 'touch')
  pointer(surface, 'pointerdown', 240, 160, 2, 'touch')
  pointer(surface, 'pointermove', 290, 160, 2, 'touch')
  expectCamera({ x: -20, y: -5, zoom: 1.2 })
  pointer(surface, 'pointercancel', 140, 160, 1, 'touch')
  pointer(surface, 'pointercancel', 290, 160, 2, 'touch')
  pointer(surface, 'pointermove', 500, 500, 2, 'touch')
  expectCamera({ x: -20, y: -5, zoom: 1.2 })
  fireEvent.click(screen.getByRole('button', { name: run().task }), { detail: 1 })
  expect(useStore.getState().route.name).toBe('board')
})


const at = (minute: number) => new Date(Date.UTC(2026, 7, 14, 10, minute)).toISOString()

it('opens a map too large to read whole at 100% on its newest runs, while Home still fits everything', () => {
  const cards = Array.from({ length: 40 }, (_, index) => card({ id: `run-${index}`, task: `task ${index}`, created_at: at(index) }))
  render(<RunMap cards={cards} scope={workspace.id} agentNames={{}} />)
  expect(camera()).toEqual({ x: 0, y: 0, zoom: 1 })
  expect(visibleCards()[0].getAttribute('data-run-id')).toBe('run-39')
  fireEvent.keyDown(canvas(), { key: 'Home' })
  expect(camera().zoom).toBeLessThan(0.5)
  expectAllCardsInView()
})

it('opens a wide swarm on a phone at 100% with its integrator in view', () => {
  width = 390
  render(<RunMap cards={[
    card({ id: 'root', mission_id: 'mission', mission_role: 'integrator' }),
    ...[1, 2, 3, 4, 5].map((n) => card({ id: `worker-${n}`, mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root', created_at: at(n) })),
  ]} scope={workspace.id} agentNames={{}} />)
  expect(camera().zoom).toBe(1)
  const bounds = canvas().getBoundingClientRect()
  const root = document.querySelector('[data-run-id="root"]')!.getBoundingClientRect()
  expect(root.left).toBeGreaterThanOrEqual(bounds.left)
  expect(root.right).toBeLessThanOrEqual(bounds.right)
})

it('shows the empty message with no runs, including after a filter removes every run', () => {
  const view = render(<RunMap cards={[]} scope={workspace.id} agentNames={{}} />)
  expect(screen.getByText('No runs in this view.')).toBeDefined()
  view.rerender(<RunMap cards={[card()]} scope={workspace.id} agentNames={{}} />)
  expect(screen.queryByText('No runs in this view.')).toBeNull()
  view.rerender(<RunMap cards={[]} scope={workspace.id} agentNames={{}} />)
  expect(screen.getByText('No runs in this view.')).toBeDefined()
})

function swarm() {
  return [
    card({ id: 'root', task: 'coordinate checkout', mission_id: 'mission', mission_role: 'integrator' }),
    { ...card({ id: 'asking', task: 'ask about tax', mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root', created_at: at(2) }), group: 'needs-you' as const, state: 'needs-you' as const, reason: 'Which tax table?' },
    card({ id: 'building', task: 'build cart', mission_id: 'mission', mission_role: 'worker', integrator_run_id: 'root', created_at: at(3) }),
  ]
}
const rootKey = JSON.stringify([workspace.id, 'root'])

it('folds a swarm to its integrator, keeps the fold, and marks hidden workers that need you', async () => {
  seedCamera({ x: 0, y: 0, zoom: 1 })
  render(<RunMap cards={swarm()} scope={workspace.id} agentNames={{}} />)
  expect(screen.getAllByRole('article')).toHaveLength(3)
  const rootBox = () => document.querySelector('[data-run-id="root"]')!.getBoundingClientRect()
  const before = rootBox()
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Collapse the workers of coordinate checkout' }))
  })
  expect(screen.getAllByRole('article').map((article) => article.dataset.runId)).toEqual(['root'])
  // The repack re-centres the integrator in a narrower unit; the camera follows so it stays put.
  expect(camera().x).not.toBe(0)
  expect(rootBox().left).toBeCloseTo(before.left, 6)
  expect(rootBox().top).toBeCloseTo(before.top, 6)
  expect(useStore.getState().boardMapFolded).toEqual({ [rootKey]: true })
  const expand = screen.getByRole('button', { name: 'Expand the workers of coordinate checkout, 2 hidden' })
  expect(expand.textContent).toBe('+2 · 1 need you')
  expect(screen.getByRole('heading', { name: 'Runs 3' })).toBeDefined()
  fireEvent.click(expand)
  expect(screen.getAllByRole('article')).toHaveLength(3)
  expect(useStore.getState().boardMapFolded).toEqual({})
})

it('reveals a folded worker from the Needs you chip by unfolding its swarm and centring it', async () => {
  seedCamera({ x: -4000, y: -4000, zoom: 0.3 })
  useStore.setState({ boardMapFolded: { [rootKey]: true } })
  render(<RunMap cards={swarm()} scope={workspace.id} agentNames={{}} />)
  expect(screen.queryByRole('button', { name: 'ask about tax' })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: /1 needs you/ }))
  await act(async () => {
    fireEvent.click(await screen.findByRole('button', { name: /ask about tax/ }))
  })
  const title = screen.getByRole('button', { name: 'ask about tax' })
  expect(document.activeElement).toBe(title)
  expect(useStore.getState().boardMapFolded).toEqual({})
  expect(camera().zoom).toBe(1)
  const bounds = canvas().getBoundingClientRect()
  const rect = title.closest('article')!.getBoundingClientRect()
  expect(rect.left + rect.width / 2).toBeCloseTo(bounds.left + bounds.width / 2, 6)
  expect(rect.top + rect.height / 2).toBeCloseTo(bounds.top + bounds.height / 2, 6)
})

it('reveals a visible worker from the Needs you chip through the unfold path without folding anything', async () => {
  seedCamera({ x: -4000, y: -4000, zoom: 0.3 })
  render(<RunMap cards={swarm()} scope={workspace.id} agentNames={{}} />)
  fireEvent.click(screen.getByRole('button', { name: /1 needs you/ }))
  await act(async () => {
    fireEvent.click(within(await screen.findByLabelText('Runs that need you')).getByRole('button', { name: /ask about tax/ }))
  })
  const title = screen.getByRole('button', { name: 'ask about tax' })
  expect(document.activeElement).toBe(title)
  expect(useStore.getState().boardMapFolded).toEqual({})
  expect(camera().zoom).toBe(1)
  expect(screen.getAllByRole('article')).toHaveLength(3)
})
