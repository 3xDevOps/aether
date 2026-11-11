import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { MultiplayerControls } from '@/routes/run/multiplayer-controls'
import type { RunRecord } from '@/store/runs'

const run = { id: 'run_1', workspace_id: 'ws_1', member_id: 'mem_1' } as RunRecord

function agent(release: () => void): AgentTerminal {
  return {
    localControl: true,
    steerable: true,
    starting: false,
    controlUnavailable: false,
    takeover: { interaction: undefined, holderProgress: undefined },
    session: { takeControl: vi.fn(), releaseControl: release, state: { connection: 'live' } },
  } as unknown as AgentTerminal
}

let frames: Map<number, FrameRequestCallback>
let nextFrame: number
let now: number

function tick() {
  const pending = [...frames.values()]
  frames.clear()
  now += 16.67
  act(() => pending.forEach((callback) => callback(now)))
}

function size(strip: HTMLElement) {
  strip.getBoundingClientRect = () => ({ left: 0, top: 0, right: 120, bottom: 32, width: 120, height: 32 }) as DOMRect
}

beforeEach(() => {
  frames = new Map()
  nextFrame = 0
  now = 0
  const matchMedia = window.matchMedia
  vi.stubGlobal('matchMedia', (query: string) => ({ ...matchMedia(query), matches: false }))
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    frames.set(++nextFrame, callback)
    return nextFrame
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
  vi.spyOn(performance, 'now').mockImplementation(() => now)
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('springs and wakes dots without blocking existing controls', () => {
  const release = vi.fn()
  const people = vi.fn()
  const view = render(<MultiplayerControls run={run} agent={agent(release)} onPeople={people} />)
  const strip = screen.getByRole('group', { name: 'Multiplayer controls' })
  const matrix = strip.querySelector<HTMLElement>('.control-matrix')!
  size(strip)

  fireEvent.pointerMove(strip, { pointerType: 'mouse', clientX: 40, clientY: 12 })
  const dot = [...strip.querySelectorAll<SVGCircleElement>('.control-matrix-grid .control-matrix-dot')]
    .find((element) => element.getAttribute('cx') === '45' && element.getAttribute('cy') === '9')!
  const before = Number(dot.getAttribute('cx'))
  tick()
  expect(Number(dot.getAttribute('cx'))).toBeGreaterThan(before)

  fireEvent.pointerDown(screen.getByRole('button', { name: /Release/ }), { pointerType: 'mouse', button: 0 })
  expect(strip.querySelector('.control-matrix-ring')).not.toBeNull()
  fireEvent.click(screen.getByRole('button', { name: /Release/ }))
  fireEvent.click(screen.getByRole('button', { name: /Multiplayer/ }))
  expect(release).toHaveBeenCalledTimes(1)
  expect(people).toHaveBeenCalledTimes(1)

  fireEvent.pointerMove(strip, { pointerType: 'mouse', clientX: 40, clientY: 90 })
  for (let i = 0; i < 400 && frames.size; i++) tick()
  expect(matrix.hasAttribute('data-dot-grid-active')).toBe(false)
  view.unmount()
  expect(frames.size).toBe(0)
})

it('supports touch drag and keyboard points', () => {
  const view = render(<MultiplayerControls run={run} agent={agent(vi.fn())} onPeople={vi.fn()} />)
  const strip = screen.getByRole('group', { name: 'Multiplayer controls' })
  size(strip)
  strip.focus()
  fireEvent.keyDown(strip, { key: 'ArrowRight' })
  expect(strip.querySelector('.control-matrix-ring')).not.toBeNull()
  fireEvent.pointerDown(strip, { pointerType: 'touch', pointerId: 7, clientX: 20, clientY: 12 })
  fireEvent.pointerMove(window, { pointerType: 'touch', pointerId: 7, clientX: 70, clientY: 12 })
  expect(frames.size).toBeGreaterThan(0)
  fireEvent.pointerUp(window, { pointerType: 'touch', pointerId: 7 })
  for (let i = 0; i < 400 && frames.size; i++) tick()
  view.unmount()
})

it('keeps a frozen ring and no animation loop with reduced motion', () => {
  const matchMedia = window.matchMedia
  vi.stubGlobal('matchMedia', (query: string) => ({ ...matchMedia(query), matches: query.includes('prefers-reduced-motion') }))
  render(<MultiplayerControls run={run} agent={agent(vi.fn())} onPeople={vi.fn()} />)
  const strip = screen.getByRole('group', { name: 'Multiplayer controls' })
  size(strip)
  fireEvent.pointerDown(strip, { pointerType: 'touch', pointerId: 1, clientX: 30, clientY: 12 })
  expect(strip.querySelector('.control-matrix-ring')).not.toBeNull()
  expect(frames.size).toBe(0)
})
