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

let frames: FrameRequestCallback[] = []

beforeEach(() => {
  frames = []
  const matchMedia = window.matchMedia
  vi.stubGlobal('matchMedia', (query: string) => ({ ...matchMedia(query), matches: query === '(hover: hover) and (pointer: fine)' }))
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => frames.push(callback))
  vi.stubGlobal('cancelAnimationFrame', (id: number) => { delete frames[id - 1] })
})

afterEach(() => vi.unstubAllGlobals())

it('fades the dots around a mouse without blocking the strip’s buttons', () => {
  const release = vi.fn()
  const people = vi.fn()
  const view = render(<MultiplayerControls run={run} agent={agent(release)} onPeople={people} />)
  const strip = screen.getByRole('group', { name: 'Multiplayer controls' })
  const dots = strip.querySelector('.control-matrix')!
  expect(dots.getAttribute('aria-hidden')).toBe('true')
  expect(strip.dataset.dotFade).toBe('true')
  strip.getBoundingClientRect = () => ({ left: 0, top: 0, right: 800, bottom: 32 }) as DOMRect

  const button = screen.getByRole('button', { name: /Release/ })
  fireEvent.pointerMove(button, { pointerType: 'mouse', clientX: 40, clientY: 12 })
  fireEvent.pointerMove(button, { pointerType: 'mouse', clientX: 60, clientY: 14 })
  expect(frames.filter(Boolean)).toHaveLength(1)
  act(() => frames.splice(0).forEach((frame) => frame(0)))
  expect(strip.style.getPropertyValue('--dot-px')).toBe('60px')
  expect(strip.style.getPropertyValue('--dot-fade')).toBe('1')

  const down = fireEvent.pointerDown(button, { pointerType: 'mouse', pointerId: 1, button: 0 })
  fireEvent.pointerUp(button, { pointerType: 'mouse', pointerId: 1, button: 0 })
  fireEvent.click(button)
  expect(down).toBe(true)
  expect(release).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole('button', { name: /Multiplayer/ }))
  expect(people).toHaveBeenCalledTimes(1)

  fireEvent.pointerMove(button, { pointerType: 'mouse', clientX: 60, clientY: 90 })
  act(() => frames.splice(0).forEach((frame) => frame(0)))
  expect(strip.style.getPropertyValue('--dot-fade')).toBe('0')

  fireEvent.pointerMove(strip, { pointerType: 'mouse', clientX: 20, clientY: 10 })
  act(() => frames.splice(0).forEach((frame) => frame(0)))
  expect(strip.style.getPropertyValue('--dot-fade')).toBe('1')
  fireEvent.pointerLeave(strip, { pointerType: 'mouse' })
  expect(strip.style.getPropertyValue('--dot-fade')).toBe('0')

  fireEvent.pointerMove(strip, { pointerType: 'mouse', clientX: 10, clientY: 10 })
  view.unmount()
  expect(frames.filter(Boolean)).toHaveLength(0)
  expect(strip.style.getPropertyValue('--dot-px')).toBe('')
})

it('keeps the dots static for touch', () => {
  render(<MultiplayerControls run={run} agent={agent(vi.fn())} onPeople={vi.fn()} />)
  const strip = screen.getByRole('group', { name: 'Multiplayer controls' })
  fireEvent.pointerMove(strip, { pointerType: 'touch', clientX: 10, clientY: 10 })
  expect(frames).toHaveLength(0)
})
