import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { type Run } from '@/lib/types'
import { layoutRunMap } from '@/routes/board/map-layout'
import { RunMap } from '@/routes/board/run-map'
import { type BoardCard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { type BoardMapViewport } from '@/store/ui'
import { alice, run, workspace } from '@/test/fixtures'

const width = 956
const height = 588
const origin = { x: 40, y: 60 }
const frames = new Map<number, FrameRequestCallback>()
let frameId = 0

function card(overrides: Partial<Run> = {}): BoardCard {
  return { run: toRecord(run(overrides)), owner: alice, state: 'working', unseen: false, paused: false }
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

function pointer(target: Element, type: string, x: number, y: number, id = 1, pointerType = 'mouse') {
  // jsdom has MouseEvent coordinates but no PointerEvent constructor/capture.
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y })
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
  frames.clear()
  useStore.setState({
    boardMapViewports: {}, workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice }, inbox: {}, runs: {}, acked: {}, pausedRuns: {},
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
      const node = this.parentElement!
      const view = camera()
      return new DOMRect(
        origin.x + view.x + Number.parseFloat(node.style.left) * view.zoom,
        origin.y + view.y + Number.parseFloat(node.style.top) * view.zoom,
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
  render(<RunMap cards={[card()]} scope={workspace.id} />)
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
  render(<RunMap cards={[card()]} scope={workspace.id} />)
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
  render(<RunMap cards={[card()]} scope={workspace.id} />)
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
  const first = render(<RunMap cards={cards} scope={workspace.id} />)
  fireEvent.wheel(canvas(), { deltaX: 5000.5, deltaY: 4000.25 })
  const blank = camera()
  expect(visibleCards()).toEqual([])
  first.unmount()
  expect(useStore.getState().boardMapViewports[workspace.id]).toEqual(blank)
  const alternate = { x: 31.125, y: 46.875, zoom: 1.17 }
  seedCamera(alternate, 'other')
  const second = render(<RunMap cards={[...cards]} scope={workspace.id} />)
  expect(camera()).toEqual(blank)
  second.rerender(<RunMap cards={cards} scope="other" />)
  expect(camera()).toEqual(alternate)
  second.rerender(<RunMap cards={[...cards]} scope={workspace.id} />)
  expect(camera()).toEqual(blank)
})

it('recovers when archived cards replace visible done cards outside the saved camera', () => {
  const done = ['done-a', 'done-b', 'done-c', 'done-d'].map((id) => card({ id, status: 'completed' }))
  const last = layoutRunMap(done).nodes.at(-1)!
  seedCamera({ x: 20 - last.x * 2, y: 20 - last.y * 2, zoom: 2 })
  const view = render(<RunMap cards={done} scope={workspace.id} />)
  expect(visibleCards().map((article) => article.getAttribute('data-run-id'))).toContain(last.card.run.id)
  const before = camera()
  const archived = card({ id: 'archived', status: 'merged', archived_at: '2026-08-14T10:00:00Z' })
  const replacement = layoutRunMap([archived]).nodes[0]
  expect((replacement.x + replacement.width) * before.zoom + before.x).toBeLessThanOrEqual(0)
  view.rerender(<RunMap cards={[archived]} scope={workspace.id} />)
  expectAllCardsInView()
  expect(camera()).not.toEqual(before)
  saveCamera()
  expect(useStore.getState().boardMapViewports[workspace.id]).toEqual(camera())
})

it('keeps a visible camera through card replacement and substantive layout changes', () => {
  const initial = { x: 17.5, y: 25.25, zoom: 0.65 }
  seedCamera(initial)
  const view = render(<RunMap cards={[card({ id: 'old' })]} scope={workspace.id} />)
  view.rerender(<RunMap cards={[card({ id: 'new-a' }), card({ id: 'new-b' })]} scope={workspace.id} />)
  expect(visibleCards().map((article) => article.getAttribute('data-run-id'))).toContain('new-a')
  expect(camera()).toEqual(initial)
})

it('does not refit an intentional blank pan for new arrays or live status and metadata updates', () => {
  const original = card({ id: 'stable' })
  const view = render(<RunMap cards={[original]} scope={workspace.id} />)
  fireEvent.wheel(canvas(), { deltaX: 5000, deltaY: 5000 })
  const blank = camera()
  expect(visibleCards()).toEqual([])
  saveCamera()
  view.rerender(<RunMap cards={[original]} scope={workspace.id} />)
  expect(camera()).toEqual(blank)
  view.rerender(<RunMap cards={[{ ...card({ id: 'stable', status: 'completed', task: 'Updated live task' }), state: 'done' }]} scope={workspace.id} />)
  expect(screen.getByRole('button', { name: 'Updated live task' })).toBeDefined()
  expect(camera()).toEqual(blank)
  // Identity changes matter even when the replacement occupies the same rect.
  view.rerender(<RunMap cards={[card({ id: 'replacement' })]} scope={workspace.id} />)
  expectAllCardsInView()
})

it('uses the latest branch-selection touch position when a second finger starts pinching', () => {
  seedCamera({ x: 0, y: 0, zoom: 1 })
  const item = card()
  render(<RunMap cards={[item]} scope={workspace.id} />)
  const surface = canvas()
  const branch = screen.getByTitle(item.run.branch)
  pointer(branch, 'pointerdown', 65, 120, 1, 'touch')
  pointer(branch, 'pointermove', 95, 120, 1, 'touch')
  expectCamera({ x: 0, y: 0, zoom: 1 })
  pointer(surface, 'pointerdown', 175, 120, 2, 'touch')
  pointer(surface, 'pointermove', 96, 120, 1, 'touch')
  const ratio = 79 / 80
  expectCamera({ x: 135.5 - origin.x - (135 - origin.x) * ratio, y: (120 - origin.y) * (1 - ratio), zoom: ratio })
  pointer(surface, 'pointerup', 96, 120, 1, 'touch')
  pointer(surface, 'pointerup', 175, 120, 2, 'touch')
  fireEvent.click(screen.getByRole('article'), { detail: 1 })
  expect(useStore.getState().route.name).toBe('board')
})

it('reveals an offscreen card when its real control receives keyboard focus', () => {
  seedCamera({ x: -1200, y: -900, zoom: 1 })
  render(<RunMap cards={[card()]} scope={workspace.id} />)
  expect(visibleCards()).toEqual([])
  const title = within(screen.getByRole('article')).getByRole('button', { name: run().task })
  fireEvent.keyDown(document.body, { key: 'Tab' })
  act(() => title.focus())
  expect(document.activeElement).toBe(title)
  expectAllCardsInView()
  // Native pointer-vs-keyboard :focus-visible heuristics require the browser
  // smoke; jsdom cannot prove that distinction and must not mock matches().
})
