import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { MultiplayerControls } from '@/routes/run/multiplayer-controls'
import type { RunRecord } from '@/store/runs'
import { useStore } from '@/store'

const run = { id: 'run_1', workspace_id: 'ws_1', member_id: 'mem_1' } as RunRecord

function agent(release: () => void, holderProgress?: number): AgentTerminal {
  return {
    localControl: true,
    steerable: true,
    starting: false,
    controlUnavailable: false,
    takeover: { interaction: undefined, holderProgress },
    session: { takeControl: vi.fn(), releaseControl: release, state: { connection: 'live' } },
  } as unknown as AgentTerminal
}

let frames: Map<number, FrameRequestCallback>
let nextFrame: number
let timers: Map<number, { callback: () => void; at: number }>
let nextTimer: number
let now: number

function tick() {
  const pending = [...frames.values()]
  frames.clear()
  now += 16.67
  act(() => pending.forEach((callback) => callback(now)))
}

function advance(ms: number) {
  now += ms
  const due = [...timers.entries()].filter(([, timer]) => timer.at <= now)
  for (const [id, timer] of due) {
    timers.delete(id)
    timer.callback()
  }
}

function strip(): HTMLElement {
  const element = screen.getByRole('group', { name: 'Multiplayer controls' })
  element.querySelector<HTMLElement>('.control-matrix')!.getBoundingClientRect = () =>
    ({ left: 0, top: 0, right: 120, bottom: 32, width: 120, height: 32 }) as DOMRect
  return element
}

function positions(layer: Element | null): string[] {
  return [...layer!.querySelectorAll('circle:not(.control-matrix-ring)')].map((dot) => `${dot.getAttribute('cx')},${dot.getAttribute('cy')}`)
}

function moved(layer: Element | null): string[] {
  return positions(layer).filter((point) => point.split(',').some((value) => Number(value) % 6 !== 3))
}

function settle() {
  for (let i = 0; i < 400 && (frames.size || timers.size); i++) {
    if (timers.size) {
      const next = Math.min(...[...timers.values()].map((timer) => timer.at))
      advance(Math.max(0, next - now))
    }
    if (frames.size) tick()
  }
}

beforeEach(() => {
  frames = new Map()
  nextFrame = 0
  timers = new Map()
  nextTimer = 0
  now = 0
  const matchMedia = window.matchMedia
  vi.stubGlobal('matchMedia', (query: string) => ({ ...matchMedia(query), matches: false }))
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    frames.set(++nextFrame, callback)
    return nextFrame
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
  vi.stubGlobal('setTimeout', (callback: () => void, delay = 0) => {
    const id = ++nextTimer
    timers.set(id, { callback, at: now + delay })
    return id
  })
  vi.stubGlobal('clearTimeout', (id: number) => timers.delete(id))
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
  const group = strip()
  const matrix = group.querySelector<HTMLElement>('.control-matrix')!

  fireEvent.pointerMove(group, { pointerType: 'mouse', clientX: 40, clientY: 12 })
  tick()
  fireEvent.pointerMove(group, { pointerType: 'mouse', clientX: 52, clientY: 12 })
  expect(frames.size).toBe(0)
  expect(timers.size).toBe(1)
  advance(100)
  tick()
  const [neutral, colored] = group.querySelectorAll('.control-matrix-grid')
  expect(group.querySelectorAll('.control-matrix-grid')).toHaveLength(2)
  expect(moved(neutral!).length).toBeGreaterThan(0)
  expect(positions(colored!)).toEqual(positions(neutral!))

  const releaseButton = screen.getByRole('button', { name: /Release/ })
  releaseButton.focus()
  fireEvent.click(releaseButton)
  expect(release).toHaveBeenCalledTimes(1)
  fireEvent.pointerDown(releaseButton, { pointerType: 'mouse', button: 0, clientX: 100, clientY: 12 })
  advance(100)
  tick()
  expect(Number(group.querySelector('.control-matrix-ring')!.getAttribute('opacity'))).toBeGreaterThan(0)
  fireEvent.pointerUp(releaseButton, { pointerType: 'mouse', button: 0 })

  const peopleButton = screen.getByRole('button', { name: /Multiplayer/ })
  peopleButton.focus()
  fireEvent.click(peopleButton)
  expect(people).toHaveBeenCalledTimes(1)

  fireEvent.pointerMove(group, { pointerType: 'mouse', clientX: 40, clientY: 90 })
  settle()
  expect(matrix.hasAttribute('data-dot-grid-active')).toBe(false)
  view.unmount()
  expect(frames.size).toBe(0)
  expect(timers.size).toBe(0)
})

it('moves the red takeover hold copy with the other dots', async () => {
  const release = vi.fn()
  const view = render(<MultiplayerControls run={run} agent={agent(release, 0.4)} onPeople={vi.fn()} />)
  const group = strip()
  fireEvent.pointerMove(group, { pointerType: 'mouse', clientX: 60, clientY: 12 })
  tick()
  const neutral = group.querySelector('.control-matrix > .control-matrix-grid')
  expect(moved(neutral).length).toBeGreaterThan(0)
  expect(positions(group.querySelector('[data-takeover-hold] .control-matrix-grid'))).toEqual(positions(neutral))

  view.rerender(<MultiplayerControls run={run} agent={agent(release)} onPeople={vi.fn()} />)
  view.rerender(<MultiplayerControls run={run} agent={agent(release, 0.6)} onPeople={vi.fn()} />)
  await act(async () => {})
  expect(positions(group.querySelector('[data-takeover-hold] .control-matrix-grid'))).toEqual(positions(neutral))
})

it('supports touch drag and cancellation', () => {
  useStore.setState({
    roomStatus: {
      [run.id]: { workspace_id: run.workspace_id, run_id: run.id, protected: false, watchers: ['mem_2'], queued_steers: 0 },
    },
  })
  const view = render(<MultiplayerControls run={run} agent={agent(vi.fn())} onPeople={vi.fn()} />)
  const group = strip()
  expect(group.hasAttribute('tabindex')).toBe(false)
  fireEvent.pointerDown(group, { pointerType: 'touch', pointerId: 7, clientX: 20, clientY: 12 })
  fireEvent.pointerMove(group, { pointerType: 'touch', pointerId: 7, clientX: 70, clientY: 12 })
  expect(frames.size).toBeGreaterThan(0)
  fireEvent.pointerCancel(group, { pointerType: 'touch', pointerId: 7 })
  settle()
  expect(group.querySelector('.control-matrix')!.hasAttribute('data-dot-grid-active')).toBe(false)
  view.unmount()
})

it('keeps the static dots with reduced motion', () => {
  const matchMedia = window.matchMedia
  vi.stubGlobal('matchMedia', (query: string) => ({ ...matchMedia(query), matches: query.includes('prefers-reduced-motion') }))
  render(<MultiplayerControls run={run} agent={agent(vi.fn())} onPeople={vi.fn()} />)
  const group = strip()
  fireEvent.pointerDown(group, { pointerType: 'touch', pointerId: 1, clientX: 30, clientY: 12 })
  expect(frames.size).toBe(0)
  expect(group.querySelector('.control-matrix-grid')).toBeNull()
})
